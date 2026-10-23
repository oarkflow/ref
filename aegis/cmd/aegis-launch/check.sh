#!/usr/bin/env bash
# Proves aegis-launch for real, in a Linux container (needs Docker, since
# memfd_create and execveat are Linux-only): builds the launcher and the real
# aegis runtime, seals the runtime as an encrypted image, DELETES the real
# runtime binary from disk, launches it anyway through aegis-launch, and
# checks that the resulting process actually serves a real application —
# all without the runtime binary ever existing on disk at launch time.
#
#   ./check.sh
set -u
cd "$(dirname "$0")"
ROOT=$(cd ../../.. && pwd)
WORK=$(mktemp -d)
pass=0 fail=0
ok()  { pass=$((pass + 1)); printf '  \033[32mok\033[0m   %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  \033[31mFAIL\033[0m %s\n         %s\n' "$1" "$2"; }
step() { printf '\n== %s\n' "$1"; }
cleanup() { [ "$fail" -eq 0 ] && rm -rf "$WORK" || echo "kept for inspection: $WORK"; }
trap cleanup EXIT

command -v docker >/dev/null || { echo "docker is required"; exit 2; }

ARCH=$(uname -m)
case "$ARCH" in
  arm64|aarch64) GOARCH=arm64; PLATFORM=linux/arm64 ;;
  x86_64|amd64)  GOARCH=amd64; PLATFORM=linux/amd64 ;;
  *) echo "unsupported host arch $ARCH"; exit 2 ;;
esac

step "build linux/$GOARCH binaries (native arch, no emulation)"
( cd "$ROOT" && GOOS=linux GOARCH=$GOARCH go build -o "$WORK/aegis" ./aegis/cmd/aegis ) \
  && ok "built aegis" || { bad "built aegis" "see above"; exit 1; }
( cd "$ROOT" && GOOS=linux GOARCH=$GOARCH go build -o "$WORK/aegis-launch" ./aegis/cmd/aegis-launch ) \
  && ok "built aegis-launch" || { bad "built aegis-launch" "see above"; exit 1; }

RUNTIME_SIZE=$(wc -c < "$WORK/aegis")
LAUNCH_SIZE=$(wc -c < "$WORK/aegis-launch")
if [ "$LAUNCH_SIZE" -lt "$((RUNTIME_SIZE / 2))" ]; then
  ok "launcher ($LAUNCH_SIZE bytes) is far smaller than the runtime it launches ($RUNTIME_SIZE bytes)"
else
  bad "launcher size" "launcher is $LAUNCH_SIZE bytes, runtime is $RUNTIME_SIZE bytes — not meaningfully smaller"
fi

mkdir -p "$WORK/app"
cp -R ../../../examples/dynamic-definitions/config ../../../examples/dynamic-definitions/scripts "$WORK/app/"

step "package the runtime and the application inside the container"
docker run --rm --platform "$PLATFORM" -v "$WORK":/work -w /work debian:bookworm-slim bash -c '
  set -e
  chmod +x aegis
  ./aegis keygen -out /work/k >/work/keygen.log 2>&1
  ./aegis package -binary /work/aegis -key /work/k.key -enckey /work/k.enc -out /work/runtime.apkg -appid runtime -ver 1 >/work/package-runtime.log 2>&1
  ./aegis keygen -out /work/ak >>/work/keygen.log 2>&1
  ./aegis package -dir /work/app -key /work/ak.key -enckey /work/ak.enc -out /work/app.apkg -appid dynamicdefs -ver 1 >/work/package-app.log 2>&1
' > "$WORK/package-container.log" 2>&1
if [ -s "$WORK/runtime.apkg" ] && [ -s "$WORK/app.apkg" ]; then
  ok "both images sealed"
else
  bad "packaging" "$(tail -20 "$WORK"/*.log 2>/dev/null)"
  exit 1
fi

step "delete the real runtime binary, then launch it anyway from the sealed image"
cat > "$WORK/test.sh" <<'EOF'
set -e
chmod +x aegis-launch
rm -f /work/aegis
./aegis-launch -image /work/runtime.apkg -key /work/k.enc -pubkey /work/k.pub -- \
  run /work/app.apkg -key /work/ak.enc -pubkey /work/ak.pub -addr :8080 \
  > /work/launch.log 2>&1 &
