#!/bin/bash
# install-opencost.sh — Idempotent OpenCost installation via Helm.
# Can be called standalone or from bootstrap.sh.
# Prerequisites: K3s running, kube-prometheus-stack installed in namespace 'monitoring'.
set -euo pipefail
export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"
export PATH="/usr/local/bin:${PATH}"

# ── Configuration (overridable via environment) ───────────────────────────────
OPENCOST_NAMESPACE="${OPENCOST_NAMESPACE:-opencost}"
OPENCOST_NODEPORT="${OPENCOST_NODEPORT:-30040}"
OPENCOST_VERSION="${OPENCOST_VERSION:-}"  # empty = latest

# kube-prometheus-stack names the Prometheus service with its release name.
PROMETHEUS_URL="${PROMETHEUS_URL:-http://kube-prometheus-stack-prometheus.monitoring.svc.cluster.local:9090}"

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] [OPENCOST] $*"; }

# ── Guard: Prometheus must be reachable ───────────────────────────────────────
log "Verifying Prometheus is reachable before installing OpenCost"
kubectl wait pods -n monitoring \
  -l "app.kubernetes.io/name=prometheus" \
  --for=condition=Ready --timeout=300s

# ── Helm repo ─────────────────────────────────────────────────────────────────
log "Adding opencost Helm repository"
helm repo add opencost https://opencost.github.io/opencost-helm-chart --force-update
helm repo update opencost

# ── Install / upgrade ─────────────────────────────────────────────────────────
log "Installing OpenCost (NodePort ${OPENCOST_NODEPORT})"

EXTRA_FLAGS=()
if [ -n "${OPENCOST_VERSION}" ]; then
  EXTRA_FLAGS+=(--version "${OPENCOST_VERSION}")
fi

helm upgrade --install opencost opencost/opencost \
  --namespace "${OPENCOST_NAMESPACE}" --create-namespace \
  "${EXTRA_FLAGS[@]}" \
  --set opencost.exporter.defaultClusterId=taonode-guardian \
  --set opencost.exporter.cloudProviderApiKey="" \
  --set opencost.prometheus.internal.enabled=false \
  --set opencost.prometheus.external.enabled=true \
  --set opencost.prometheus.external.url="${PROMETHEUS_URL}" \
  --set opencost.ui.enabled=true \
  --set service.type=NodePort \
  --set service.nodePort="${OPENCOST_NODEPORT}" \
  --wait --timeout=5m

# ── Verify ────────────────────────────────────────────────────────────────────
log "Waiting for OpenCost pods to be Ready"
kubectl wait pods -n "${OPENCOST_NAMESPACE}" --all \
  --for=condition=Ready --timeout=300s

log "OpenCost installed successfully."
log "  UI NodePort : ${OPENCOST_NODEPORT}"
log "  Prometheus  : ${PROMETHEUS_URL}"
log ""
log "SSH tunnel to access the UI:"
log "  ssh -N -L ${OPENCOST_NODEPORT}:localhost:${OPENCOST_NODEPORT} ubuntu@<EC2_PUBLIC_IP>"
log "  Then open: http://localhost:${OPENCOST_NODEPORT}"
