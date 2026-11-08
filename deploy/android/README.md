# Automatic Android releases

The release worker checks the configured main branch every minute. It runs on the
Android build machine, in a dedicated clone, and posts `release/android` commit
statuses. This works without a GitHub Actions runner; it does **not** turn a queued
Actions job into a successful run. The worker does not deploy Hub services or nodes.

For each new source commit it runs publication/signing tests, the privacy scan,
Android transport/settings/agents/notification tests and a Release build. The version
code is `100000 + git rev-list --first-parent --count <source>`. Retries retain that
version and reuse only a hash-verified successful build. The worker refuses a main
branch history that is not descended from its last publication. Do not reset history
or change this version scheme after installing released builds.

A separate signing host polls a single request queue over authenticated SSH. The
existing private key remains there. Signing checks the APK package, version, input
hash, certificate and 16KB alignment. Password commands return secrets only into
process memory; command output is never logged. The signed APK is returned before
its receipt is atomically exposed. Use a dedicated, trusted SSH account: write access
to this queue authorizes signing for the configured application.

The publisher independently verifies the APK, certificate and metadata. It uploads
into a unique staging directory under the Hub's `apps/android`. A filesystem lock
serializes publication: lower versions and same-version/different-hash packages are
rejected. A complete content-addressed APK is flushed before atomically replacing
`latest.json`. Previous APKs are retained. Build, signing or upload failure leaves
the existing manifest and package usable. Never edit the manifest manually to
force a downgrade.

## Build host setup

Install Python 3.11+, Git, GitHub CLI with repository status-write permission, the
project's Java/Android SDK and SSH access. Use the existing authenticated Hub upload
entry. Copy `android_release_worker.py` into
`~/.local/lib/persona-android-release/` and the service/timer files into
`~/.config/systemd/user/`. Set the service's PATH to include the required tools.
Create `~/.config/persona/android-release.json` with mode 0600:

```json
{
  "repository": "owner/repository",
  "git_url": "https://github.com/owner/repository.git",
  "branch": "main",
  "checkout": "/absolute/project/.claude/worktrees/automation-android-release",
  "state_directory": "/absolute/private/android-release",
  "signing_queue": "/absolute/private/android-release/signing",
  "application_id": "com.example.app",
  "certificate_sha256": "<existing certificate SHA-256>",
  "build_environment": {"ANDROID_HOME": "/absolute/sdk", "JAVA_HOME": "/absolute/jdk"},
  "hub_ssh": "hub",
  "remote_directory": "/absolute/runtime/apps/android",
  "verify_command": ["/absolute/verify-published-apk"]
}
```

`verify_command` receives the local signature receipt path as its last argument.
It must verify authenticated HTTPS metadata and full APK download, reject
unauthenticated access, compare the source/version/hash, and verify the original
signing certificate. It must exit nonzero on failure. This adapter is local because
its credentials and trust setup belong to the installation. `ssh_command` optionally
sets an argv array for the existing pinned SSH adapter (arguments are `--`, target,
remote command); the default is ordinary SSH. Never disable host-key or TLS checks.

After checking the configuration, enable the actual worker:

```sh
systemctl --user daemon-reload
systemctl --user enable --now persona-android-release.timer
systemctl --user start persona-android-release.service
systemctl --user status persona-android-release.timer
```

Keep the user service manager running when logged out (for example, with user
lingering configured by the machine administrator). Every attempt has a durable
`runs/<source-sha>/release.log`; successful publication writes `published.json`.
Failures are retried on the next timer cycle, without changing the version or last
usable Hub release. Monitor the `release/android` status and the service journal.

## Signing host setup

Install `android_signing_worker.py`, `android_apk_atomic.py` and
`publish_android_apk.py` together. Configure its `--config` JSON with `ssh_target`,
`remote_queue_dir`, `keystore`, `alias`, `expected_cert_sha256`, `expected_package`,
`lock_file`, `sdk_dir`, and `password_command` (an argv array). An optional
`key_password_command` supports separate key passwords. Use the existing credential
provider; never put passwords or private keys in this repository.

Run this one-shot command every 30 seconds with the host scheduler, including the
required Java/SDK environment and umask 0077. A local file lock prevents overlapping
signing polls. The build host holds its own pipeline lock while waiting for the
signed receipt. Protect queue access and keep SSH host verification enabled.

## Phone behavior and validation

The existing Settings screen checks for updates on resume and offers download and
installation for a newer version. Downloads retain the paired TLS origin,
authentication, redirect restrictions and SHA-256 checks. Installation checks the
package and declared version and rejects versions not newer than the installed app,
including a version that changes during download. Android's PackageInstaller checks
the signing identity and asks the user to confirm. The app explicitly uses
`USER_ACTION_REQUIRED`; this is not silent installation on consumer phones.

Run `python3 -m unittest discover -s scripts/release -p 'test_*android*.py'`
and the Android `AppInstallerTest`, `AppUpdateTransportTest`, `AppUpdateShotsTest`
tests. The screenshot test renders the real Settings screen on the build host; it
is not evidence of a physical-phone installation. Verify the real Hub independently,
and record physical installation evidence separately without bypassing system consent.
