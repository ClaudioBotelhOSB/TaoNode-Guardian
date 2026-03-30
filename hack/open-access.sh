#!/usr/bin/env bash
# hack/open-access.sh
# =============================================================================
# Zero-Touch Demo Access — TaoNode Guardian
# =============================================================================
# Executar da máquina local APÓS terraform apply + ArgoCD sync.
#
# O IP da EC2 muda a cada terraform destroy + apply.
# O script resolve o IP atual em 3 camadas (primeira que funcionar vence):
#   1. EC2_IP=<ip> bash hack/open-access.sh     (override manual explícito)
#   2. terraform output -raw instance_ip         (estado local do Terraform)
#   3. aws ec2 describe-instances por tag Name   (AWS CLI puro, sem estado)
#
# Uso:
#   bash hack/open-access.sh
#   EC2_IP=<ip> bash hack/open-access.sh         # override direto
#   PUBLIC_CIDR=203.0.113.5/32 bash hack/open-access.sh  # força CIDR da sala
# =============================================================================
set -euo pipefail

# ── Configuração ──────────────────────────────────────────────────────────────
REGION="${AWS_DEFAULT_REGION:-us-east-1}"
SG_NAME="${SG_NAME:-taonode-guardian-k3s-sg}"
KUBECONFIG="${KUBECONFIG:-${HOME}/.kube/taonode-aws.yaml}"
TF_DIR="${TF_DIR:-$(dirname "$0")/../infra/aws}"
INPUT_CIDR="${1:-${PUBLIC_CIDR:-}}"
ARGOCD_TIMEOUT="${ARGOCD_TIMEOUT:-300}"  # segundos aguardando ArgoCD

export KUBECONFIG

# ── Cores ─────────────────────────────────────────────────────────────────────
BOLD=$'\033[1m'; RESET=$'\033[0m'
CYAN=$'\033[1;36m'; GREEN=$'\033[1;32m'; YELLOW=$'\033[1;33m'; RED=$'\033[1;31m'

log()    { printf "${CYAN}[access]${RESET} %s\n" "$*"; }
ok()     { printf "${GREEN}[access] ✓ %s${RESET}\n" "$*"; }
warn()   { printf "${YELLOW}[access] ⚠ %s${RESET}\n" "$*"; }
fail()   { printf "${RED}[access] ✗ %s${RESET}\n" "$*" >&2; exit 1; }
header() { printf "\n${BOLD}%s${RESET}\n%s\n" "$1" "$(printf '─%.0s' {1..60})"; }

require_cmd() { command -v "$1" >/dev/null 2>&1 || fail "comando necessário não encontrado: $1"; }

# ── Pré-requisitos ────────────────────────────────────────────────────────────
require_cmd aws
require_cmd kubectl
require_cmd curl

# ── 1. Descobrir IP da EC2 ────────────────────────────────────────────────────
header "1/5 — IP da EC2"

# Camada 3: AWS CLI — não depende de estado local do Terraform
resolve_ec2_ip_from_aws() {
  local ip
  ip="$(
    aws ec2 describe-instances \
      --region "${REGION}" \
      --filters \
        "Name=tag:Name,Values=taonode-guardian-k3s" \
        "Name=instance-state-name,Values=running" \
      --query "Reservations[0].Instances[0].PublicIpAddress" \
      --output text 2>/dev/null
  )"
  # describe-instances retorna "None" quando não encontra
  [[ -n "$ip" && "$ip" != "None" ]] && printf '%s\n' "$ip"
}

if [[ -n "${EC2_IP:-}" ]]; then
  # Camada 1: variável de ambiente explícita
  log "EC2_IP definido via variável de ambiente: ${EC2_IP}"

elif EC2_IP="$(terraform -chdir="${TF_DIR}" output -raw instance_ip 2>/dev/null)" \
     && [[ -n "${EC2_IP}" ]]; then
  # Camada 2: terraform output (estado local)
  log "IP obtido via terraform output"

elif EC2_IP="$(resolve_ec2_ip_from_aws)" && [[ -n "${EC2_IP}" ]]; then
  # Camada 3: AWS CLI por tag Name=taonode-guardian-k3s
  log "IP obtido via AWS CLI (describe-instances)"

else
  fail "não foi possível descobrir o IP da EC2.
       Opções:
         EC2_IP=<ip> bash hack/open-access.sh
         terraform -chdir=infra/aws output instance_ip
         aws ec2 describe-instances --filters Name=tag:Name,Values=taonode-guardian-k3s"
fi

ok "EC2_IP = ${EC2_IP}"

# ── 2. Abrir portas no AWS SG ─────────────────────────────────────────────────
header "2/5 — AWS Security Group"

detect_public_cidr() {
  local ip
  for url in \
    "https://checkip.amazonaws.com" \
    "https://api.ipify.org" \
    "https://ifconfig.me/ip"
  do
    ip="$(curl -fsS --max-time 5 "$url" | tr -d '[:space:]')" || true
    if [[ "$ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
      printf '%s/32\n' "$ip"; return 0
    fi
  done
  return 1
}

validate_cidr() {
  [[ "$1" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$ ]] \
    || fail "CIDR inválido: $1"
}

resolve_sg_id() {
  if [[ -n "${SG_ID:-}" ]]; then printf '%s\n' "$SG_ID"; return 0; fi
  mapfile -t matches < <(
    aws ec2 describe-security-groups \
      --region "$REGION" \
      --filters "Name=group-name,Values=${SG_NAME}" \
      --query "SecurityGroups[].GroupId" \
      --output text | tr '\t' '\n' | sed '/^$/d'
  )
  case "${#matches[@]}" in
    0) fail "Security Group '${SG_NAME}' não encontrado na região ${REGION}" ;;
    1) printf '%s\n' "${matches[0]}" ;;
    *) fail "múltiplos SGs com nome '${SG_NAME}'; exporte SG_ID explicitamente" ;;
  esac
}

