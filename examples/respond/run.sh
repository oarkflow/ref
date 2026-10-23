#!/usr/bin/env bash
# End-to-end demo of `aegis run -respond=on`.
#
#   examples/respond/run.sh
#
# 1. Seals a tiny app into a signed, encrypted .apkg.
# 2. Creates and signs an escalation policy (respond-keygen, respond-sign).
# 3. Runs the sealed app clean: the controller stays at "normal".
# 4. Runs it again with an instrumentation variable set (FRIDA_VERSION, which
#    the integrity "environment" check flags) and watches the posture climb:
#    writes blocked, requests blocked, then the process exits with code 75
#    so a supervisor would start a fresh copy.
#
# Everything lives in a temp directory that is removed at the end. Needs go
# and curl. Uses -integrity-policy=warn only so the process can start with the
# simulated attack already present; production should keep the default.
set -u

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
work=$(mktemp -d)
addr=127.0.0.1:18080
pid=""
cleanup() { [ -n "$pid" ] && kill "$pid" 2>/dev/null; rm -rf "$work"; }
trap cleanup EXIT

step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }
die()  { printf '\033[31mFAIL\033[0m %s\n' "$1" >&2; exit 1; }

step "Build aegis"
(cd "$repo" && go build -o "$work/aegis" ./aegis/cmd/aegis) || die "build"
aegis="$work/aegis"

step "Seal the app (package signing key + encryption key)"
"$aegis" keygen -out "$work/pkg" >/dev/null || die "keygen"
"$aegis" package -dir "$here/app" -key "$work/pkg.key" -enckey "$work/pkg.enc" \
  -out "$work/app.apkg" -appid com.example.respond -ver 1.0.0 >/dev/null || die "package"
echo "sealed: app.apkg"

step "Create and sign an escalation policy (separate key from the package key)"
"$aegis" respond-keygen -out "$work/policy" | sed 's/^/  /'
# solo-confidence 0.8 lets this one detector act alone, to keep the demo short.
# The default (0.95) would stop at "restricted" until a second source agrees.
"$aegis" respond-sign -key "$work/policy.key" -out "$work/policy.json" \
  -solo-confidence 0.8 -quiet-period 5m | sed 's/^/  /'
pubhex=$(tr -d '[:space:]' < "$work/policy.pub")

run_app() { # extra environment assignments as arguments
  env "$@" "$aegis" run "$work/app.apkg" -pubkey "$work/pkg.pub" -key "$work/pkg.enc" \
    -addr "$addr" -integrity-policy=warn \
    -respond=on -respond-policy "$work/policy.json" -respond-pubkey "$pubhex" \
    -respond-interval 1s >"$work/out.log" 2>"$work/err.log" &
  pid=$!
}
code() { curl -s -o /dev/null -m 2 -w '%{http_code}' -X "$1" "http://$addr$2" 2>/dev/null || true; }
wait_up() { for _ in $(seq 50); do [ "$(code GET /livez)" = 200 ] && return 0; sleep 0.2; done; return 1; }

step "Run clean: nothing suspicious, controller stays at normal"
run_app A=1
wait_up || { cat "$work/err.log"; die "app did not start"; }
echo "GET  /hello -> $(code GET /hello)"
echo "POST /hello -> $(code POST /hello)"
sleep 3
grep -c 'Respond\] #' "$work/err.log" | sed 's/^/respond events after 3s: /'
kill "$pid"; wait "$pid" 2>/dev/null; pid=""

step "Run under simulated instrumentation (FRIDA_VERSION set)"
run_app FRIDA_VERSION=16.0.0
wait_up || { cat "$work/err.log"; die "app did not start"; }
last=""
while kill -0 "$pid" 2>/dev/null; do
  now="GET=$(code GET /hello) POST=$(code POST /hello) livez=$(code GET /livez)"
  [ "$now" != "$last" ] && echo "  $now" && last="$now"
  sleep 0.3
done
wait "$pid"; rc=$?; pid=""
echo "process exit code: $rc"

step "Controller evidence log (hash chained)"
grep 'Respond\]' "$work/err.log" | sed 's/^ *//'

[ "$rc" = 75 ] || die "expected exit code 75 (regenerate), got $rc"
printf '\n\033[32mok\033[0m  clean run stayed normal; attack run escalated and exited 75\n'
