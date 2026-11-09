#!/bin/bash
# Publish personad for anyone to install (docs/operations/release.md "Publishing personad"): build the four binaries with the web client
# inside (no skills in them: those live in each machine's ~/.persona/skills), and the connector persona-link for the
# same four (the node service, docs/operations/link.md), check them for private values, write the release manifest latest.json
# (every file's sha256, docs/operations/hub-update.md "Publishing"), then deploy the site/ worker with dl/ and install.sh added, at the
# config's publish.site_url. Every hub of the release channel updates itself to it. The worker also runs the push
# relay (/relay/*): it is deployed with it.
#
#   curl -fsSL <publish.site_url>/install.sh | sh
#
#   ./scripts/release/publish.sh            # build, check, deploy
#   NO_DEPLOY=1 ./scripts/release/publish.sh  # build and check only (build-publish/site)
#   SITE_ONLY=1 ./scripts/release/publish.sh  # the pages only: site/ as committed with the release already served
#
# Never deploy site/ with a plain `wrangler deploy`: it serves without dl/ and install.sh, and every hub's update
# check and the install script 404 (docs/lessons/server.md).
#
# The downloads (dl/*.gz) live in the R2 bucket DL_BUCKET, not in the site: a Workers static file is at most 25 MiB
# (#2541). A release goes to one of two slots, <slot>/<name>, the one not served now; the deploy then switches dl/SLOT
# (a static file, deployed with latest.json) to it. The slot left keeps the release before until the next one, so a
# download that started before the deploy finishes. Only the newest version is served there. Every release also goes
# to release/<version>/ (its node builds and its manifest as manifest.json), kept: a hub's manager fetches the node build
# of the hub's own version from there (dl/<version>/…, docs/operations/node-update.md), a Docker image's version too (X.Y.Z, when
# the release is the tag vX.Y.Z). release.sh runs this after uploading to TestFlight.
set -euo pipefail
cd "$(dirname "$0")/../.."
_env=$(python3 scripts/persona_config.py env) || exit $?
eval "$_env"; unset _env
[ -n "$PERSONA_PUBLISH_URL" ] || { echo "publish.site_url is not set in the config (docs/operations/config.md)" >&2; exit 2; }
WRANGLER="${PERSONA_WRANGLER:-}"
DL_BUCKET="${PERSONA_DL_BUCKET:-persona-dl}" # the r2_buckets binding DL in site/wrangler.jsonc
# wr <dir> <args…>: wrangler in <dir> (a copy of site/, for its account).
wr() { local dir="$1"; shift; (cd "$dir" && if [ -n "$WRANGLER" ]; then "$WRANGLER" "$@"; else npx wrangler "$@"; fi); }
# check_served <SHA256SUMS> <slot>: what the site serves now is that release: dl/SLOT, and every download at its size.
check_served() {
  local sums="$1" slot="$2" dir name size got
  dir="$(dirname "$sums")"
  [ "$(curl -fsSL "$PERSONA_PUBLISH_URL/dl/SLOT" 2>/dev/null)" = "$slot" ] || { echo "served dl/SLOT is not $slot" >&2; return 1; }
  while read -r _ name; do
    size=""
    [ -f "$dir/$name" ] && size="$(wc -c <"$dir/$name" | tr -d ' ')"
    got="$(curl -fsSI "$PERSONA_PUBLISH_URL/dl/$name" | tr -d '\r' | awk 'tolower($1)=="content-length:"{print $2}')"
    [ -n "$got" ] || { echo "served dl/$name is missing" >&2; return 1; }
    [ -z "$size" ] || [ "$got" = "$size" ] || { echo "served dl/$name is $got bytes, not $size" >&2; return 1; }
  done < "$sums"
}
# settled <what> <command…>: the command passes now, or within about two minutes. Right after a deploy the site can
# still serve the release before for a while (the entry and the edge cache; three releases on 2026-11-06, #5698).
settled() {
  local what="$1" try
  shift
  for try in $(seq 1 12); do
    "$@" 2>/dev/null && return 0
    [ "$try" -lt 12 ] && sleep 10
  done
  "$@" || { echo "$what still differs about two minutes after the deploy" >&2; return 1; }
}
served_is() { [ "$(curl -fsSL "$PERSONA_PUBLISH_URL/dl/$1" 2>/dev/null)" = "$(cat "$2")" ]; }
# node_archives <dir>: the node builds among the downloads (personad-<os>-<arch>.gz, persona-node-<os>-<arch>.gz),
# the only ones a manager fetches from dl/<version>/.
node_archives() {
  local gz
  for gz in "$1"/*.gz; do
    [[ "${gz##*/}" =~ ^(personad|persona-node)-(linux|darwin)-(amd64|arm64)\.gz$ ]] && printf '%s\n' "$gz"
  done
  return 0
}
# check_versioned <dir> <version>: dl/<version>/ serves that release: its manifest, and every node build at its size.
check_versioned() {
  local dir="$1" v="$2" gz size got
  curl -fsSL "$PERSONA_PUBLISH_URL/dl/$v/manifest.json" | cmp -s - "$dir/latest.json" || { echo "served dl/$v/manifest.json differs" >&2; return 1; }
  while IFS= read -r gz; do
    size="$(wc -c <"$gz" | tr -d ' ')"
    got="$(curl -fsSI "$PERSONA_PUBLISH_URL/dl/$v/${gz##*/}" | tr -d '\r' | awk 'tolower($1)=="content-length:"{print $2}')"
    [ "$got" = "$size" ] || { echo "served dl/$v/${gz##*/} is ${got:-missing}, not $size bytes" >&2; return 1; }
  done < <(node_archives "$dir")
}
same_as_served() { curl -fsSL "$PERSONA_PUBLISH_URL/$1" | cmp -s - "$2"; }
# deploy_site <dir>: deploy that copy of site/ (with dl/ and install.sh in it), the log in <dir>/../deploy.log.
deploy_site() {
  local dir="$1" log="$1/../deploy.log"
  # cf's OAuth token lasts about an hour and wrangler does not renew it; any cf call that finds it stale does (twice a
  # release failed here with "Authentication error" until `cf auth whoami` ran).
  command -v cf >/dev/null && cf auth whoami >/dev/null 2>&1 || true
  wr "$dir" deploy >"$log" 2>&1 \
    || { tail -20 "$log" >&2; echo "deploy failed; see $log (only run cf auth login for authentication errors)" >&2; exit 1; }
}

# SITE_ONLY: no new build. The release served now (VERSION, SHA256SUMS, latest.json, SLOT and install.sh) is fetched
# back and deployed again next to site/ as committed; its downloads stay where they are, in the bucket's served slot.
if [ -n "${SITE_ONLY:-}" ]; then
  if [ -z "${NO_DEPLOY:-}" ] && [ -n "$(git status --porcelain -- site)" ]; then
    echo "site/ has changes not committed: commit them first" >&2; exit 1
  fi
  OUT=build-publish-site
  rm -rf "$OUT"
  mkdir -p "$OUT/dl"
  get() { curl -fsSL --retry 5 --retry-all-errors --retry-delay 3 --max-time 60 "$PERSONA_PUBLISH_URL/$1" -o "$2"; }
  for f in VERSION SHA256SUMS latest.json; do get "dl/$f" "$OUT/dl/$f"; done
  get install.sh "$OUT/install.sh"
  get dl/SLOT "$OUT/dl/SLOT" 2>/dev/null || { echo "the site serves no dl/SLOT (a release from before the bucket, #2541): run a full publish" >&2; exit 1; }
  SLOT="$(cat "$OUT/dl/SLOT")"
  case "$SLOT" in a|b) ;; *) echo "unexpected served dl/SLOT: $SLOT" >&2; exit 1 ;; esac
  while read -r _ name; do
    case "$name" in */*|""|.*) echo "unexpected name in the served SHA256SUMS: $name" >&2; exit 1 ;; esac
  done < "$OUT/dl/SHA256SUMS"
  VERSION="$(cat "$OUT/dl/VERSION")"
  cp -R site "$OUT/site"
  cp -R "$OUT/dl" "$OUT/site/public/dl"
  cp "$OUT/install.sh" "$OUT/site/public/install.sh"
  python3 scripts/deploy/check_asset_sizes.py "$OUT/site/public"
  if [ -n "${NO_DEPLOY:-}" ]; then echo "site with personad $VERSION ready in $OUT/site (not deployed)."; exit 0; fi
  deploy_site "$OUT/site"
  for f in VERSION SHA256SUMS latest.json; do
    settled "served dl/$f" same_as_served "dl/$f" "$OUT/dl/$f" || exit 1
  done
  settled "served install.sh" same_as_served install.sh "$OUT/install.sh" || exit 1
  settled "the served downloads" check_served "$OUT/dl/SHA256SUMS" "$SLOT" || exit 1
  echo "site deployed with personad $VERSION unchanged: $PERSONA_PUBLISH_URL"
  exit 0
fi

# The build number orders releases (UTC YYYYMMDDHHMM): hubs install only a larger one.
BUILD="$(date -u +%Y%m%d%H%M)"
VERSION="${VERSION:-$(git describe --always --dirty 2>/dev/null || echo dev)-$BUILD}"
COMMIT="$(git rev-parse HEAD)"
# A release is the committed code: latest.json says which commit and lists every one it contains, and a hub
# installs a release by itself only when its own commit is among them (docs/operations/hub-update.md "No rollback"). Published with
# changes not committed, every hub running it would wait for a person from then on. The binaries say which commit
# they are (STAMP, "-dirty" only in a NO_DEPLOY build).
STAMP="$COMMIT"
if [ -n "$(git status --porcelain -- server web)" ]; then
  [ -n "${NO_DEPLOY:-}" ] || { echo "server/ or web/ has changes not committed: commit them first" >&2; exit 1; }
  STAMP="$COMMIT-dirty"
fi
OUT=build-publish
rm -rf "$OUT"
mkdir -p "$OUT/bin"

# The web client goes into the binary (server/internal/hub/webdist, embedded); the placeholder stays.
WEBDIST=server/internal/hub/webdist
cleanup() { find "$WEBDIST" -mindepth 1 ! -name README.md -exec rm -rf {} + 2>/dev/null || true; }
trap cleanup EXIT
# The public registry, whatever this machine's npm is set to: package-lock.json resolves there, and a mirror can miss
# a package (2026-11-06 one lacked elkjs and the release stopped here).
NPM_REGISTRY="${PERSONA_NPM_REGISTRY:-https://registry.npmjs.org/}"
(cd web && { [ -d node_modules ] || npm ci --no-audit --no-fund --registry="$NPM_REGISTRY"; } && npm run build) >/dev/null
cleanup
cp -R web/dist/. "$WEBDIST/"
# The binaries' privacy scan below reads printable strings, which gzip hides: scan the web client as built first. Then
# keep its hashed assets gzipped, as the hub sends them (docs/web/web.md "Precompressed assets"), and check that it did.
python3 scripts/release/check_bundle_privacy.py --whole-short "$WEBDIST"
(cd server && go run ./internal/webasset/precompress internal/hub/webdist)
[ -n "$(find "$WEBDIST/assets" -type f -name '*.gz' -print -quit)" ] || { echo "no precompressed web assets in $WEBDIST" >&2; exit 1; }
left="$(find "$WEBDIST/assets" -type f -size +1023c \( -name '*.js' -o -name '*.mjs' -o -name '*.css' -o -name '*.svg' \
  -o -name '*.json' -o -name '*.txt' -o -name '*.html' -o -name '*.xml' -o -name '*.wasm' \) -print -quit)"
[ -z "$left" ] || { echo "web asset left uncompressed: $left" >&2; exit 1; }

# Public dependency constants are classified by build-info checksums; verify the actual module cache before building.
(cd server && go mod verify)
LDFLAGS="-s -w -X main.version=$VERSION -X main.channel=release -X main.build=$BUILD -X main.commit=$STAMP -X main.updateURL=$PERSONA_PUBLISH_URL/dl"
# The runtime packages personad carries (the DSH plugin's code, docs/integrations/runtimes.md "Auto-update"), from integrations/.
(cd server && go generate ./internal/runtimepkg)
# -tags public changes no code (no file builds differently with it, TestOneBuild): it marks the binary for
# check_bundle_privacy.py, which then knows the dependency constants it may skip.
for t in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64; do
  (cd server && GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build -tags public -trimpath -ldflags "$LDFLAGS" \
    -o "../$OUT/bin/personad-${t%/*}-${t#*/}" ./cmd/personad)
  (cd server && GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build -tags public -trimpath -ldflags "$LDFLAGS" \
    -o "../$OUT/bin/persona-link-${t%/*}-${t#*/}" ./cmd/persona-link)
  # The device's own program (cmd/persona-node): install.sh --node puts it at ~/.persona/bin/persona-node. It is in
  # SHA256SUMS, and in latest.json under nodes (a hub's own update still swaps personad alone; its manager fetches
  # persona-node from dl/<version>/ for a device that runs it, docs/operations/node-update.md "Node packages").
  (cd server && GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build -tags public -trimpath -ldflags "$LDFLAGS" \
    -o "../$OUT/bin/persona-node-${t%/*}-${t#*/}" ./cmd/persona-node)
