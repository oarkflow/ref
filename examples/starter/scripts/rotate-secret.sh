#!/usr/bin/env bash
# Rotates one of this starter's secrets in Vault to a fresh random value and
# re-renders it to disk. Pair with a restart of the server (or, for
# zero-downtime rotation, the deploy.Supervisor hot-swap described in
# docs/deploy.md at the repository root) to pick it up — see
# CONFIGURATION.md's "Secrets in production" for why a restart, not a
# hot in-place swap, is this starter's rotation step.
#
# Usage:
#   VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root \
#     ./scripts/rotate-secret.sh session_secret ./.data/secrets
set -euo pipefail

: "${VAULT_ADDR:?set VAULT_ADDR, e.g. http://127.0.0.1:8200}"
: "${VAULT_TOKEN:?set VAULT_TOKEN}"
VAULT_PATH="${VAULT_PATH:-secret/data/starter}"
KEY="${1:?usage: rotate-secret.sh <session_secret|webhook_secret> [out_dir]}"
OUT_DIR="${2:-./.data/secrets}"

case "$KEY" in
session_secret | webhook_secret) ;;
*)
	echo "rotate-secret: unknown key %q — this starter only has session_secret and webhook_secret" "$KEY" >&2
	exit 1
	;;
esac

current=$(curl -sS -H "X-Vault-Token: $VAULT_TOKEN" "$VAULT_ADDR/v1/$VAULT_PATH")
new_value=$(openssl rand -hex 32)

# KV v2's data endpoint replaces the whole value map on write, so every
# existing key must be carried forward alongside the one being rotated —
# writing just {"$KEY": "..."} would silently delete the other secret.
payload=$(echo "$current" | jq --arg k "$KEY" --arg v "$new_value" '.data.data * {($k): $v}')

curl -sS -H "X-Vault-Token: $VAULT_TOKEN" -X POST \
	-d "{\"data\": $payload}" \
	"$VAULT_ADDR/v1/$VAULT_PATH" >/dev/null

echo "rotate-secret: $KEY rotated in Vault"
"$(dirname "$0")/render-secrets-from-vault.sh" "$OUT_DIR"