echo $! > /work/launch.pid
sleep 3
ps -p "$(cat /work/launch.pid)" -o pid,comm,args --no-headers > /work/ps.out 2>&1 || true
curl -s -o /work/live.out -w '%{http_code}' http://127.0.0.1:8080/live > /work/live.code
curl -s -o /work/quote.out -w '%{http_code}' -XPOST http://127.0.0.1:8080/quote \
  -H 'X-API-Key: dev-clerk-key-change-me-0123456789' -H 'content-type: application/json' \
  -d '{"lines":[{"qty":1,"price":9}]}' > /work/quote.code
kill "$(cat /work/launch.pid)" 2>/dev/null || true
EOF
docker run --rm --platform "$PLATFORM" -v "$WORK":/work -w /work -p 18711:8080 debian:bookworm-slim bash -c '
  apt-get -qq update >/dev/null 2>&1 && apt-get -qq install -y curl procps >/dev/null 2>&1
  bash /work/test.sh
' > "$WORK/run-container.log" 2>&1

if [ ! -f "$WORK/aegis" ]; then
  ok "the real runtime binary is gone from disk before launch (deleted by the test, confirmed absent)"
else
  bad "runtime binary presence" "aegis still exists on disk; the deletion in the container script did not take"
fi

if [ -f "$WORK/ps.out" ] && grep -q 'aegis run' "$WORK/ps.out"; then
  ok "the launcher's own process now runs as \"aegis run ...\" ($(cat "$WORK/ps.out"))"
else
  bad "process replacement" "expected the launcher's pid to show as running \"aegis run ...\"; ps output: $(cat "$WORK/ps.out" 2>/dev/null)"
fi

if [ -f "$WORK/live.code" ] && [ "$(cat "$WORK/live.code")" = "200" ] && grep -q '"ok":true' "$WORK/live.out" 2>/dev/null; then
  ok "GET /live served by the exec'd runtime: $(cat "$WORK/live.out")"
else
  bad "GET /live" "code=$(cat "$WORK/live.code" 2>/dev/null), body=$(cat "$WORK/live.out" 2>/dev/null)"
fi

if [ -f "$WORK/quote.code" ] && [ "$(cat "$WORK/quote.code")" = "200" ] && grep -q 'subtotal' "$WORK/quote.out" 2>/dev/null; then
  ok "POST /quote (authenticated, through the node type and helper machinery) served: $(cat "$WORK/quote.out")"
else
  bad "POST /quote" "code=$(cat "$WORK/quote.code" 2>/dev/null), body=$(cat "$WORK/quote.out" 2>/dev/null)"
fi

if grep -q 'aegis-launch: \[signature\] verified' "$WORK/launch.log" 2>/dev/null && grep -q '\[Signature\] Verified' "$WORK/launch.log" 2>/dev/null; then
  ok "both layers verified their signature (the launcher's image, and the exec'd runtime's own app package)"
else
  bad "two-layer signature check" "expected both a launcher-side and a runtime-side signature line; log: $(cat "$WORK/launch.log" 2>/dev/null | head -20)"
fi

step "refusals: unsigned image, wrong key, missing runtime args"
docker run --rm --platform "$PLATFORM" -v "$WORK":/work -w /work debian:bookworm-slim bash -c '
  chmod +x aegis-launch
  echo "--- no pubkey, no skip flag ---"
  ./aegis-launch -image /work/runtime.apkg -key /work/k.enc -- run /work/app.apkg 2>&1; echo "exit=$?"
  echo "--- wrong key ---"
  ./aegis-launch -image /work/runtime.apkg -key 0000000000000000000000000000000000000000000000000000000000000000 -pubkey /work/k.pub -- run /work/app.apkg 2>&1; echo "exit=$?"
  echo "--- no runtime args after -- ---"
  ./aegis-launch -image /work/runtime.apkg -key /work/k.enc -pubkey /work/k.pub 2>&1; echo "exit=$?"
' > "$WORK/refusals.log" 2>&1

if grep -q 'requires -pubkey' "$WORK/refusals.log" && grep -c 'exit=1' "$WORK/refusals.log" | grep -q '^3$'; then
  ok "all three refusals triggered with exit=1"
else
  bad "refusals" "$(cat "$WORK/refusals.log")"
fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
