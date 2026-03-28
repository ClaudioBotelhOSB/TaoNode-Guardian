// internal/ai/prompts.go
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
	"fmt"
	"strings"
	"text/template"
)

// systemPrompt defines the LLM persona and the exact JSON output contract.
// It is sent as the "system" field in every OllamaRequest so it is part of
// the model's context window but not quoted back in the response.
//
// Key design decisions:
//   - Low temperature (0.1) + format="json" + explicit schema = structured output.
//   - All valid enum values are listed explicitly to reduce hallucination.
//   - "No markdown" instruction is critical — see parseReport in advisor.go.
const systemPrompt = `You are a senior Site Reliability Engineer (SRE) specializing in Bittensor blockchain infrastructure running on Kubernetes. You analyze anomaly data from subnet node operators and produce concise, actionable incident reports.

STRICT OUTPUT RULES:
1. Respond with ONLY a valid JSON object. No markdown code fences, no preamble, no trailing text.
2. Use exactly these five fields with these exact key names.
3. All string values must be non-empty.

Required JSON schema:
{
  "severity": "critical|high|medium|low",
  "summary": "<one sentence describing the incident>",
  "root_cause_category": "sync-loss|network-partition|disk-pressure|gpu-fault|config-error|unknown",
  "recommended_action": "<specific Kubernetes or node-level remediation command or procedure>",
  "confidence": <float 0.0–1.0>
}

Severity guidelines:
- critical: risk of validator slashing, data loss, or complete sync failure
- high: node is degraded with active block lag or isolation risk
- medium: anomaly detected but node is still functional
- low: early warning signal, no immediate action required

Confidence guidelines:
- 0.9–1.0: multiple corroborating signals, clear root cause
- 0.6–0.8: likely root cause, some uncertainty
- 0.3–0.5: ambiguous signals, best-effort diagnosis
- 0.0–0.2: insufficient data`

// userPromptTmpl is compiled once at package init. It renders an IncidentContext
// into the user-turn text that is sent as the "prompt" field in OllamaRequest.
var userPromptTmpl = template.Must(template.New("incident").Funcs(template.FuncMap{
	"formatScore": func(s float32) string { return fmt.Sprintf("%.3f", s) },
	"formatTime":  func(t interface{ Format(string) string }) string { return t.Format("15:04:05 UTC") },
	"formatDate":  func(t interface{ Format(string) string }) string { return t.Format("2006-01-02") },
}).Parse(`TaoNode Incident — Analysis Request

Node:     {{ .NodeName }} (namespace: {{ .Namespace }})
Network:  {{ .Network }} | Subnet ID: {{ .SubnetID }}
Role:     {{ .Role }}
Phase:    {{ .CurrentPhase }}

Anomaly Scores (noise floor > 0.1, sorted by severity):
{{- range .AnomalyScores }}
  • {{ .Type }}: {{ formatScore .Score }} — {{ .Detail }}
{{- end }}
{{- if .RecentTelemetry }}

Recent Telemetry:
{{- range .RecentTelemetry }}
  • {{ formatTime .Timestamp }} | block_lag={{ .BlockLag }} peers={{ .PeerCount }} disk_pct={{ .DiskUsagePct }}% state={{ .SyncState }}
{{- end }}
{{- end }}
{{- if .PastIncidents }}

Past Incidents on this node:
{{- range .PastIncidents }}
  • {{ formatDate .Timestamp }} [{{ .Severity }}] {{ .Category }}: {{ .Resolution }}
{{- end }}
{{- end }}
{{- if .RecoveryHistory }}

Recovery History:
{{- range .RecoveryHistory }}
  • {{ formatDate .Timestamp }} strategy={{ .Strategy }} outcome={{ .Outcome }}
{{- end }}
{{- end }}

Produce the JSON incident report.`))

// buildUserPrompt renders the userPromptTmpl with the given IncidentContext.
func buildUserPrompt(ic IncidentContext) (string, error) {
	var sb strings.Builder
	if err := userPromptTmpl.Execute(&sb, ic); err != nil {
		return "", fmt.Errorf("render user prompt: %w", err)
	}
	return sb.String(), nil
}
