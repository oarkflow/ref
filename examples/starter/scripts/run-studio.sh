#!/usr/bin/env bash
# Runs the starter with REF Studio (the visual editor) for local development.
#
#   ./scripts/run-studio.sh            # app on :8080, Studio on http://127.0.0.1:8081/studio/
#   PORT=9000 STUDIO_ADDR=127.0.0.1:9001 ./scripts/run-studio.sh
#
# Data, generated secrets and the built binary live in .data/studio/ (git-ignored).
# Development only: the tokens below are printed on purpose. Set STARTER_*_TOKEN
# yourself to use your own.
set -euo pipefail
cd "$(dirname "$0")/.."

: "${GOTOOLCHAIN:=go1.26.5}"; export GOTOOLCHAIN
STATE=.data/studio
mkdir -p "$STATE/secrets" "$STATE/seeds"

secret() { # secret <file> : create a random secret once, print it
  [ -s "$STATE/$1" ] || openssl rand -hex 32 > "$STATE/$1"
  cat "$STATE/$1"
}

export SESSION_SECRET="${SESSION_SECRET:-$(secret session.secret)}"
export WEBHOOK_SECRET="${WEBHOOK_SECRET:-$(secret webhook.secret)}"
export ADMIN_EMAIL="${ADMIN_EMAIL:-admin@example.com}"
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-ChangeMe-Dev-123}"
export PORT="${PORT:-8080}"
export AUTO_MIGRATE="${AUTO_MIGRATE:-true}"
export DB_DSN="${DB_DSN:-file:$PWD/$STATE/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)}"
export MIGRATIONS_DIR="${MIGRATIONS_DIR:-$PWD/resources/migrations}"
export MIGRATIONS_SEED_DIR="${MIGRATIONS_SEED_DIR:-$PWD/$STATE/seeds}"
export SECRETS_DIR="${SECRETS_DIR:-$PWD/$STATE/secrets}"

export STARTER_SUPERVISOR=1 STARTER_STUDIO=1 STARTER_STUDIO_PERSIST="${STARTER_STUDIO_PERSIST:-1}"
export STARTER_ADMIN_ADDR="${STUDIO_ADDR:-127.0.0.1:8081}"
export STARTER_ADMIN_TOKEN="${STARTER_ADMIN_TOKEN:-admin-token-0000001}"
export STARTER_REVIEWER_TOKEN="${STARTER_REVIEWER_TOKEN:-reviewer-token-0001}"
export STARTER_EDITOR_TOKEN="${STARTER_EDITOR_TOKEN:-editor-token-000001}"

echo "building…"
go build -o "$STATE/starter" ./cmd/server

cat <<MSG

  App     http://127.0.0.1:${PORT}   (sign in: ${ADMIN_EMAIL} / ${ADMIN_PASSWORD})
  Studio  http://${STARTER_ADMIN_ADDR}/studio/
  Tokens  editor: ${STARTER_EDITOR_TOKEN}
          reviewer: ${STARTER_REVIEWER_TOKEN}
          admin: ${STARTER_ADMIN_TOKEN}

MSG
exec "$STATE/starter"
