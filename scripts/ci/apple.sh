#!/bin/bash
# Public, local-runnable Apple checks. Requires macOS 26, Xcode 26.0.x,
# XcodeGen, Python 3 and rsync. No developer account, hub or personal config.
# Usage: scripts/ci/apple.sh [all|ios|macos|packages]
# CI: macos-26, DEVELOPER_DIR=/Applications/Xcode_26.0.1.app/Contents/Developer.
# Cache CI_SPM_CACHE (default ~/Library/Caches/persona-ci/spm); keep the printed
# output directory's *.xcresult bundles when investigating failures.
set -euo pipefail
source "$(dirname "$0")/common.sh"
LANE="${1:-all}"
case "$LANE" in all|ios|macos|packages) ;; *) printf 'Usage: %s [all|ios|macos|packages]\n' "$0" >&2; exit 2 ;; esac
[ "$#" -le 1 ] || { printf 'apple-ci: too many arguments\n' >&2; exit 2; }
[ "$(uname -s)" = Darwin ] || { printf 'apple-ci: FAIL requires macOS 26 and Xcode 26.0.x\n' >&2; exit 1; }
for tool in python3 xcodebuild xcrun xcodegen rsync git; do
  command -v "$tool" >/dev/null || { printf 'apple-ci: FAIL missing %s\n' "$tool" >&2; exit 1; }
done
# Never silently fall back to the hosted image's newer default compiler.
export DEVELOPER_DIR="${DEVELOPER_DIR:-/Applications/Xcode_26.0.1.app/Contents/Developer}"
XCODE_VERSION=$(xcodebuild -version)
printf '%s\n' "$XCODE_VERSION"
case "$XCODE_VERSION" in 'Xcode 26.0'*$'\n'*) ;; *) printf 'apple-ci: FAIL select Xcode 26.0.x explicitly\n' >&2; exit 1 ;; esac
MACOS_VERSION=$(sw_vers -productVersion)
[ "${MACOS_VERSION%%.*}" -ge 26 ] || { printf 'apple-ci: FAIL macOS 26 is needed to run the Mac/PersonaKit tests\n' >&2; exit 1; }
xcrun swift --version
xcodegen --version

OUTPUT=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/persona-apple-ci.XXXXXX")
DIAGNOSTICS="${CI_APPLE_DIAGNOSTICS:-$OUTPUT/diagnostics}"
SOURCE="$OUTPUT/source"
PROJECT="$SOURCE/apple/Persona.xcodeproj"
SPM="${CI_SPM_CACHE:-$HOME/Library/Caches/persona-ci/spm}"
mkdir -p "$SOURCE" "$SPM"
SPM=$(cd "$SPM" && pwd)
SIM=""
SIM_STARTED=0
SIM_NAME="Persona CI $(basename "$OUTPUT")"
cleanup() {
  local code=$?
  trap - EXIT
  # A failed boot may already have created the device without returning its ID.
  if [ -z "$SIM" ] && [ "$SIM_STARTED" = 1 ]; then
    SIM=$(python3 -I - "$SIM_NAME" <<'PY'
import json, subprocess, sys
try:
    data = json.loads(subprocess.check_output(["xcrun", "simctl", "list", "devices", "-j"], text=True, timeout=60))
    hits = [d["udid"] for devices in data["devices"].values() for d in devices if d["name"] == sys.argv[1]]
    if len(hits) == 1:
        print(hits[0])
except (OSError, ValueError, subprocess.SubprocessError) as error:
    print(f"apple-ci: could not locate owned simulator for cleanup: {error}", file=sys.stderr)
PY
    ) || true
  fi
  if [ "$code" -ne 0 ]; then
    # Capture simulator state before cleanup; only allowlisted logs and xcresult
    # bundles leave the disposable output, never synthetic/private config or keys.
    ci_run 'Collect Apple failure diagnostics' 180 python3 "$CI_ROOT/scripts/ci/apple_diagnostics.py" \
      "$OUTPUT" "$DIAGNOSTICS" || printf 'apple-ci: failure diagnostics could not be collected\n' >&2
  fi
  if [ -n "$SIM" ]; then
    ci_run 'Shut down owned simulator' 120 xcrun simctl shutdown "$SIM" || true
    ci_run 'Delete owned simulator' 120 xcrun simctl delete "$SIM" || true
  fi
  printf 'apple-ci: %s (exit %s); evidence: %s\n' "$([ "$code" = 0 ] && printf PASS || printf FAIL)" "$code" "$OUTPUT"
  exit "$code"
}
trap cleanup EXIT
printf 'apple-ci: output %s; SwiftPM cache %s\n' "$OUTPUT" "$SPM"

