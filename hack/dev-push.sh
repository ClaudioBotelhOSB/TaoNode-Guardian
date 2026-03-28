#!/bin/bash
# hack/dev-push.sh — Inner-loop dev tool: build, stream image to K3s, rollout restart.
#
# Usage: ./hack/dev-push.sh [user@ec2-ip]
#   e.g: ./hack/dev-push.sh ubuntu@54.x.x.x
#
# Requires: docker, ssh (with key-based auth), go 1.23+
# No registry needed — image is streamed via stdin directly into the K3s containerd store.
set -euo pipefail

TARGET="${1:?Usage: $0 user@ec2-ip}"
IMG="taonode-guardian:dev"
NAMESPACE="taonode-guardian-system"
DEPLOYMENT="taonode-guardian-controller-manager"
BINARY="bin/manager"

log()  { echo "[$(date '+%H:%M:%S')] [dev-push] $*"; }
fail() { echo "[$(date '+%H:%M:%S')] [dev-push] ERROR: $*" >&2; exit 1; }

# ── Guard: must run from repo root ───────────────────────────────────────────
[[ -f "go.mod" ]] || fail "Run this script from the repository root."

# ── Step a: Compile binary ───────────────────────────────────────────────────
log "Building binary: CGO_ENABLED=0 go build → ${BINARY}"
CGO_ENABLED=0 go build \
  -trimpath \
  -ldflags="-s -w" \
  -o "${BINARY}" \
  cmd/main.go

# ── Step b: Build Docker image (reuses BuildKit layer cache) ──────────────────
log "Building Docker image: ${IMG}"
docker build \
  --build-arg BINARY="${BINARY}" \
  -t "${IMG}" \
  .

# ── Step c: Stream image into K3s containerd (no registry hop) ───────────────
log "Streaming ${IMG} → ${TARGET} (k3s ctr images import)"
docker save "${IMG}" | ssh -- "${TARGET}" "sudo k3s ctr images import -"

# ── Step d: Rollout restart (picks up the new image from containerd) ─────────
log "Triggering rollout restart: ${NAMESPACE}/${DEPLOYMENT}"
ssh -- "${TARGET}" \
  "sudo kubectl -n ${NAMESPACE} rollout restart deployment/${DEPLOYMENT}"

# ── Step e: Wait for rollout to complete ─────────────────────────────────────
log "Waiting for rollout to complete (timeout: 120s)"
ssh -- "${TARGET}" \
  "sudo kubectl -n ${NAMESPACE} rollout status deployment/${DEPLOYMENT} --timeout=120s"

log "Done. Controller running image ${IMG} on ${TARGET}."
