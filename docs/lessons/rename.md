# Lessons: a naming cutover without compatibility

The #277 requirement changed on 2026-11-06: Persona is the only source/runtime spelling, without command aliases, fallback reads or dual writes. Keep actual registered application IDs unchanged; all other renamed contracts require a coordinated, approval-gated [cutover](../operations/persona-cutover.md). Earlier compatibility guarantees are not valid for this version.

## Inventory first, replace second, resolve collisions third

Record a full baseline SHA: several worktrees share moving remote refs. Count contents and filenames separately and enumerate identifiers, binaries/services, paths, environment variables, MCP names, display text and filenames. A case-preserving replacement is only the beginning.

Existing canonical symbols collide with former-name aliases. A renamed wrapper may inherit from itself; a renamed helper may recurse into itself; enum cases, manifest receivers, schemes, JSON catalog keys and environment assignments may become duplicates. Remove the obsolete entry, preserve the real implementation and add a negative regression test. In Python, removing a duplicate tuple member must leave a trailing comma; `for key in ("PERSONA_HOME")` iterates characters.

A same-path binary symlink can replace the just-installed binary with a loop. Never retain alias creation after both names have collapsed. State roots now initialize one directory; runtime startup does not move or alias an existing installation. Migration tools must refuse independent destination stores rather than overwrite or merge them. New upstream tests can have the same collision: use an explicit custom `.state-link` fixture to test symlink security, not a second spelling of the canonical root. Keep the real sandbox deny/control assertions and deduplicate configured/default root inventories.

## Audit generated names against the public-name policy

A brand replacement can accidentally construct a private repository name even when each original token looked generic. Use canonical extension terminology for UI-test environment variables and mock flags, and check code, filenames, localization keys, PR text and commit messages together. Marketplace directory lookups use only `persona-extensions.json`; do not fold an obsolete filename into a duplicated canonical fallback. Rebuild extension-skill digest markers under `.persona-extension-skills` without deleting user-owned skills.

## Brand names can also identify distinct avatars

Mochi already has ID `persona`. Visor must use `persona-visor`, not the same ID. Preserve both drawings and use the same IDs in schema, server, web, Apple and Android. Schema ownership belongs to one integrator; regenerate every language centrally and check drift. Migrate persisted Visor identifiers only in typed look/extension/config fields, not arbitrary messages or registered app IDs.

## Compile-time and operating-system identities differ

Targets, modules, Info.plist principal classes and Swift symbols must agree. A text scan does not prove AppIntents metadata, push-to-start ActivityAttributes or notification extensions load. Mac builds and signed artifact checks are still required before deploying or releasing; Linux static checks must not be reported as native builds.

On Android, previous-brand wrappers, duplicate intent filters and Gradle fallbacks are removed. Keep the Health Connect `ViewPermissionUsageActivity` alias: Android uses it for permission access and it is not a brand compatibility entry. Existing queued workers and notifications may need cancellation/recreation during the cutover; compilation alone does not prove their upgrade behavior.

## Cut over protocols and data together

MCP server names change fully qualified tool names; update prompts, allowed-tool lists and permission settings in the same maintenance window. Service roots, config locations, database settings, socket locations, environment names and headers must all agree. Previous short assistant links are deliberately invalid, including links already sent in messages/notifications/documents. Do not add a compatibility reader to make a test pass.

Back up data and credentials privately, stop all old entry points (including socket activation), apply explicit one-time migrations, verify the new version, and retain a reversible journal and old binaries. Never globally replace user-owned transcript/message contents. A source merge is not permission to deploy.

## Shared wire fixtures have two sides

A derived wire case stores both `input` and `expected.fields`. The hub's golden `-update` refreshes the captured input, not every client's expected-field map. When `proto.Features` gains a capability, update the matching documented `/features` expectation too and run the Apple/Android/web consumers; a server-only green test can miss that stale expectation. Keep exact assertions rather than filtering out the new capability.

## Verify every test temporary root

An isolated `HOME` and `TMPDIR` were not enough on the local Go toolchain: `testing.T.TempDir()` still created fixtures beneath `GOTMPDIR`, even while `os.TempDir()` reported the isolated root. Set and verify `GOTMPDIR`, `TMPDIR`, `TMP` and `TEMP` as well as `HOME`/`PERSONA_HOME`. Every invocation needs a new, unique root, including the final full run after targeted checks; reuse only build/module caches and the isolated parent database configuration. Reusing HOME carried stale attachment sockets, synthetic health reports and spool rows into the next run. Per-process roots also do not isolate tests within one command: a fixture using an environment-aware identity helper must set its own `PERSONA_HOME`, not merely pass a new `home` argument, or another test can see its synthetic health report. Preserve failed sandboxes privately rather than resetting a path that a previous child may still reference. Put fixtures outside real user-home ancestors: a security test that intentionally scans parent directories can otherwise find a real `.codex/config.toml`. Correct the external runner's paths, never weaken the product's ancestor scan or hide the failure with a source overlay.