done
# The programs released with personad (update.ProgramNames): built the same way, on the same platforms; a hub swaps
# them with personad. personad-wechat is the WeChat bot (docs/extensions/wechat.md), a plugin of its own. (Telegram's connector is
# an extension now, released from the extension market: #1704.)
for t in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64; do
  for p in wechat; do
    (cd server && GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build -tags public -trimpath -ldflags "$LDFLAGS" \
      -o "../$OUT/bin/personad-$p-${t%/*}-${t#*/}" "./cmd/personad-$p")
  done
  # The hub's and the manager's own programs (docs/server/server.md "Programs"): the same code as personad's hub and
  # manager commands, each without the other's.
  for p in persona-hub persona-manager; do
    (cd server && GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build -tags public -trimpath -ldflags "$LDFLAGS" \
      -o "../$OUT/bin/$p-${t%/*}-${t#*/}" "./cmd/$p")
  done
done
cleanup
python3 scripts/persona_identity.py --render-installer scripts/deploy/install.sh "$OUT/install.sh" "$PERSONA_PUBLISH_URL/dl"

# Anyone can download these: no private values in them (CLAUDE.md), and no copied skill (only the built-in ones
# assistantskills.Redistributable lists; a copied skill lives in the user's ~/.persona/skills or comes with its
# extension). install.sh is the repo's (scripts/privacy_check.py covers it) plus the site's address, which is public
# on purpose.
python3 scripts/release/check_bundle_privacy.py --whole-short "$OUT/bin"
python3 scripts/release/check_bundle_skills.py "$OUT/bin/personad-linux-amd64"
python3 scripts/release/check_bundle_skills.py "$OUT/bin/persona-manager-linux-amd64" # it embeds the same skills
host_bin="$OUT/bin/personad-$(uname -s | tr A-Z a-z)-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
[ "$("$host_bin" version)" = "$VERSION" ] || { echo "the built personad does not report $VERSION" >&2; exit 1; }
host_link="$OUT/bin/persona-link-${host_bin##*/personad-}"
[ "$("$host_link" version)" = "$VERSION" ] || { echo "the built persona-link does not report $VERSION" >&2; exit 1; }
host_node="$OUT/bin/persona-node-${host_bin##*/personad-}"
[ "$("$host_node" version -v)" = "$("$host_bin" version -v)" ] || { echo "persona-node is not the same build as personad" >&2; exit 1; }
"$host_bin" version -v | grep -qx "channel release, build $BUILD" || { echo "the built personad is not release build $BUILD" >&2; exit 1; }
for p in personad-wechat persona-hub persona-manager; do
  host_p="$OUT/bin/$p-${host_bin##*/personad-}"
  [ "$("$host_p" version -v)" = "$("$host_bin" version -v)" ] || { echo "$p is not the same build as personad" >&2; exit 1; }
