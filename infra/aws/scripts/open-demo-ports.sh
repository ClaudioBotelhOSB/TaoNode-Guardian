#!/usr/bin/env bash
# infra/aws/scripts/open-demo-ports.sh
#
# Abre as portas da demo (80, 443, 8123, 9000) no Security Group da EC2,
# restritas ao IP público atual (ou ao CIDR passado como argumento).
#
# Uso:
#   bash open-demo-ports.sh                       # detecta IP automaticamente
#   bash open-demo-ports.sh 203.0.113.10/32        # IP fixo (ex: rede do cliente)
#   PUBLIC_CIDR=203.0.113.10/32 bash open-demo-ports.sh
#   SG_ID=sg-0abc123 bash open-demo-ports.sh       # pula descoberta por nome
#
# AVISO DE DRIFT: regras criadas via CLI não estão no Terraform.
# Para fechar após a demo: use os comandos "revoke" impressos ao final.
# Para IaC permanente: adicione ao main.tf com var.admin_cidrs.

set -euo pipefail

REGION="${AWS_DEFAULT_REGION:-us-east-1}"
SG_NAME="${SG_NAME:-taonode-guardian-k3s-sg}"
INPUT_CIDR="${1:-${PUBLIC_CIDR:-}}"

log()  { printf '[open-demo-ports] %s\n' "$*"; }
fail() { printf '[open-demo-ports] ERROR: %s\n' "$*" >&2; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

validate_cidr() {
  local cidr="$1"
  [[ "$cidr" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$ ]] || \
    fail "invalid CIDR: $cidr"
}

detect_public_cidr() {
  local ip
  for url in \
    "https://checkip.amazonaws.com" \
    "https://api.ipify.org" \
    "https://ifconfig.me/ip"
  do
    ip="$(curl -fsS --max-time 5 "$url" | tr -d '[:space:]')" || true
    if [[ "$ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
      printf '%s/32\n' "$ip"
      return 0
    fi
  done
  return 1
}

resolve_sg_id() {
  if [[ -n "${SG_ID:-}" ]]; then
    printf '%s\n' "$SG_ID"
    return 0
  fi

  mapfile -t matches < <(
    aws ec2 describe-security-groups \
      --region "$REGION" \
      --filters "Name=group-name,Values=${SG_NAME}" \
      --query "SecurityGroups[].GroupId" \
      --output text | tr '\t' '\n' | sed '/^$/d'
  )

  case "${#matches[@]}" in
    0) fail "security group '${SG_NAME}' not found in region ${REGION}" ;;
    1) printf '%s\n' "${matches[0]}" ;;
    *) fail "multiple security groups named '${SG_NAME}' found; export SG_ID explicitly" ;;
  esac
}

authorize_port() {
  local sg_id="$1"
  local cidr="$2"
  local port="$3"
  local desc="$4"
  local output

  if output="$(
    aws ec2 authorize-security-group-ingress \
      --region "$REGION" \
      --group-id "$sg_id" \
      --ip-permissions \
        "IpProtocol=tcp,FromPort=${port},ToPort=${port},IpRanges=[{CidrIp=${cidr},Description='${desc}'}]" \
      2>&1
  )"; then
    log "opened tcp/${port} for ${cidr}"
    return 0
  fi

  if grep -q "InvalidPermission.Duplicate" <<<"$output"; then
    log "tcp/${port} for ${cidr} already exists — skipping"
    return 0
  fi

  # Real error: print output and abort
  printf '%s\n' "$output" >&2
  fail "failed to open tcp/${port} for ${cidr}"
}

# ── Pré-validações ────────────────────────────────────────────────────────────
require_cmd aws
require_cmd curl

# ── Resolver CIDR ─────────────────────────────────────────────────────────────
if [[ -n "$INPUT_CIDR" ]]; then
  MY_CIDR="$INPUT_CIDR"
  [[ "$MY_CIDR" == */* ]] || MY_CIDR="${MY_CIDR}/32"
else
  log "detecting current public IP..."
  MY_CIDR="$(detect_public_cidr)" || fail "unable to detect public IP; pass it explicitly: bash $0 <cidr>"
fi

validate_cidr "$MY_CIDR"

# ── Resolver SG ──────────────────────────────────────────────────────────────
SG_ID="$(resolve_sg_id)"

log "region:         ${REGION}"
log "security group: ${SG_ID}"
log "cidr:           ${MY_CIDR}"
echo ""

# ── Abrir portas ─────────────────────────────────────────────────────────────
authorize_port "$SG_ID" "$MY_CIDR"  80   "Grafana LB - demo"
authorize_port "$SG_ID" "$MY_CIDR"  443  "ArgoCD HTTPS - demo"
authorize_port "$SG_ID" "$MY_CIDR"  8123 "ClickHouse HTTP - demo"
authorize_port "$SG_ID" "$MY_CIDR"  9000 "ClickHouse Native - demo"

# ── Resumo + comandos de cleanup ─────────────────────────────────────────────
cat <<EOF

Done. Ports 80, 443, 8123, 9000 open for ${MY_CIDR}.

Access URLs:
  Grafana:    http://<ec2-public-ip>
  ArgoCD:     https://<ec2-public-ip>
  ClickHouse: http://<ec2-public-ip>:8123/play

Revoke after the demo:
  aws ec2 revoke-security-group-ingress --region ${REGION} --group-id ${SG_ID} \\
    --ip-permissions 'IpProtocol=tcp,FromPort=80,ToPort=80,IpRanges=[{CidrIp=${MY_CIDR}}]'
  aws ec2 revoke-security-group-ingress --region ${REGION} --group-id ${SG_ID} \\
    --ip-permissions 'IpProtocol=tcp,FromPort=443,ToPort=443,IpRanges=[{CidrIp=${MY_CIDR}}]'
  aws ec2 revoke-security-group-ingress --region ${REGION} --group-id ${SG_ID} \\
    --ip-permissions 'IpProtocol=tcp,FromPort=8123,ToPort=8123,IpRanges=[{CidrIp=${MY_CIDR}}]'
  aws ec2 revoke-security-group-ingress --region ${REGION} --group-id ${SG_ID} \\
    --ip-permissions 'IpProtocol=tcp,FromPort=9000,ToPort=9000,IpRanges=[{CidrIp=${MY_CIDR}}]'

If the presentation network has a different IP, re-run:
  bash $0 <new-cidr>/32
EOF
