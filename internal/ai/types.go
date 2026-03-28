// internal/ai/types.go
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
	"time"

	"github.com/ClaudioBotelhOSB/taonode-guardian/internal/analytics"
)

// ──────────────────────────────────────────────────────────────────────────────
// Ollama wire types
// ──────────────────────────────────────────────────────────────────────────────

// OllamaRequest is the JSON body sent to POST /api/generate.
type OllamaRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	System  string         `json:"system,omitempty"`
	Stream  bool           `json:"stream"`           // always false — we need the full response
	Format  string         `json:"format,omitempty"` // "json" to hint structured output
	Options *OllamaOptions `json:"options,omitempty"`
}

// OllamaOptions tunes inference behaviour.
type OllamaOptions struct {
	// Temperature controls creativity; 0.1 minimises hallucination in structured output.
	Temperature float32 `json:"temperature,omitempty"`
	// NumPredict is the max number of tokens to generate.
	NumPredict int `json:"num_predict,omitempty"`
	// TopP nucleus sampling parameter.
	TopP float32 `json:"top_p,omitempty"`
	// Seed enables reproducible outputs (0 = random).
	Seed int `json:"seed,omitempty"`
}

// OllamaResponse is the JSON body returned by POST /api/generate (stream=false).
type OllamaResponse struct {
	Model     string `json:"model"`
	Response  string `json:"response"` // the generated text (or JSON)
	Done      bool   `json:"done"`
	CreatedAt string `json:"created_at"`

	// Instrumentation fields — useful for metrics but not required for parsing.
	TotalDurationNs    int64 `json:"total_duration"`
	PromptEvalCount    int   `json:"prompt_eval_count"`
	PromptEvalDuration int64 `json:"prompt_eval_duration"`
	EvalCount          int   `json:"eval_count"`
	EvalDuration       int64 `json:"eval_duration"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Incident analysis types — input and output of Advisor.AnalyzeIncident
// ──────────────────────────────────────────────────────────────────────────────

// IncidentContext is the structured context sent to the LLM for analysis.
// Fields are populated by the reconciler; RecentTelemetry, PastIncidents,
// and RecoveryHistory are nil when the analytics plane is unavailable.
type IncidentContext struct {
	NodeName     string
	Namespace    string
	Network      string
	SubnetID     int32
	Role         string
	CurrentPhase string

	// AnomalyScores is the output of analytics.AnomalyDetector.EvaluateNode.
	AnomalyScores []analytics.AnomalyScore

	// RecentTelemetry holds recent chain-health snapshots for LLM context.
	// Populated by the analytics context_builder; nil when unavailable.
	RecentTelemetry []TelemetryPoint

	// PastIncidents holds a summary of previously resolved incidents on this node.
	PastIncidents []PastIncident

	// RecoveryHistory holds the node's last N recovery attempts and outcomes.
	RecoveryHistory []RecoveryEvent
}

// TelemetryPoint is a lightweight chain-health snapshot used in the LLM prompt.
type TelemetryPoint struct {
	Timestamp    time.Time
	BlockLag     int64
	PeerCount    int32
	SyncState    string
	DiskUsagePct uint8
}

// PastIncident is a historical incident summary used for LLM context enrichment.
type PastIncident struct {
	Timestamp  time.Time
	Severity   string
	Category   string // matches root_cause_category
	Resolution string // what fixed it
}

// RecoveryEvent records a single past recovery attempt.
type RecoveryEvent struct {
	Timestamp time.Time
	Strategy  string // "restart", "snapshot-restore", "cordon-and-alert"
	Outcome   string // "recovered", "failed", "timed-out"
}

// IncidentReport is the parsed output of the LLM inference call.
// JSON tags must match the schema declared in prompts.systemPrompt.
type IncidentReport struct {
	Severity          string  `json:"severity"`            // "critical", "high", "medium", "low"
	Summary           string  `json:"summary"`             // one-sentence description
	RootCauseCategory string  `json:"root_cause_category"` // see prompts.go for valid values
	RecommendedAction string  `json:"recommended_action"`  // specific actionable step
	Confidence        float32 `json:"confidence"`          // 0.0–1.0
}
