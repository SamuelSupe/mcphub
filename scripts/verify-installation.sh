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
export MCPHUB_CONFIG_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
export MCPHUB_DATABASE_URL="${MCPHUB_TEST_POSTGRES_DSN:?test PostgreSQL database required}"
unset MCPHUB_PRIMARY_API_KEY MCPHUB_PRIMARY_BACKEND_URL

for source in "$examples/config.example.yaml" "$examples"/deploy/config.*.yaml; do
  directory="$work_dir/$(basename "$source")"
  mkdir "$directory"
  cp "$source" "$directory/config.yaml"
  "$binary" validate --config "$directory/config.yaml"
done

# Run the shipped files unchanged, including their ports and relative DB paths.
unset MCPHUB_AUTH_ISSUER MCPHUB_ADMIN_CLIENT_ID

for name in config.example.yaml config.local.yaml config.remote-sqlite.yaml config.remote-postgres.yaml; do
  directory="$work_dir/$name"
  printf '%s\n' installation-acceptance-password | "$binary" init-admin --config "$directory/config.yaml" --username admin --password-stdin
  if [ "$name" = config.example.yaml ] || [ "$name" = config.local.yaml ]; then
    origin=http://127.0.0.1:8081
    host=127.0.0.1:8081
  else
    origin=https://admin.example.com
    host=admin.example.com
  fi
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
    test "$(curl --silent --max-time 2 -o "$directory/ready.json" -w '%{http_code}' http://127.0.0.1:8080/readyz)" = 200
    curl --fail --silent --max-time 2 -H "Host: $host" http://127.0.0.1:8081/ > /dev/null
    test "$(curl --silent --max-time 2 -o /dev/null -w '%{http_code}' -H "Host: $host" http://127.0.0.1:8081/api/v1/overview)" = 401
    test "$(curl --silent --max-time 5 -o /dev/null -w '%{http_code}' -D "$directory/login.headers" -H "Host: $host" -H "Origin: $origin" -H 'Content-Type: application/json' --data '{"username":"admin","password":"installation-acceptance-password"}' http://127.0.0.1:8081/auth/local-login)" = 204
    # Exercise the private listener as a TLS proxy would: the remote cookie is
    # Secure and cannot be collected in curl's cookie jar over this HTTP hop.
    cookie="$(awk 'tolower($1) == "set-cookie:" {split($2, parts, ";"); print parts[1]}' "$directory/login.headers")"
    curl --fail --silent --max-time 2 -H "Host: $host" -H "Cookie: $cookie" http://127.0.0.1:8081/auth/session > "$directory/session.json"
    python3 -c 'import json,sys; s=json.load(open(sys.argv[1])); assert s["builtin"] and s["initialized"] and s["authenticated"] and s["can_configure"]' "$directory/session.json"
    kill "$server_pid"
    wait "$server_pid"
    server_pid=""
    "$binary" validate --config "$directory/config.yaml"
    printf 'PASS startup/restart/login %s attempt=%s (built-in issuer: ready=200)\n' "$name" "$attempt"
  done
done
