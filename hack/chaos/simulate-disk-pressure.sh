#!/bin/bash
# hack/chaos/simulate-disk-pressure.sh
# Simulate rapid disk fill on the chain-simulator to trigger disk_exhaustion anomaly.
#
# Usage:
#   ./hack/chaos/simulate-disk-pressure.sh [--usage-percent 88] [--duration 60]
#
# Options:
#   --usage-percent   Simulated disk_usage_percent to report (0–99). Default: 88
#   --duration        Seconds before disk usage is reset to baseline. Default: 60
#   --namespace       Kubernetes namespace of the chain-simulator. Default: taonode-guardian-system
#
# Effect on the operator:
#   DiskExhaustionHorizonHours (default 48h) fires when the projected time-to-full
#   falls below the threshold. Setting usage-percent to 88+ causes the anomaly
#   detector to project disk exhaustion within hours and emit a critical score.
#   spec.chainStorage.autoExpand is triggered if ThresholdPercent (default 80) is exceeded.
#
# Requires: kubectl, curl.
set -euo pipefail

# ── Defaults ──────────────────────────────────────────────────────────────────
USAGE_PERCENT=88
DURATION=60
NAMESPACE="taonode-guardian-system"
LOCAL_PORT=18082
SVC_NAME="chain-simulator"
SVC_PORT=9944

# ── Arg parsing ───────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --usage-percent) USAGE_PERCENT="$2"; shift 2 ;;
    --duration)      DURATION="$2";      shift 2 ;;
    --namespace)     NAMESPACE="$2";     shift 2 ;;
    *) echo "[chaos] Unknown option: $1" >&2; exit 1 ;;
  esac
done

log()  { echo "[$(date '+%H:%M:%S')] [chaos/disk-pressure] $*"; }
fail() { echo "[$(date '+%H:%M:%S')] [chaos/disk-pressure] ERROR: $*" >&2; exit 1; }

[[ "${USAGE_PERCENT}" -ge 0 && "${USAGE_PERCENT}" -le 99 ]] || \
  fail "--usage-percent must be between 0 and 99 (got: ${USAGE_PERCENT})"

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

# ── Inject disk pressure ──────────────────────────────────────────────────────
log "Injecting disk-fill fault: usage=${USAGE_PERCENT}%, duration=${DURATION}s"
RESPONSE=$(curl -sf -X POST "${BASE_URL}/admin/disk-fill" \
  -H "Content-Type: application/json" \
  -d "{\"disk_usage_percent\": ${USAGE_PERCENT}, \"duration_seconds\": ${DURATION}}")

log "Response: ${RESPONSE}"
log "Disk usage set to ${USAGE_PERCENT}%. Expected operator reaction:"
if [[ "${USAGE_PERCENT}" -ge 80 ]]; then
  log "  1. spec.chainStorage.autoExpand threshold exceeded → PVC expansion patch"
fi
log "  2. anomaly_scores: disk_exhaustion score → 0.8+ (critical)"
log "  3. AI advisor generates 'storage_pressure' root-cause report"
log "  4. Discord embed sent if discordWebhookSecret is configured"
log ""
log "Watch PVC:   kubectl get pvc -n ${NAMESPACE} -w"
log "Watch node:  kubectl get taonodes -n ${NAMESPACE} -w"
log ""
log "Disk pressure will auto-reset after ${DURATION}s."
sleep "${DURATION}"
log "Fault window elapsed. Simulator has reset disk usage to baseline."
