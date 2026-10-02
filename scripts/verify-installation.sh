#!/usr/bin/env bash
set -euo pipefail

binary="$(realpath "${1:?binary path required}")"
examples="$(realpath "${2:?example directory required}")"
work_dir="$(mktemp -d)"
server_pid=""
cleanup() {
  if [ -n "$server_pid" ]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf "$work_dir"
}
trap cleanup EXIT

# Unreachable test endpoints exercise startup without contacting a real tenant.
export MCPHUB_PUBLIC_URL=https://hub.example.com/mcp
export MCPHUB_AUTH_ISSUER=https://127.0.0.1:1
export MCPHUB_ADMIN_PUBLIC_URL=https://admin.example.com
export MCPHUB_ADMIN_CLIENT_ID=mcphub-admin-web
export MCPHUB_CONFIG_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
export MCPHUB_DATABASE_URL="${MCPHUB_TEST_POSTGRES_DSN:?test PostgreSQL database required}"

for source in "$examples/config.example.yaml" "$examples"/deploy/config.*.yaml; do
  directory="$work_dir/$(basename "$source")"
  mkdir "$directory"
  cp "$source" "$directory/config.yaml"
  "$binary" validate --config "$directory/config.yaml"
done

# Run the shipped files unchanged, including their ports and relative DB paths.
for name in config.example.yaml config.local.yaml config.remote-sqlite.yaml config.remote-postgres.yaml; do
  directory="$work_dir/$name"
  for attempt in 1 2; do
    "$binary" serve --config "$directory/config.yaml" > "$directory/server.log" 2>&1 &
    server_pid=$!
    healthy=false
    for _ in {1..100}; do
      if ! kill -0 "$server_pid" 2>/dev/null; then
        cat "$directory/server.log"
        exit 1
      fi
      if curl --fail --silent --max-time 1 http://127.0.0.1:8080/healthz > /dev/null; then
        healthy=true
        break
      fi
      sleep 0.1
    done
    if [ "$healthy" != true ]; then
      cat "$directory/server.log"
      exit 1
    fi
    curl --fail --silent --max-time 2 http://127.0.0.1:8080/.well-known/oauth-protected-resource/mcp > /dev/null
    test "$(curl --silent --max-time 2 -o "$directory/ready.json" -w '%{http_code}' http://127.0.0.1:8080/readyz)" = 503
    if [ "$name" = config.example.yaml ] || [ "$name" = config.local.yaml ]; then
      curl --fail --silent --max-time 2 http://127.0.0.1:8081/ > /dev/null
    else
      curl --fail --silent --max-time 2 -H 'Host: admin.example.com' http://127.0.0.1:8081/ > /dev/null
    fi
    kill "$server_pid"
    wait "$server_pid"
    server_pid=""
    "$binary" validate --config "$directory/config.yaml"
    printf 'PASS startup/restart %s attempt=%s (unavailable test issuer: ready=503)\n' "$name" "$attempt"
  done
done
