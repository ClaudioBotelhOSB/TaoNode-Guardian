#!/bin/bash
# TaoNode Guardian — K3s bootstrap script
# Runs as root via EC2 user_data / cloud-init on first boot.
# Output is captured automatically in /var/log/cloud-init-output.log
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
export PATH="/usr/local/bin:${PATH}"

GITHUB_USER="ClaudioBotelhOSB"
REPO_URL="https://${GITHUB_USER}:${GITHUB_TOKEN}@github.com/${GITHUB_USER}/taonode-guardian.git"
REPO_DIR="/opt/taonode-guardian"
STATE_DIR="/var/lib/taonode-guardian"

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] [BOOTSTRAP] $*"; }
wait_ready() {
  local ns="$1"; shift
  log "Waiting for pods in namespace '${ns}' to be Ready..."
  kubectl wait pods -n "${ns}" --all --for=condition=Ready --timeout=600s
}
rand_secret() { head -c 32 /dev/urandom | base64 | tr -d '\n'; }
ensure_secret_file() {
  local path="$1"
  if [ ! -s "${path}" ]; then
    umask 077
    rand_secret > "${path}"
  fi
  chmod 600 "${path}"
}

# ── STEP 1: System dependencies, tooling, and repo clone ─────────────────────
log "STEP 1: Installing system dependencies"
apt-get update -y
apt-get install -y curl wget git jq ca-certificates apt-transport-https gnupg

log "STEP 1: Installing Helm"
curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash

log "STEP 1: Installing kustomize"
KUSTOMIZE_VERSION="5.4.2"
curl -fsSL "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv${KUSTOMIZE_VERSION}/kustomize_v${KUSTOMIZE_VERSION}_linux_amd64.tar.gz" \
  | tar -xz -C /usr/local/bin kustomize
chmod +x /usr/local/bin/kustomize

log "STEP 1: Cloning repository to ${REPO_DIR}"
mkdir -p "${STATE_DIR}"
chmod 700 "${STATE_DIR}"
ensure_secret_file "${STATE_DIR}/grafana-admin-password"
ensure_secret_file "${STATE_DIR}/clickhouse-password"
GRAFANA_ADMIN_PASSWORD=$(cat "${STATE_DIR}/grafana-admin-password")
CLICKHOUSE_PASSWORD=$(cat "${STATE_DIR}/clickhouse-password")
if [ -d "${REPO_DIR}/.git" ]; then
  git -C "${REPO_DIR}" pull --ff-only
else
  git clone --depth=1 "${REPO_URL}" "${REPO_DIR}"
fi

# ── STEP 2: K3s (no traefik, no servicelb) ───────────────────────────────────
log "STEP 2: Installing K3s"
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server \
  --disable traefik \
  --disable servicelb \
  --write-kubeconfig-mode 644" sh -

# Persist KUBECONFIG for interactive SSH sessions
echo 'export KUBECONFIG=/etc/rancher/k3s/k3s.yaml' > /etc/profile.d/k3s.sh
chmod +x /etc/profile.d/k3s.sh

# ── STEP 3: Wait for K3s node Ready ──────────────────────────────────────────
log "STEP 3: Waiting for K3s node to become Ready"
kubectl wait nodes --all --for=condition=Ready --timeout=300s
kubectl get nodes -o wide

# ── STEP 4: cert-manager ──────────────────────────────────────────────────────
log "STEP 4: Installing cert-manager via Helm"
helm repo add jetstack https://charts.jetstack.io --force-update
helm repo update jetstack
helm upgrade --install cert-manager jetstack/cert-manager \
  --namespace cert-manager --create-namespace \
  --set installCRDs=true \
  --wait --timeout=5m

# ── STEP 5: Wait for cert-manager (+ webhook warm-up) ────────────────────────
log "STEP 5: Waiting for cert-manager pods"
wait_ready cert-manager
# The admission webhook needs ~15 s after pod readiness to register its TLS config.
log "STEP 5: Allowing 20 s for cert-manager webhook registration"
sleep 20

# ── STEP 6: ArgoCD ────────────────────────────────────────────────────────────
log "STEP 6: Installing ArgoCD"
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n argocd \
  -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml

# ── STEP 7: Wait for ArgoCD + patch server to NodePort 30080 ─────────────────
log "STEP 7: Waiting for ArgoCD server deployment"
kubectl rollout status deployment argocd-server -n argocd --timeout=600s

log "STEP 7: Patching argocd-server to NodePort 30080"
kubectl patch svc argocd-server -n argocd --type='merge' -p '{
  "spec": {
    "type": "NodePort",
    "ports": [
      {
        "name": "http",
        "port": 80,
        "protocol": "TCP",
        "targetPort": 8080,
        "nodePort": 30079
      },
      {
        "name": "https",
        "port": 443,
        "protocol": "TCP",
        "targetPort": 8080,
        "nodePort": 30080
      }
    ]
  }
}'

