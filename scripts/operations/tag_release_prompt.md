# Version-tag platform publication

## Repeating Claude-loop prompt

Read this complete prompt before acting. This machine publishes only platforms
with an existing private configuration at
`~/.config/persona/tag-release/<platform>.json`. That file is mode 0600 and its
schema is documented in `docs/operations/release.md` ("Private version-tag
consumers").

Run `python3 <source>/scripts/operations/tag_release.py --config <private-config> --once`.
Use the installed copy selected by that machine's private configuration, not a
branch you happen to have checked out. The script discovers every remote `v*`
tag, skips a platform/tag pair whose receipt is already complete, and performs
all source, lock, and asset checks. If it reports missing prerequisites or a
failed command, preserve the failure and report it; do not bypass the check or
invent an artifact.

For each configured platform, independently report its exact tag and whether it
completed, is still processing, or is blocked. Do not create or move a tag. Do
not merge a release PR merely because a tag exists. If no private platform
configuration exists, do nothing and report that this machine has no tag
publisher. Never read unrelated credentials to improvise one.

The following rules apply to the optional per-platform `agent` command, when
the private configuration uses an Agent to choose the platform's existing
publication steps. The coordinator invokes it once per new tag, not every poll.


The environment identifies exactly one request in repository `<owner>/<name>`:

- `PERSONA_RELEASE_TAG`: an existing immutable `v*` tag, never a branch.
- `PERSONA_RELEASE_COMMIT`: the full commit resolved from that tag.
- `PERSONA_RELEASE_VERSION`: the tag without its leading `v`.
- `PERSONA_RELEASE_REPOSITORY`: the public `owner/name` repository.
- `PERSONA_RELEASE_PLATFORM`: this machine's single configured platform.
- `PERSONA_RELEASE_WORKTREE`: the clean, detached checkout of that commit.
- `PERSONA_RELEASE_COMMAND`: the private argv publication command.
- `PERSONA_RELEASE_RECEIPT`: the private artifact receipt path, if configured.

Use the existing credentials and configuration on this machine. Never copy
keys, tokens, account identifiers, or private endpoint configuration to GitHub
Actions, the public repository, issues, pull requests, or logs. Do not run a
secret-discovery command for a generic release task. Do not create, move, or
delete a public tag, and do not publish a different branch or a later commit.

Choose only the publication steps already supported for this platform. Reuse
its existing release lock and resumable build receipts. Keep platform signing,
store publication, and GitHub asset upload as distinct results; one must not be
reported as another. Missing configuration or a failed signature/publication
check is an explicit failure, never a successful skipped platform.

For `ios`, use the existing resumable TestFlight action and verify the exact
commit's build is VALID and in the Internal group. The Watch build is embedded
in the iOS archive. The frozen native Mac app is not restarted by a generic
release; desktop support is the web artifact plus Persona Link.

For `android`, use the existing Android signing/publication pipeline. Never
publish a debug or unsigned APK as a Release. Verify package, version code,
source commit, and signing certificate; upload only a verified signed APK.
Without the private signing and publication configuration, stop with an
explicit prerequisite error.

For `hub`, use the existing publication script and its deployment lock. Publish
its verified multi-platform binary, web, node, and connector downloads under
this version tag. Do not manually deploy a Worker or bypass its live-readback
checks.

For `web`, use the existing hub/web publication receipt. A successful artifact
upload does not by itself establish that a separate production deployment is
healthy.

When the private command produces artifacts, write a mode-0600 JSON receipt at
`PERSONA_RELEASE_RECEIPT` only after all platform checks for those artifacts
pass. Store-only publication (for example TestFlight) uses the same receipt
with `"store_verified": true` and an empty `assets` list after checking the
exact build's platform state. Never upload a development archive merely to
satisfy an asset requirement. The receipt schema is:

```json
{
  "commit": "<exact PERSONA_RELEASE_COMMIT>",
  "store_verified": true,
  "assets": [
    {
      "path": "/absolute/path/to/verified-artifact",
      "name": "Persona-<version>-<platform>.<extension>",
      "sha256": "<lowercase SHA-256 of the file>"
    }
  ]
}
```

Return with an explicit result for this platform and commit. The caller uploads
the receipt's files to the existing GitHub Release and verifies their remote
SHA-256 digests. Do not claim all platforms are complete: each private worker
owns only its configured platform.
