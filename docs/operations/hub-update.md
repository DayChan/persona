# hub versions, update checks and automatic updates

User, 2026-11-02: "Can the phone manage the hub's version? Does the hub check for updates, can it update itself? …When the App opens, offer a one-tap hub update, or have the hub update itself."
Issue #284 introduced automatic installation of manually published public releases. Private Hub installations can also publish validated canonical-branch builds automatically, as described below. Both paths use the same updater and rollback mechanism.
Implemented in 4 steps; after each step, the corresponding "Plan" is rewritten as the current state, keeping the trade-offs.

## Automatic private Hub releases

`scripts/release/hub_release.py --config <private-config.json>` can run from a
systemd user timer on a dedicated Linux build machine. It fetches the configured `source_branch` (default `main`) into an
exclusive checkout (never a developer checkout), runs server vet/tests and race checks, script suites, web
checks and the binary privacy scan, then publishes the tested commit. It reports
`hub-release/worker` commit statuses. These are independent CI results, not GitHub
Actions run results. This provides an execution path when hosted runner labels
have no available capacity or the operator cannot administer those runners.

The private config supplies `repository`, `checkout`, `state_dir`, `update_url`
and `publish_command` (an argv array), with optional `source_branch` (default
`main`). The dedicated checkout must use the selected repository as `origin`.
The worker verifies that origin identifies the configured status repository before
fetching or reporting statuses. State records bind the repository, source branch, update URL and commit: another source cannot
reuse a successful validation or an interrupted publication cache. The existing
build floor is retained when the source changes.
Use `--validate-only` to run the complete gates and retain a validated artifact
without publication. Its `hub-release/validation` status and `validated` record
are distinct from a confirmed `hub-release/worker` publication result. It never
invokes the publisher or changes the remote manifest. Normal retries can reuse
that artifact only for the same repository, branch, update URL, commit and package policy.
Use `--commit <full-sha>` to validate or reconcile one fixed candidate even when
main advances; both source checks must still prove it belongs to the configured
branch. Without this option, the worker follows the current branch head.

Templates are in `deploy/hub/`;
`hub-release.env` supplies `PERSONA_HUB_RELEASE_CHECKOUT`, the build-tool `PATH`,
and `PERSONA_CONFIG`. Use a private launcher when credentials need to be loaded
from a vault at runtime rather than stored in the environment file.
Enable the timer only after the bootstrap and publication checks below pass. The publisher is
`scripts/release/publish_hub_release.py --config <ssh-config.json>`; its config
supplies `remote_root` and `credentials_command`, which returns SSH credentials
as JSON (`host`, `port`, `username`, `password`, or a fields array keyed by id).
SSH host keys must already be trusted. Credentials are never written to release
artifacts. The publisher needs Python and Paramiko; the remote needs Python.
The optional `release_namespace` defaults to `hub` and selects a feed below
`<remote_root>/releases/`. Named feeds must start with `hub-` and use lowercase
letters, numbers and hyphens. Set the build's update URL to that same feed. A new
feed preserves the old manifest; it does not establish source ancestry or switch
an installation to the new feed. A history transition requires a separately
verified bootstrap. Standard publication and updater ancestry checks remain
unchanged within each feed. All feeds share one publication lock because the
installed Hub and managed node pointer are shared.
The build environment needs Node 24, Go matching `server/go.mod`, and a Python
3.11 environment with Pillow 12.0.0 and PyYAML 6.0.3 for the committed image and project checks. Keep these
in the release user's tool directories. For an isolated Docker Compose CLI plugin,
set `PERSONA_TEST_DOCKER_CONFIG` to a credential-free directory containing
`cli-plugins/docker-compose`; deployment tests only render configuration offline.
The worker passes its Python interpreter through to every script suite.
The SSH publisher uses Paramiko 4.0.0 in the same private environment.

Build with `update_url=file://<remote_root>/releases/hub` for a private source.
By default, SSH uploads only the Hub platform and atomically promotes its manifest. The
existing updater reads that local source with the same checksums, binary version,
ancestry, idle checks, edge handover, confirmation and rollback as HTTPS sources.
An authenticated update check also evaluates the existing automatic-update policy;
it never overrides a disabled setting or retries a marked bad build.

The worker holds a local lock for the whole cycle, and publication holds a remote
lock through confirmation. Build numbers must increase and new source must contain
the previous source. Files have immutable checksum names. Interrupted attempts
reuse the same build and artifact, with a 15-minute failure backoff; they cannot
turn a rolled-back build into a new version just by retrying. Recent local attempts
and remote artifacts have bounded retention. Failure leaves the updater in charge
of rollback; the publisher never restarts the host or the Hub service.

Bootstrap once with a verified binary containing the private update source, set
`auto_update_nodes: false` in `hub.json`, and enable Hub automatic updates using
`PUT /api/v1/hub/update/settings` with `{"auto":true}`. Use `hub switch` for the
bootstrap, preserving the previous binary. The worker then publishes and requests
an update check; confirmation requires the expected version/build, healthy Hub,
unchanged remote node versions and a matching installed binary checksum.

### Node upgrade policy

`hub.json`'s `auto_update_nodes` defaults to **false**. This applies to reconnect
upgrades, automatic fleet deployment, resuming queued transfers, downloading node
platforms and installing node packages after Hub confirmation. Explicit **Redeploy**
continues to work; an interrupted queued transfer requires another explicit
Redeploy while the policy is off. Already installed candidates still receive their
confirmation, so disabling upgrades cannot strand them.

With the policy off, a confirmed update to another version sets aside the node builds
the Hub leaves in `bin/nodes/`: each `personad-<os>-<arch>` with no `.version` file
beside it becomes `personad-<os>-<arch>.prev`. The manager takes an unlabelled
personad there for a build of the Hub's own version, so left in place it would put
the previous version back on every computer that Redeploy reaches (#1825). Set
aside, Redeploy fetches the Hub's version from the release (`docs/operations/node-update.md`,
"Node packages"). A build with a `.version` file says which one it is and stays; the
directory is never created.

The manager receives this policy in `devices.config`. An old external manager that
does not understand it must be updated separately before relying on that gate;
the built-in manager changes together with the Hub. Existing package containment
must remain intact during a Hub-only rollout. The publisher rejects unmanaged
`bin/nodes` contents and never writes Android artifacts.

Set `publish_nodes: true` in both private worker and SSH publisher configs to
continuously publish matching `personad` packages for `linux-amd64` and
`darwin-arm64`. This is package publication only: the publisher never requests
computer deployment and `auto_update_nodes` remains false. Users choose when
to update each computer in the client.

Both binaries are built from the same fixed clean commit with the same version,
release channel and build flags, after the full validation gates. The standard
release manifest binds compressed and executable SHA-256 hashes to that source
and build; the Linux descriptor must equal the Hub descriptor. Before promoting
the Hub manifest, publication verifies both archives, executable platforms, Go
main package, clean VCS commit and hashes, and runs the Linux self-check. Go
`-trimpath` omits linker flags from build info, so foreign runtime version values
are established by this controlled build manifest rather than a `.version` file.
An old Hub-only cache does not satisfy an enabled node publication requirement.