log "STEP 7: ArgoCD admin password is stored in secret argocd/argocd-initial-admin-secret"

# ── STEP 8: Prometheus + Grafana stack (Grafana NodePort 30030) ───────────────
log "STEP 8: Installing kube-prometheus-stack"
helm repo add prometheus-community \
  https://prometheus-community.github.io/helm-charts --force-update
helm repo update prometheus-community
helm upgrade --install kube-prometheus-stack \
  prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace \
  --set grafana.service.type=NodePort \
  --set grafana.service.nodePort=30030 \
  --set grafana.adminPassword="${GRAFANA_ADMIN_PASSWORD}" \
  --set prometheus.prometheusSpec.scrapeInterval=30s \
  --wait --timeout=10m

# ── STEP 9: OpenCost (NodePort 30040) ─────────────────────────────────────────
log "STEP 9: Installing OpenCost"
chmod +x "${REPO_DIR}/infra/aws/scripts/install-opencost.sh"
"${REPO_DIR}/infra/aws/scripts/install-opencost.sh"

# ── STEP 10: ClickHouse via Altinity Operator ──────────────────────────────────
log "STEP 10: Installing Altinity ClickHouse Operator"
kubectl apply -f \
  https://raw.githubusercontent.com/Altinity/clickhouse-operator/master/deploy/operator/clickhouse-operator-install-bundle.yaml

log "STEP 10: Waiting for clickhouse-operator pod"
kubectl wait pods -n kube-system -l app=clickhouse-operator \
  --for=condition=Ready --timeout=300s

kubectl create namespace clickhouse --dry-run=client -o yaml | kubectl apply -f -

log "STEP 10: Creating ClickHouseInstallation CR"
kubectl apply -n clickhouse -f - <<CHEOF
apiVersion: clickhouse.altinity.com/v1
kind: ClickHouseInstallation
metadata:
  name: taonode
spec:
  configuration:
    clusters:
      - name: cluster
        layout:
          shardsCount: 1
          replicasCount: 1
    users:
      guardian/password: ${CLICKHOUSE_PASSWORD}
      guardian/networks/ip: "::/0"
  defaults:
    templates:
      podTemplate: default
      dataVolumeClaimTemplate: data-volume
  templates:
    podTemplates:
      - name: default
        spec:
          containers:
            - name: clickhouse
              image: clickhouse/clickhouse-server:24.3
              resources:
                requests:
                  cpu: "1"
                  memory: "4Gi"
                limits:
                  cpu: "2"
                  memory: "8Gi"
    volumeClaimTemplates:
      - name: data-volume
        spec:
          accessModes:
            - ReadWriteOnce
          resources:
            requests:
              storage: 20Gi
CHEOF

log "STEP 10: Waiting for ClickHouse pods (image pull may take several minutes)"
kubectl wait pods -n clickhouse \
  -l "clickhouse.altinity.com/chi=taonode" \
  --for=condition=Ready --timeout=600s

