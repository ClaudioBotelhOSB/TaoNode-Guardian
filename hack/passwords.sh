#!/usr/bin/env bash
# hack/passwords.sh — Imprime URLs e senhas de todos os painéis
# Requer: KUBECONFIG configurado e kubectl funcionando
set -euo pipefail

BOLD='\033[1m'; RESET='\033[0m'
GREEN='\033[1;32m'; CYAN='\033[1;36m'; YELLOW='\033[1;33m'; RED='\033[1;31m'

fail() { printf "${RED}✗ %s${RESET}\n" "$*" >&2; exit 1; }

KUBECONFIG="${KUBECONFIG:-${HOME}/.kube/taonode-aws.yaml}"
export KUBECONFIG

kubectl get nodes &>/dev/null \
  || fail "kubectl não conectou. Verifique KUBECONFIG=${KUBECONFIG}"

# IP público via serviço LoadBalancer do ArgoCD (sempre tem o IP da EC2)
EC2_IP="${EC2_IP:-$(
  kubectl get svc argocd-server -n argocd \
    -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null
)}"
[[ -z "$EC2_IP" ]] && EC2_IP="<ip-nao-disponivel>"

# ArgoCD
ARGOCD_PASSWORD=""
if kubectl get secret argocd-initial-admin-secret -n argocd &>/dev/null; then
  ARGOCD_PASSWORD="$(kubectl get secret argocd-initial-admin-secret -n argocd \
    -o jsonpath='{.data.password}' | base64 -d)"
else
  ARGOCD_PASSWORD="<rotacionada — use 'argocd account update-password'>"
fi

# Grafana
GRAFANA_PASSWORD=""
if kubectl get secret grafana-admin-secret -n monitoring &>/dev/null; then
  GRAFANA_PASSWORD="$(kubectl get secret grafana-admin-secret -n monitoring \
    -o jsonpath='{.data.admin-password}' | base64 -d)"
else
  GRAFANA_PASSWORD="<não encontrada — verifique namespace monitoring>"
fi

cat <<EOF

${BOLD}┌─────────────────────────────────────────────────────────┐${RESET}
${BOLD}│           TaoNode Guardian — Acesso Demo                │${RESET}
${BOLD}├─────────────────────────────────────────────────────────┤${RESET}
${BOLD}│ Grafana   ${RESET}  http://${EC2_IP}:3000
${BOLD}│           ${RESET}  Usuário : admin
${BOLD}│           ${RESET}  Senha   : ${GREEN}${BOLD}${GRAFANA_PASSWORD}${RESET}
${BOLD}│                                                         │${RESET}
${BOLD}│ ArgoCD    ${RESET}  http://${EC2_IP}:8080
${BOLD}│           ${RESET}  Usuário : admin
${BOLD}│           ${RESET}  Senha   : ${GREEN}${BOLD}${ARGOCD_PASSWORD}${RESET}
${BOLD}│                                                         │${RESET}
${BOLD}│ ClickHouse${RESET}  http://${EC2_IP}:8123/play
${BOLD}└─────────────────────────────────────────────────────────┘${RESET}

EOF