done

# Releases only move forward: a larger build number than the one served, and the served commit an ancestor of this one.
curl -fsSL "$PERSONA_PUBLISH_URL/dl/latest.json" -o "$OUT/served.json" 2>/dev/null || : > "$OUT/served.json"
"$host_bin" release check-next "$OUT/served.json" --build "$BUILD" --commit "$COMMIT" || exit 1

# The privacy scan checked the raw binaries; only the gzipped ones are published (to the bucket, see above).
for bin in "$OUT/bin"/personad-* "$OUT/bin"/persona-link-* "$OUT/bin"/persona-node-* "$OUT/bin"/persona-hub-* "$OUT/bin"/persona-manager-*; do
  gzip -n -9 -c "$bin" > "$bin.gz"
done
python3 scripts/persona_identity.py --artifact-aliases "$OUT/bin"
# Include the device node and compatibility copies in the same verified download index.
(cd "$OUT/bin" && shasum -a 256 *.gz > SHA256SUMS && printf '%s\n' "$VERSION" > VERSION)

# The manifest hubs update from: version, build number, commit and every download's sha256 (not signed, the user's
# call: it guards against a broken download over HTTPS, docs/operations/hub-update.md "Security").
"$host_bin" release manifest --dir "$OUT/bin" --version "$VERSION" --build "$BUILD" --commit "$COMMIT" --out "$OUT/bin/latest.json"

