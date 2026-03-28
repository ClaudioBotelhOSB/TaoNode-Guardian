// internal/ai/ollama_client.go
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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// ollamaGeneratePath is the Ollama API endpoint for text generation.
	ollamaGeneratePath = "/api/generate"

	// maxErrorBodyBytes limits how much of an error response body we read
	// to prevent OOM when an upstream proxy returns a large HTML error page.
	maxErrorBodyBytes = 2048
)

// ollamaClient is an HTTP client scoped to a single Ollama server.
// It is unexported — callers interact with the package via Advisor.
type ollamaClient struct {
	baseURL    string
	httpClient *http.Client
}

// newOllamaClient creates an ollamaClient with a bounded HTTP transport (v4 R10).
//
// Transport tuning prevents file-descriptor exhaustion under load:
//   - MaxIdleConns / MaxIdleConnsPerHost: caps the idle connection pool size.
//   - IdleConnTimeout: reclaims stale connections after 90 s.
//   - TLSHandshakeTimeout: fail fast on TLS negotiation stalls.
//
// timeout is applied at the http.Client level and propagates a hard deadline
// across dial + TLS + request write + response read. Context deadlines from
// the Reconcile loop will cancel earlier via req.WithContext().
func newOllamaClient(endpoint string, timeout time.Duration) *ollamaClient {
	transport := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableCompression:  false,
	}
	return &ollamaClient{
		baseURL: strings.TrimRight(endpoint, "/"),
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
	}
}

// generate sends a POST /api/generate request to the Ollama server.
//
// Context injection (v4 R10): the HTTP request is created with
// http.NewRequestWithContext so that cancellation of the Reconcile loop
// (or the 30 s AI advisory timeout) immediately aborts the in-flight call,
// preventing goroutine leaks.
func (c *ollamaClient) generate(ctx context.Context, req OllamaRequest) (OllamaResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return OllamaResponse{}, fmt.Errorf("marshal ollama request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+ollamaGeneratePath,
		bytes.NewReader(body),
	)
	if err != nil {
		return OllamaResponse{}, fmt.Errorf("build ollama http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return OllamaResponse{}, fmt.Errorf("ollama http call: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxErrorBodyBytes))
		return OllamaResponse{}, fmt.Errorf(
			"ollama returned HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	var resp OllamaResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return OllamaResponse{}, fmt.Errorf("decode ollama response: %w", err)
	}
	return resp, nil
}
