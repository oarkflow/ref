#!/usr/bin/env bash
# Checks one release binary for the things a build is supposed to remove:
# this machine's source paths, this project's package paths (which `-s -w`
# alone does not remove — see docs/aegis-protection-plan.md, phase 2), and
# known development-only default secrets. Exits non-zero on any finding.
#
#   scripts/verify-build.sh bin/release/aegis-linux-amd64
#   scripts/verify-build.sh --allow-package-paths bin/release/aegis-linux-amd64   (plain build)
#
# `--allow-package-paths` skips the package-path check: a plain `-trimpath
# -s -w` build is expected to still contain github.com/oarkflow/... strings
# (Go's runtime keeps function-name strings for panics and stack traces
# independent of the symbol table nm reads); only garble actually removes
# them. Use it to confirm a plain build at least has no source paths or
# secrets, without failing on the one thing it was never going to fix.
set -u

allow_package_paths=0
if [ "${1:-}" = "--allow-package-paths" ]; then
  allow_package_paths=1
  shift
fi

bin=${1:-}
if [ -z "$bin" ] || [ ! -f "$bin" ]; then
  echo "usage: $0 [--allow-package-paths] <path-to-binary>" >&2
  exit 2
fi

fail=0
ok()  { printf '  \033[32mok\033[0m   %s\n' "$1"; }
bad() { fail=1; printf '  \033[31mFAIL\033[0m %s\n         %s\n' "$1" "$2"; }

# This machine's own source checkout path, and the generic home-directory
# shape every developer's machine has one of. Either appearing in a shipped
# binary names a person's machine and directory layout.
repo_dir=$(cd "$(dirname "$0")/.." && pwd)
home_pattern="$HOME"

hits=$(strings -a "$bin" | grep -Fc -- "$repo_dir" 2>/dev/null || true)
if [ "${hits:-0}" -gt 0 ]; then
  bad "no source paths" "$hits occurrence(s) of $repo_dir"
else
  ok "no source paths ($repo_dir)"
fi

hits=$(strings -a "$bin" | grep -Fc -- "$home_pattern" 2>/dev/null || true)
if [ "${hits:-0}" -gt 0 ]; then
  bad "no home-directory paths" "$hits occurrence(s) of $home_pattern"
else
  ok "no home-directory paths"
fi

if [ "$allow_package_paths" -eq 1 ]; then
  echo "  (skipped: package-path check, --allow-package-paths)"
else
  hits=$(strings -a "$bin" | grep -Fc 'github.com/oarkflow/' 2>/dev/null || true)
  if [ "${hits:-0}" -gt 0 ]; then
    bad "no proprietary package paths" "$hits occurrence(s) of github.com/oarkflow/ (plain -s -w does not remove these; build with garble, or accept this with --allow-package-paths)"
  else
    ok "no proprietary package paths (github.com/oarkflow/...)"
  fi
fi

# Known development-only defaults from this repo's example applications.
# None of these is a secret an attacker could use against a real
# deployment — they are literally "change me" placeholders — but a binary
# naming one is a binary that embedded example/test fixtures it should not
# have, and is worth catching before anyone mistakes the pattern for real.
secrets=(
  "dev-admin-key-change-me"
  "dev-clerk-key-change-me"
  "dev-viewer-key-change-me"
)
secret_hit=0
for s in "${secrets[@]}"; do
  if strings -a "$bin" | grep -qF -- "$s"; then
    bad "no known dev-default secrets" "found \"$s\" in the binary"
    secret_hit=1
  fi
done
[ "$secret_hit" -eq 0 ] && ok "no known dev-default secrets"

# Informational only: go tool nm's view, which -s -w does remove, so this is
# expected to report "no symbols" for a stripped binary (any build type).
if command -v go >/dev/null 2>&1; then
  if go tool nm "$bin" >/dev/null 2>&1; then
    n=$(go tool nm "$bin" 2>/dev/null | grep -c 'oarkflow/ref' || true)
    echo "  (info) go tool nm: ${n:-0} oarkflow/ref symbol(s) in the symbol table"
  else
    echo "  (info) go tool nm: no symbol table (expected for a -s -w build)"
  fi
fi

exit $fail