# XcodeGen writes tracked plists/entitlements, and the normal config helper writes
# Config.xcconfig. Generate in a snapshot, not over a developer's private files.
# git's file list retains local source edits but excludes ignored build/config
# files. Copy all repository inputs: source-based tests read schema/, spec/,
# Android design tokens, the release script and the hub's persona assets too.
git ls-files --cached --others --exclude-standard -z -- . ':(exclude).ci-cache/**' |
  python3 -I -c 'import os, sys; root = sys.argv[1]; paths = sys.stdin.buffer.read().split(b"\0"); sys.stdout.buffer.write(b"".join(p + b"\0" for p in paths if p and os.path.lexists(os.path.join(os.fsencode(root), p))))' \
    "$CI_ROOT" > "$OUTPUT/inputs"
ci_run 'Snapshot Apple source and test fixtures' 120 rsync -a --from0 --files-from="$OUTPUT/inputs" "$CI_ROOT/" "$SOURCE/"
export PERSONA_CONFIG="$OUTPUT/config.json"
python3 -I - "$PERSONA_CONFIG" <<'PY'
import json, sys
with open(sys.argv[1], "w") as output:
    json.dump({"app": {"bundle_id": "org.example.persona.ci", "team_id": ""}}, output)
PY
# These are explicitly public synthetic values; persona_config never opens the
# developer's config. DEVELOPER_DIR already selected above wins in its env helper.
export PERSONA_META_WEARABLES=NO
ci_run 'Generate public unsigned Apple config' 60 python3 "$SOURCE/scripts/persona_config.py" xcconfig
ci_run 'Generate Apple project' 120 xcodegen generate --spec "$SOURCE/apple/project.yml" --project "$SOURCE/apple"
[ -s "$SOURCE/apple/PersonaTests/TokenBaseline.json" ] || { printf 'apple-ci: FAIL committed token baseline missing\n' >&2; exit 1; }

UNSIGNED=(CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO CODE_SIGN_IDENTITY= DEVELOPMENT_TEAM=)
# Runnable hosts use local ad-hoc signatures, not distribution signatures.
# Unsigned simulator runs are not equivalent for Keychain/persistence tests
# (docs/lessons/testing.md #6192). No certificate/profile or secret is required.
TEST_SIGNING=(CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=- CODE_SIGN_STYLE=Manual DEVELOPMENT_TEAM= PROVISIONING_PROFILE_SPECIFIER=)
XCODE=(xcodebuild -project "$PROJECT" -configuration Debug -clonedSourcePackagesDirPath "$SPM/SourcePackages")
resolve() {
  ci_run "Resolve Swift packages ($1)" 900 "${XCODE[@]}" -scheme "$1" -resolvePackageDependencies
}
result() {
  local name="$1"; shift
  ci_run "Inspect test results ($name)" 120 python3 "$CI_ROOT/scripts/ci/apple_result.py" "$OUTPUT/$name.xcresult" "$@"
}

if [ "$LANE" = all ] || [ "$LANE" = ios ] || [ "$LANE" = packages ]; then
  ci_run 'PersonaKit unit tests' 900 xcrun swift test --package-path "$SOURCE/apple/Packages/PersonaKit" --scratch-path "$SPM/PersonaKit"
  # The Persona Link menu bar app's package (apple/PersonaLink): its own SwiftPM package, outside apple/project.yml.
  ci_run 'Persona Link unit tests' 600 xcrun swift test --package-path "$SOURCE/apple/PersonaLink" --scratch-path "$SPM/PersonaLink" -Xswiftc -warnings-as-errors
