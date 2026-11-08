#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/persona-ci-db.XXXXXX")"
container="persona-ci-db-$(basename "$fixture" | tr '[:upper:]' '[:lower:]')"
cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -f "$fixture/database.json"
  rmdir "$fixture" 2>/dev/null || true
}
trap cleanup EXIT
ci_run 'Postgres 18 fixture' 180 docker run --detach --name "$container" \
  -e POSTGRES_USER=persona -e POSTGRES_PASSWORD=ci-only -e POSTGRES_DB=persona_test \
  -p 127.0.0.1::5432 postgres:18-alpine
ci_run 'Postgres readiness' 90 python3 scripts/ci/postgres.py "$container" "$fixture/database.json"
ci_run 'Go formatting' 120 python3 scripts/ci/gofmt.py server sdk/go
ci_run 'Server vet' 600 bash -c 'cd server && go vet ./...'
ci_run 'Process RPC lifecycle race tests' 420 bash -c 'cd server && go test -race -p 2 -count=1 -timeout=5m ./internal/rpc ./internal/extension/host ./internal/bus'
ci_run 'Node RPC withdrawal race tests' 420 bash -c 'cd server && go test -race -p 2 -count=1 -timeout=5m -run "^TestNodeRPC" ./internal/node'
ci_run 'Server tests with Postgres' 1200 bash -c 'cd server && python3 ../scripts/test/server_test_env.py --database-file "$1" -- go test -p 2 -count=1 -timeout=10m ./...' bash "$fixture/database.json"
ci_run 'Server build' 600 bash -c 'cd server && go build ./...'
ci_run 'Windows node build and vet' 600 bash -c 'cd server && GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=amd64 go vet ./...'
ci_run 'Extension schema and SDK bundle' 300 bash -c 'python3 scripts/test/test_extension_schema.py && python3 scripts/test/test_sdk_bundle.py'
ci_run 'Go SDK tests' 300 bash -c 'cd sdk/go && go test -count=1 -timeout=4m ./...'