authorize_port() {
  local sg_id="$1" cidr="$2" port="$3" desc="$4" output
  if output="$(
    aws ec2 authorize-security-group-ingress \
      --region "$REGION" \
      --group-id "$sg_id" \
      --ip-permissions \
        "IpProtocol=tcp,FromPort=${port},ToPort=${port},IpRanges=[{CidrIp=${cidr},Description='${desc}'}]" \
      2>&1
  )"; then
    ok "tcp/${port} aberto para ${cidr}"
  elif grep -q "InvalidPermission.Duplicate" <<<"$output"; then
    warn "tcp/${port} para ${cidr} já existe — ignorando"
  else
    printf '%s\n' "$output" >&2
    fail "falha ao abrir tcp/${port}"
  fi
}

if [[ -n "$INPUT_CIDR" ]]; then
  MY_CIDR="$INPUT_CIDR"
  [[ "$MY_CIDR" == */* ]] || MY_CIDR="${MY_CIDR}/32"
else
  log "detectando IP público do operador..."
  MY_CIDR="$(detect_public_cidr)" \
    || fail "não foi possível detectar o IP. Use: bash $0 <seu-cidr>/32"
fi
validate_cidr "$MY_CIDR"

SG_ID="$(resolve_sg_id)"
log "SG: ${SG_ID} | CIDR: ${MY_CIDR}"

authorize_port "$SG_ID" "$MY_CIDR" 3000  "Grafana LB - demo"
authorize_port "$SG_ID" "$MY_CIDR" 8080  "ArgoCD HTTP - demo"
authorize_port "$SG_ID" "$MY_CIDR" 8123  "ClickHouse HTTP - demo"

# ── 3. Aguardar ArgoCD ────────────────────────────────────────────────────────
header "3/5 — Aguardando ArgoCD"
log "timeout: ${ARGOCD_TIMEOUT}s (ARGOCD_TIMEOUT para ajustar)"

log "Aguardando namespace argocd existir..."
DEADLINE=$(( $(date +%s) + ARGOCD_TIMEOUT ))
until kubectl get ns argocd > /dev/null 2>&1; do
  [ "$(date +%s)" -ge "$DEADLINE" ] && fail "timeout: namespace argocd nao apareceu. Verifique KUBECONFIG=${KUBECONFIG}"
  sleep 5
done
ok "namespace argocd encontrado"

kubectl wait pods \
  -n argocd \
  -l app.kubernetes.io/name=argocd-server \
  --for=condition=Ready \
  --timeout="${ARGOCD_TIMEOUT}s" \
  && ok "argocd-server pronto" \
  || fail "argocd-server nao ficou Ready em ${ARGOCD_TIMEOUT}s"

# ── 4. Extrair senhas ─────────────────────────────────────────────────────────
header "4/5 — Senhas"

# ArgoCD — initial-admin-secret (apagado após primeira rotação)
ARGOCD_PASSWORD=""
if kubectl get secret argocd-initial-admin-secret -n argocd &>/dev/null; then
  ARGOCD_PASSWORD="$(
    kubectl get secret argocd-initial-admin-secret -n argocd \
      -o jsonpath='{.data.password}' | base64 -d
  )"
  ok "ArgoCD — senha inicial extraída"
else
  warn "argocd-initial-admin-secret não encontrado (já foi rotacionada?)"
  ARGOCD_PASSWORD="<rotacionada — use 'argocd account update-password'>"
fi

# Grafana — grafana-admin-secret no namespace monitoring
GRAFANA_PASSWORD=""
if kubectl get secret grafana-admin-secret -n monitoring &>/dev/null; then
  GRAFANA_PASSWORD="$(
    kubectl get secret grafana-admin-secret -n monitoring \
      -o jsonpath='{.data.admin-password}' | base64 -d
  )"
  ok "Grafana — senha extraída"
else
  warn "grafana-admin-secret não encontrado no namespace monitoring"
  GRAFANA_PASSWORD="<não disponível — verifique o namespace>"
fi

# ── 5. Resumo de Acesso ───────────────────────────────────────────────────────
header "5/5 — Resumo de Acesso"

echo ""
echo "  TaoNode Guardian — Acesso Demo"
echo "  ================================================"
echo ""
echo "  Grafana     http://${EC2_IP}:3000"
echo "               Usuario: admin"
echo "               Senha:   ${GRAFANA_PASSWORD}"
echo ""
echo "  ArgoCD      http://${EC2_IP}:8080"
echo "               Usuario: admin"
echo "               Senha:   ${ARGOCD_PASSWORD}"
echo ""
echo "  ClickHouse  http://${EC2_IP}:8123/play"
echo ""
echo "  Kubecost    http://${EC2_IP}:9090"
echo ""
echo "  ================================================"
echo ""
