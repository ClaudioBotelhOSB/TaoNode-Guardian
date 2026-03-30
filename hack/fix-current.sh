#!/usr/bin/env bash
# hack/fix-current.sh
# Corrige cluster K3s existente: TLS SAN, Klipper, servicos LoadBalancer, kubeconfig.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TF_DIR="${SCRIPT_DIR}/../infra/aws"
SSH_KEY="${SSH_KEY:-${HOME}/.ssh/taonode-demo}"
SSH_USER="${SSH_USER:-ubuntu}"
KUBECONFIG_PATH="${KUBECONFIG_PATH:-${HOME}/.kube/taonode-aws.yaml}"
REGION="${AWS_DEFAULT_REGION:-us-east-1}"
SG_NAME="${SG_NAME:-taonode-guardian-k3s-sg}"

SSH_OPTS=(-i "${SSH_KEY}" -o StrictHostKeyChecking=no -o ConnectTimeout=10 -o BatchMode=yes)

log()  { echo "[fix] $*"; }
ok()   { echo "[fix] OK: $*"; }
warn() { echo "[fix] WARN: $*"; }
fail() { echo "[fix] FAIL: $*" >&2; exit 1; }
step() { echo; echo "=== $* ==="; }

# ── SG helpers ────────────────────────────────────────────────────────────────
detect_public_cidr() {
  local ip
  for url in "https://checkip.amazonaws.com" "https://api.ipify.org" "https://ifconfig.me/ip"; do
    ip="$(curl -fsS --max-time 5 "$url" | tr -d '[:space:]')" || true
    [[ "$ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] && echo "${ip}/32" && return 0
  done
  return 1
}

open_port() {
  local sg_id="$1" cidr="$2" port="$3"
  local out
  out="$(aws ec2 authorize-security-group-ingress \
    --region "$REGION" --group-id "$sg_id" \
    --ip-permissions "IpProtocol=tcp,FromPort=${port},ToPort=${port},IpRanges=[{CidrIp=${cidr}}]" \
    2>&1)" && ok "tcp/${port} aberto" \
  || { grep -q "InvalidPermission.Duplicate" <<<"$out" && warn "tcp/${port} ja existe" \
       || { echo "$out" >&2; fail "falha tcp/${port}"; }; }
}

# ── 1. IP da EC2 ──────────────────────────────────────────────────────────────
step "1/5 - IP da EC2"

if [[ -z "${EC2_IP:-}" ]]; then
  EC2_IP="$(terraform -chdir="${TF_DIR}" output -raw instance_ip 2>/dev/null)" || true
fi
if [[ -z "${EC2_IP:-}" ]]; then
  EC2_IP="$(aws ec2 describe-instances --region "$REGION" \
    --filters "Name=tag:Name,Values=taonode-guardian-k3s" "Name=instance-state-name,Values=running" \
    --query "Reservations[0].Instances[0].PublicIpAddress" --output text 2>/dev/null)" || true
  [[ "${EC2_IP}" == "None" ]] && EC2_IP=""
fi
[[ -z "${EC2_IP:-}" ]] && fail "nao foi possivel descobrir o IP. Use: EC2_IP=<ip> bash hack/fix-current.sh"
ok "EC2_IP = ${EC2_IP}"

# ── 2. Abrir portas SSH + K3s API no SG ───────────────────────────────────────
step "2/5 - Security Group"

MY_CIDR="${PUBLIC_CIDR:-}"
if [[ -z "$MY_CIDR" ]]; then
  MY_CIDR="$(detect_public_cidr)" || fail "nao foi possivel detectar IP local. Use: PUBLIC_CIDR=x.x.x.x/32"
fi
[[ "$MY_CIDR" != */* ]] && MY_CIDR="${MY_CIDR}/32"
log "CIDR local: ${MY_CIDR}"

SG_ID="${SG_ID:-$(aws ec2 describe-security-groups --region "$REGION" \
  --filters "Name=group-name,Values=${SG_NAME}" \
  --query "SecurityGroups[0].GroupId" --output text)}"
log "SG: ${SG_ID}"

open_port "$SG_ID" "$MY_CIDR" 22
open_port "$SG_ID" "$MY_CIDR" 6443
sleep 5

# ── 3. Fix remoto: cert K3s + Klipper + servicos ──────────────────────────────
step "3/5 - Certificado K3s + Klipper + servicos"
log "Conectando a EC2..."

ssh "${SSH_OPTS[@]}" "${SSH_USER}@${EC2_IP}" bash -s -- "${EC2_IP}" << 'REMOTE'
set -euo pipefail
EC2_IP="$1"

echo "[remote] config.yaml: tls-san = ${EC2_IP}"
sudo tee /etc/rancher/k3s/config.yaml > /dev/null << YAMLEOF
tls-san:
  - ${EC2_IP}
YAMLEOF

echo "[remote] Removendo --disable servicelb do systemd..."
sudo sed -i 's/ --disable servicelb//' /etc/systemd/system/k3s.service
sudo systemctl daemon-reload

echo "[remote] Parando K3s..."
sudo systemctl stop k3s

echo "[remote] Removendo certificados antigos..."
sudo rm -rf /var/lib/rancher/k3s/server/tls

echo "[remote] Iniciando K3s..."
sudo systemctl start k3s

echo "[remote] Aguardando API server..."
until sudo kubectl get nodes > /dev/null 2>&1; do sleep 3; done

echo "[remote] Aguardando node Ready..."
sudo kubectl wait --for=condition=Ready nodes --all --timeout=120s

echo "[remote] Aguardando Klipper pods (max 90s)..."
FOUND=false
for i in $(seq 1 30); do
  COUNT=$(kubectl get pods -n kube-system -l svccontroller.k3s.cattle.io/svcname --no-headers 2>/dev/null | wc -l)
  if [ "$COUNT" -gt 0 ]; then
    FOUND=true
    break
  fi
  sleep 3
done
if [ "$FOUND" = "true" ]; then
  echo "[remote] Klipper ativo"
else
  echo "[remote] WARN: Klipper pods nao encontrados ainda (pode demorar mais)"
fi

echo "[remote] Patch ArgoCD: LoadBalancer porta 8080..."
kubectl patch svc argocd-server -n argocd --type=merge \
  -p '{"spec":{"type":"LoadBalancer","ports":[{"name":"http","port":8080,"protocol":"TCP","targetPort":8080}]}}'

ARGS=$(kubectl get deployment argocd-server -n argocd \
  -o jsonpath='{.spec.template.spec.containers[0].args}' 2>/dev/null || echo "")
if echo "$ARGS" | grep -q insecure; then
  echo "[remote] ArgoCD --insecure ja configurado"
else
  echo "[remote] Adicionando --insecure ao argocd-server..."
  kubectl patch deployment argocd-server -n argocd --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--insecure"}]'
fi

echo "[remote] Patch Grafana: LoadBalancer porta 3000..."
kubectl patch svc kube-prometheus-stack-grafana -n monitoring --type=merge \
  -p '{"spec":{"type":"LoadBalancer","ports":[{"name":"http","port":3000,"protocol":"TCP","targetPort":3000}]}}' \
  2>/dev/null && echo "[remote] Grafana patchado" || echo "[remote] WARN: Grafana svc nao encontrado ainda (ArgoCD ainda sincronizando)"

echo "[remote] Servicos LoadBalancer:"
kubectl get svc -A --no-headers 2>/dev/null | grep LoadBalancer || echo "(nenhum ainda)"

echo "[remote] Concluido."
REMOTE

ok "Fix remoto concluido"

# ── 4. Kubeconfig ─────────────────────────────────────────────────────────────
step "4/5 - Kubeconfig"
mkdir -p "$(dirname "${KUBECONFIG_PATH}")"
scp "${SSH_OPTS[@]}" "${SSH_USER}@${EC2_IP}:/etc/rancher/k3s/k3s.yaml" "${KUBECONFIG_PATH}"
TMP=$(mktemp)
sed "s/127.0.0.1/${EC2_IP}/" "${KUBECONFIG_PATH}" > "${TMP}" && mv "${TMP}" "${KUBECONFIG_PATH}"
chmod 600 "${KUBECONFIG_PATH}"
export KUBECONFIG="${KUBECONFIG_PATH}"
kubectl get nodes > /dev/null 2>&1 && ok "kubectl OK" || fail "kubectl falhou. Verifique kubeconfig."

# ── 5. Portas demo + senhas ───────────────────────────────────────────────────
step "5/5 - Acesso"
EC2_IP="${EC2_IP}" KUBECONFIG="${KUBECONFIG_PATH}" bash "${SCRIPT_DIR}/open-access.sh"
