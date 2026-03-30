#!/usr/bin/env bash
# hack/up.sh
# =============================================================================
# TaoNode Guardian — Deploy completo em um comando
# =============================================================================
# Uso:
#   bash hack/up.sh              # aplica terraform + kubeconfig + acesso
#   bash hack/up.sh --no-apply   # pula o terraform apply (cluster já existe)
#
# Variáveis de ambiente opcionais:
#   SSH_KEY           caminho para a chave SSH     (padrão: ~/.ssh/taonode-demo)
#   SSH_USER          usuário SSH da EC2            (padrão: ubuntu)
#   KUBECONFIG_PATH   destino do kubeconfig local   (padrão: ~/.kube/taonode-aws.yaml)
#   PUBLIC_CIDR       CIDR para abrir no SG         (padrão: detectado automaticamente)
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TF_DIR="${SCRIPT_DIR}/../infra/aws"
SSH_KEY="${SSH_KEY:-${HOME}/.ssh/taonode-demo}"
SSH_USER="${SSH_USER:-ubuntu}"
KUBECONFIG_PATH="${KUBECONFIG_PATH:-${HOME}/.kube/taonode-aws.yaml}"
SKIP_APPLY=false

[[ "${1:-}" == "--no-apply" ]] && SKIP_APPLY=true

# ── Cores ─────────────────────────────────────────────────────────────────────
BOLD='\033[1m'; RESET='\033[0m'
CYAN='\033[1;36m'; GREEN='\033[1;32m'; YELLOW='\033[1;33m'; RED='\033[1;31m'

log()    { printf "${CYAN}[up]${RESET} %s\n" "$*"; }
ok()     { printf "${GREEN}[up] ✓ %s${RESET}\n" "$*"; }
warn()   { printf "${YELLOW}[up] ⚠ %s${RESET}\n" "$*"; }
fail()   { printf "${RED}[up] ✗ %s${RESET}\n" "$*" >&2; exit 1; }
header() { printf "\n${BOLD}%s${RESET}\n%s\n" "$1" "$(printf '─%.0s' {1..60})"; }

require_cmd() { command -v "$1" >/dev/null 2>&1 || fail "comando necessário não encontrado: $1"; }
require_cmd terraform
require_cmd ssh
require_cmd scp

# ── 1. Terraform apply ────────────────────────────────────────────────────────
header "1/3 — Infraestrutura (Terraform)"

if $SKIP_APPLY; then
  warn "--no-apply: pulando terraform apply"
else
  terraform -chdir="${TF_DIR}" apply -auto-approve
fi

EC2_IP=$(terraform -chdir="${TF_DIR}" output -raw instance_ip 2>/dev/null) \
  || fail "não foi possível obter instance_ip do terraform output. Verifique se o apply concluiu."

[[ -z "$EC2_IP" ]] && fail "terraform output retornou IP vazio"
ok "EC2_IP = ${EC2_IP}"

# ── 2. Aguardar SSH + copiar kubeconfig ───────────────────────────────────────
header "2/3 — Kubeconfig"

SSH_OPTS=(-i "${SSH_KEY}" -o StrictHostKeyChecking=no -o ConnectTimeout=5 -o BatchMode=yes)

log "Aguardando SSH em ${EC2_IP}..."
until ssh "${SSH_OPTS[@]}" "${SSH_USER}@${EC2_IP}" true 2>/dev/null; do
  printf '.'
  sleep 5
done
echo
ok "SSH disponível"

log "Aguardando K3s gerar o kubeconfig..."
until ssh "${SSH_OPTS[@]}" "${SSH_USER}@${EC2_IP}" \
      "test -f /etc/rancher/k3s/k3s.yaml" 2>/dev/null; do
  printf '.'
  sleep 5
done
echo
ok "kubeconfig disponível na EC2"

mkdir -p "$(dirname "${KUBECONFIG_PATH}")"
scp "${SSH_OPTS[@]}" \
    "${SSH_USER}@${EC2_IP}:/etc/rancher/k3s/k3s.yaml" \
    "${KUBECONFIG_PATH}"

# Substituir 127.0.0.1 pelo IP público (portável: sem sed -i in-place)
TMP=$(mktemp)
sed "s/127.0.0.1/${EC2_IP}/" "${KUBECONFIG_PATH}" > "${TMP}" && mv "${TMP}" "${KUBECONFIG_PATH}"
chmod 600 "${KUBECONFIG_PATH}"
export KUBECONFIG="${KUBECONFIG_PATH}"

ok "Kubeconfig salvo em ${KUBECONFIG_PATH}"
log "  export KUBECONFIG=${KUBECONFIG_PATH}"

# ── 3. Abrir portas + aguardar ArgoCD + imprimir senhas ───────────────────────
header "3/3 — Acesso"

EC2_IP="${EC2_IP}" \
PUBLIC_CIDR="${PUBLIC_CIDR:-}" \
KUBECONFIG="${KUBECONFIG_PATH}" \
bash "${SCRIPT_DIR}/open-access.sh"
