#!/usr/bin/env bash
set -euo pipefail

binary="$(realpath "${1:?client binary path required}")"
version="${2:?release version required}"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
export MCPHUB_HOME="$work_dir/state"

test "$("$binary" --version)" = "mcpbridge $version (client)"
for command in setup login connect status logout doctor admin; do
  "$binary" "$command" --help > /dev/null 2>&1
done
"$binary" status --profile release-smoke > /dev/null
"$binary" logout --profile release-smoke > /dev/null
if "$binary" validate --config missing.yaml > "$work_dir/error.log" 2>&1; then
  exit 1
fi
grep -Fq 'mcphub server executable' "$work_dir/error.log"
printf 'PASS MCPBridge %s: client identity, commands, state and server-command guidance\n' "$version"
