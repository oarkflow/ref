#!/usr/bin/env bash
# Renders this starter's secrets from a running HashiCorp Vault (KV v2) into
# files that resources/config/00_app.bcl's `secret { file ... }` fallback
# reads — the same shape a Vault Agent Injector sidecar or the AWS/GCP
# Secrets Manager CSI driver produces in a real cluster, just driven by curl
# instead of a long-running agent, so it works anywhere `curl`/`jq` do.
#
# This does not require the `vault` CLI: it talks to Vault's HTTP API
# directly, the same API the CLI and Vault Agent themselves use.
#
# Usage:
#   VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root \
#     ./scripts/render-secrets-from-vault.sh ./.data/secrets
#
# Re-run it any time a secret rotates in Vault (by hand, by a Vault dynamic
# lease, by whatever rotates it) to update the files on disk. It does not
# restart the server — see CONFIGURATION.md's "Secrets in production" for
# why a restart (not a hot in-place swap) is this starter's rotation step,
# and what to reach for instead if you need zero-downtime rotation.
set -euo pipefail

: "${VAULT_ADDR:?set VAULT_ADDR, e.g. http://127.0.0.1:8200}"
: "${VAULT_TOKEN:?set VAULT_TOKEN}"
VAULT_PATH="${VAULT_PATH:-secret/data/starter}"
OUT_DIR="${1:-./.data/secrets}"

mkdir -p "$OUT_DIR"
chmod 700 "$OUT_DIR"

response=$(curl -sS -H "X-Vault-Token: $VAULT_TOKEN" "$VAULT_ADDR/v1/$VAULT_PATH")
if ! echo "$response" | jq -e '.data.data' >/dev/null 2>&1; then
	echo "render-secrets-from-vault: unexpected response from $VAULT_ADDR/v1/$VAULT_PATH: $response" >&2
	exit 1
fi

render() {
	key="$1"
	value=$(echo "$response" | jq -r --arg k "$key" '.data.data[$k] // empty')
	if [ -z "$value" ]; then
		echo "render-secrets-from-vault: Vault has no key %q under $VAULT_PATH" "$key" >&2
		exit 1
	fi
	target="$OUT_DIR/$key"
	tmp="$target.tmp.$$"
	# Write-then-rename: a reader (this starter's own boot, or a re-render
	# racing a concurrent read) never observes a partially written secret —
	# rename is atomic on the same filesystem, a plain write to the final
	# path is not.
	printf '%s' "$value" >"$tmp"
	chmod 600 "$tmp"
	mv "$tmp" "$target"
	echo "render-secrets-from-vault: wrote $target"
}

render session_secret
render webhook_secret
