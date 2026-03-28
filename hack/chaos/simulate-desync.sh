#!/bin/bash
# hack/chaos/simulate-desync.sh
# Inject a sync-desync fault into the chain-simulator, wait, then trigger resync.
#
# Usage:
#   ./hack/chaos/simulate-desync.sh [--desync-duration 60] [--lag-blocks 800]
#
# Options:
#   --desync-duration  How long the fault is active before auto-resync (seconds). Default: 60
#   --lag-blocks       Block lag injected to trigger the operator's SyncPolicy. Default: 800
#   --no-resync        Skip the automatic /admin/resync call (leave node degraded).
#   --namespace        Kubernetes namespace of the chain-simulator. Default: taonode-guardian-system
#
# Requires: kubectl (with current-context pointing to the target cluster), curl.
set -euo pipefail

# ── Defaults ──────────────────────────────────────────────────────────────────
DESYNC_DURATION=60
LAG_BLOCKS=800
AUTO_RESYNC=true
NAMESPACE="taonode-guardian-system"
LOCAL_PORT=18080
SVC_NAME="chain-simulator"
SVC_PORT=9944

# ── Arg parsing ───────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --desync-duration) DESYNC_DURATION="$2"; shift 2 ;;
    --lag-blocks)      LAG_BLOCKS="$2";      shift 2 ;;
    --no-resync)       AUTO_RESYNC=false;    shift   ;;
    --namespace)       NAMESPACE="$2";       shift 2 ;;
    *) echo "[chaos] Unknown option: $1" >&2; exit 1 ;;
  esac
done

log()  { echo "[$(date '+%H:%M:%S')] [chaos/desync] $*"; }
fail() { echo "[$(date '+%H:%M:%S')] [chaos/desync] ERROR: $*" >&2; exit 1; }

# ── Validate dependencies ─────────────────────────────────────────────────────
command -v kubectl >/dev/null 2>&1 || fail "kubectl not found in PATH"
command -v curl    >/dev/null 2>&1 || fail "curl not found in PATH"

# ── Port-forward setup ────────────────────────────────────────────────────────
log "Starting kubectl port-forward → ${SVC_NAME}:${SVC_PORT} on localhost:${LOCAL_PORT}"
kubectl port-forward \
  -n "${NAMESPACE}" \
  "svc/${SVC_NAME}" \
  "${LOCAL_PORT}:${SVC_PORT}" \
  --pod-running-timeout=30s &
PF_PID=$!
trap 'log "Tearing down port-forward (PID ${PF_PID})"; kill "${PF_PID}" 2>/dev/null; wait "${PF_PID}" 2>/dev/null || true' EXIT

# Give the tunnel a moment to establish.
sleep 2

BASE_URL="http://localhost:${LOCAL_PORT}"

# ── Health check ──────────────────────────────────────────────────────────────
log "Health-checking chain-simulator at ${BASE_URL}/health"
curl -sf "${BASE_URL}/health" >/dev/null || fail "chain-simulator is not reachable — is the deployment running?"

# ── Phase 1: Inject desync ────────────────────────────────────────────────────
log "Injecting desync fault: lag_blocks=${LAG_BLOCKS}, duration=${DESYNC_DURATION}s"
RESPONSE=$(curl -sf -X POST "${BASE_URL}/admin/desync" \
  -H "Content-Type: application/json" \
  -d "{\"lag_blocks\": ${LAG_BLOCKS}, \"duration_seconds\": ${DESYNC_DURATION}}")

log "Response: ${RESPONSE}"
log "Fault injected. Operator should transition node → Degraded within 2 reconcile cycles."
log "Watch:  kubectl get taonodes -n ${NAMESPACE} -w"
log "Logs:   kubectl logs -n ${NAMESPACE} deployment/taonode-guardian-controller-manager -f"

# ── Phase 2: Wait and resync ──────────────────────────────────────────────────
if [[ "${AUTO_RESYNC}" == "true" ]]; then
  log "Waiting ${DESYNC_DURATION}s before triggering /admin/resync…"
  sleep "${DESYNC_DURATION}"

  log "Triggering /admin/resync"
  RESYNC_RESP=$(curl -sf -X POST "${BASE_URL}/admin/resync" \
    -H "Content-Type: application/json" \
    -d "{}")
  log "Resync response: ${RESYNC_RESP}"
  log "Node should recover → Recovering → Synced shortly."
else
  log "--no-resync set. Node remains in degraded state. Run /admin/resync manually."
fi
