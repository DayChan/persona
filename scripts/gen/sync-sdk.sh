#!/bin/sh
# Copies the extension SDK of this checkout into an extension repository (docs/extensions/extensions.md "Writing an extension: SDK, templates and CLI"):
# <repo>/sdk/go (the Go SDK: wire, ext, exttest, and a go.mod) and <repo>/sdk/ts (the TypeScript SDK), and records
# which commit it came from in <repo>/sdk/VERSION. Extension repositories keep this copy instead of fetching the
# private main repository; their go.mod replaces github.com/nwverse/persona/sdk/go with ./sdk/go.
#
#   scripts/gen/sync-sdk.sh <extension repository> [go|ts|all]
set -eu
here=$(cd "$(dirname "$0")/../.." && pwd)
dest=${1:?usage: scripts/gen/sync-sdk.sh <extension repository> [go|ts|all]}
what=${2:-all}
[ -d "$dest" ] || { echo "no such directory: $dest" >&2; exit 1; }
commit=$(git -C "$here" rev-parse --short HEAD 2>/dev/null || echo unknown)
dirty=$(git -C "$here" status --porcelain -- sdk 2>/dev/null | head -1)
mkdir -p "$dest/sdk"
if [ "$what" = all ] || [ "$what" = go ]; then
  rm -rf "$dest/sdk/go"
  mkdir -p "$dest/sdk/go"
  for d in wire ext exttest; do
    mkdir -p "$dest/sdk/go/$d"
    cp -R "$here/sdk/go/$d/." "$dest/sdk/go/$d/"
  done
  cp "$here/sdk/go/go.mod" "$dest/sdk/go/go.mod"
fi
if [ "$what" = all ] || [ "$what" = ts ]; then
  rm -rf "$dest/sdk/ts"
  mkdir -p "$dest/sdk/ts"
  # Keep the declared development commands usable; never copy installed dependencies or build output.
  cp -R "$here/sdk/ts/src" "$here/sdk/ts/scripts" "$here/sdk/ts/typechecks" "$dest/sdk/ts/"
  cp "$here/sdk/ts/package.json" "$here/sdk/ts/package-lock.json" "$here/sdk/ts/README.md" "$here"/sdk/ts/tsconfig*.json "$dest/sdk/ts/"
  # Tests use package-local canonical fixtures; do not overwrite the destination repository's schema.
  mkdir -p "$dest/sdk/ts/fixtures"
  cp "$here"/schema/extension/examples/UIStream*.json "$here/schema/extension/examples/Manifest.custom-contributions.json" "$dest/sdk/ts/fixtures/"
fi
{
  echo "persona $commit${dirty:+ (with uncommitted changes)}"
  echo "copied by scripts/gen/sync-sdk.sh; do not edit, copy again"
} > "$dest/sdk/VERSION"
echo "sync-sdk: $what from persona $commit${dirty:+ (with uncommitted changes)} into $dest/sdk"
