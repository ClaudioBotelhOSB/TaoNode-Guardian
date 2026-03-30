#!/bin/bash
# =============================================================================
# TaoNode Guardian — K3s Bootstrap Script
# =============================================================================
# Executed as root via EC2 user_data / cloud-init on first boot.
# Output: /var/log/cloud-init-output.log
#
# DESIGN PRINCIPLE — "Bootstrap installs the plane; ArgoCD owns the apps"
# ─────────────────────────────────────────────────────────────────────────────
# This script does exactly FOUR things then exits:
#   1. Install K3s + tooling (helm, kustomize)
#   2. Install cert-manager (ArgoCD depends on it for webhooks)
#   3. Install ArgoCD
#   4. Create mandatory pre-ArgoCD secrets (ghcr-login-secret, grafana password,
#      clickhouse credentials) and apply the Root App-of-Apps
#
# Everything else (Prometheus, Kubecost, Ollama, ClickHouse, TaoNode Operator,
# TaoNode CRs) is reconciled by ArgoCD from the Git repository.
# =============================================================================
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
export PATH="/usr/local/bin:${PATH}"

GITHUB_USER="ClaudioBotelhOSB"
REPO_URL="https://${GITHUB_USER}:${GITHUB_TOKEN}@github.com/${GITHUB_USER}/taonode-guardian.git"
REPO_DIR="/opt/taonode-guardian"
STATE_DIR="/var/lib/taonode-guardian"

# GHCR_PAT is injected by Terraform via user_data templating.
# It must have `read:packages` scope to pull ghcr.io/claudiobotelhosb/taonode-guardian:latest
GHCR_USER="claudiobotelhosb"   # lowercase — required by ghcr.io
GHCR_EMAIL="botelho.claudiosb@gmail.com"

# ── Helpers ───────────────────────────────────────────────────────────────────
log()  { echo "[$(date '+%Y-%m-%d %H:%M:%S')] [BOOTSTRAP] $*"; }