# The site with dl/ and install.sh added, deployed from a copy (site/ in the tree stays as committed).
rm -rf "$OUT/site"
cp -R site "$OUT/site"
mkdir -p "$OUT/site/public/dl"
# The slot not served now (a when nothing is): the release before stays in the other one until the next release.
served_slot="$(curl -fsSL "$PERSONA_PUBLISH_URL/dl/SLOT" 2>/dev/null || true)"
SLOT=a
[ "$served_slot" = a ] && SLOT=b
printf '%s\n' "$SLOT" > "$OUT/bin/SLOT"
cp "$OUT/bin/SHA256SUMS" "$OUT/bin/VERSION" "$OUT/bin/latest.json" "$OUT/bin/SLOT" "$OUT/site/public/dl/"
cp "$OUT/install.sh" "$OUT/site/public/install.sh"
python3 scripts/deploy/check_asset_sizes.py "$OUT/site/public"
if [ -n "${NO_DEPLOY:-}" ]; then
  echo "personad $VERSION built and checked in $OUT (not deployed)."
  exit 0
fi
# The downloads first, into the slot no one is served from; then the deploy that points dl/SLOT at it.
# cf's OAuth token lasts about an hour and runs out between uploads too (twice on 2026-11-06, #4202): refresh it before
# each file, and once more before the one retry.
refresh_cf() { command -v cf >/dev/null && cf auth whoami >/dev/null 2>&1 || true; }
# put <file>: upload it into the slot. Absolute paths: wr runs wrangler inside the site copy.
# put_at <key> <file> <content type>: upload it to that key of the bucket, once more after a refresh when it fails.
put_at() {
  local key="$1" file="$2" type="$3"
  refresh_cf
  up() { wr "$OUT/site" r2 object put "$DL_BUCKET/$key" --file "$file" --content-type "$type" --remote >>"$OUT/upload.log" 2>&1; }
  up || { sleep 5; refresh_cf; up; } \
    || { tail -20 "$OUT/upload.log" >&2; echo "upload of $key to R2 failed; see $OUT/upload.log" >&2; exit 1; }
}
for gz in "$PWD/$OUT/bin/"*.gz; do
  put_at "$SLOT/${gz##*/}" "$gz" application/gzip
