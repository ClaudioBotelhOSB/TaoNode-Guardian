// internal/controller/chain_probe.go
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

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
)

const chainProbePort = 9616

// probeChainHealth discovers the Pod IP of the TaoNode's StatefulSet pod-0
// and makes an HTTP GET to the chain-probe sidecar at :9616/health.
//
// Implementation follows v4 R9:
//  1. Get the pod named {tn.Name}-0 (StatefulSet pod-0 naming convention)
//  2. Extract pod.Status.PodIP (return error if not yet assigned)
//  3. HTTP GET http://{podIP}:9616/health with context deadline
//  4. Decode JSON body into ChainHealthResult
//  5. Record ProbeLatency on the result
func (r *TaoNodeReconciler) probeChainHealth(ctx context.Context, tn *taov1alpha1.TaoNode) (*ChainHealthResult, error) {
	podName := fmt.Sprintf("%s-0", tn.Name)
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      podName,
		Namespace: tn.Namespace,
	}, pod); err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", tn.Namespace, podName, err)
	}

	if pod.Status.PodIP == "" {
		return nil, fmt.Errorf("pod %s/%s has no IP assigned yet", tn.Namespace, podName)
	}

	probeURL := fmt.Sprintf("http://%s:%d/health", pod.Status.PodIP, chainProbePort)
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build probe request: %w", err)
	}

	httpClient := r.ProbeHTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe %s: %w", probeURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("probe returned HTTP %d", resp.StatusCode)
	}

	var result ChainHealthResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode probe response from %s: %w", probeURL, err)
	}

	result.ProbeLatency = time.Since(start)
	return &result, nil
}