wait_ready() {
  local ns="$1"; shift
  log "Waiting for all pods in '${ns}' to be Ready (timeout 10m)..."
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

# ── STEP 1: System dependencies ──────────────────────────────────────────────
log "STEP 1: Installing system dependencies"
apt-get update -y
apt-get install -y curl wget git jq ca-certificates apt-transport-https gnupg

log "STEP 1: Installing Helm"
curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash

log "STEP 1: Installing kustomize v5.4.2"
KUSTOMIZE_VERSION="5.4.2"
curl -fsSL \
  "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv${KUSTOMIZE_VERSION}/kustomize_v${KUSTOMIZE_VERSION}_linux_amd64.tar.gz" \
  | tar -xz -C /usr/local/bin kustomize
chmod +x /usr/local/bin/kustomize

log "STEP 1: Pre-generating persistent secrets"
mkdir -p "${STATE_DIR}"
chmod 700 "${STATE_DIR}"
ensure_secret_file "${STATE_DIR}/grafana-admin-password"
ensure_secret_file "${STATE_DIR}/clickhouse-password"
GRAFANA_ADMIN_PASSWORD=$(cat "${STATE_DIR}/grafana-admin-password")
CLICKHOUSE_PASSWORD=$(cat "${STATE_DIR}/clickhouse-password")

log "STEP 1: Cloning repository"
if [ -d "${REPO_DIR}/.git" ]; then
  git -C "${REPO_DIR}" pull --ff-only
else
  git clone --depth=1 "${REPO_URL}" "${REPO_DIR}"
fi

# ── STEP 2: K3s (no traefik — Klipper servicelb enabled for LoadBalancer) ────
# --tls-san adds the EC2 public IP to the K3s TLS certificate so that kubectl
# from outside the EC2 works without --insecure-skip-tls-verify.
log "STEP 2: Fetching EC2 public IP for TLS SAN"
EC2_PUBLIC_IP=$(curl -sf --max-time 5 http://169.254.169.254/latest/meta-data/public-ipv4 || echo "")
if [[ -z "$EC2_PUBLIC_IP" ]]; then
  log "WARNING: could not fetch public IP from metadata — kubectl from outside will require --insecure-skip-tls-verify"
fi

log "STEP 2: Installing K3s (tls-san: ${EC2_PUBLIC_IP:-none})"
EXEC_ARGS="server --disable traefik --write-kubeconfig-mode 644"
[[ -n "$EC2_PUBLIC_IP" ]] && EXEC_ARGS="${EXEC_ARGS} --tls-san ${EC2_PUBLIC_IP}"
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="${EXEC_ARGS}" sh -

# Persist KUBECONFIG for interactive SSH sessions
echo 'export KUBECONFIG=/etc/rancher/k3s/k3s.yaml' > /etc/profile.d/k3s.sh
chmod +x /etc/profile.d/k3s.sh

# ── STEP 3: Wait for K3s API + node Ready ────────────────────────────────────
log "STEP 3: Waiting for K3s API server"
until kubectl get nodes &>/dev/null; do sleep 5; done

log "STEP 3: Allowing 15 s for metadata stabilisation"
sleep 15

log "STEP 3: Waiting for node Ready state"
for i in {1..5}; do
  kubectl wait --for=condition=Ready nodes --all --timeout=60s && break || sleep 10
done

# ── STEP 4: cert-manager ──────────────────────────────────────────────────────
log "STEP 4: Installing cert-manager (ArgoCD webhook dependency)"
helm repo add jetstack https://charts.jetstack.io --force-update
helm repo update jetstack
helm upgrade --install cert-manager jetstack/cert-manager \
  --namespace cert-manager --create-namespace \
  --set installCRDs=true \
  --wait --timeout=5m

wait_ready cert-manager
log "STEP 4: Allowing 20 s for cert-manager webhook TLS registration"
sleep 20

# ── STEP 5: ArgoCD ────────────────────────────────────────────────────────────
log "STEP 5: Installing ArgoCD"
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
kubectl apply --server-side -n argocd \
  -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml

log "STEP 5: Waiting for ArgoCD server"
kubectl rollout status deployment argocd-server -n argocd --timeout=600s

log "STEP 5: Patching argocd-server to LoadBalancer (HTTP port 8080)"
kubectl patch svc argocd-server -n argocd --type='merge' -p '{
  "spec": {
    "type": "LoadBalancer",
    "ports": [
      {
        "name": "http",
        "port": 8080,
        "protocol": "TCP",
        "targetPort": 8080
      }
    ]
  }
}'

log "STEP 5: Disabling argocd-server TLS (HTTP-only demo mode)"
kubectl patch deployment argocd-server -n argocd --type='json' \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--insecure"}]'

log "STEP 5: Creating ArgoCD repo credentials for private GitHub repo"
kubectl apply -f - <<ARGOCD_REPO
apiVersion: v1
kind: Secret
metadata:
  name: github-repo-creds
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  type: git
  url: https://github.com/${GITHUB_USER}/taonode-guardian
  username: ${GITHUB_USER}
  password: ${GITHUB_TOKEN}
ARGOCD_REPO

# ── STEP 6: Pre-ArgoCD secrets ────────────────────────────────────────────────
# These secrets must exist BEFORE ArgoCD syncs the child apps because:
#   - ghcr-login-secret  → imagePullSecret for the TaoNode Guardian image
#   - grafana-admin-secret → consumed by the kube-prometheus-stack Helm values
#   - taonode-guardian-clickhouse → consumed by the operator Deployment
#
# They are created with --dry-run=client | kubectl apply so re-runs are idempotent.

log "STEP 6: Creating namespace taonode-guardian-system"
kubectl create namespace taonode-guardian-system \
  --dry-run=client -o yaml | kubectl apply -f -

log "STEP 6: Creating ghcr-login-secret in taonode-guardian-system"
kubectl create secret docker-registry ghcr-login-secret \
  --docker-server=ghcr.io \
  --docker-username="${GHCR_USER}" \
  --docker-password="${GHCR_PAT}" \
  --docker-email="${GHCR_EMAIL}" \
  -n taonode-guardian-system \
  --dry-run=client -o yaml | kubectl apply -f -

log "STEP 6: Creating namespace monitoring"
kubectl create namespace monitoring \
  --dry-run=client -o yaml | kubectl apply -f -

log "STEP 6: Creating grafana-admin-secret in monitoring namespace"
kubectl create secret generic grafana-admin-secret \
  -n monitoring \
  --from-literal=admin-password="${GRAFANA_ADMIN_PASSWORD}" \
  --from-literal=admin-user=admin \
  --dry-run=client -o yaml | kubectl apply -f -

log "STEP 6: Creating namespace clickhouse"
kubectl create namespace clickhouse \
  --dry-run=client -o yaml | kubectl apply -f -

log "STEP 6: Creating taonode-guardian-clickhouse secret"
kubectl create secret generic taonode-guardian-clickhouse \
  -n taonode-guardian-system \
  --from-literal=endpoint="clickhouse://clickhouse-taonode.clickhouse.svc:9000" \
  --from-literal=username=guardian \
  --from-literal=password="${CLICKHOUSE_PASSWORD}" \
  --from-literal=database=taonode_guardian \
  --dry-run=client -o yaml | kubectl apply -f -

# Mirror the clickhouse secret into the clickhouse namespace for the CR
kubectl create secret generic taonode-guardian-clickhouse \
  -n clickhouse \
  --from-literal=password="${CLICKHOUSE_PASSWORD}" \
  --dry-run=client -o yaml | kubectl apply -f -

# ── STEP 7: Altinity ClickHouse Operator ─────────────────────────────────────
# Installed imperatively because Altinity does not publish a Helm chart in a
# stable registry compatible with ArgoCD multi-source. The operator CRDs must
# exist before the ClickHouseInstallation CR (managed by ArgoCD foundations app).
log "STEP 7: Installing Altinity ClickHouse Operator"
kubectl apply -f \
  https://raw.githubusercontent.com/Altinity/clickhouse-operator/master/deploy/operator/clickhouse-operator-install-bundle.yaml

log "STEP 7: Waiting for clickhouse-operator pod to be scheduled"
until [ "$(kubectl get pods -n kube-system -l app=clickhouse-operator --no-headers 2>/dev/null | wc -l)" -gt 0 ]; do
  sleep 3
done
kubectl wait pods -n kube-system -l app=clickhouse-operator \
  --for=condition=Ready --timeout=300s

# ── STEP 8: Apply Root App-of-Apps → ArgoCD takes over ───────────────────────
# From this point, ArgoCD reconciles ALL workloads declared in argocd/apps/:
#   • foundations  (wave 1) — cert-manager, kube-prometheus-stack, ClickHouse CR, Ollama
#   • observability (wave 1) — Kubecost 2.8.0, TaoNode ServiceMonitor
#   • control-plane (wave 2) — TaoNode Guardian CRDs + Operator (ghcr.io image)
#   • data-plane    (wave 3) — TaoNode CRs (sample miner)
log "STEP 8: Applying Root App-of-Apps"
kubectl apply -n argocd -f "${REPO_DIR}/argocd/apps/root.yaml"

# ── STEP 9: Wait for ClickHouse and seed demo data ──────────────────────────
# ArgoCD will deploy the ClickHouseInstallation CR (wave 1). We wait for the
# pod to be Ready, then seed the taonode_guardian database with demo data.
log "STEP 9: Waiting for ClickHouse pod (up to 5 min)"
CH_READY=false
for i in $(seq 1 60); do
  if kubectl get pod chi-clickhouse-taonode-taonode-0-0-0 -n clickhouse \
       --no-headers 2>/dev/null | grep -q '1/1.*Running'; then
    CH_READY=true
    break
  fi
  sleep 5
done

if [ "${CH_READY}" = "true" ]; then
  log "STEP 9: ClickHouse is Ready — seeding demo data"
  kubectl exec -i -n clickhouse chi-clickhouse-taonode-taonode-0-0-0 -- \
    clickhouse-client --multiquery < "${REPO_DIR}/hack/clickhouse-seed.sql"
  log "STEP 9: Seed data loaded"
else
  log "STEP 9: WARNING — ClickHouse not ready after 5 min, skipping seed. Run manually:"
  log "  kubectl exec -i -n clickhouse chi-clickhouse-taonode-taonode-0-0-0 -- clickhouse-client --multiquery < ${REPO_DIR}/hack/clickhouse-seed.sql"
fi

# ── Summary ───────────────────────────────────────────────────────────────────
PUBLIC_IP=$(curl -sf http://169.254.169.254/latest/meta-data/public-ipv4 || echo "<public-ip>")

log "============================================================"
log "Bootstrap complete! ArgoCD is now reconciling all workloads."
log ""
log "  Kubeconfig (copy from remote):"
log "    scp -i ~/.ssh/taonode-demo ubuntu@${PUBLIC_IP}:/etc/rancher/k3s/k3s.yaml ~/.kube/taonode-aws.yaml"
log "    export KUBECONFIG=~/.kube/taonode-aws.yaml"
log ""
log "  ArgoCD initial admin password:"
log "    kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d"
log ""
log "  Grafana admin password:"
log "    cat /var/lib/taonode-guardian/grafana-admin-password"
log ""
log "  Access URLs (Klipper LoadBalancer — run infra/aws/scripts/open-demo-ports.sh to open SG):"
log "    Grafana    → http://${PUBLIC_IP}:3000"
log "    ArgoCD     → http://${PUBLIC_IP}:8080"
log "    ClickHouse → http://${PUBLIC_IP}:8123/play"
log ""
log "  Watch ArgoCD sync:"
log "    kubectl get applications -n argocd -w"
log "============================================================"
