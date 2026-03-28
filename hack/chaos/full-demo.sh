#!/bin/bash
# hack/chaos/full-demo.sh
# Orchestrates all three chaos scenarios in sequence for the Fase 9F live demo.
#
# Demo flow (total ≈ 5 minutes):
#   00:00  Desync fault    — operator detects block lag, AI advisor fires
#   01:30  Auto-resync     — node recovers (Recovering → Synced)
#   02:00  Peer-loss fault — anomaly score spikes, Discord embed sent
#   02:45  Peers restored  — node stabilises
#   03:00  Disk pressure   — PVC auto-expand triggered, AI reports storage_pressure
#   04:00  Disk reset      — operator acknowledges recovery
#   04:30  Summary printed
#
# Usage:
#   ./hack/chaos/full-demo.sh [--namespace taonode-guardian-system] [--fast]
#
# Options:
#   --namespace   K8s namespace. Default: taonode-guardian-system
#   --fast        Compress all wait times to 1/4 for CI/quick validation.
#
# Requires: kubectl, curl. All three chaos scripts must be in the same directory.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="taonode-guardian-system"
FAST=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --namespace) NAMESPACE="$2"; shift 2 ;;
    --fast)      FAST=true;      shift   ;;
    *) echo "[demo] Unknown option: $1" >&2; exit 1 ;;
  esac
done

# Scale durations by 1 in normal mode, 0.25 in fast mode.
scale() {
  local val="$1"
  if [[ "${FAST}" == "true" ]]; then
    echo $(( val / 4 < 2 ? 2 : val / 4 ))
  else
    echo "${val}"
  fi
}

DESYNC_DURATION=$(scale 90)
PEER_DURATION=$(scale 45)
DISK_DURATION=$(scale 60)

# ── Helpers ───────────────────────────────────────────────────────────────────
BOLD='\033[1m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
RED='\033[0;31m'
RESET='\033[0m'

banner() {
  echo ""
  echo -e "${BOLD}${CYAN}══════════════════════════════════════════════════════${RESET}"
  echo -e "${BOLD}${CYAN}  $*${RESET}"
  echo -e "${BOLD}${CYAN}══════════════════════════════════════════════════════${RESET}"
  echo ""
}

step()    { echo -e "[$(date '+%H:%M:%S')] ${BOLD}${GREEN}▶  $*${RESET}"; }
info()    { echo -e "[$(date '+%H:%M:%S')] ${CYAN}   $*${RESET}"; }
warn()    { echo -e "[$(date '+%H:%M:%S')] ${YELLOW}⚠  $*${RESET}"; }
pause()   {
  local secs="$1" msg="${2:-Waiting}"
  echo -e "[$(date '+%H:%M:%S')] ${CYAN}   ${msg} (${secs}s)…${RESET}"
  sleep "${secs}"
}

# ── Pre-flight checks ─────────────────────────────────────────────────────────
command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found" >&2; exit 1; }
command -v curl    >/dev/null 2>&1 || { echo "ERROR: curl not found"    >&2; exit 1; }

for script in simulate-desync.sh simulate-peer-loss.sh simulate-disk-pressure.sh; do
  [[ -f "${SCRIPT_DIR}/${script}" ]] || { echo "ERROR: ${script} not found in ${SCRIPT_DIR}" >&2; exit 1; }
  chmod +x "${SCRIPT_DIR}/${script}"
done

# ── Demo start ────────────────────────────────────────────────────────────────
banner "TaoNode Guardian — Chaos Engineering Demo"
echo -e "  Namespace : ${NAMESPACE}"
echo -e "  Fast mode : ${FAST}"
echo -e "  Durations : desync=${DESYNC_DURATION}s  peer-loss=${PEER_DURATION}s  disk=${DISK_DURATION}s"
echo ""
warn "This demo injects real faults. Ensure you are targeting a NON-PRODUCTION cluster."
pause 5 "Starting in"

# ── Phase 1: Sync Desync ──────────────────────────────────────────────────────
banner "Phase 1/3 — Sync Desync (block lag injection)"
step "Injecting desync fault (lag_blocks=800, duration=${DESYNC_DURATION}s)"
info "Expected: node transitions Synced → Degraded → AI advisory → Discord embed"
info "Watch in another terminal:"
info "  kubectl get taonodes -n ${NAMESPACE} -w"

"${SCRIPT_DIR}/simulate-desync.sh" \
  --namespace "${NAMESPACE}" \
  --desync-duration "${DESYNC_DURATION}" \
  --lag-blocks 800

step "Phase 1 complete — node should be recovering or synced"
pause 10 "Letting the cluster stabilise"

# ── Phase 2: Peer Loss ────────────────────────────────────────────────────────
banner "Phase 2/3 — Peer Loss (network partition)"
step "Injecting peer-loss fault (peer_count=1, duration=${PEER_DURATION}s)"
info "Expected: peer_churn_velocity anomaly fires, AI reports 'network_partition'"
info "Watch events:"
info "  kubectl get events -n ${NAMESPACE} --field-selector reason=AIAdvisory"

"${SCRIPT_DIR}/simulate-peer-loss.sh" \
  --namespace "${NAMESPACE}" \
  --peer-count 1 \
  --duration "${PEER_DURATION}"

step "Phase 2 complete — peers restored"
pause 10 "Letting the cluster stabilise"

# ── Phase 3: Disk Pressure ────────────────────────────────────────────────────
banner "Phase 3/3 — Disk Pressure (storage exhaustion)"
step "Injecting disk-fill fault (usage=88%, duration=${DISK_DURATION}s)"
info "Expected: autoExpand patch sent to PVC, disk_exhaustion anomaly score → critical"
info "Watch PVC:"
info "  kubectl get pvc -n ${NAMESPACE} -w"

"${SCRIPT_DIR}/simulate-disk-pressure.sh" \
  --namespace "${NAMESPACE}" \
  --usage-percent 88 \
  --duration "${DISK_DURATION}"

step "Phase 3 complete — disk usage reset"

# ── Summary ───────────────────────────────────────────────────────────────────
banner "Demo Complete — Summary"
echo -e "  ${GREEN}✓${RESET}  Phase 1: Sync desync → operator recovery loop demonstrated"
echo -e "  ${GREEN}✓${RESET}  Phase 2: Peer loss   → AI advisory + Discord notification fired"
echo -e "  ${GREEN}✓${RESET}  Phase 3: Disk fill   → autoExpand + storage anomaly demonstrated"
echo ""
echo "  Grafana dashboards for review:"
echo "    Anomaly Realtime : http://localhost:30030/d/tng-anomaly-realtime"
echo "    FinOps Cost      : http://localhost:30030/d/tng-finops-cost"
echo ""
echo "  Full audit in ClickHouse:"
echo "    SELECT * FROM taonode_guardian.anomaly_scores ORDER BY timestamp DESC LIMIT 50;"
echo ""
