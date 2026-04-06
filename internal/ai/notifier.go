// internal/ai/notifier.go
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

// Package ai — Notifier sends AI incident reports to a Discord channel
// via an incoming webhook URL. It formats the IncidentReport as a Discord
// Embed with color-coded severity and structured fields.
//
// The webhook URL is never hardcoded: callers must pass it explicitly,
// typically after reading it from a Kubernetes Secret. This keeps the
// Notifier free of any client-go dependency.
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

// discordEmbedColor maps IncidentReport.Severity → Discord embed color (decimal RGB).
var discordEmbedColor = map[string]int{
	"critical": 15158332, // 0xE74C3C — red
	"high":     15105570, // 0xE67E22 — orange
	"medium":   16312092, // 0xF8C300 — amber
	"low":      3447003,  // 0x3498DB — blue
}

// defaultEmbedColor is used when the severity is unrecognised.
const defaultEmbedColor = 8421504 // 0x808080 — grey

// ── Wire types ────────────────────────────────────────────────────────────────

// discordWebhookBody is the JSON body for a Discord incoming webhook POST.
type discordWebhookBody struct {
	Username string         `json:"username,omitempty"`
	Embeds   []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Color       int           `json:"color"`
	Fields      []embedField  `json:"fields,omitempty"`
	Footer      *embedFooter  `json:"footer,omitempty"`
	Timestamp   string        `json:"timestamp,omitempty"` // ISO 8601
}

type embedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type embedFooter struct {
	Text string `json:"text"`
}

// ── Notifier ─────────────────────────────────────────────────────────────────

// Notifier sends Discord embeds for AI incident reports.
// Construct with NewNotifier; safe for concurrent use after construction.
type Notifier struct {
	webhookURL string
	httpClient *http.Client
}

// NewNotifier creates a Notifier for the given Discord webhook URL.
// Returns an error if webhookURL is empty.
func NewNotifier(webhookURL string) (*Notifier, error) {
	if strings.TrimSpace(webhookURL) == "" {
		return nil, fmt.Errorf("discord webhookURL must not be empty")
	}
	return &Notifier{
		webhookURL: webhookURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}, nil
}

// Send formats report + ic into a Discord Embed and POSTs it to the webhook.
//
// Non-retrying: the controller's reconcile loop provides outer retry semantics.
// A failed notification is logged by the caller but does not block reconciliation.
func (n *Notifier) Send(ctx context.Context, report *IncidentReport, ic IncidentContext) error {
	if report == nil {
		return nil
	}

	embed := n.buildEmbed(report, ic)
	body := discordWebhookBody{
		Username: "TaoNode Guardian",
		Embeds:   []discordEmbed{embed},
	}

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal discord payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL,
		bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build discord request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("discord webhook POST: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Discord returns 204 No Content on success.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("discord webhook returned HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// buildEmbed constructs the Discord Embed from an IncidentReport.
func (n *Notifier) buildEmbed(report *IncidentReport, ic IncidentContext) discordEmbed {
	color, ok := discordEmbedColor[strings.ToLower(report.Severity)]
	if !ok {
		color = defaultEmbedColor
	}

	severityEmoji := severityToEmoji(report.Severity)
	title := fmt.Sprintf("%s [%s] %s/%s", severityEmoji,
		strings.ToUpper(report.Severity), ic.Namespace, ic.NodeName)

	fields := []embedField{
		{
			Name:   "Root Cause",
			Value:  fmt.Sprintf("`%s`", report.RootCauseCategory),
			Inline: true,
		},
		{
			Name:   "Confidence",
			Value:  fmt.Sprintf("%.0f%%", report.Confidence*100),
			Inline: true,
		},
		{
			Name:   "Phase",
			Value:  fmt.Sprintf("`%s`", ic.CurrentPhase),
			Inline: true,
		},
		{
			Name:   "Recommended Action",
			Value:  report.RecommendedAction,
			Inline: false,
		},
	}

	// Append top anomaly scores if present.
	if len(ic.AnomalyScores) > 0 {
		var scores strings.Builder
		for _, a := range ic.AnomalyScores {
			if a.Score >= 0.4 {
				fmt.Fprintf(&scores, "• `%s` → %.2f\n", a.Type, a.Score)
			}
		}
		if scores.Len() > 0 {
			fields = append(fields, embedField{
				Name:   "Active Anomaly Scores",
				Value:  scores.String(),
				Inline: false,
			})
		}
	}

	return discordEmbed{
		Title:       title,
		Description: report.Summary,
		Color:       color,
		Fields:      fields,
		Footer: &embedFooter{
			Text: fmt.Sprintf("TaoNode Guardian AI Advisor  •  %s/%s  •  subnet %d",
				ic.Network, ic.Role, ic.SubnetID),
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

// severityToEmoji maps severity strings to Unicode emoji for embed titles.
func severityToEmoji(severity string) string {
	switch strings.ToLower(severity) {
	case "critical":
		return "\U0001F534" // 🔴
	case "high":
		return "\U0001F7E0" // 🟠
	case "medium":
		return "\U0001F7E1" // 🟡
	case "low":
		return "\U0001F7E2" // 🟢
	default:
		return "\u26AA" // ⚪
	}
}
