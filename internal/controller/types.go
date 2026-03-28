// internal/controller/types.go
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

package controller

import "time"

// ChainHealthResult is returned by probeChainHealth().
// The chain-probe sidecar exposes this as JSON at GET :9616/health.
// ProbeLatency is measured by the caller, not serialized from the sidecar.
type ChainHealthResult struct {
	CurrentBlock     int64         `json:"currentBlock"`
	NetworkBlock     int64         `json:"networkBlock"`
	FinalizedBlock   int64         `json:"finalizedBlock"`
	PeerCount        int32         `json:"peerCount"`
	SyncState        string        `json:"syncState"`
	RuntimeVersion   string        `json:"runtimeVersion"`
	Epoch            int64         `json:"epoch"`
	ProbeLatency     time.Duration `json:"-"` // measured by the caller
	DiskUsagePercent int32         `json:"diskUsagePercent"`
	DiskUsedBytes    uint64        `json:"diskUsedBytes"`
	DiskTotalBytes   uint64        `json:"diskTotalBytes"`
	GPUUtilPercent   uint8         `json:"gpuUtilPercent,omitempty"`
	GPUMemUsedBytes  uint64        `json:"gpuMemUsedBytes,omitempty"`
	GPUTempCelsius   uint16        `json:"gpuTempCelsius,omitempty"`
}
