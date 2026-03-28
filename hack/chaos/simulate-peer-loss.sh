#!/bin/bash
# hack/chaos/simulate-peer-loss.sh
# Simulate a network partition that drops connected peers to near-zero.
#
# Usage:
#   ./hack/chaos/simulate-peer-loss.sh [--peer-count 1] [--duration 45]
#
# Options:
#   --peer-count   Number of peers to leave connected (0 = full isolation). Default: 1
#   --duration     Seconds before peers are automatically restored. Default: 45
#   --namespace    Kubernetes namespace of the chain-simulator. Default: taonode-guardian-system
#
# Effect on the operator:
#   PeerChurnVelocityThreshold (default 5 peers/min) fires an anomaly score.
#   If peer_count drops below the probe's minimum, the circuit breaker opens
#   and the AI advisor produces a "network_partition" root-cause report.
#
# Requires: kubectl, curl.
set -euo pipefail

# ── Defaults ──────────────────────────────────────────────────────────────────
PEER_COUNT=1
DURATION=45
NAMESPACE="taonode-guardian-system"
LOCAL_PORT=18081
SVC_NAME="chain-simulator"
SVC_PORT=9944

# ── Arg parsing ───────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --peer-count) PEER_COUNT="$2"; shift 2 ;;
    --duration)   DURATION="$2";   shift 2 ;;
    --namespace)  NAMESPACE="$2";  shift 2 ;;
    *) echo "[chaos] Unknown option: $1" >&2; exit 1 ;;
  esac
done

log()  { echo "[$(date '+%H:%M:%S')] [chaos/peer-loss] $*"; }
fail() { echo "[$(date '+%H:%M:%S')] [chaos/peer-loss] ERROR: $*" >&2; exit 1; }

command -v kubectl >/dev/null 2>&1 || fail "kubectl not found in PATH"
command -v curl    >/dev/null 2>&1 || fail "curl not found in PATH"

# ── Port-forward ──────────────────────────────────────────────────────────────
log "Starting kubectl port-forward → ${SVC_NAME}:${SVC_PORT} on localhost:${LOCAL_PORT}"
kubectl port-forward \
  -n "${NAMESPACE}" \
  "svc/${SVC_NAME}" \
  "${LOCAL_PORT}:${SVC_PORT}" \
  --pod-running-timeout=30s &
PF_PID=$!
trap 'log "Tearing down port-forward (PID ${PF_PID})"; kill "${PF_PID}" 2>/dev/null; wait "${PF_PID}" 2>/dev/null || true' EXIT
sleep 2

BASE_URL="http://localhost:${LOCAL_PORT}"

# ── Health check ──────────────────────────────────────────────────────────────
curl -sf "${BASE_URL}/health" >/dev/null || fail "chain-simulator is not reachable"

# ── Inject peer loss ──────────────────────────────────────────────────────────
log "Injecting peer-loss fault: peer_count=${PEER_COUNT}, duration=${DURATION}s"
RESPONSE=$(curl -sf -X POST "${BASE_URL}/admin/peer-loss" \
  -H "Content-Type: application/json" \
  -d "{\"peer_count\": ${PEER_COUNT}, \"duration_seconds\": ${DURATION}}")

log "Response: ${RESPONSE}"
log "Peers dropped to ${PEER_COUNT}. Expected operator reaction:"
log "  1. anomaly_scores: peer_churn_velocity score → 0.7+"
log "  2. AI advisor generates 'network_partition' root-cause report"
log "  3. Discord embed sent if discordWebhookSecret is configured"
log ""
log "Watch:  kubectl get taonodes -n ${NAMESPACE} -w"
log "Events: kubectl get events -n ${NAMESPACE} --field-selector reason=AIAdvisory"
log ""
log "Peers will be auto-restored after ${DURATION}s."
sleep "${DURATION}"
log "Fault window elapsed. Simulator has restored peer connections."
