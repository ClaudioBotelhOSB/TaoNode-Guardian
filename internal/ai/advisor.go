// internal/ai/advisor.go
/*
Copyright 2026 Claudio Botelho.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Config holds all tunable parameters for the Advisor.
type Config struct {
	// OllamaEndpoint is the base URL of the Ollama server (e.g. "http://ollama:11434").
	OllamaEndpoint string

	// Model is the Ollama model name to use (default: "llama3.2").
	Model string

	// RequestTimeout is the per-call HTTP timeout passed to the ollamaClient.
	// Should be >= the Reconcile loop's AI advisory timeout (default: 30 s).
	RequestTimeout time.Duration

	// MaxCallsPerMinute limits inference requests to prevent thundering-herd
	// on Ollama when the cluster enters a CrashLoopBackOff storm (default: 5).
	MaxCallsPerMinute int
}

// Advisor is the AI incident analysis engine. It wraps an ollamaClient with
// prompt engineering, response parsing, and a built-in rate limiter.
//
// Safe for concurrent use: the rate limiter uses its own mutex; the ollamaClient
// is stateless after construction.
type Advisor struct {
	client  *ollamaClient
	model   string
	limiter *advisorRateLimiter
}

// NewAdvisor creates an Advisor. Returns an error if OllamaEndpoint is empty.
func NewAdvisor(cfg Config) (*Advisor, error) {
	if cfg.OllamaEndpoint == "" {
		return nil, fmt.Errorf("OllamaEndpoint must not be empty")
	}
	if cfg.Model == "" {
		cfg.Model = "llama3.2"
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.MaxCallsPerMinute == 0 {
		cfg.MaxCallsPerMinute = 5
	}

	return &Advisor{
		client:  newOllamaClient(cfg.OllamaEndpoint, cfg.RequestTimeout),
		model:   cfg.Model,
		limiter: newAdvisorRateLimiter(cfg.MaxCallsPerMinute, time.Minute),
	}, nil
}

// AnalyzeIncident sends the IncidentContext to the configured LLM and returns
// a parsed IncidentReport.
//
// Returns (nil, nil) when the internal rate limiter throttles the call — the
// controller treats nil as "no advisory available" and skips gracefully.
func (a *Advisor) AnalyzeIncident(ctx context.Context, ic IncidentContext) (*IncidentReport, error) {
	if !a.limiter.allow() {
		return nil, nil // rate-limited; caller logs and continues
	}

	userPrompt, err := buildUserPrompt(ic)
	if err != nil {
		return nil, fmt.Errorf("build incident prompt: %w", err)
	}

	req := OllamaRequest{
		Model:  a.model,
		Prompt: userPrompt,
		System: systemPrompt,
		Stream: false,
		Format: "json",
		Options: &OllamaOptions{
			Temperature: 0.1, // deterministic structured output
			NumPredict:  512, // sufficient for the JSON schema
		},
	}

	resp, err := a.client.generate(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ollama inference: %w", err)
	}

	return parseReport(resp.Response)
}

// ──────────────────────────────────────────────────────────────────────────────
// Response parser (v5 J2)
// ──────────────────────────────────────────────────────────────────────────────

// markdownFenceRE matches JSON content wrapped in markdown code fences.
// LLMs frequently produce: ```json\n{...}\n``` or ```\n{...}\n```
var markdownFenceRE = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// parseReport extracts and unmarshals an IncidentReport from raw LLM output.
//
// Resilience strategy (v5 J2):
//  1. Strip markdown code fences with a regex to handle ```json ... ``` wrapping.
//  2. Locate the first '{' and last '}' to discard preamble/postamble text.
//  3. Attempt json.Unmarshal; return a descriptive error on failure.
//
// This three-step cleaning pipeline handles the most common failure modes
// observed with open-weight models (fence wrapping, leading explanation text,
// trailing "Is there anything else?" text).
func parseReport(raw string) (*IncidentReport, error) {
	cleaned := raw

	// Step 1: strip markdown code fences.
	if m := markdownFenceRE.FindStringSubmatch(cleaned); len(m) > 1 {
		cleaned = m[1]
	}

	// Step 2: trim surrounding whitespace, then find the JSON object boundaries.
	cleaned = strings.TrimSpace(cleaned)
	if start := strings.Index(cleaned, "{"); start >= 0 {
		if end := strings.LastIndex(cleaned, "}"); end > start {
			cleaned = cleaned[start : end+1]
		}
	}

	// Step 3: unmarshal.
	var report IncidentReport
	if err := json.Unmarshal([]byte(cleaned), &report); err != nil {
		// Truncate raw response in the error message to keep logs readable.
		preview := raw
		if len(preview) > 300 {
			preview = preview[:300] + "…"
		}
		return nil, fmt.Errorf("parse LLM report (unmarshal): %w | raw: %s", err, preview)
	}

	// Clamp confidence to [0, 1] in case the model emits an out-of-range value.
	if report.Confidence < 0 {
		report.Confidence = 0
	}
	if report.Confidence > 1 {
		report.Confidence = 1
	}

	return &report, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal rate limiter
// ──────────────────────────────────────────────────────────────────────────────

// advisorRateLimiter is a simple fixed-window rate limiter.
// It prevents the Advisor from flooding Ollama during a fleet-wide
// CrashLoopBackOff storm where many nodes trigger AI advisory simultaneously.
type advisorRateLimiter struct {
	mu       sync.Mutex
	calls    int
	maxCalls int
	window   time.Duration
	resetAt  time.Time
}

func newAdvisorRateLimiter(maxCallsPerWindow int, window time.Duration) *advisorRateLimiter {
	return &advisorRateLimiter{
		maxCalls: maxCallsPerWindow,
		window:   window,
		resetAt:  time.Now().Add(window),
	}
}

// allow returns true if a call is permitted under the current rate limit window.
// The window resets lazily on the first call after the window expires.
func (r *advisorRateLimiter) allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if time.Now().After(r.resetAt) {
		r.calls = 0
		r.resetAt = time.Now().Add(r.window)
	}

	if r.calls >= r.maxCalls {
		return false
	}
	r.calls++
	return true
}