fi
if [ "$LANE" = all ] || [ "$LANE" = ios ]; then
  for sdk in iphoneos iphonesimulator watchos watchsimulator; do
    ci_run "Require $sdk SDK" 60 xcrun --sdk "$sdk" --show-sdk-version
  done
  # Current hosted images omit the Xcode 26.0-compatible iOS/watch runtimes.
  # Generic device builds validate both platforms for the embedded watch app,
  # even though SDK lookup succeeds. Prepare both before resolving/building.
  # The pinned Xcode selects compatible patch runtimes (such as iOS 26.0.1).
  # Validate 26.0.x explicitly; never substitute a newer minor or compiler.
  for platform in iOS watchOS; do
    runtimes="$OUTPUT/runtimes-$platform-before.json"
    ci_run "List $platform simulator runtimes" 120 bash -c 'xcrun simctl list runtimes -j > "$1"' -- "$runtimes"
    if ! runtime=$(python3 "$CI_ROOT/scripts/ci/apple_runtime.py" "$runtimes" --platform "$platform"); then
      ci_run "Download matching $platform 26.0 simulator runtime" 1200 xcodebuild -downloadPlatform "$platform" -architectureVariant universal
      runtimes="$OUTPUT/runtimes-$platform-after.json"
      ci_run "List downloaded $platform simulator runtimes" 120 bash -c 'xcrun simctl list runtimes -j > "$1"' -- "$runtimes"
      runtime=$(python3 "$CI_ROOT/scripts/ci/apple_runtime.py" "$runtimes" --platform "$platform")
    fi
    printf 'apple-ci: selected compatible %s runtime %s\n' "$platform" "$runtime"
    if [ "$platform" = iOS ]; then IOS_RUNTIME="$runtime"; fi
  done
  resolve Persona
  # Generic device builds compile the actual watch/widget/notification/share
  # dependency graph; do not delete the watch target to accommodate the runner.
  ci_run 'Unsigned iOS and embedded watch/extensions build' 1800 "${XCODE[@]}" -scheme Persona \
    -destination 'generic/platform=iOS' -derivedDataPath "$OUTPUT/ios-device" \
    -disableAutomaticPackageResolution "${UNSIGNED[@]}" build

  # sim-boot is the sole creation/boot entry, with a fresh stock simulator (not
  # paired to a real hub), pinned to the validated compiler-compatible runtime.
  SIM_STARTED=1
  ci_run 'Create and boot stock iOS 26.0 test simulator' 1000 bash -c \
    'SIM_BOOT_TIMEOUT=900 "$1" --profile stock --runtime "$4" --device-type "iPhone 17 Pro" "$2" > "$3" 2> >(tee "$5" >&2)' \
    -- "$SOURCE/scripts/app/sim-boot.sh" "$SIM_NAME" "$OUTPUT/simulator.log" "$IOS_RUNTIME" "$OUTPUT/simulator-boot.log"
  # Accept only the helper's single UDID, never a partial/error log.
  SIM=$(python3 -I - "$OUTPUT/simulator.log" <<'PY'
import re, sys
matches = re.findall(r"(?m)^([0-9A-Fa-f]{8}-(?:[0-9A-Fa-f]{4}-){3}[0-9A-Fa-f]{12})$", open(sys.argv[1]).read())
if len(matches) != 1:
    sys.exit("apple-ci: FAIL simulator helper did not return exactly one UDID")
print(matches[0])
PY
  )
  # The real EventKit round-trip uses only this disposable simulator's store.
  # TCC grants can precede installation of the synthetic CI app.
  CI_BUNDLE_ID=$(python3 -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["app"]["bundle_id"])' "$PERSONA_CONFIG")
  # On a just-booted stock simulator the grant waits for the device's own services: 54.5 s on a passing hosted run,
  # over 60 s on a failing one (#1402). A single attempt with room to spare; a hang still ends the lane.
  ci_run 'Grant Reminders access to the owned test app' 300 xcrun simctl privacy "$SIM" grant reminders "$CI_BUNDLE_ID"
  export TEST_RUNNER_PERSONA_EVENTKIT_ROUNDTRIP=1

  IOS_TESTS=("${XCODE[@]}" -scheme Persona -destination "platform=iOS Simulator,id=$SIM" \
    -destination-timeout 120 -derivedDataPath "$OUTPUT/ios-tests" -parallel-testing-enabled NO \
    -disableAutomaticPackageResolution "${TEST_SIGNING[@]}")
  # Explicit fixture-only exclusions, not evidence of those integrations passing.
  # Swift Testing method selectors include (); without it disabled integration
  # cases remain selected and appear as unexpected skips in the result bundle.
  IOS_EXCLUSIONS=(
    -skip-testing:PersonaTests/OldHubCompatibilityIntegrationTests
    "-skip-testing:PersonaTests/NotificationServiceTests/opensWhatTheRelayDelivered()"
    "-skip-testing:PersonaTests/VideoFileTests/nativeVideoPlaysSeeksAndCleansUp()"
    "-skip-testing:PersonaTests/VideoFileResourceTests/realHTTPReadsMiddleTailAndRejectsChangedRevision()"
  )
  printf '%s\n' 'apple-ci: excluded integration cases: old-hub lab; relay/APNs payload lab; native video fixture; real video HTTP fixture; destructive HTML loop (dedicated external watchdog required). UI tests are a separate mock-hub/screenshot lane.'
  # WebKit suites must run in separate batches, not interleave in Swift Testing
  # despite @MainActor or xcodebuild's parallel-testing switch (#2025).
  ci_run 'iOS offline unit and design/token tests' 1800 "${IOS_TESTS[@]}" "${IOS_EXCLUSIONS[@]}" \
    -only-testing:PersonaTests \
    -skip-testing:PersonaTests/AssistantHTMLCardTests -skip-testing:PersonaTests/AssistantHTMLSecurityTests \
    -resultBundlePath "$OUTPUT/ios-unit.xcresult" test
  result ios-unit TypeScaleTests IconVocabularyTests TokenRatchetTests StyleSpecTests
  ci_run 'iOS HTML card tests (separate batch)' 600 "${IOS_TESTS[@]}" \
    -only-testing:PersonaTests/AssistantHTMLCardTests -resultBundlePath "$OUTPUT/ios-html-card.xcresult" test
  result ios-html-card AssistantHTMLCardTests
  ci_run 'iOS bounded HTML security tests (separate batch)' 600 "${IOS_TESTS[@]}" \
    -only-testing:PersonaTests/AssistantHTMLSecurityTests \
    "-skip-testing:PersonaTests/AssistantHTMLSecurityTests/aRealInfiniteLoopCanBeDiscardedWithoutBlockingANewCard()" \
    -resultBundlePath "$OUTPUT/ios-html-security.xcresult" test
  result ios-html-security AssistantHTMLSecurityTests
  ci_run 'Verify token baseline was not rewritten' 60 cmp "$CI_ROOT/apple/PersonaTests/TokenBaseline.json" "$SOURCE/apple/PersonaTests/TokenBaseline.json"
