#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
mode="${1:-smoke}"
if [ "$mode" = version ]; then
  if [ "${GITHUB_REF_TYPE:-}" = tag ]; then
    version="${GITHUB_REF_NAME#v}"
  else
    version="$(git describe --always)-$(date -u +%Y%m%d%H%M)"
  fi
  image="ghcr.io/$(printf '%s' "${GITHUB_REPOSITORY:?}" | tr '[:upper:]' '[:lower:]')"
  printf 'version=%s\nimage=%s\n' "$version" "$image" >> "${GITHUB_OUTPUT:?}"
  exit 0
fi
if [ "$mode" != smoke ]; then
  printf 'usage: scripts/ci/hub-image.sh [version|smoke]\n' >&2
  exit 2
fi
version="${CI_IMAGE_VERSION:-dev}"
image="persona-hub:ci"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/persona-ci-image.XXXXXX")"
# A compose project name takes only lowercase letters, digits, "-" and "_": the folder's "." (and any other) becomes "-".
export COMPOSE_PROJECT_NAME="persona-ci-$(basename "$fixture" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9_\n-' '-')"
cleanup() {
  local status=$?
  if [ -f "$fixture/.env" ]; then
    (cd "$fixture" && docker compose logs --tail=40 && docker compose down --volumes --remove-orphans) || true
    docker run --rm --user 0 --entrypoint sh \
      --mount "type=bind,src=$fixture,dst=/fixture" "$image" -c 'rm -rf /fixture/data /fixture/postgres' || true
  fi
  rm -f "$fixture/.env" "$fixture/compose.yaml"
  rmdir "$fixture" 2>/dev/null || true
  printf 'Hub image smoke finished (exit %s)\n' "$status"
}
trap cleanup EXIT
ci_run 'Container privacy' 180 python3 scripts/privacy_check.py
if [ "${CI_IMAGE_PREBUILT:-0}" != 1 ]; then
  ci_run 'Hub image build (amd64)' 2400 docker buildx build --load --platform linux/amd64 \
    --build-arg "VERSION=$version" --build-arg NODE_PLATFORMS=linux/amd64 -t "$image" .
fi
cp deploy/docker/compose.yaml "$fixture/compose.yaml"
read -r port tls_port < <(python3 - <<'PY'
import socket
sockets = [socket.socket(), socket.socket()]
for sock in sockets:
    sock.bind(('127.0.0.1', 0))
print(*(sock.getsockname()[1] for sock in sockets))
PY
)
printf 'PERSONA_IMAGE=%s\nPERSONA_ADMIN_ACCESS=loopback\nPERSONA_PORT=127.0.0.1:%s\nPERSONA_TLS_PORT=127.0.0.1:%s\nPERSONA_PUBLIC_URL=http://127.0.0.1:%s\nPUID=%s\nPGID=%s\n' \
  "$image" "$port" "$tls_port" "$port" "$(id -u)" "$(id -g)" > "$fixture/.env"
export CI_IMAGE_VERSION="$version" CI_IMAGE_FIXTURE="$fixture" CI_IMAGE_PORT="$port" CI_IMAGE_TLS_PORT="$tls_port"
ci_run 'Compose hub smoke' 300 bash "$CI_ROOT/scripts/ci/image-smoke.sh"