Verified executables and version sidecars are staged under the immutable
`releases/nodes/<version>/` directory. Once the matching Hub is healthy and
confirmed, `bin/nodes` atomically points there. Subsequent publications accept
only a managed pointer with matching metadata, sidecars and executable hashes;
they never restore quarantined packages. The pointer remains available for
explicit client updates. Existing computer versions must stay unchanged during
publication. `GET /api/v1/machines` can report package capability, but a previous
missing-package result may remain cached for ten minutes.

The same publisher also retains a consumer download mirror under the selected
`releases/<release_namespace>/node-packages/` (the private feed stays separate from
`releases/hub`). Each immutable `<version>/` has `manifest.json`,
`personad-linux-amd64.gz` and `personad-darwin-arm64.gz`. These are byte-for-byte
copies of the verified compressed archives, with canonical consumer names; their
sizes, compressed and native hashes, source, version, build and protocol metadata
stay unchanged. `source-manifest.json` retains the original producer descriptors.
Existing qualified Linux native packages are reused, not rebuilt. Versioned
mirrors are retained independently of native-cache pruning and upload cleanup.

Download preparation runs inside the original `.publish.lock`, before Hub
promotion. The version directory and download `latest.json` become public only
after the original Hub health, confirmation and serving-process hash checks.
Linked directories/files, changed immutable contents and a different namespace
fail closed. Publishing these files never deploys a node or enables node updates.

A directory on disk is not a working remote package source. The configured
HTTPS provider must mount **only** this mirror as a read-only static root and map
`<base>/<version>/manifest.json`, its two canonical archives and
`<base>/latest.json` to it. An installation may expose only these privacy-checked
release artifacts anonymously over HTTPS. If its provider requires authentication,
the consumer supports credentials in `node_packages.url` through HTTP Basic;
it does not inject the Hub API token or arbitrary custom headers. Never put
credentials in artifact files. Allow only `latest.json`, valid versioned
`manifest.json` and the two canonical archives. Return 404 for the release root,
directory listings, hidden staging directories, the producer's
`source-manifest.json`, unlisted paths and missing versions/files. A
reverse-proxy-only route does not serve these files. Select the formal
`node_packages.url` through the installation's managed configuration; an existing
`off` setting remains off until that owner changes it. This helper never rewrites
Caddy, mounts, credentials or a running Hub configuration.

Before reporting a remote source ready, fetch the versioned manifest and **both**
archives through the configured HTTPS provider, compare the manifest's
source/version/build to the confirmed running Hub and verify both compressed
size/hash and decompressed native hashes. Check 404 for an absent version, and
authentication failure without credentials when the provider is private. Local
mirror tests and Hub confirmation do not establish remote reachability or
consumer installation. The publisher's confirmed receipt reports the relative
mirror directory and consumer manifest hash for this separate read-only check.

An explicit single-node publisher transfer can recover a transient Link failure
without starting another publication. Only identical chunks for the already
selected transaction, offset and bytes are retried (at most four attempts), inside
the original install deadline. Every relay call retains the manager owner, machine
UID, executable and platform checks. Identity conflicts and invalid offsets fail;
expired transactions never trigger a replacement `begin`. A commit is sent once:
a lost or gateway-error reply is ambiguous, so only a fresh matching handshake,
required capability and guardian confirmation establish success. Rejected commits
still fail, and cleanup errors retain the original transfer failure.

The SSH publisher sends keepalives and drains node-publisher stdout and stderr
concurrently into the original worker log, including transfer offsets and remote
tracebacks. The existing command timeout also bounds output collection. Missing
SSH exit status or an interrupted connection cannot create a confirmed receipt;
it does not reconnect and replay the remote publication. Keepalives reduce idle
connection loss but do not prove a broken route has recovered. Preserve the failed
run and let its sole runtime owner decide a subsequent explicit retry.

A Hub-only edge switch may proceed while sessions on proven resident nodes or remote herdr/herdr-core hosts run:
their processes belong to the terminal host and survive the transport reconnect.
Local and unknown/direct assistant sessions still wait for idle. Fleet upgrades
retain the conservative wait for every working session.

This private automatic publication mode supersedes the manual-only publishing
restriction below for configured private installations. Public `publish.sh`
releases retain their separate publishing workflow.

## Implementation progress

- **Step 1 (one binary and a version manifest) done** (the "Current state" below this section describes things before work started):
  - Skills out of the binary: `internal/assistantskills` only holds built-in skills (currently empty); node runs `Sync` every time it starts the assistant: it links the user's skills in `~/.persona/skills/`,
    deletes broken links, and automatically moves skills an older version installed into the assistant directory (`TestSyncMovesTheOldCopy`); the gate command only appears when a skill that carries it exists (`TestGatedCommandOnlyWithTheSkill`);
    without it, the assistant's CLAUDE.md only covers MCP's `lark_*` (golden `assistant_claude_md.bare`); the hub asks node via `skills.list`;
    `GET /api/v1/skills` is the read-only list for the App. The copy of the Lark gateway skill was moved under the repo's `skills/` at the time (#1571 deleted that copy and its sync script; the skill is now provided by the Lark extension).
  - One binary: there are no longer files that differ by `public` (`TestOneBuild`); publish.sh keeps `-tags public` only as a marker for the privacy scan (the scan uses it to recognize dependency constants it can skip).
  - Channel and build number: `main.channel`, `main.build`, `main.updateURL`; publish.sh and deploy.sh stamp `release`, Docker stamps `docker`;
    `personad version -v` shows the channel and build number; `/api/v1/health` and `hello` carry `channel`, `build`, `protocol`, `min_app_build`.
  - Version manifest: `internal/update` (manifest, `CheckNext`, no downgrade `TestNoDowngrade`), `personad release manifest|check-next`;
    publish.sh publishes `latest.json` (sha256 of each file), and before publishing checks that the build number increases and that the live version's commit is an ancestor of this one.
    Step 1 originally had ed25519 signatures; after the user changed their mind, step 2 removed them (see "Security").
- **Step 2 (hub update and rollback) done**: `hub/selfupdate.go` (check, download and verify, wait for idle and "Update Now", replace, confirm or roll back, bad versions, update history, node updating along),
  `internal/update`'s download / replace / watchdog, `personad update-watch`; APIs in section 12.
- **Step 3 (App) done**: `apple/Shared/HubUpdate.swift` (state, history, Skills, compatibility check `HubCompat`, which banner to show `HubNotice`),
  `Views/Settings/HubUpdateViews.swift` (Settings › hub, banner, Skills page); after an automatic update is installed or rolled back, the hub sends a regular notification. Section 10 has been rewritten as the current state.
- **Step 4 (Docker check and notice) done**: a Docker hub asks the registry every hour which tags its image has (`internal/update/image.go`);
  if the newest release is newer than itself it shows a notice, and the App gives the command to run (pinned versions edit `.env` first). See the "Docker" section.

## Current state (verified 2026-11-02)

- Version: `personad version` is a string written in with `-X main.version`.
  - `deploy.sh`, `publish.sh`: `git describe --always --dirty` plus a minute-resolution timestamp, like `v0.1.1-23-gabc1234-202611021530`;
  - Docker image: built by CI when a `v*` tag is pushed; the version is the tag without `v`.