done
# The versioned copies, kept: release/<version>/ (and release/X.Y.Z/ when the release is the tag vX.Y.Z, the version
# of the Docker image built from that tag), the node builds and the manifest only: a manager fetches a node build from
# there (docs/operations/node-update.md). The tag is looked up among all the commit's tags (release.sh tags it testflight/<build>
# too, and a lightweight vX.Y.Z is not what git describe picks).
VERSIONS=("$VERSION")
# A build with changes not committed (-dirty) is not the tag's release, and gets no alias.
if [[ "$VERSION" != *-dirty* ]] && tag="$(git tag --points-at HEAD | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1)" && [ -n "$tag" ]; then
  VERSIONS+=("${tag#v}")
fi
NODE_ARCHIVES=()
while IFS= read -r gz; do NODE_ARCHIVES+=("$gz"); done < <(node_archives "$PWD/$OUT/bin")
for v in "${VERSIONS[@]}"; do
  for gz in "${NODE_ARCHIVES[@]}"; do
    put_at "release/$v/${gz##*/}" "$gz" application/gzip
  done
  put_at "release/$v/manifest.json" "$PWD/$OUT/bin/latest.json" application/json
done
deploy_site "$OUT/site"

# What is served now is what was built (allowing the site a moment to switch over).
settled "served dl/VERSION" served_is VERSION "$OUT/bin/VERSION" \
  || { echo "deployed, but $PERSONA_PUBLISH_URL/dl/VERSION says '$(curl -fsSL "$PERSONA_PUBLISH_URL/dl/VERSION" 2>/dev/null)'" >&2; exit 1; }
settled "served dl/SHA256SUMS" same_as_served dl/SHA256SUMS "$OUT/bin/SHA256SUMS" || exit 1
settled "served dl/latest.json" same_as_served dl/latest.json "$OUT/bin/latest.json" || exit 1
settled "the served downloads" check_served "$OUT/bin/SHA256SUMS" "$SLOT" || exit 1
for v in "${VERSIONS[@]}"; do
  settled "dl/$v/" check_versioned "$OUT/bin" "$v" || exit 1
done
echo "personad $VERSION published: curl -fsSL $PERSONA_PUBLISH_URL/install.sh | sh"