fi
if [ "$LANE" = all ] || [ "$LANE" = macos ]; then
  ci_run 'Require macOS SDK' 60 xcrun --sdk macosx --show-sdk-version
  resolve PersonaMac
  ci_run 'Unsigned macOS app and notification extension build' 1800 "${XCODE[@]}" -scheme PersonaMac \
    -destination 'generic/platform=macOS' -derivedDataPath "$OUTPUT/macos-build" \
    -disableAutomaticPackageResolution "${UNSIGNED[@]}" build
  # The real unsigned build above keeps all shipping entitlements. A Mac ad-hoc
  # test host cannot claim profile-authorized push or shared Keychain groups:
  # AMFI rejects it before main, even when codesign --verify succeeds. Prepare
  # only the disposable snapshot for offline execution; retain its sandbox,
  # other permissions, hardened-runtime settings and ad-hoc signatures.
  ci_run 'Prepare profile-free Mac offline test entitlements' 60 python3 "$CI_ROOT/scripts/ci/apple_entitlements.py" \
    "$SOURCE/apple/Mac/Resources/PersonaMac.entitlements" \
    "$SOURCE/apple/PersonaNotify/PersonaMacNotify.entitlements"
  printf '%s\n' 'apple-ci: excluded Mac integration suites: ExtensionStreamSnapshotTests (5 live extension/hub cases requiring PERSONA_STREAM_TEST_PAIR); OldHubCompatibilityIntegrationTests (old-hub lab requiring PERSONA_COMPAT_TEST_URL, excluded on iOS too). Mac UI/screenshot tests are a separate lane.'
  ci_run 'macOS offline unit tests' 1800 "${XCODE[@]}" -scheme PersonaMac \
    -destination 'platform=macOS' -derivedDataPath "$OUTPUT/macos-tests" -parallel-testing-enabled NO \
    -disableAutomaticPackageResolution "${TEST_SIGNING[@]}" \
    -only-testing:PersonaMacTests -skip-testing:PersonaMacTests/ExtensionStreamSnapshotTests \
    -skip-testing:PersonaMacTests/OldHubCompatibilityIntegrationTests \
    -resultBundlePath "$OUTPUT/macos-unit.xcresult" test
  result macos-unit
fi