- hub:
  - `GET /api/v1/health` returns `{ok, version, features}`; the WebSocket `hello` carries `server_version`;
  - when a node's version differs from the hub's, the machine list marks it and offers **Redeploy** (`devices/deploy.go`, using binaries in `~/.persona/bin/nodes/` on the hub machine).
- The **only difference** between the full build and the public build is `-tags public`:
  - the full build embeds the Lark gateway skill into the binary with `go:embed` (`internal/assistantskills`, copied from upstream, not redistributable);
  - when node starts the assistant process, it installs it into the assistant directory's `.claude/skills/` and puts its command on PATH (`node/larkgate.go`);
  - the public build does not include it.
- Publishing: `publish.sh` builds the public build for four platforms, gzips them and, together with `SHA256SUMS` and `VERSION`, uploads them with wrangler to `dl/` under `publish.site_url` (Cloudflare Workers static assets),
  keeping only the latest version. wrangler uses an OAuth login that expires after about an hour, so it cannot run unattended.
- `install.sh` only installs the binary (`~/.persona/bin/personad`), not a service; the resulting hub machine has no `~/.persona/bin/nodes`, so tapping **Redeploy** on the public build fails.
- Our own hub is deployed over ssh (`deploy.sh`, plus the main session's deploy script), with three gates:
  - run all tests against real Postgres on the hub machine;
  - wait if the assistant has a turn running;
  - keep the previous version; done only once every machine has reconnected after the restart.
- App: Settings only shows the App's own version; machine details show the node version. There is no update check or self-update at all.

## Principles

1. **Only one binary**: skills are not in the binary; every hub (ours included) runs the same release build and can update automatically.
2. The user's skills live in their own space, and updates never touch them.
3. The publisher releases manually (`publish.sh`); each hub (ours included) updates itself when idle, then updates its own nodes only when `auto_update_nodes` is explicitly enabled. When a change on `main` is urgently needed, it can be deployed directly and will not be downgraded by an older public release.
4. Check sha256 before installing and have the new binary self-test; if any step fails, the current binary is left alone.
5. Do not restart while a chat is running or waiting for an answer.
6. If the new version does not start or machines cannot connect, automatically go back to the previous version; a version that was rolled back is not retried automatically.
7. No staged rollout: every gate before release must pass; after release all hubs update at once, and each rolls back on its own if it has problems.

## 1. Skills move out of the binary

- **The user's skill space**: `~/.persona/skills/<name>/` on the machine that runs the assistant.
  - Each directory is one skill (with a `SKILL.md` inside); the user adds and edits them, and personad only reads, never writes;
  - The machine that runs the assistant is usually the hub machine's local node; when the assistant is configured on another machine, it is that one.
- **Syncing into the assistant directory**: every time node starts the assistant process (both Claude and Codex assistants; `/agent` and the AI twin also use the assistant directory):
  1. For each skill under `~/.persona/skills/`, create a symlink at `<assistant dir>/.claude/skills/<name>`.
     When the user edits a skill, it takes effect the next time the assistant starts; Claude Code only reads skill files, so symlinks are fine.
  2. Delete links that point into `~/.persona/skills/` but whose target is gone; real directories the user placed directly in `.claude/skills/` are not touched.
  3. Built-in skills shipped in the binary (only ones we may redistribute; currently none) are still copied in by version stamp; a same-named skill in the user space wins.
  4. The gate command is only put on PATH when a skill that carries it is installed; the section about it in the assistant's prompt (`prompts/assistant/claude_md.md`) is also only written in when it is installed.
- **Migration**: two ways, each done only once.
  - **Automatic**: when node starts the assistant, each real directory under `<assistant dir>/.claude/skills/` installed by an old personad (with a `.persona-skills` version stamp next to it) that has no same-named entry in `~/.persona/skills/` yet
    is moved to `~/.persona/skills/<name>`, replaced in place with a symlink, and one log line is written; if a same-named user skill already exists, the user's wins.
  - **Manual** (on our hub, done by the main session): copy from the assistant directory's `.claude/skills/<name>` to `~/.persona/skills/<name>`.
    (Since #1571 such skills are provided by the Lark extension, see the top of `docs/extensions/lark-agent.md`; no more manual copying.)
  - That way the assistant still has its original skills when the new version starts. The copy of the upstream skill was moved under the repo's `skills/` at the time (the upstream sync script stayed, now writing there), and does not go into any binary;
    it does not matter that the public build lacks it: users put it (or their own skills) in `~/.persona/skills/`.
- **Drop `-tags public`**: there is no difference between the two anymore; `files_public.go` / `files_bundled.go` merge into one embed holding only built-in skills.
  - The privacy scan before release stays, and also checks that every skill in the binary is registered as redistributable (`assistantskills.Redistributable`);
  - A test guards it: the built-in skills directory may only contain skills registered as redistributable.
- **App**: Settings › Assistant › Skills, a read-only list (name, the description from `SKILL.md`, whether it comes from "my skills" or **Built-in**).
  API `GET /api/v1/skills`; the hub asks the node that runs the assistant. Management (add / delete / edit) comes later.

## 2. Build channels

The binary gets three more `-X` variables (`cmd/personad`):

| Variable | Value |
| --- | --- |
| `main.channel` | `release` (published, updates automatically), `docker` (in the image, only suggests pulling a new image), `dev` (default when unset: local builds, tests) |
| `main.build` | A monotonic build number, see "Version numbers" |
| `main.updateURL` | `dl/` at the publish address (`publish.site_url`, already public). If unset, no checks |

- Only `release` swaps its own binary; `docker` tells the user to pull the image; `dev` only shows the version.
- Direct deploys (`deploy.sh` and the main session's deploy script) also stamp `release` and use the build time as the build number: newer than the public release at the time, so they are not downgraded; once the publisher releases a newer version, the hub takes over on its own (section 6).
- `/api/v1/health` and `hello` also carry `channel`, `build` and `protocol`.

## 3. Version numbers: release time as the build number

- Newer vs older is compared by **release time** (UTC, `YYYYMMDDHHMM`, an integer); the version string stays as is, for people only (decided by the user 2026-11-02).
- Why:
  - every version string already ends with this time, so there is no new counter to maintain, and a tag is not required every time;
  - there is only one publisher, so time naturally increases;
  - nobody has to decide whether this counts as minor or patch.
- Guarding against non-monotonic numbers: `publish.sh` reads the live manifest and refuses to publish in two cases:
  - the new build number is not larger than the live one;
  - the live version's commit is not an ancestor of this one.
- A build number only says it was released later, not that the code is newer: before installing automatically, the hub also checks whether the new version's code contains its own commit ("no regression").
- Docker keeps the image tag's semver, compared only within the docker channel.

## 4. Version manifest

`dl/` gets one more file, `latest.json`:

```json
{
  "version": "v0.1.1-30-gabc1234-202611021530",
  "build": 202611021530,
  "commit": "abc1234…",
  "released": "2026-11-02T07:30:00Z",
  "min_app_build": 95,
  "protocol": 3,
  "notes": "Highlights of this version",
  "files": {
    "linux-amd64": {"name": "personad-linux-amd64.gz", "size": 12345678, "sha256": "<of the archive>", "binary_sha256": "<after decompression>"},
    "linux-arm64": {}, "darwin-arm64": {}, "darwin-amd64": {}
  },
  "ancestors": "<base64>"
}
```

- `ancestors`: all commits reachable from this commit (`git rev-list <commit>`), the first 6 bytes of each, sorted, then base64 ("no regression").
- `programs`: programs published together with personad that the hub runs alongside (`update.ProgramNames`: `personad-wechat`, `docs/extensions/wechat.md`; `persona-hub` and `persona-manager`, [../server/server.md](../server/server.md) ("Programs");
  Telegram's `personad-telegram` was also here up to #1704; after that it is an extension and goes through the extension market):
  `{"personad-wechat": {"linux-amd64": {name, size, sha256, binary_sha256}, …}}`. Old hubs ignore this field and still only swap personad.

- `nodes`: the other programs a node can run, published with personad (`update.NodePrograms`: `persona-node`, the device's own): `{"persona-node": {"linux-amd64": {name, size, sha256, binary_sha256}, …}}`.
  `node_protocol`, `node_protocol_min`, `node_capabilities`: what the release's nodes announce in `node.info` (`docs/protocol/protocol.md` ("Node handshake")), so a manager of another build sends one only when it drives it (`docs/operations/node-update.md` ("Node packages")). Old hubs ignore all three.
- Every release is also kept at `dl/<version>/` (its node builds, `personad-*` and `persona-node-*`, and this manifest as `manifest.json`; also `dl/X.Y.Z/` for the tag `vX.Y.Z`), where managers fetch the node build of their hub's version; `dl/latest.json` and `dl/*.gz` stay the newest release.
- A downloaded file must match both `sha256` (archive) and `binary_sha256` (decompressed), and the new binary's `version -v` must report the version and build number in the manifest.
  `SHA256SUMS` stays for install.sh.
- `min_app_build`: Apps below it cannot use this hub version. For `protocol`, see "Compatibility".
- There is no minimum node build: a node reports a version string, and the hub redeploys any node whose version differs from its own. That redeploy runs after the new hub has confirmed
  (section 8, step 9 "Update nodes along with it"), not before the new hub starts; a machine that fails to update keeps the reason.

## 5. Public publishing (manual, publish.sh; user, 2026-11-02)

Public releases use `publish.sh` manually on the publisher's dev machine, and the site's Worker is under the publisher's Cloudflare account. Private canonical-branch releases use the serialized worker described in "Automatic private Hub releases".
- The push relay (`docs/operations/push-relay.md`, `/relay/*`) is in the same Worker as the downloads, so every time `publish.sh` redeploys the site it deploys the relay too
  (a Worker deploy is an atomic switch, never half old and half new; relay code changes also go live with this release).
- Tests must still pass before publishing, plus `publish.sh`'s own checks (if any fails, nothing is published):
  1. Read the live `latest.json` (if any): the new build number must be larger, and the live version's commit must be an ancestor of this one (`git merge-base --is-ancestor`);
  2. Build the four platforms (with `channel=release`, `build`, `updateURL` written in), privacy scan (the binary may only contain redistributable skills);
  3. Generate `latest.json` (version, build number, commit, the commits it contains `ancestors`, release time, sha256 of each platform's archive and decompressed binary, `min_app_build`, `protocol`);
     do not publish when the working tree has uncommitted changes in server/ or web/ (`NO_DEPLOY=1` still builds, and the commit in the binary carries `-dirty`): whatever commit the manifest names, the binary must be exactly it;
  4. After publishing, read back `VERSION`, `SHA256SUMS`, `latest.json` and compare them with the local ones.

### Security: no signing (decided by the user 2026-11-02)

User: "Why sign at all? Skip signing for the Linux service, it's too complicated." The trade-off, spelled out:
- **Protects against**: corrupted or truncated downloads (if sha256 does not match, delete and do not install), and installing a binary that does not run or is not this version (`version -v` self-check, plus confirmation after it starts, otherwise roll back).
  Downloads go over HTTPS; tampering in transit requires breaking TLS first.
- **Does not protect against**: the publish site (the Worker at `publish.site_url`, the publisher's Cloudflare account) being impersonated or compromised. Then an attacker could replace both the binary and the sha256 in `latest.json`,
  and every hub with automatic updates on would install it, running as the user that runs the hub.
- The user knows this and decided against signing. To add it later: put a signature next to the manifest, compile the public key into the binary, and have the hub install only what verifies (step 1 once implemented this; see git history).
- Hubs that do not want this risk: turn automatic updates off in the App, or deploy with Docker (you decide when you pull the image; but see the next point for the node builds a Docker hub fetches).
- Node builds the manager fetches for the computers (`docs/operations/node-update.md` ("Node packages")) are checked the same way: the manifest's sha256s over HTTPS, the build's platform and kind, and `version -v` where it can run (the manager's own platform; over the node RPC the node's `node-update-check` reads the version too, the ssh and tailcat paths do not). Not signed either. A hub sends builds of its own version (the release's of that version included) to its computers by itself, Docker hubs too and whatever the automatic-update setting; a fallback build (another version) only when someone chooses **Redeploy**, and an update queued for the hub's version never resumes as one. A build put in `bin/nodes` by hand with a `.version` of another version goes out by itself too: putting it there is the owner's choice. `node_packages.url` set to `off`, or builds put in `bin/nodes` by hand, avoids the site for them.

## 6. Direct deploys (when a change on the default branch is urgently needed)

The existing path of building from the default branch and deploying directly (`deploy.sh`, plus the main session's deploy script) stays, for when waiting for a release is not an option.
- The resulting binary also gets `channel=release`, `build` (build time, UTC `YYYYMMDDHHMM`) and `updateURL`:
  - it is newer than the public release live at the time, so the hub's auto-update will not install the older public release: **no downgrade**;
  - once the publisher releases a newer version (larger build number), the hub takes over on its own;
  - `TestNoDowngrade` guards this: if the manifest's build number is not larger than its own, it is not installed; equal is not installed either.
- It also gets `commit` (`-X main.commit`, with `-dirty` appended when server/ has uncommitted changes): a later public release only installs itself if it contains this commit ("no regression").
- The main session's deploy script lives outside the repo and must add the same four `-X` flags to its `go build` (the report gives the line to add).
- **Deploy only from a working tree that contains the default branch**: `deploy.sh` first fetches the remote's default branch (how it finds the branch: the comment above the check); if the current HEAD does not contain it, nothing is deployed and it exits 2 (same if it cannot be fetched);
  deploying a branch that is behind would swap changes someone else just deployed (hub and web) back to old ones. For a deliberate rollback, use `PERSONA_DEPLOY_ANY_COMMIT=1`. See `docs/lessons/server.md` ("Deploying from a stale branch").
- The hub machine also needs `personad-wechat` (the WeChat bot program, `docs/extensions/wechat.md`): `deploy.sh` builds the same version for the hub and puts it next to personad; nodes do not need it.
  Telegram is not here: it is an extension (#1704), installed and upgraded on the hub from the extension market.
- **Update a hub with `deploy.sh <hub machine id>`, not by copying a binary and running `personad hub switch` by hand.** `deploy.sh` first puts this build's node packages for every platform in the hub's `bin/nodes/`, then switches the hub blue-green (no restart while the socket units are unchanged, `docs/operations/hub-restart.md`).
  A hand switch leaves `bin/nodes/` on the previous build: the hub hands out a `personad-<os>-<arch>` there as its own version (it has no version file), so **Redeploy** in the app puts the old node back on a computer.
  To check: every `bin/nodes/persona-node-*.version` on the hub machine equals the hub's `/api/v1/health` version.
- No staged rollout: after a release all hubs (ours included) update at the same time when idle; problems are handled by each hub's own rollback and bad-version marking.

### No regression (2026-11-02, #454)

Comparing only build numbers can go backwards: a directly deployed hub runs commit D on `main` (build number = deploy time); later someone runs
`publish.sh` from code that is behind `main` (commit B), the build number is larger, and the hub would "update" itself to the older B. `publish.sh`'s `check-next` only compares against the live release and cannot cover directly deployed hubs.

- **How it decides**: the binary records its own commit (`-X main.commit`), and the manifest carries all commits the new version contains (`ancestors`).
  The hub only installs automatically when the new version is this very commit, or `ancestors` contains it (`update.Build.Hold`, `TestHold`).
- **Why not ask GitHub**: the repository is private, the hub cannot necessarily reach GitHub, and it should not be given a token; with the manifest carrying the list, no network lookup is needed.
  6 bytes per commit, 900 commits is about 7 KB (base64); a few hundred KB a year from now is still within the manifest's 1 MB limit;
  the chance that a commit not in the list happens to collide with some prefix is about 10^-10 at ten thousand commits.
- **Cannot confirm counts as not satisfied**: the binary has no recorded commit (old deploy scripts), it has uncommitted changes (`-dirty`), the commit is too short, the manifest has no `ancestors` (old `publish.sh`), or it cannot be read.
  A direct deploy from a PR branch that is later squash-merged into `main` cannot be confirmed either (the merge produces a different commit): the next deploy from `main` or a manual update fixes it.
- **When not satisfied**:
  - It is not installed automatically. The App still says a new version is available, also when automatic updates are on; Settings › hub shows the reason (`latest.held`), and manual **Update Hub** can still be tapped;
  - The update history gets one "not installed automatically" entry per new version (`result: held`) with the reason, e.g. "新版本（提交 b2b2b2b2）不包含这个 hub 正在运行的提交 d4d4d4d4" ("the new version (commit b2b2b2b2) does not contain commit d4d4d4d4 that this hub is running"; text emitted by the hub).
- When automatic updates are off it would not install by itself anyway, so nothing is recorded.

### Programs swapped together with personad (2026-11-03, #533)

The programs (`update.ProgramNames`): `personad-wechat` (the WeChat bot), and `persona-hub` and `persona-manager`, the hub's and the manager's own programs ([../server/server.md](../server/server.md) ("Programs")).

- When the manifest has `programs` for this hub's platform, they are downloaded and verified (same sha256, and `version -v` must be this version), then swapped in together with personad: placed next to personad,
  existing ones kept as `.prev`, and ones that did not exist before recorded as newly installed. If any one fails to download or verify, the whole update is skipped. A program whose swap fails midway is left as it was, and going back leaves it too.
- Rollback (the new version does not start, the watchdog, an error during the swap) swaps them back together with personad and deletes the newly installed ones (`update.RestoreAll`, `programs` in `pending.json`).
- **Which program the hub goes on as** (`run` in `pending.json`, `Pending.RunPath`): a hub run as `persona-hub` restarts, hands over and rolls back as the `persona-hub` the update swapped, so it stays on its own program; a hub run as `personad` stays on `personad` (no `run`).
  `target` stays `personad`, the release's main download and what the watchdog runs as `personad.prev`. A build that predates `run` (an older edge or watchdog) reads only `target` and brings the hub back as `personad hub run`, which serves the same hub; a `pending.json` an older build wrote has no `run` and goes back to `target` as before.
  A release without `persona-hub` for this platform leaves a hub run as `persona-hub` by hand on `personad` (which the update did replace), not on a `persona-hub` it did not.
- **Which program a service runs** is its unit's (or agent's) to say: an update replaces the files and never edits a service. On Linux `hub setup` writes the hub's unit to run `persona-hub run` and the built-in manager's to run `persona-manager assistant` when each is beside `personad` and its build ([../server/server.md](../server/server.md) ("Programs")); a Mac's agents stay on `personad` for now. A unit it wrote earlier runs `personad` until setup runs again (the install command with `--hub` runs it), which moves the unit to the program and restarts it; `PERSONA_SPLIT_PROGRAMS=0` in setup's environment keeps it on `personad`, and the unit keeps the variable until setup runs again without it. A self-update alone never moves a hub between `personad` and `persona-hub`.
- **A release without `persona-hub` or `persona-manager` for this platform** (`hub.unreplaced`) leaves that file as it was. That is fine while nothing runs it, and the update goes ahead.
  It is not installed while a service runs that program, because the service would go on starting the old build beside the new `personad` (`hub/selfupdate_services.go`). A service here is one of:
  - A systemd unit whose `ExecStart` runs it: the last one in `[Service]`, drop-ins read after the unit, read as systemd and `hub-systemd.sh` read it. The user's units come from `~/.config/systemd/user`, `/etc/systemd/user`, `~/.local/share/systemd/user` and `/usr/lib/systemd/user`, the machine's from `/etc/systemd/system` and `/usr/lib/systemd/system`. A unit in an earlier directory shadows one by the same name in a later one, and drop-ins come from all of them. It counts when systemd says it is enabled (or static, generated…) or active; when `systemctl` cannot answer (no user manager to ask, say), every such unit counts.
  - A launchd agent or daemon (`~/Library/LaunchAgents`, `/Library/LaunchAgents`, `/Library/LaunchDaemons`; XML, or binary through `plutil`) whose program is it: `Program`, else `ProgramArguments[0]`. One that says `Disabled` does not count.
  - The systemd service the hub itself runs in, when it runs as `persona-hub` and that unit's own `ExecStart` is this `persona-hub` or `personad` (switched to `persona-hub` inside it). In any other service (a terminal multiplexer's, `systemd-run`'s) it was started by hand.
  Not seen: units under `/run`, units under `/lib/systemd/{system,user}` where `/lib` is not merged into `/usr`, template instances (`name@instance.service`), a launchd job that says `Disabled` but was enabled in launchd's override database (`launchctl enable`, `launchctl load -w`), a program wrapped in another command (`sh -c '…/persona-hub run'`) or named without a path. Such a service does not hold the release back.
  The services are looked at when the release is read and again just before the swap (a unit pointed at the program while the update waited for idle).
  Settings › hub shows the reason (`latest.held`, as for a release behind this hub's code above), the history gets one `held` entry per release, and **Update Hub** is refused with the same reason (409).
  `install.sh` also refuses such a release before anything changes, with a wider check of its own: a unit file or drop-in in those systemd directories that has the program's path in an `ExecStart` line (as is, under `%h`, or as systemd escapes it), or a property list that has it in a `<string>`. A unit a later drop-in took back to `personad`, a disabled one and a wrapper count there too, so it refuses some releases the hub would install.
- The first update that carries programs: the watchdog running is the old personad, which does not know `programs`, so a rollback only swaps personad back; the new programs left behind do not match the old hub version, and the hub will not start them
  (status says "not the same version"), until the next update.

## 7. Checking for updates

- **Release build**:
  - The hub checks 1 minute after startup, then every hour (every 6 hours until 2026-11-07, when the user asked for hourly so a release reaches hubs sooner); the App can "Check now" (`POST /api/v1/hub/update/check`);
  - It only fetches the manifest (a few KB): there is an update only if the build number is larger than its own and not in the bad-version list; the result is broadcast to open Apps.
  - While an install is running or the new process has not confirmed the update, checks refresh `latest` and `last_check` without changing `state`, `progress` or `error`. This applies to manual checks, the first check after startup and hourly checks, including responses to requests started before the install. A failed check advances `last_check` and keeps the previous `latest`; it still returns an error to the caller. Confirmation also prevents a check from starting another automatic install.
- **Docker build**:
  - Every hour, query the image repository's tags (anonymous GHCR `tags/list`), take the highest stable semver, and show a notice if it is newer;
  - The hub installs nothing; Docker pulls the image and verifies by digest;
  - CI writes the image name into the binary at build time; it is not hard-coded in the source.
- **dev**: no checks, only shows the version.
- Network unavailable or bad manifest: show the reason under **Check for Updates** in the App when no install or confirmation is in progress; otherwise keep the update's progress and error. Do not hammer retries (keep the hourly interval).

## 8. Updating (release build; automatic and manual use the same flow)

```
Download → Verify → Prepare → Wait for idle → Replace → Restart → Confirm → (on failure) Roll back
```

1. **Download**:
   - Download into `~/.persona/update/<build>/`: the archive for the hub's own platform, plus node binaries for **every platform** in the release (four platforms, for updating nodes along with the hub);
     not just the platforms of machines currently connected or known to the hub at the moment of the update. Machines added after the update, or that had never connected when it happened,
     need these for **Redeploy** too, and the manifest is only read during this self-update (#3186). The node binary for the hub's own platform is the hub binary itself,
     so reuse the copy already downloaded and verified rather than downloading it twice;
   - If a node binary for any platform fails to download or verify, the self-update fails (just like programs below). Previously it downloaded only the platforms of the machines known at the time;
     now a missing platform is one that should have been in the release, and installing cannot recover it later. The release script uploads every `.gz` first, then switches the site serving `latest.json`
     (`scripts/release/publish.sh`), so "listed in the manifest but not uploaded yet" cannot happen;
   - Restart interrupted downloads, at most 3 attempts; rename the temporary file only after the download finishes.
   - The downloads live only as long as the update: `<build>/` is removed when the update fails (download, check, swap or restart), once it is confirmed and its node binaries are in place, and when it goes back. A hub start also removes every other `<build>/` (one that went back through the watchdog or the entry process, which run the old build, or one an older build left), keeping only the one it is confirming.
2. **Verify**:
   - Continue only if both sha256 values match the manifest; otherwise delete the download and report an error, leaving the current binary untouched;
   - After decompression, run `<new> version` once; it must report the manifest's version and the `release` channel.
3. **Prepare**:
   - Put node binaries in `~/.persona/bin/nodes/`: publicly installed hubs can now **Redeploy** too;
   - Write `~/.persona/update/pending.json`: from/to versions, previous version's path, start time, machines online before restart, startup count.
   - This is the only exception to "all server state goes into Postgres": rollback must work even when the new version cannot connect to the database or cannot start at all, so the marker sits beside the binary and is deleted when the update ends.
     Update history and bad versions still go into Postgres.
4. **Wait for idle** (check every 30 seconds; also do not restart while redeploying a machine): automatic and manual are the same: **restart only when the assistant and every Agent are idle**, with no time-of-day restriction (user, 2026-11-02).
   - Idle = no session working or waiting for the user's answer (both assistant chats and Agents in herdr count). If they never become idle, never install; keep the download ready.
   - For a manual update, the App shows "what is still busy" (which chats, on which machine), and offers **Update Now** in a second confirmation:
     the confirmation states, "This interrupts anything waiting on the hub (the assistant's current turn, uploads in progress, terminal connections). Agents in herdr aren't affected and reconnect on their own."
     After confirmation, skip waiting for idle; all other steps (verification, rollback) stay the same.
5. **Replace**: backups and temporary files still use the compatible names `personad.prev` / `personad.new`; atomically replace the actual server binary in the same directory. The target path follows the installation layout and runtime migration implementation; the product becoming Persona does not mean the updater has already renamed these files.
6. **Restart**:
   - **With an entry process** (`personad hub run` on Linux and macOS, since 2026-11-06), use blue-green switching ([hub-restart.md](hub-restart.md), "Blue-green switching"):
     - The hub asks the entry process to switch to the new binary and gives it the expected build number;
     - The entry process first starts the new version as standby; only after its version matches and self-check passes does it hold connections, release the old version, activate the new one and switch routing;
     - If the new version cannot start, the entry process returns `switch_failed`; the old version restores the files, deletes pending and records this attempt as failed (not as a bad version, since the new version never ran);
     - No watchdog is started: the entry process is the watchdog.
   - **Without an entry process** (Windows, `PERSONA_HUB_EDGE=0`): start the watchdog first (below), then `syscall.Exec` the new binary with the same arguments and environment
     (the program the hub runs as: `persona-hub` for a hub run as `persona-hub`, see "Programs swapped together with personad").
     Carry the socket passed by systemd along (`LISTEN_FDS`, `execListenEnv` in `listen.go`; this step used to be missing, so the new process failed because the port was occupied);
     restart under the same process id, without relying on the service manager.
   - The local node is a child process of the hub and exits with it; the new hub starts it with the new binary. It runs as the `personad` beside the manager's program (or as that program when no `personad` of its build is there), and as `persona-node` only when one of the new build is beside it, which the update does not put there ([../server/server.md](../server/server.md) ("Programs")).
7. **Confirm**: on startup the new hub sees `pending.json`; success requires all three conditions below (delete pending, write update history, broadcast "Updated to …"):
   - HTTP is up;
   - The database is connected;
   - Every machine that was online before restart reconnects within 2 minutes (offline machines do not count).
8. **Rollback**, two layers:
   - **The new process itself**: if conditions are not met within 2 minutes, or startup count exceeds 3 (repeated crashes under a service manager), replace with `.prev` and `exec` it.
     If the startup count cannot be written to `pending.json` (disk full, read-only, wrong directory owner), roll back immediately too: without a stored count, repeated crashes can never reach 3 (#2613).
     If the directory remains unwritable (read-only, wrong owner), the rollback record cannot be written and `pending.json` cannot be deleted either: this rollback does not enter history, mark a bad version or send a push; after 3 minutes the watchdog restarts the already-rolled-back hub once more, and every later startup logs a deletion failure.
   - **With an entry process**, a new process that needs to roll back asks it to switch to the old binary: if already serving, perform a `rollback` switch; if not yet serving (startup count exceeded),
     exit with code 75 so the entry process immediately restarts the restored old version at the same path. In the two cases below, the entry process handles rollback: restore the files from pending, write the rollback marker, start the old version:
     - The new version fails to activate;
     - The new version does not report "confirmed" within 3 minutes.
   - **Watchdog** (without an entry process; code from the old binary, `personad update-watch`, started separately before exec and detached from the process group):
     if it does not see the new build number and "confirmed" from the local `/api/v1/health` within 3 minutes, it rolls back itself:
     - Under systemd (with `INVOCATION_ID`) or launchd: restore `.prev`, kill the main process, and let the service manager start the old binary;
     - Without a service manager: after restoring it, start the old hub in the background with the original arguments, as the program it ran as (`run` in `pending.json`, else `target`).
     - It signals the hub only while the hub is still its parent process (SIGTERM, then SIGKILL after 10 seconds if it is still there): once the hub has exited, its pid may belong to another process.
     - Before restoring anything it claims the marker by renaming `pending.json` to `pending.json.watch`. A confirm that removed the marker first (while the last health check was still out, or timed out) ends the watchdog with nothing undone; a confirm that finds it already claimed records nothing and leaves the update to the watchdog. If the restore fails, the marker goes back. A marker it cannot rename (an update directory that is read-only or has the wrong owner, where a confirm cannot remove it either) does not stop it: it goes back without the claim. A watchdog that dies while it holds the claim (power loss, or a stop of the hub unit: under systemd it shares the hub's cgroup) leaves `pending.json.watch`; the hub's next start renames it back to `pending.json` and handles the update as usual.
   - After rollback, record this build number in the **bad-version** list: automatic updates no longer try it and wait for a newer version. The user can still install it manually; the App says, "This version failed to start last time and was rolled back."
9. **Update nodes along with it (only with `auto_update_nodes: true`)**:
   - After the hub confirms success, run `deploy.go` for each online machine with a different version (push binary, restart the persistent node, wait for it to reconnect), one at a time;
   - Keep the failure reason for machines that fail; it does not affect the hub. If an old node does not support the update protocol, ask for one local upgrade first;
   - SSH / tailcat keep their original deploy path; Persona Link active / passive connections and nodes forwarded by member hubs update through existing RPC, with local rollback and reconnect confirmation. Offline Link machines store their target in Postgres and resume on their next connection; old targets stop when the hub version changes. See [node-update.md](node-update.md).
10. **History**: one record per update (automatic or manual): from/to versions, start and end times, result (updated / rolled back with reason / failed with reason), automatic or manual.
    Stored in Postgres (`hub_update_history` in settings, most recent 50 records kept).

## 9. Three ways to run

| | Release (our hub, installed with install.sh) | Docker | dev |
| --- | --- | --- | --- |
| Check | Version manifest | Image tags in the registry (see "Docker") | No checks |
| Update | Automatic (on by default) / one tap in the App | Suggest `docker compose pull && docker compose up -d` (edit `.env` first if a version is pinned) | None |
| Restart | `exec` the new binary itself | User recreates the container | — |
| Rollback | Automatic (`.prev`, bad versions are not retried) | Restore the previous tag | — |
| node | Automatically redeployed with the hub | An image built with `UPDATE_URL` carries its own platform's build only, and the manager fetches the others from the release of the image's version, or the newest one it drives; one built without carries all four platforms' personad (`docs/operations/node-update.md` ("Node packages"), `docs/operations/docker.md` ("Node builds")) | The hub's `bin/nodes` (`deploy.sh`) |
| Urgent change on `main` | Direct deploy (stamped `release` and build time; an older public release will not downgrade it, a newer one takes over automatically) | — | — |

### Docker (step 4)

- **Which image to check**: compose passes `.env`'s `PERSONA_IMAGE` into the container, and that is what the hub checks; if absent (old compose files), use the build-time
  `main.image` (`release.yml` passes `ghcr.io/<owner>/<repo>`, not hard-coded).
- **How to check**: registry `GET /v2/<repo>/tags/list`; first obtain a pull token as directed by its `WWW-Authenticate` response (public images allow anonymous access, both ghcr.io
  and Docker Hub do this), and follow `Link` for pagination. Only recognize `X.Y.Z`, and take the highest; it counts as a "new image" only if newer than the hub's own version. Its own version must also be `X.Y.Z`
  (or a prerelease such as `-rc1`): a manually built `edge` image uses `git describe`, is newer than the tag, and cannot be compared, so only the latest stable version is shown.
- **Private images**: when the repo is private, its ghcr image is private by default too, so anonymous access cannot get a token. Set `PERSONA_REGISTRY_TOKEN` in `.env` (a token that can read this image),
  or make the image public; when unset, Settings › hub explains why it cannot check and what to set. It does not read credentials saved by `docker login` on the NAS (they are on the host, not in the container).
- **App**: at the top of Agents, "New hub image: X", with only × (remembered per version; the next version is shown again). Settings › hub gives the commands; when a version tag is pinned, first show the line to edit `.env`.
- Not done: pulling images or recreating containers for the user (requires mounting the Docker socket, effectively handing the NAS to the hub).

## 10. In the App

- **Settings › hub** (a new row in the **Server** group, with version and status on the right; `HubSettingsPage`):
  - Version, channel (**Release** / **Docker** / **Development**), last check, **Check for Updates** (pull down also checks);
  - Update: when available, show the version and notes, and **Update Hub**; mark versions that were rolled back (automatic updates will not install them again);
    while updating, show progress (downloading / waiting for idle / restarting / **Confirming the new version**); while waiting for idle, list busy chats and machines, with **Update Now…** below,
    followed by a second confirmation explaining what will be interrupted (the assistant's current turn, uploads in progress, terminal connections; Agents in herdr are unaffected);
    Development explains why it will not update itself; Docker shows the command to run on the NAS, with copy;
  - **Update Hub Automatically** toggle (release builds only, on by default);
  - personad on each computer: version, **Mismatch** if not from the same deployment as the hub; open the machine page for **Redeploy**;
  - **Update History**: last 10 attempts, result (updated / rolled back / **Update failed** / **Not installed automatically**), automatic attempts marked, from/to versions, time, rollback and held-update reasons.
  - Old hub (no `/api/v1/hub/update`): explain that install.sh must be run once on the hub machine to update it.
- Android Settings has the same items (**Hub Version**, `HubUpdatePage`, see `../android/android.md`, "Hub version"); Android does not yet have the row at the top of Agents.
- **A row at the top of Agents** (`HubNoticeRow`, same position and appearance as **Persona is signed in on your Mac**, hidden while searching): only one at a time, most important first:
  1. App too old (its build number is below the hub's `min_app_build`): "This app is too old for this hub. Update it in TestFlight", cannot dismiss;
  2. Hub too old (`protocol` below what the App requires; old hubs that do not report it count as 0): "The hub is out of date; some features won't work", dismissed with × and not shown again;
  3. Updating: e.g. "Hub updating: waiting for 2 busy chats to finish";
  4. Rolled back: reason, × to dismiss (remembered for this version);
  5. First-time notice: "The hub updates itself when idle", with **Turn Off** (disables automatic updates) and **OK**; tapping either makes the hub record `auto_notice_seen`, and it does not appear again;
  6. New version with automatic updates off: "Hub update available: X", with **Update** and × (not shown again for this version). Not shown with automatic updates on; it will install itself;
     Docker hubs show "New hub image: X", with only ×. After dismissing one, the next appears as usual; the App remembers the most recently dismissed notices.
  Tapping elsewhere in the row opens the hub page (sheet). No popup or blocked interaction; button labels stay single-line, and each button handles only its own action (`.borderless`, `HubNoticeUITests`).
  Only on Agents: the assistant page is a chat, and an extra top row would push it down; hub status belongs with machines and Agents.
  On the web (#2897), it sits below the filter bar in the sidebar's Agents block (`web/src/views/agents/HubNoticeRow.tsx`); items, order and buttons match the App, and dismissals are stored in this browser (last 8, same keys as the App);
  no items 1 or 2: the web is served by the hub itself, so their versions always match. Tapping the row opens Settings › hub.
- **Notifications**: after an automatic update installs, push "Hub updated to X"; after rollback, push "Hub update failed" with the reason (hub's `updater.notify`). Manual updates are visible in the App and do not send an extra push.
- **During restart**: disconnection is expected; the usual "Connecting…" title becomes "Hub updating…", and its explanation says the hub is restarting and will automatically return to the previous version if the new one cannot start; after reconnect, show the new version.
- **Settings › Assistant › Skills** (`SkillsPage`): skills on the computer running the assistant (`GET /api/v1/skills`), read-only, marked as user-provided or built-in, explaining where to put them and when they take effect.
- All use the design system; screenshots reviewed for both styles (Liquid Glass, BeautifulUI) × light/dark.

## 11. Compatibility

- `protocol`: the App ↔ hub major version (`proto.Protocol`), incremented only for incompatible changes; fine-grained capabilities still use `features`.
  The hub reports `protocol` and `min_app_build` in `/api/v1/health` and `hello`; the App records its minimum required `protocol`.
- Old Apps ignore these fields and keep working; they just do not show update-related features.
- Old hubs lack these APIs: the App shows the version, and **Check for Updates** says, "This hub is too old to support updates in the App."
  Publicly installed old hubs need one manual install.sh run to get an update-capable version; our hub gets one final ssh deploy from the main session.

## 12. API (draft)

| Method | Path | Description |
| --- | --- | --- |
| GET | `/api/v1/hub/update` | `{version, build, channel, can_update, latest?{version, build, released, notes, bad}, state, progress, busy[]{machine, session, title}, error, auto, auto_notice, last_check}` |
| POST | `/api/v1/hub/update/check` | Check immediately |
| POST | `/api/v1/hub/update` | Start update (409 with a reason for non-release builds); `{"now": true}` is **Update Now** after the second confirmation, skipping the idle wait |
| PUT | `/api/v1/hub/update/settings` | `{auto}`; `{auto_notice_seen: true}` |
| GET | `/api/v1/hub/update/history` | Update history, newest first |
| GET | `/api/v1/skills` | Skills on the machine running the assistant: `[{name, description, source: user\|builtin}]` |
| WS event | `hub_update` | Push to open Apps when state or progress changes |

`state`: `idle` / `checking` / `available` / `downloading` / `waiting_idle` / `restarting` / `confirming` / `updated` / `rolled_back` / `failed`.

## 13. Tests

- Go:
  - Bad download: delete without installing when sha256 does not match; restart an interrupted download;
  - Retry after interrupted download; delete without replacing on a sha256 mismatch;
  - New version cannot start: compile a deliberately unstartable binary in the test; verify both the new process's self-check and the watchdog can roll back, record the bad version and do not retry it automatically;
  - Wait for idle: do not restart while the assistant or any Agent is busy (automatic and manual alike); `now` skips the wait;
  - Checks during installation or confirmation preserve the update state, progress and error on success and failure, including when an install starts while the manifest request is in flight;
  - Skills: symlink creation and cleanup, automatic migration of old installs, user precedence on a name collision, gate command only present with its skill; built-in skill directory contains only redistributable skills;
  - dev / docker channels never install, however new the manifest is (API 409);
  - **No downgrade** (`TestNoDowngrade`): a directly deployed binary (build number = build time) does not install a manifest with a smaller or equal build number; only larger ones install;
- publish.sh: no publishing if the build number does not increase or the live commit is not an ancestor.
- App: FakeHub tests for the hub row in Settings, first-time notice, banner (appears, dismissed with × and not shown again, progress, explanation while disconnected, both incompatibility notices), update history and Skills list.
  Screenshots: two styles × light/dark.

## 14. Implementation steps

1. **One binary and a version manifest**: move skills out (user space, symlinks, migration, `ol` and prompts on demand, read-only list API), remove `-tags public`,
   channel and build number (publish.sh, deploy.sh, Docker), `internal/update` (manifest, pre-release checks), publish.sh publishes the manifest and performs the two monotonicity checks;
   `/api/v1/health` carries the new fields. Before deploying, put the Lark gateway skill in our hub's `~/.persona/skills/` (or rely on automatic migration).
2. **Hub update and rollback**: check, download and verify, wait for idle (**Update Now**), replace, watchdog, bad versions, history, no downgrade; update nodes along with it (add `via` and tailcat).
3. **App**: hub row in Settings, first-time notice, banner, progress, what it is waiting for, update history, incompatibility notices, Skills list.
4. **Docker checks and notices**.

## Public-release decisions (2026-11-02)

The private publication and Hub-only session policies are described in "Automatic private Hub releases" above.

1. Move skills out of the binary into the user's own space (`~/.persona/skills/`), never overwritten by updates; merge full and public builds into one, and every hub updates automatically,
   ours included (user: "Skills definitely need to be highly customizable"). Keep ssh deployment only for emergencies.
2. Automatic updates are **on by default**; clearly tell the user the first time and offer a way to turn them off; record every update; do not automatically retry rolled-back versions.
3. No time-of-day restriction: install when the assistant and Agents are all idle. Do not restart while a chat is running or waiting for an answer.
4. Use release time (UTC `YYYYMMDDHHMM`) as the build number.
5. **Manual public publication**: the publisher runs publish.sh (the Worker is under their account); enforce manifest, build-number and commit-ancestry checks.
   Our hub also updates automatically to their releases; direct deploys remain for urgent changes on `main` and will not be downgraded.
6. No staged rollout; all hubs update at once after release, each handles its own rollback.
9. **No signing** (user: "Skip signing for the Linux service, it's too complicated"): the manifest has only sha256, fetched over HTTPS; install automatically when it matches. This only protects against corrupt transfers, not an impersonated or compromised publish site (see "Security").
7. Manual updates also wait for the assistant and Agents to be idle; the App shows what it is waiting for and offers **Update Now** in a second confirmation.
8. The skill space is `~/.persona/skills/` on the machine running the assistant.