A runner-wide `umask 077` also changes fixture creation: a requested `0640` seed becomes `0600` before a mode-preservation test begins. Keep the parent runner restrictive, logs explicitly `0600`, and test roots `0700`; use the fixture's expected default umask only in the isolated test child. Never change real profile permissions, relax the preservation assertion, or report the original failed run as passed.

## Preserve meaningful checks

JSON baseline paths may use escaped slashes; decode paths and retain every metric and threshold. Do not raise token baselines or remove source checks. Update old compatibility tests to assert canonical behavior, collision safety and rejection of obsolete inputs; merely changing an expected token can hide recursive aliases. Every full test invocation must start with a fresh outer HOME as well as fixture-specific overrides. Collapsing old/new `Setenv` calls can clear the sandbox root and make a CLI contact an existing profile; compile success does not catch this. Audit all duplicated environment assignments before running mutation tests, and retain an external private incident record if isolation fails. Never perform automatic live cleanup. Record exact test commands, skips and unavailable platforms. After rebasing, repeat the scan because unrelated merges can reintroduce previous-brand files.

## A helper that makes what is missing turns a rename into an empty store (2026-11-07, #6149)

`scripts/deploy/db.sh` ran on every hub deploy: if the container was missing, it created one with a fresh volume and wrote that container's address into `hub.json`. Renaming its container from `jarvis-postgres` to `persona-postgres` made every hub set up before the rename look like a new one. The next deploy made an empty `persona-postgres` and would have pointed `hub.json` at it. One hub was saved only because the old container still held port 18661, so `docker run` failed before `hub.json` was written. It left a Created container and an empty volume behind.

- A helper that provisions on absence must decide from what is **configured**, not from today's names. db.sh now keeps an existing `database_url`, starts only the container made with that URL's user, and creates nothing while any database container, volume or URL exists.
- Grep a rename for every "create if missing" path: container / volume / unit / directory / config-file names that a deploy, setup or start step looks up and then creates. Each one needs a test with the old resource present and the new name absent.
- Values derived from the old name break the same way: a user or database in a URL, the `<db>_test` the test helper derives. Read them from the configured URL rather than from constants.


## A stored working folder outlives a moved data directory (2026-11-07, #6299)

The node keeps each conversation's working folder in the hub's database (`node_sessions.cwd`) and resumes a conversation there when the request names none. `spawnClaude` runs `MkdirAll` on that folder. Rehearsing the cutover on a copy of a real database showed every stored row of the main conversation naming the folder inside the old data directory, so the first resume after the move would have made an empty folder at the old path and run the main conversation there, without its instructions, skills or memory, and with no error.

- A stored path that is gone is looked for by its name in the current data directory, else the default; it is never created again where it was (`storedCwd`).
- Before a cutover, scan the database for absolute paths into the directory being moved (`node_sessions`, settings, payloads) and decide for each column whether it is read again. Historical payloads can stay; a value the code acts on cannot.

## Moving the data directory moves the assistant's conversation history (2026-11-07, #164)

Claude Code names each folder's conversation directory after the folder's absolute path. The cutover moved `~/.<previous-name>` to `~/.persona`, so the assistant, now running in `~/.persona/assistant`, looked in an empty project directory; its earlier conversations were all still in the one for the old path, and the user saw every earlier conversation gone. The runbook had moved the root, the database and the config, but no harness state outside the root.

- A rename that moves a directory agents run in must also move every harness's per-folder state that is keyed by that path: Claude Code's `projects/<key>` in each config directory, and the same for any other agent.
- Move it while nothing writes; don't copy it (next lesson).
- A transcript also records the folder it ran in, and the server reads ownership from that: moving the project directory alone did not bring the conversations back into the assistant's list (#163).

## A copied conversation directory diverges from its original (2026-11-08, #1533)

The runbook copied Claude Code's project directories to the new keys and kept the old ones. The stored sessions still named folders in the old data directory, and an old-name alias made those folders resolve, so the restarted main conversation kept appending to the old copy while new sessions wrote to the new one. Readers picked different copies: the web took its items from one file and its revision from the other, and a sent message stayed "sending" (#1423).

- Move per-folder state with its folder; when both keys exist, merge and refuse a file that differs, never pick one silently. `persona-cutover.py --claude-config-dir` does this.
- Rewrite every stored path that is acted on in the same window (`node_sessions.cwd`): an alias hides a stale path instead of failing.