log "STEP 10: Creating taonode_guardian database"
CHPOD=$(kubectl get pods -n clickhouse -l "clickhouse.altinity.com/chi=taonode" \
  -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n clickhouse "${CHPOD}" -- \
  clickhouse-client --user guardian --password "${CLICKHOUSE_PASSWORD}" \
  --query="CREATE DATABASE IF NOT EXISTS taonode_guardian"

# ── STEP 11: Ollama ────────────────────────────────────────────────────────────
log "STEP 11: Installing Ollama (CPU inference)"
helm repo add ollama https://otwld.github.io/ollama-helm/ --force-update
helm repo update ollama
helm upgrade --install ollama ollama/ollama \
  --namespace ollama --create-namespace \
  --set ollama.gpu.enabled=false \
  --set resources.requests.cpu="1" \
  --set resources.requests.memory="4Gi" \
  --set resources.limits.cpu="4" \
  --set resources.limits.memory="16Gi" \
  --set service.type=ClusterIP \
  --wait --timeout=10m

log "STEP 11: Pre-pulling llama3.1:8b-instruct-q4_K_M model (background)"
OLLAMA_POD=$(kubectl get pods -n ollama -l app.kubernetes.io/name=ollama \
  -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n ollama "${OLLAMA_POD}" -- \
  ollama pull llama3.1:8b-instruct-q4_K_M &
OLLAMA_PULL_PID=$!

# ── STEP 12: TaoNode CRDs + Operator + Chain Simulator + Sample TaoNode ────────
log "STEP 12: Applying TaoNode Guardian CRDs"
kubectl apply -f "${REPO_DIR}/config/crd/bases/"

log "STEP 12: Applying RBAC manifests"
kubectl apply -f "${REPO_DIR}/config/rbac/"

log "STEP 12: Creating taonode-guardian-system namespace"
kubectl create namespace taonode-guardian-system \
  --dry-run=client -o yaml | kubectl apply -f -

log "STEP 12: Deploying TaoNode Guardian Operator"
kubectl apply -n taonode-guardian-system \
  -f "${REPO_DIR}/config/manager/manager.yaml"

kubectl rollout status deployment taonode-guardian-controller-manager \
  -n taonode-guardian-system --timeout=300s

log "STEP 12: Creating ClickHouse credentials secret for the operator"
kubectl create secret generic taonode-guardian-clickhouse \
  -n taonode-guardian-system \
  --from-literal=username=guardian \
  --from-literal=password="${CLICKHOUSE_PASSWORD}" \
  --dry-run=client -o yaml | kubectl apply -f -

log "STEP 12: Deploying chain-simulator (simulates :9616/health endpoint)"
kubectl apply -n default -f - <<'SIMEOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: chain-simulator
  labels:
    app: chain-simulator
spec:
  replicas: 1
  selector:
    matchLabels:
      app: chain-simulator
  template:
    metadata:
      labels:
        app: chain-simulator
    spec:
      containers:
        - name: probe
          image: ghcr.io/claudiobotelhosb/taonode-guardian-probe:latest
          imagePullPolicy: Always
          ports:
            - containerPort: 9616
          env:
            - name: SIMULATE_CHAIN
              value: "true"
          readinessProbe:
            httpGet:
              path: /health
              port: 9616
            initialDelaySeconds: 5
            periodSeconds: 10
---
apiVersion: v1
kind: Service
metadata:
  name: chain-simulator
spec:
  selector:
    app: chain-simulator
  ports:
    - name: probe
      port: 9616
      targetPort: 9616
SIMEOF

log "STEP 12: Applying sample TaoNode CR (miner-demo-sn1)"
kubectl apply -f - <<'TAOEOF'
apiVersion: tao.guardian.io/v1alpha1
kind: TaoNode
metadata:
  name: miner-demo-sn1
  namespace: taonode-guardian-system
  labels:
    tao.guardian.io/network: testnet
    tao.guardian.io/role: miner
    tao.guardian.io/subnet: "1"
spec:
  network: testnet
  subnetID: 1
  role: miner
  image: "ghcr.io/opentensor/subtensor:latest"
  version: "1.9.4"
  resources:
    requests:
      cpu: "1"
      memory: 2Gi
    limits:
      cpu: "2"
      memory: 4Gi
  chainStorage:
    storageClass: local-path
    size: 20Gi
  syncPolicy:
    maxBlockLag: 100
    recoveryStrategy: restart
    maxRestartAttempts: 3
    probeIntervalSeconds: 30
    syncTimeoutMinutes: 60
  monitoring:
    enabled: true
    port: 9615
  analytics:
    enabled: true
    clickhouseRef:
      endpoint: "clickhouse://clickhouse-taonode.clickhouse.svc:9000"
      database: taonode_guardian
      credentialsSecret: taonode-guardian-clickhouse
      tls: false
    ingestion:
      batchSize: 1000
      flushIntervalSeconds: 10
      chainEvents: true
      minerTelemetry: true
      reconcileAudit: true
    anomalyDetection:
      enabled: true
      evaluationIntervalSeconds: 60
      syncDriftThreshold: 50
      diskExhaustionHorizonHours: 48
  aiAdvisor:
    enabled: true
    endpoint: "http://ollama.ollama.svc:11434"
    model: "llama3.1:8b-instruct-q4_K_M"
    timeoutSeconds: 30
    minAnomalyScoreForAdvisory: "0.6"
    contextWindowTokens: 4096
  nodeSelector:
    kubernetes.io/os: linux
TAOEOF

# ── Wait for background Ollama model pull to finish ───────────────────────────
log "Waiting for Ollama model pull to complete (PID ${OLLAMA_PULL_PID})"
wait "${OLLAMA_PULL_PID}" || log "Ollama model pull completed (or was already cached)"

# ── Summary ───────────────────────────────────────────────────────────────────
PUBLIC_IP=$(curl -sf http://169.254.169.254/latest/meta-data/public-ipv4 || echo "<public-ip>")

log "============================================================"
log "Bootstrap complete!"
log ""
log "  Kubeconfig:  scp ubuntu@${PUBLIC_IP}:/etc/rancher/k3s/k3s.yaml ~/.kube/taonode.yaml"
log ""
log "  SSH tunnels (one per tab):"
log "    ssh -N -L 30030:localhost:30030 ubuntu@${PUBLIC_IP}  # Grafana    -> http://localhost:30030"
log "    ssh -N -L 30080:localhost:30080 ubuntu@${PUBLIC_IP}  # ArgoCD     -> https://localhost:30080"
log "    ssh -N -L 30040:localhost:30040 ubuntu@${PUBLIC_IP}  # OpenCost   -> http://localhost:30040"
log ""
log "  kubectl get taonode -A"
log "============================================================"
