#!/bin/bash
# hack/dev-logs.sh — Stream live logs from the TaoNode Guardian controller on a remote K3s node.
#
# Usage: ./hack/dev-logs.sh [user@ec2-ip] [optional: container-name]
#   e.g: ./hack/dev-logs.sh ubuntu@54.x.x.x
#        ./hack/dev-logs.sh ubuntu@54.x.x.x manager
#
# Requires: ssh (with key-based auth configured)
# Exits cleanly on Ctrl+C.
set -euo pipefail

TARGET="${1:?Usage: $0 user@ec2-ip [container]}"
CONTAINER="${2:-}"
NAMESPACE="taonode-guardian-system"
DEPLOYMENT="taonode-guardian-controller-manager"

log()  { echo "[$(date '+%H:%M:%S')] [dev-logs] $*"; }
fail() { echo "[$(date '+%H:%M:%S')] [dev-logs] ERROR: $*" >&2; exit 1; }

# ── Build the kubectl logs command ────────────────────────────────────────────
KUBECTL_CMD="sudo kubectl logs -f -n ${NAMESPACE} deployment/${DEPLOYMENT}"
KUBECTL_CMD+=" --all-containers=true"
KUBECTL_CMD+=" --prefix=true"
KUBECTL_CMD+=" --timestamps=true"

if [[ -n "${CONTAINER}" ]]; then
  [[ "${CONTAINER}" =~ ^[a-z0-9]([-.a-z0-9_]*[a-z0-9])?$ ]] || \
    fail "Invalid container name: ${CONTAINER}"
  KUBECTL_CMD="sudo kubectl logs -f -n ${NAMESPACE} deployment/${DEPLOYMENT}"
  KUBECTL_CMD+=" -c ${CONTAINER}"
  KUBECTL_CMD+=" --timestamps=true"
  log "Filtering to container: ${CONTAINER}"
fi

# ── Verify the deployment exists before connecting ───────────────────────────
log "Checking deployment ${DEPLOYMENT} on ${TARGET}"
ssh -- "${TARGET}" \
  "sudo kubectl get deployment ${DEPLOYMENT} -n ${NAMESPACE} --no-headers" \
  2>/dev/null || {
    echo ""
    log "WARNING: Deployment not found yet. Retrying in 5 s…"
    sleep 5
  }

log "Streaming logs from ${NAMESPACE}/${DEPLOYMENT} on ${TARGET}"
log "Press Ctrl+C to stop."
echo "──────────────────────────────────────────────────────────────────────"

# -t allocates a TTY so Ctrl+C is forwarded cleanly through the SSH tunnel.
ssh -t -- "${TARGET}" "${KUBECTL_CMD}"
