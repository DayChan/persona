# TestFlight releases

TestFlight internal testing (testers in the configured `app_store.testers`), plus external testing joined through a public link (see "External testing" at the end).

## Accounts and identifiers

The product is Persona (previously under another product name), but the existing Bundle IDs, App Group, keychain groups, signing profile names and App Store Connect app record stay the same; don't recreate the App because of the rename.
Since 2026-11-03 the store name in App Store Connect is **Persona Side** (formerly "Persona Herd"; the home screen still shows `CFBundleDisplayName`, Persona), and the privacy policy URL is `app_store.external.privacy_url` (the website's privacy policy page). The store name lives in the App Info localizations (`name` in `appInfoLocalizations`) and can be changed directly via the API while the version hasn't been submitted for review; see "Changing the store name" below. "Persona App Store" and "Persona Watch App Store" below are existing signing profile names and stay as they are; the generated project / targets and archive / `.app` products use the Persona name, and old project / scheme aliases and module identity are in [compatibility migration](rename.md). Historical build records keep the names of their time. New default config and `personad` / `personad` compatibility are in [naming and configuration](config.md#naming-and-configuration).

All in the build config (`docs/operations/config.md`): `app.bundle_id` / `app.team_id`, `app_store.key_id` / `issuer_id` / `key_path` (the API key file lives only
outside the repo, e.g. `~/.appstoreconnect/`), `app_store.app_id` (the app record's numeric ID), `app_store.testers`, `app_store.feedback_email`.

| Item | Notes |
| --- | --- |
| Bundle ID | iOS `<bundle_id>`, watch `<bundle_id>.watchkitapp`, Live Activity `<bundle_id>.liveactivity`, notification service extension `<bundle_id>.notify` (push relay, `docs/operations/push-relay.md`), share extension `<bundle_id>.share` (#4586); new watch widget `app.watch_widget_bundle_id` (default `<bundle_id>.watchkitapp.widgets`, #1147). `scripts/release/asc_signing.py` covers these targets; #1147 did not register the new resources |
| App ID capabilities | iOS: `PUSH_NOTIFICATIONS`, `USERNOTIFICATIONS_TIMESENSITIVE` (time-sensitive notifications, #1572), `HEALTHKIT`, `APP_GROUPS`; Live Activity: `APP_GROUPS`; watch app and watch widget: `APP_GROUPS` using the separate configured watch group. The App Group itself can only be created on the website and attached to App IDs ("App Group" below) |
| Provisioning profiles | "Persona App Store", "Persona Watch App Store", "Persona Share App Store", etc. (`IOS_APP_STORE`; the share extension's was added 2026-11-06, so run `asc_signing.py` once before its first release to register its Bundle ID and profile); `asc_signing.py` deletes and recreates them and installs them into `~/Library/MobileDevice/Provisioning Profiles/` |
| Signing identity | `Apple Distribution: … (team_id)` in the login keychain |
| App Store Connect App ID | After creating the app record on the website, put it in the config's `app_store.app_id` |

The API key's role is App Manager, which can't use cloud-managed signing (`-allowProvisioningUpdates` cloud signing fails), so
archiving uses automatic signing, and at export `release.sh` generates ExportOptions from config (`scripts/persona_config.py export-options`) and re-signs with the two App Store profiles specified manually.

## 现有描述文件的名称（#6150）

产品名称不决定已经注册的描述文件名称。若签名预检因新默认名称报 `missing or invalid`，先在构建配置的 `app_store.provisioning_profiles` 中填写各 Bundle ID 对应的现有名称，再运行 `asc_signing.py --check`。签名预检与 ExportOptions 使用同一个映射；未配置的 target 仍使用 Persona 默认名称。

此配置只选择已有资源，不修改 Bundle ID、证书、capability 或描述文件。不要为名称不匹配直接运行不带 `--check` 的签名脚本：该脚本会删除并重建所选描述文件。若现有描述文件确实失效或缺少权限，预检仍会阻止发布，需要单独处理。

`--dev` 仍单独处理开发描述文件：名称中的 `App Store` 替换为 `Development`；自定义名不含 `App Store` 时追加 ` Development`，不复用或删除同名的发布描述文件。

## First-time setup (in order)

1. Fill in `~/.config/persona/config.json` (`app`, and the key part of `app_store`).
2. `python3 scripts/release/asc_signing.py`: registers the two Bundle IDs, turns on push, time-sensitive notifications and HealthKit for iOS, creates the two App Store profiles and installs them locally. Idempotent; rerun whenever capabilities change.
3. **Create the app record on the website** (the API can't): App Store Connect › Apps › New App: iOS, name, primary language, Bundle ID, SKU, full access.
   Put the App ID in the config's `app_store.app_id`.
4. `python3 scripts/release/asc_testflight.py setup`: creates the internal group `Internal` (`hasAccessToAllBuilds`) and adds the configured testers.
5. `NO_UPLOAD=1 ./scripts/release/release.sh` to verify archive + export (works without an app record).
6. `./scripts/release/release.sh` uploads the first build → `python3 scripts/release/asc_testflight.py wait` waits for processing and adds it to Internal automatically.

Without `app_store.app_id`, `scripts/release/asc_testflight.py` fails right away with a hint.

## Every release

```sh
./scripts/release/release.sh                      # build number = yyyymmddHHMM; archive, check, upload, tag testflight/<build number>
python3 scripts/release/asc_testflight.py status  # recent builds and their processing state
python3 scripts/release/asc_testflight.py info "What to Test for this build"
git push --tags
```

The build number can also be given by hand: `./scripts/release/release.sh 42`.

What `release.sh` does:

1. `xcodegen generate` (the source is `apple/project.yml`, generating `Persona.xcodeproj`; the old `Persona.xcodeproj` is a symlink to the project entry, and generated content isn't committed).
2. Release archive to `~/Library/Developer/Xcode/Archives/<date>/Persona <build number>.xcarchive` (dSYMs kept for Organizer symbolication),
   with the watch App embedded.
3. `check_bundle_strings.py` checks `Persona.app` and `Persona.app/Watch/Persona.app`: `*UsageDescription` in Info.plist
   must not be empty or equal to the key, and the same for `*.lproj/InfoPlist.strings` (placeholder text gets auto-rejected by App Review).
4. `strings` checks that the Release binary contains none of the simulator switches that exist only under `#if DEBUG`, such as `pairURL`, `openSession`, `showNewChat`.
   `check_bundle_privacy.py` checks that the whole bundle has no personal values (external testing is a public link, see the end).
5. `xcodebuild -exportArchive`: manual signing + `method=app-store-connect` + the two profile mappings. **This step is the App Store signing**:
   the archive uses automatically signed development profiles (so `aps-environment` in the archive is `development`), and export re-signs with the App Store profiles to
   `production`. With `NO_UPLOAD=1`, `destination` changes from `upload` to `export`, and it additionally checks that `aps-environment` in the exported ipa
   really is `production`.
6. `scripts/release/entitlements.py check`: in the archive (plus the exported ipa with `NO_UPLOAD=1`), every bundle carries its target's keychain groups, and the group name in Info.plist is among them; see "entitlements" below.
7. `git tag -f testflight/<build number>`.

A build shows up in TestFlight about 10–30 minutes after archiving. `asc_testflight.py wait [minutes] [build number]` checks every 30 seconds until
`processingState=VALID`, then writes the default What to Test and adds it to the `Internal` group; `INVALID` / `FAILED` exits with an error
(the reason is on the build page in App Store Connect). **Pass the build number release.sh printed**: a just-uploaded build can't be found yet, and without the build number
"the latest build" is the previous one (long since VALID), so What to Test gets written to the old build (hit on 2026-10-25).

## Unattended default-branch releases

`scripts/operations/autorelease.py` is a standard-library worker for a user-level systemd timer, not an Agent-session task.
Install the worker in `~/.local/lib/persona-autorelease/`, its private JSON configuration in
`~/.local/state/persona-autorelease/config.json`, and the supplied `persona-autorelease.service` and
`persona-autorelease.timer` from `scripts/operations/` in `~/.config/systemd/user/`.
Enable linger for the user if the service must survive logout. The timer checks every ten minutes and coalesces intervening commits.

CI admission belongs to the PR's local verification: run every affected platform check and `scripts/privacy_check.py` before
merging. PRs start no hosted checks. A scheduler explicitly dispatches full CI on `main`, including iOS and Mac; Mac remains
independent of the `CI required` aggregate. Failures are fixed separately rather than being treated as local PR verification.
The unattended worker consumes the newest already-merged `origin/main`, then applies path selection and local build/release verification.
It does not wait for the scheduled main CI result before releasing; the two checks are separate operational gates.

Each target has its own last-success revision, path patterns, command argument vector, and timeout. Worker events are appended to
`worker.log` in the private state directory; `log_output: true` additionally retains command output in mode-0600 per-step files
under its `logs/` subdirectory. Do not enable shell tracing or print credentials from action commands. Recommended triggers:

| Target | Paths |
| --- | --- |
| Hub | `server/**`, `integrations/**`, `sdk/go/**`, `schema/**`, `spec/**`, `scripts/deploy/**`, `scripts/operations/**`, `scripts/persona_config.py`, `scripts/persona_identity.py` |
| Web | `web/**`, `schema/extension/**`, `spec/fixtures/injected/**`, `scripts/deploy/**`, `scripts/operations/**`, `scripts/persona_config.py`, `scripts/persona_identity.py` |
| TestFlight | `apple/**`, `schema/**`, `spec/**`, `scripts/release/**`, `scripts/i18n/**`, `scripts/operations/**`, `scripts/persona_config.py`, `scripts/persona_identity.py`, `scripts/privacy_check.py`, `scripts/app/sim-boot.sh` |

A missing baseline runs that target once. Later checks compare the last successful commit directly with the newest branch HEAD,
including deleted paths. Failure does not advance that baseline; two failures at the same HEAD suspend that target until another
HEAD appears. Lock contention exits 75 and is a deferral, not a failure. Successful actions only log; failures invoke the configured
notification command, retaining failed notifications in a durable outbox for subsequent delivery attempts.

Use a dedicated detached clean worktree on the hub and another on the build computer. The worker refuses to discard dirty files.
`scripts/operations/hub_action.py deploy` retains the deployment lock through a live API and serving-binary verification;
`testflight_action.py` retains the release lock through upload, `asc_testflight.py wait 45 <build>`, and Internal-group verification.
Its checkpoint resumes processing a previously uploaded build instead of uploading it again. Set `NO_PUBLISH=1` for TestFlight-only
work. Do not disable the deploy script's freshness check.

Manual `deploy.sh` now takes the hub's existing `releases/hub/.publish.lock`, shared with release promotion; callers on another
computer retain it through an SSH connection. Manual `release.sh` takes the build computer's
`~/.local/state/persona/locks/release.lock`. Older checkouts that predate these locks must be updated before manual use.
The release script runs under `PERSONA_XCODE_RUNNER` (default `~/.local/bin/xcode-slot`) when installed, covering every xcodebuild
subprocess, not just archive. The unattended release uses no simulator.

Failure notifications use hub-local `POST /api/v1/notify`, reading the runtime token inside the process. No credentials enter
arguments or logs. `sent:true` is hub acceptance, not a phone-display receipt; verify a controlled failure on the phone during installation.

```sh
systemctl --user daemon-reload
systemctl --user enable --now persona-autorelease.timer
systemctl --user list-timers persona-autorelease.timer
systemctl --user start persona-autorelease.service             # one immediate run
systemctl --user disable --now persona-autorelease.timer       # pause future runs
systemctl --user stop persona-autorelease.service              # also stop an active run
systemctl --user enable --now persona-autorelease.timer        # resume
journalctl --user -u persona-autorelease.service -n 100 --no-pager
python3 ~/.local/lib/persona-autorelease/autorelease.py --config ~/.local/state/persona-autorelease/config.json --inject-failure
```

Keep a private STATE document alongside the configuration, recording the actual paths, notification receipt, last successful revisions,
and TestFlight build evidence. Runtime/build configuration and signing keys remain outside the repository.

## Releasing with asc

`scripts/release/asc_cli.py` runs the [App Store Connect CLI](https://github.com/rorkai/App-Store-Connect-CLI) (`asc`) with your own App Store Connect API key; it can do uploads, TestFlight, review submission, metadata and signing.
The key is the same config as the release scripts above, so configure it once and everything works. The script pins asc's version and sha256, downloads it to `~/.cache/persona/asc/<version>/` on first run, and refuses to install if the checksum fails. Telemetry is always off.

1. **Set up the key**: in App Store Connect › Users and Access › Integrations › App Store Connect API, create a team key (the App Manager role is enough to upload builds and submit for review), download `AuthKey_<Key ID>.p8`,
   and put it outside the repo (e.g. `~/.appstoreconnect/`, `chmod 600`). Put the Key ID, Issuer ID, file path and the app record's numeric ID into `~/.config/persona/config.json` as
   `app_store.key_id` / `issuer_id` / `key_path` / `app_id`. Environment variables `ASC_KEY_ID`, `ASC_ISSUER_ID`, `ASC_PRIVATE_KEY_PATH` (or `ASC_KEY_PATH`), `ASC_APP_ID` also work and take precedence.
   The repo's `.gitignore` ignores `*.p8` and `.asc/`.
2. **Check**: `python3 scripts/release/asc_cli.py check`. It only reads config locally and sends no requests: lists asc's version and location, the last four characters of the Key ID, the key file and its permissions, and the App ID; the last line is `ok`.
   Anything missing is reported with the environment variable name and config key. To confirm the key is valid on Apple's side, run `python3 scripts/release/asc_cli.py auth status --validate` (sends a request).
3. **Use**:
   ```sh
   UPLOADER=asc ./scripts/release/release.sh                         # archive, export, check the ipa, upload to TestFlight with asc
   python3 scripts/release/asc_testflight.py wait 45 <build number>           # then as before: into the Internal group, write What to Test, attach the external group
   python3 scripts/release/asc_cli.py review submit --version <version> --build-number <build number> --dry-run   # before submitting, see which steps it would take; changes nothing
   python3 scripts/release/asc_cli.py review submit --version <version> --build-number <build number> --confirm   # attach the uploaded build to the App Store version and submit for review
   python3 scripts/release/asc_cli.py <any asc command>                 # e.g. builds list --output table, review status, validate --version <version>
   ```
   `UPLOADER=asc` goes through the `NO_UPLOAD=1` export and ipa checks (`aps-environment` is `production`, entitlements), then uploads with `asc builds upload`; when unset, `xcodebuild` still uploads directly at export.
   asc's own help: `python3 scripts/release/asc_cli.py --help`, `… <command> --help` (no key needed).

**Customizing**: to change asc itself, fork upstream, run `go build -o ~/bin/asc .` in the fork (needs the Go version in `go.mod`), then `ASC_BIN=~/bin/asc python3 scripts/release/asc_cli.py …`; key and telemetry handling stay the same.
Persona's own release logic (test groups, What to Test, external testing) stays in scripts like `asc_testflight.py`, or is composed from asc commands in `asc_cli.py`.
**Upgrading asc**: change `VERSION` and `SHA256` in `asc_cli.py` (from `asc_<version>_checksums.txt` in the release), and run `python3 -B -m unittest discover -s scripts/release -p 'test_asc_cli.py'`.

asc only uses the key in Persona's config: `asc_cli.py` strips `ASC_PROFILE`, `ASC_CONFIG_PATH`, `ASC_BYPASS_KEYCHAIN`, `ASC_PRIVATE_KEY`, `ASC_PRIVATE_KEY_B64` at runtime,
so a profile saved to the keychain by `asc auth login` never gets mixed in. The App Store version (version number, screenshots, description) must be prepared first on the website or with `asc versions` / `asc metadata` before `review submit` can submit.

## Why sign this way

| Stage | Signing | Profile |
| --- | --- | --- |
| Archive `xcodebuild archive` | Automatic (`CODE_SIGN_STYLE=Automatic`) | The development `iOS Team Provisioning Profile` |
| Export `xcodebuild -exportArchive` | Manual (`signingStyle=manual`, `signingCertificate=Apple Distribution`) | "Persona App Store" / "Persona Watch App Store" |

An App Store Connect API key can only do what App Manager can, and cloud-managed signing needs Admin; manual signing gets around that.
Changing Bundle ID capabilities (say, adding another capability) invalidates the old profiles; just rerun `scripts/release/asc_signing.py`.

## entitlements (2026-11-02)

The App and its extensions hand things over through shared groups: the push relay's private key goes through a keychain group (`<team>.<bundle id>.push`, App → PersonaNotify), and the Dynamic Island avatar and avatar pack images go through the App Group (`group.<bundle id>`, App → PersonaWidgets, next section).
Every bundle's signature must have its target's `keychain-access-groups` and `com.apple.security.application-groups`, and the group the Info.plist tells the code to use (`PersonaPushKeychainGroup`, `PersonaAppGroup`) must be one of them; if anything is missing, it silently falls back to defaults.
The App Group ID itself has no team prefix, so the Info.plist writes `$(PERSONA_APP_GROUP)` directly, and it has a value even in an unsigned archive.

- **Info.plist group names use `$(DEVELOPMENT_TEAM).`, not `$(AppIdentifierPrefix)`**: the latter only has a value during Xcode signing and is empty in an unsigned archive. Entitlements files still use `$(AppIdentifierPrefix)`.
- **Unsigned archive (`CODE_SIGNING_ALLOWED=NO`, when this Mac has no distribution certificate and export uses cloud signing)**: bundles in the archive have no entitlements, and export signs according to what's in the bundle.
  Before export, `python3 scripts/release/entitlements.py sign <archive>` ad hoc signs inside-out, giving each bundle its target's entitlements (found via `xcodebuild -showBuildSettings`,
  with build variables expanded and `$(AppIdentifierPrefix)` set to the team's; if something can't be expanded it errors instead of leaving it empty). If you only run `codesign --entitlements <original file>` on the App and leave out the extensions,
  export expands the App's `$(PERSONA_BUNDLE_ID)` to empty and the extensions get no groups at all (that's what happened in build 92: the Dynamic Island avatar didn't follow the setting, `docs/apple/live-activity.md`).
- **Check after export**: `python3 scripts/release/entitlements.py check <ipa>` lists every problem and exits non-zero; it reports 9 issues on build 92's ipa.
  App Groups are checked the same way: every bundle's signature has its target's group, and the Info.plist's `PersonaAppGroup` is one of them.
  In the exported ipa / pkg, the push entitlement (`aps-environment`; on Mac `com.apple.developer.aps-environment`) must be `production`. A Mac .pkg can be checked too.
- **Time-sensitive notifications** (`com.apple.developer.usernotifications.time-sensitive`, #1572): the hub's "Needs you" pushes and plugins' urgent alerts carry `interruption-level: time-sensitive`.
  If the App's signature lacks this entitlement, the system shows them all as `active`: they don't break through Focus, so with Focus on the phone doesn't ring and the watch doesn't get them either, and nothing reports an error. The App ID must first enable `USERNOTIFICATIONS_TIMESENSITIVE`
  (`asc_signing.py` enables it; afterwards the old profiles are invalid and need one rerun to rebuild); `entitlements.py check` errors if the exported App lacks it.
  **After merging #1572 and before the next release, run `python3 scripts/release/asc_signing.py` once** (if you sign development builds, add `--dev` to rebuild the Development profiles).
  `release.sh` runs `asc_signing.py --check` at the start, so a missing capability or invalid profile stops it before archiving rather than failing at export.
  The Mac App also requests this entitlement (#1655, PersonaMac in `project.yml`): Mac and iPhone share one App ID (UNIVERSAL), so the capability enabled above covers the Mac too;
  `entitlements.py check` requires it for the Mac .pkg / archive as well (checked whenever the target requests it, `scripts/release/test_entitlements.py`).
  No .pkg has been exported and checked yet in an environment that can ship the Mac version, and no banner has been seen on a Mac with Focus on; close #1655 after that's done.
  The Apple Vision Pro version (PersonaVision) also requests it (#2435); its bundle id is the same as the iPhone's, so it's covered by the capability above too. No banner has been seen on Vision Pro with Focus on yet.

## App Group (2026-11-02)

The App and the Dynamic Island extension (PersonaWidgets) hand over the avatar and avatar pack images through files in the App Group container (`docs/apple/live-activity.md` ("Avatar"); the Live Activity extension can't read the keychain on a phone).
The group name defaults to `group.<bundle id>` and can be changed with the config's `app.app_group` (`docs/operations/config.md`); it's written into both targets' entitlements (`com.apple.security.application-groups`) and the Info.plist's `PersonaAppGroup`.
It was registered on the developer website on 2026-11-02 and attached to the App IDs of the App and the Dynamic Island extension; since then, the TestFlight flow (unsigned archive → `entitlements.py sign` → export with API key cloud signing) produces an ipa where both bundles carry the group.

**API keys can't register App Groups** (checked 2026-11-02):
- The App Store Connect API has no App Group endpoints (`GET /v1/appGroups` is 404). `bundleIdCapabilities` can only turn on the `APP_GROUPS` capability, not attach a specific group (settings only accepts three keys, see "Pitfalls" below).
  fastlane's `produce group` / `associate_group` use Apple ID sign-in (website cookies), not an API key.
- At export, API key + `-allowProvisioningUpdates` cloud signing regenerates profiles from the App ID's existing capabilities, but can't create or attach a group; if it isn't attached, export fails.
- So when switching to another account or bundle id, **finish on the website first, then build with App Group entitlements**.

Steps on the developer website (developer.apple.com › Account › Certificates, IDs & Profiles, as Account Holder or Admin):
1. Identifiers › "+" › choose **App Groups** › Continue › Description `Persona`, Identifier `group.<bundle id>` (matching the config's `app.app_group`) › Continue › Register.
2. Identifiers › switch the top-right filter back to App IDs › click `<bundle id>` › check **App Groups** under Capabilities › click Configure next to it (Edit if already checked) ›
   check `group.<bundle id>` › Continue › Save › Confirm in the dialog (changing capabilities invalidates this App ID's existing profiles).
3. Do step 2 for the Dynamic Island extension's App ID `<bundle id>.liveactivity` too. The notification extension does not need it.
   For #1147, register the **separate watch group** (`app.watch_app_group`, default `<phone group>.watch`) and attach it to the existing watch app and the new configured watch widget App ID. These account changes and regenerated watch profiles require explicit approval; they have not been performed as part of the code change.
4. Profiles: cloud-signed export (automatic signing) regenerates them itself; `release.sh`'s manual signing needs a rerun of `python3 scripts/release/asc_signing.py`,
   which checks every profile for the group, and if missing prints the steps above and exits non-zero.
5. After a release export, `scripts/release/entitlements.py check` verifies the App Groups of the app and its extensions, including both watch bundles.

The #1147 system-surface store is independent of Fleet's avatar files, because it contains per-device credentials. Validate first unlock, offline expiry, pairing/rotation/removal, WidgetCenter full reports (including removal of the last configuration), paired-watch direct requests, and real background `extension_widget` pushes on devices before claiming acceptance. Simulator-only unsigned builds cannot validate App Group signing or background APNs delivery. No build, screenshots or device acceptance were run for #1147 while builds were paused.

## Changing the store name

The store name (shown in the App Store / TestFlight, unrelated to the home screen's `CFBundleDisplayName`) must be globally unique and lives in the App Info localizations. While the version is still `PREPARE_FOR_SUBMISSION`, an App Manager API key can change it:

```sh
python3 scripts/release/asc.py GET /v1/apps/<app id>/appInfos                    # get the appInfo id
python3 scripts/release/asc.py GET /v1/appInfos/<appInfo id>/appInfoLocalizations # one per language; get the id
# PATCH /v1/appInfoLocalizations/<id>, with name (and privacyPolicyUrl) in attributes; an occupied name returns 409
```

Renaming doesn't replace the app record: Bundle IDs, test groups, public links, released builds and existing installs are unaffected.

## Pitfalls

- **`bundleIdCapabilities` cannot take invented settings**: passing
  `settings: [{"key": "PUSH_NOTIFICATIONS", ...}]` for `PUSH_NOTIFICATIONS` is rejected with 409 (`settings/0/key` only accepts `ICLOUD_VERSION`,
  `DATA_PROTECTION_PERMISSION_LEVEL`, `APPLE_ID_AUTH_APP_CONSENT`). For push and HealthKit, pass an empty `settings: []`.
- **A profile with the same name blocks rebuilding**: after a capability change, the old profile becomes `INVALID`, and creating a new one with the same name returns 409. `asc_signing.py` deletes the same-named profile first, then creates it.
- **The test-group list is sometimes incomplete** (#945): `GET /v1/apps/{id}/betaGroups` sometimes returns an empty list, omits groups, or times out. The script used to treat that as a missing group and create one;
  on 2026-11-03 it created 4 extra Friends groups in one day, attached builds to an empty group, while all testers were in the earliest group. Now groups are matched by name and internal/external type, and the earliest-created group wins among duplicates.
  Only after three queries find nothing is a group considered absent. Only `setup` and `external` create groups; `wait` / `info` / `catch-up` error if none is found (during a release, just one line with a retry command).
  The script doesn't delete the extra empty groups; a person decides whether to delete them.
- **Create internal testers with `POST /v1/betaTesters`, including the group relationship**: if the same email already has a tester record under another App, directly linking it returns 409;
  on 409, the script prompts you to add that person to this App's `Internal` group on the website.
- **`aps-environment` in the archive is always `development`**: archiving uses a development profile; final signing happens at export. Don't check the archived product for this.
- **Push and production**: `UIBackgroundModes` uses `remote-notification` (the watch App has no background modes); `ITSAppUsesNonExemptEncryption=false`
  is already in `apple/project.yml`, avoiding the export-compliance questions on every upload.
- **No iCloud directories**: this Mac isn't unlocked; skip anything involving `~/Library/Mobile Documents`.

## Pending for the leader

(None. 2026-10-24: the clipJSON Chinese-truncation problem is fixed, and transcript.go has been gofmt'd.)

## Push (APNs)

- Create an APNs key at developer.apple.com (Sandbox & Production, Team Scoped); neither the Key ID nor the .p8 file goes into the repo.
- File: `~/.persona/apns_key.p8` on the hub machine (config `~/.persona/apns.json`), with another copy in a password manager. A key can only be downloaded once; if lost, create a new one.
- The page says the Apple Developer Program License Agreement has been updated; the Account Holder must accept it at developer.apple.com (doesn't affect app creation or TestFlight uploads).

## External testing (public link)

Internal testers must be team members; friends use external testing in the "Friends" group (`app_store.external.group`). On 2026-10-30 the user chose a **public link**
(`public_link: true`): share `https://testflight.apple.com/join/…` in a group, and anyone who clicks can install. Since 2026-11-03 there is **no tester cap** (`public_link_limit: 0`, previously 50, after which the link stopped accepting people): 0 or an omitted `public_link_limit` disables the cap, subject only to Apple's limit of 10,000 external testers per App; a positive number sets the cap.
Current public link: <https://testflight.apple.com/join/vsjfy3hE> (`asc_testflight.py external` prints it every time; update this if it changes; checked 2026-11-03, it belongs to the earliest Friends group).
iPhone and Mac use the same link (the same Friends group); opening it in TestFlight on a Mac gives the Mac version.
Since 2026-11-03 this link may be written in plain text in the repo (requested; README's installation instructions include it). Ask separately before putting it on the website (`site/`); it isn't there now. If the link changes (for example, the group is recreated), update both README and this doc.
No email collection needed; `app_store.external.testers` still works, and listed emails also get invitations. With `public_link: false`, only those emails can join.

```sh
python3 scripts/release/asc_testflight.py external   # create the group, enable/disable the public link per config, write test info, attach the latest build, submit for review; print the public link
```

- **Public link = anyone with the link can install**: builds must contain nothing personal (hub address, hostname, email, paths…). Before uploading, `release.sh` runs
  `scripts/release/check_bundle_privacy.py <Persona.app>` (using the same sensitive-word list as `privacy_check.py`, checking binary strings, plists and lzfse metadata;
  bundle ids, team ids and signing data are inherent to every build and don't count). Custom release scripts must run it before uploading too.
- **No separate command needed for each release**: with `app_store.external` configured, after attaching Internal, `wait` and `info` also attach this build to Friends
  and submit for review (inviting listed testers if any). A failure here only prints one line (with the retry command `external`) and doesn't fail the release.
- **A new build can't be submitted while another build of the same version is in review** (422 `ANOTHER_BUILD_IN_REVIEW`, "submit after review finishes"), and nothing used to submit it later:
  while Mac 110 was in review, 111–127 were all attached to Friends but stuck in `READY_FOR_BETA_SUBMISSION`; external testers never got them (2026-11-03, #792).
  `asc_testflight.py [--platform mac] catch-up`: if the latest VALID build hasn't been submitted, attach it to Friends and submit (without changing its What to Test);
  if another is in review, say to wait for the next run; if already submitted / approved / rejected, write nothing. Each local auto-tf round (every 10 minutes while waiting for a new commit) runs this once for iPhone and once for Mac.
- External builds must pass **Beta App Review** (the first takes about a day; later builds of the same version usually pass automatically). A contact is required before submission:
  `first_name` / `last_name` / `phone` / `email` in `app_store.external.review`. If missing, the script only attaches the build, without submitting or adding testers.
- The public link opens before any build passes review (the API gives the address directly), but the page says "This beta isn't accepting any new testers right now";
  joining is possible only after review passes.
- Review state is `buildBetaDetail.externalBuildState`: `READY_FOR_BETA_SUBMISSION` (attached, not submitted) → `WAITING_FOR_BETA_REVIEW` →
  `IN_BETA_REVIEW` → `BETA_APPROVED` / `BETA_REJECTED`.
- Friends deploy their own hubs: without an APNs key from a developer account, newer Apps receive notifications and Live Activities through the publisher's push relay
  (requires `app.push_relay` in the release config and successful device registration; see `docs/operations/push-relay.md`); older versions without a relay only receive live updates while the App is open.
  For Gmail login they must create their own Google OAuth client and put it in their manager's `manager.json` on the hub machine (`docs/extensions/mail.md`).
- Test info needs a privacy policy URL (`app_store.external.privacy_url`); Chinese and English descriptions live in `BETA_INFO` in `asc_testflight.py`.
- Without a hub, the App only has a pairing page: review notes (`review.notes`) must explain that this App works with a self-hosted hub, or reviewers may ask how to use it.

## Mac version (2026-11-02)

The Mac App (target `PersonaMac`, old `PersonaMac` scheme compatible) shares the iPhone's bundle id, so it is **the same app record** in App Store Connect (macOS platform added, version 1.0),
and uses the same TestFlight: the same Internal / Friends groups and public link. Testers install TestFlight on a Mac to see Mac builds.

- **Build numbers**: a number can only be used once per platform; different platforms can reuse it. Rule: **the Mac build and iPhone build in one release use the same number**
  (same number = same commit, easy to match when investigating); iPhone builds and standalone Mac builds use **the largest number across both platforms plus one**
  (`python3 scripts/release/asc_testflight.py next-build`), so one number never identifies two different commits on the two platforms.
- **`asc_testflight.py --platform mac`**: `status` / `wait` / `info` / `external` / `catch-up` only consider Mac builds; without it, only iPhone builds.
  Previously platforms weren't distinguished; after Mac builds appeared, "the latest VALID build" could be on the other platform, and What to Test would be written to the wrong build.
- **Icon**: the system doesn't round Mac icon corners; the icon itself has a rounded square and shadow (the macOS grid: a centered 824 square within 1024). Besides the single-size iOS image in `AppIcon`,
  there are `idiom: mac` sizes 16–512 (1x / 2x), generated from the iPhone image by `python3 scripts/assets/make_icon.py mac` (also written when regenerating everything).
  Without these the Mac package has no icon and the Mac App Store rejects it. Xcode 26's `AppIcon.icns` only has sizes 16 and 128; the full set is in `Assets.car`
  (the system reads it using `CFBundleIconName`), so the check looks for 512 and 512@2x in `Assets.car` (`xcrun assetutil --info`).
- **Info.plist**: `LSApplicationCategoryType` (productivity) and all permission descriptions are defined in the `PersonaMac` target in `apple/project.yml`.
- **Sandbox**: `com.apple.security.app-sandbox`, plus networking (client and local server for port forwarding), user-selected files, contacts / calendar / location / photos,
  microphone / camera (entitlements in `project.yml`).
- **Push** (since 2026-11-02, `docs/apple/mac.md` ("Remote push (Mac)")): `com.apple.developer.aps-environment` (the macOS name; iOS uses `aps-environment`).
  The App ID is UNIVERSAL and already has Push Notifications enabled, so no developer-account change is needed; the archive has `development`, and cloud signing at App Store export changes it to `production`.
  The package also includes notification service extension `PersonaNotify.appex` (target `PersonaMacNotify`, module / notification main class still `PersonaNotify`, in `Contents/PlugIns/`, sandbox + `.push` keychain group, #432).
  Its bundle id is the same as the iPhone extension's (UNIVERSAL App ID); cloud signing creates its Mac profile at export. `entitlements.py sign / check` signs and checks it too.
- **Pre-release checks** match the iPhone's: permission descriptions (`check_bundle_strings.py`), privacy scan (`check_bundle_privacy.py`, recognizes Mac's
  `Contents/` layout, skips `embedded.provisionprofile`, scans the entire universal binary), no DEBUG switches in Release; also checks the category and 512 / 512@2x icons.
- Export produces a `.pkg` (ExportOptions `method` is also `app-store-connect`), cloud-signed like the iPhone: the App uses Apple Distribution,
  the `.pkg` uses 3rd Party Mac Developer Installer (`pkgutil --check-signature` says "issued by Apple (Development)"; all certificates of this type do).
  Upload with `altool -t macos`. The local release script runs `entitlements.py sign` before export, as for the iPhone, then `entitlements.py check <.pkg>` afterwards:
  every bundle has its target's entitlements, and push is `production` (`check` recognizes Mac's `Contents/` layout and `.pkg`; it checks the push environment in iPhone ipas too).
- **Package files must be readable by other users** (Mac 110, 2026-11-02): files in the `.pkg` were owner-readable only, causing ITMS-90255 at upload,
  "files that are only readable by the root user" (macOS verifies signatures as the user running the App). auto-tf's umask is 077;
  the script's `cp -R` made the copied archive entirely 0700 / 0600, which never showed up when testing in an interactive shell (umask 022). The release script now starts with `umask 022` and copies archives with `ditto` (preserves permissions).
  It checks the `.app` before export and the App unpacked from the `.pkg` afterwards: every file a+r, every directory a+rx, otherwise no upload. The iPhone script was changed too (ipas aren't checked for this; 110 passed there).
- **Automatic release**: after releasing the iPhone build, if this commit includes changes used by the Mac App (`apple/Mac`, `apple/Shared`, `apple/Persona` except a few iOS-only
  files, `apple/Packages`, `project.yml`, `Package.resolved`), release Mac next with the same number. Report the Mac result separately; its failure doesn't affect the iPhone release.

## Publishing personad (`scripts/release/publish.sh`, 2026-10-31)

Let others install the server without getting the source: prebuilt `personad` and the installer are hosted on the `site/` Worker (configured `publish.site_url`).

```sh
curl -fsSL <publish.site_url>/install.sh | sh   # install to ~/.persona/bin/personad; verify against SHA256SUMS, refuse if wrong
```

- On the computer that runs the hub (`--hub`, a `hub.json`, or the two there already) it installs `persona-hub` and `persona-manager` with `personad`, the same release, each checked before any is put in place. A release without one of them leaves the one there as it was, unless a systemd unit or launchd agent runs it: then it is refused before anything changes ([../server/server.md](../server/server.md) ("Programs"), [hub-update.md](hub-update.md) ("A release without `persona-hub` or `persona-manager`")).
- After installing `personad`, if herdr can't be found on this computer (PATH and directories node searches, such as `~/.local/bin`), install it with herdr's official installer (`https://herdr.dev/install.sh`)
  too, so sessions appear in Agents after the first install. Failure only prints one warning and doesn't affect `personad`; computers using Orca / Herdr App can skip it with `PERSONA_SKIP_HERDR=1`.
  `deploy.sh` does the same when installing `personad` on each computer (`scripts/deploy/ensure-herdr.sh`; keep the two sets of steps aligned).

- `release.sh` runs this after uploading TestFlight. The version is `<commit>-<build number>`, matching that TestFlight release; `NO_PUBLISH=1` only releases TestFlight,
  and no publish happens without `publish.site_url` in config. Standalone: `./scripts/release/publish.sh`; `NO_DEPLOY=1` only builds and checks (outputs in `build-publish/`).
- Website-only changes (`site/`): `SITE_ONLY=1 ./scripts/release/publish.sh` skips a new build, fetches the small files of the currently published version (`VERSION`, `SHA256SUMS`, `latest.json`, `SLOT`, `install.sh`) and deploys them with the committed `site/`, leaving archives in R2 unchanged. Afterwards it checks the live small files are unchanged and every archive still exists (`HEAD` size). It refuses if there's no live `dl/SLOT` yet (a release from before R2); do one full publish first. **Don't run `wrangler deploy` directly in `site/`**: it makes `/dl/` and `install.sh` return 404 (`docs/lessons/server.md`).
- Web dependencies always install from the official npm registry (`npm ci --registry=https://registry.npmjs.org/`, overridable with `PERSONA_NPM_REGISTRY`), ignoring local npm config. `package-lock.json` already points to the official registry; a locally configured mirror may lack packages (on 2026-11-06 it lacked `elkjs`, stopping release before web bundling).
- Build four platforms (linux / darwin × amd64 / arm64), with `-tags public -trimpath -s -w`, embedding `channel=release`, build number (UTC `YYYYMMDDHHMM`) and `updateURL`:
  - **Web bundled into the binary** (`server/internal/hub/webdist`, `go:embed`): the hub uses it when the hub machine has no `~/.persona/web`; otherwise it still uses the directory.
  - **No skills included**: the skill connecting to the Lark gateway was copied from a private upstream repository (GPL-3.0) and cannot be redistributed. Skills live in `~/.persona/skills/` on each machine,
    so there is only one binary, used by our own hub too (`docs/operations/hub-update.md`). The script ensures the binary only contains skills registered as redistributable (`scripts/release/check_bundle_skills.py`).
    `-tags public` changes no code (`TestOneBuild`); it is only a privacy-scan marker.
  - **Version manifest** `dl/latest.json` (each platform's archive and unpacked sha256, build number, commit), unsigned (the user's decision; trade-offs in `docs/operations/hub-update.md` ("Security"));
    before publishing, check that the build number is greater than the live one, the live version's commit is an ancestor of this one, and server/ and web/ have no uncommitted changes. The manifest includes all commits in this release (`ancestors`):
    hubs read it for auto-update, but only install automatically if it includes the commit they're running (`docs/operations/hub-update.md` ("Preventing rollback")).
  - **Programs published together**: `personad-wechat` (WeChat bot, a separate program, `docs/extensions/wechat.md`), built for all four platforms with the same
    `-X` flags, privacy scan and size check as personad; its `version -v` must match personad's. The manifest's `programs` lists them, and hub auto-update replaces them together.
  - Before upload, run `check_bundle_privacy.py --whole-short`: binaries must contain none of the configured values (except Go module paths; values of four characters or fewer match whole words,
    since Unicode tables and compressed JS contain short letter combinations everywhere). A few protocol names, official contact links and standard network constants in public dependencies are classified by exact context, without allowing whole domains or private network ranges;
    it requires the binary's `public` marker, unreplaced dependencies and checksums matching `server/go.sum`, and `go mod verify` before building. Registered personal values always take precedence and are blocked; see `docs/lessons/testing.md`.
- Build the connector `persona-link-<os>-<arch>` for the same four platforms too (2026-11-02, node service, `install.sh --node`, see `docs/operations/link.md` ("Installing the node service")), with the same privacy scan and compression, included in `SHA256SUMS`.
- Build the device's own program `persona-node-<os>-<arch>` (`server/cmd/persona-node`) for the same four platforms, scanned, compressed and included in `SHA256SUMS` the same way (it must report the same `version -v` as personad). `install.sh --node` installs it at `~/.persona/bin/persona-node` and the node service runs it ([link.md](link.md) "The device's program"). `latest.json` lists it under `nodes` (old hubs ignore that): hub self-update still brings `personad-*` alone, and the manager fetches persona-node from `dl/<version>/` for a device that runs it (`docs/operations/node-update.md` ("Node packages")).
- Downloads are `personad-<os>-<arch>.gz`: scan the raw binary first, then compress with `gzip -n -9`; `SHA256SUMS` verifies the archive. The installer verifies it before unpacking and installing; failed verification / decompression never overwrites the old version.
  **Archives go in R2 bucket `persona-dl`, not the site** (#2541): Workers static assets have a 25 MiB per-file limit ([official limit](https://developers.cloudflare.com/workers/platform/limits/#static-assets));
  after #1002 embedded tiered avatar images, compressed personad exceeded it. The Worker binds the bucket as `DL` (`site/wrangler.jsonc`); `worker.mjs` serves `/dl/*.gz` from it (`GET` / `HEAD` / `Range` / `If-None-Match`),
  while other `/dl/` files remain static. The bucket has slots `a` and `b`: publishing uploads archives to the inactive slot (`<slot>/<filename>`), then deploys the site, switching `dl/SLOT` and `latest.json` together.
  The other slot keeps the previous version until the next publish, so downloads in progress at deployment can finish.
  Every release is also uploaded to `release/<version>/` (its node builds, `personad-<os>-<arch>.gz` and `persona-node-<os>-<arch>.gz`, and `latest.json` as `manifest.json`), and to `release/X.Y.Z/` when the commit carries the tag `vX.Y.Z` at publish time (tag before publishing; looked up with `git tag --points-at`, as the commit has its `testflight/<build>` tag too; never for a `-dirty` build); these are kept (about 160 MB a release, twice for a tagged one: remove old ones from the bucket by hand once no hub of theirs is left), and `worker.mjs` serves them as `/dl/<version>/<file>`. A hub's manager fetches the node build of its hub's version there (`docs/operations/node-update.md` ("Node packages")); publishing checks the served copy too. The site only has small files now; `check_asset_sizes.py` still checks the whole site directory. Local `build-publish/bin` keeps uncompressed binaries for debugging but doesn't upload them.
  Regression: `python3 -B -m unittest discover -s scripts/deploy -p 'test_compressed_publish.py'`.
- **Before releasing a hub ahead of the App, check compatibility with the released App** (#1693): if this hub makes clients draw something new (a new connector-login `kind`, interaction type,
  card / event fields) but the latest TestFlight doesn't yet include the client changes, installed Apps show it blank or error (the WeChat plugin's `kind: qr` on an App without #1401
  is an empty form with a permanently disabled "Next"). Before publishing the hub, compare against the latest TestFlight commit (`git merge-base --is-ancestor <client change> <TestFlight commit>`):
  if missing, release TestFlight first, make the new feature fall back to something old Apps understand, or raise `proto.MinAppBuild` (`server/internal/proto/proto.go`, hello and
  `min_app_build` in `latest.json`) to the build containing it so old Apps prompt for an update. Raising `min_app_build` makes every older App unable to use this hub; ask the user first.
- Only the latest version is hosted (R2 also retains the previous one, as above). Bucket uploads and deployment use configured `publish.wrangler` (otherwise `npx wrangler`). Cloudflare OAuth expires after one hour;
  on an auth error, run `cf auth login` and retry. After deployment it reads back `dl/VERSION`, `dl/SHA256SUMS`, `dl/latest.json`, `dl/SLOT` to confirm the just-built version is live, and checks every archive's size with `HEAD`.
  The push relay is in the same Worker (`/relay/*`) and is redeployed on each publish too.
- Push relay deployed (2026-11-02): resident `personad relay` + Cloudflare Tunnel + website Worker forwarding; public health, origin protection, registration and authentication verified.
  Release config needs `app.push_relay`; only a new TestFlight with the notification extension registers. Notification decryption on real devices still needs separate acceptance; see `docs/operations/push-relay.md`.
- Not yet available (others can install, but it's still awkward; see `docs/operations/config.md`):
  Developer ID notarization of the macOS binary (browser downloads get blocked by Gatekeeper; `curl` doesn't).

## Publishing the hub image (`.github/workflows/hub-image.yml`, 2026-11-01)

Docker hubs (`docs/operations/docker.md`) use the ghcr image, linux/amd64 + linux/arm64:

```sh
git tag v1.2.0 && git push origin v1.2.0   # -> ghcr.io/<owner>/<repo>:1.2.0 and :latest
```

- Smoke-test first (amd64 + compose), then build and push both architectures only after it passes. Prerelease tags containing `-` (`v1.3.0-rc1`) only push the version tag, without touching `latest`.

- To get an image of the current branch without a tag: manually run hub image in Actions (`edge`, `sha-<commit>`).
- Image smoke tests run through an explicit workflow dispatch; only the documented `v*` release-tag event starts image publication automatically (`packages: write` is granted only to the publish job). This exception supplies the version and `latest` images used by Docker installations; no branch push or PR starts it.
- The image's personad uses the tag minus `v` as its version (`personad version`); when node differs, the App prompts to redeploy, using the image's own builds or the release's (`docs/operations/docker.md` ("Node builds")).
- The image's personad is on the `docker` channel: no skills (like all binaries), no self-update; a new image prompts you to pull it (`docs/operations/hub-update.md`).
- When the repository is private, the package is private too; NAS installs need `docker login ghcr.io` (`docs/operations/docker.md` ("Image")).

## Android publisher repository migration and status retries

`scripts/release/android_release_worker.py` uses a private configuration file. Its `repository`, `git_url`, `branch` and
dedicated `checkout` must all identify the intended source repository. Checkpoints and build receipts carry the repository identity;
an old checkpoint without that identity is rejected rather than silently attributed to a different repository.

When moving to rewritten history, pause the existing publisher, retain its evidence and use a separate `state_directory` for the new
repository. Keep the existing package, signing certificate and signing entry. Never post an old commit's status to the new repository
or treat an old verified APK as a build of the new source. Compare migrated file content when commit IDs have changed.

Versions are `version_base + first-parent commit count`; `version_base` defaults to 100000. Before enabling the new lane, choose a
fixed nonnegative base so its first version exceeds the currently published APK. Keep that base stable thereafter. The worker rejects
history rewrites and version regressions within the lane; the atomic Hub publisher also rejects stale versions across lanes.

After publishing and completing the configured full online verifier, the worker durably records `verified.json`, including the source,
repository, package, version, signed APK hash and certificate. Only then does it post the GitHub success status and advance `published.json`.
If that status write fails, the next cycle for the same source revalidates the local artifact and retries only the status/checkpoint;
it does not build, sign, publish or download again. The receipt proves the completed verification, not current availability after an
unrelated deployment. A newer source follows the normal full pipeline. Preserve failure logs and report publication, verification,
status reporting and handset installation separately. Legacy releases without a verified receipt are not automatically imported.

