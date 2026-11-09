# Persona-only cutover (#277)

## Authorization and release boundary

This is a **one-time, coordinated maintenance operation**, not runtime compatibility. The final installation has only Persona names: no previous command aliases, environment fallbacks, dual writes, directory aliases, or parallel old/new services. It supersedes the compatibility approach in [rename.md](rename.md) for this cutover. The historical document is not permission to run its compatibility commands.

**Production operations are currently prohibited.** The authorized work is source text and sandbox rehearsals only; Apple work is text-only at this stage. A successful Linux rehearsal, a merged change, or an `--approve` flag does not lift that restriction. Mac verification must finish, then the user must explicitly approve the production maintenance window. No deployment, service restart, live database connection, client release, or TestFlight upload is authorized by this document.

Freeze one commit and a complete artifact set for the hub, every manager, every node/link/runtime, relay, web, iOS, watch, Mac, Android, and extensions before scheduling the window. Do not use a blue-green switch: its old and new processes overlap. Do not bring an unconverted machine or old client back into the fleet. An unreachable computer/device delays acceptance; it is not silently left on the old release.

The literal previous product name is deliberately not embedded in this document or the maintenance tool. Supply it through the required `--source-name` parameter; `<previous-name>` denotes that input below. This is not a runtime fallback, and the parameter must never become a new product alias. The CLI rejects the canonical `persona` source and uppercase `PERSONA` on every action, including SQL emission, so Mochi cannot accidentally be treated as the previous Visor ID.

## Inventory before the window

Use machine IDs from configuration, not hostnames, in the private maintenance worksheet. Record each machine's OS, service owner/domain, actual roots (including custom roots and XDG overrides), binaries and checksums, role, service definitions/drop-ins, external environment files, runtime integrations, database/volume associations, and rollback artifact. Store the worksheet and logs outside the public repository with private permissions.

Also list each machine's agent harness config directories (`~/.claude` and every `CLAUDE_CONFIG_DIR` a running agent or launcher uses) and, in each, the conversation project directories whose name encodes a folder under the root being moved (see "Agent conversation history" below). The filesystem tool does not touch them.

Inspect deployed definitions rather than guessing from new names. Inputs to this plan are [server.md](../server/server.md), [network.md](network.md), [rename.md](rename.md), `scripts/deploy/hub-systemd.sh`, `node-launchd.sh`, `link-systemd.sh`, `db.sh`, and the connector's platform installers. Existing deployment helpers may restart services or initialize a missing store; **do not run them during inventory or before data has been moved**. Review the final helpers at the frozen commit before any later approved use.

Target identities from the implementation (WeChat is now a marketplace extension, not a required server artifact;
its old executable filename remains recognized by the historical filesystem tool):

| Layer | Persona-only target | Manual work |
| --- | --- | --- |
| Runtime / build configuration | `~/.persona`, `~/.config/persona/config.json` | Custom roots, other users, XDG locations, network mounts and Windows ACLs require a separate reviewed path plan. |
| Programs | `personad` (including the `node` subcommand), `persona-link` | Install matching verified artifacts; remove previous aliases from PATH and package wrappers. |
| Linux | `personad.service`, `personad.socket`, `personad-node.service`, `persona-link.service` | Recreate units, enabled links, timers, drop-ins and environment files; preserve actual listen addresses and owner. |
| Mac | `com.persona.hub`, `com.persona.node` | Boot out actual prior labels/domains; update plist filename, Label, ProgramArguments, EnvironmentVariables, working/log paths and login helpers. Stock code has no separate `com.persona.link` LaunchAgent; inventory custom connector jobs by their actual Label. |
| Windows | Scheduled task `Persona Link` | Export and disable/end the actual prior task; update XML action, arguments, environment, working directory, triggers and account. Keep the new task disabled until the barrier. |
| Database infrastructure | Persona-only container/project/volume, database and role names | Preserve data and credentials; the filesystem tool does not operate Docker or rename database infrastructure. |
| Agent integration | MCP server `persona`, full tools `mcp__persona__…`, `PERSONA_*` | Update each approved harness config and restart sessions only after the barrier. |

Real application registrations are not product copy. Keep configured Bundle IDs, applicationId, App Store records, APNs topics, App Groups, Keychain groups, signing profiles, external OAuth registrations and permission identities unchanged. The Android Health Connect `ViewPermissionUsageActivity` alias is an explicitly authorized system-permission exception, not a previous-brand alias. Do not replace real IDs in config or database rows.

## Impact: this is a breaking replacement

- All protocols, headers, runtime subprotocols, message/context tags, API field names, serialized values and MCP names must match across all peers. There is no mixed-version negotiation or reader fallback in the final release. Prove incompatible old inputs are rejected in each client/runtime's tests.
- Only `/persona` routes remain. Historical `/j` links in user messages, notifications, documents, bookmarks and shared pages **stop working**. The previous URL scheme also stops working. Do not repair this by rewriting conversations or all stored URLs. Users recreate needed links with the new client; pending old notifications may need to be dismissed and regenerated.
- The old built-in **Visor** ID becomes `persona-visor` once, in typed appearance-ID fields. Other previous-namespace built-in IDs (Circuit, Pixel, Facet and Blob) become `persona-circuit`, `persona-pixel`, `persona-facet` and `persona-blob`; keep any `/revision` suffix. **Mochi already uses `persona` and stays `persona`**. `design: "visor"` is a drawing selector, not the old product ID, and stays unchanged. Do not collapse both avatars into `persona` during the naming migration. These five historical built-in choices have since been retired (#6219): the look API displays Mochi for them, while their migrated IDs remain reserved to prevent extension collisions.
- Rename persisted preference/cache keys together with their readers and writers. Export approved settings before conversion; perform an explicit one-time key move or deliberate cache invalidation, never dual writing. Clear/rebuild disposable ChatCache, StateCache, avatar caches, runtime inventories and stale extension reports only after saving necessary state. Extension-skill digests now use `.persona-extension-skills`: preserve user-owned skills and invalidate old digest markers as disposable cache; never keep an old marker reader. Keep unsent drafts, outbox actions and approvals until they have an explicit migration decision; do not replay them automatically.
- iOS/watch/Mac preferences, App Group files, Keychain data, archived Swift types, Shortcuts/App Intents, ActivityKit attributes/widget kinds and Android component/serialization/WorkManager identities cannot be repaired by changing a hub directory. Verify a real upgrade under the unchanged registration IDs, pair phone/watch versions, re-register controls/shortcuts if required, and end/recreate old Live Activities with the approved client. The maintenance tool has no access to client preferences and claims no automatic migration there.
- Cryptographic domains change independently of file paths: push payload derivation now uses `persona push v1`, and relay grants authenticate with `persona relay grant v1`. Already sealed push payloads cannot be opened by the new client: drain/dismiss old notifications during maintenance and regenerate required pushes from their original events after the whole fleet matches. Do not rewrite historical messages or retain an old-domain reader. Existing relay grants may remain stored for renewal, but are not usable under the new AAD; see the mandatory renewal acceptance below. Ordinary extension/mail/store AES-GCM boxes use no AAD, and the knowledge-base owner domain already uses Persona, so those ciphertexts/keys do not need re-sealing merely for the rename.
- Shells, already-running Agents, MCP sessions and third-party extension processes retain inherited names and paths. Drain them and recreate them after conversion. Do not source shell configuration to discover values; treat it as inert text. User-written prompts and real chat content are not a global replacement target.

## Automated filesystem portion

Run `python3 -I scripts/migrate/persona-cutover.py --help` with Python 3.9 or newer. Python's isolated mode avoids importing code from a supplied data directory. The tool never invokes a shell, reads shell configuration for execution, starts a service, contacts a remote host, or connects to a database.

Default behavior is **plan only**, with no files or directories created:

```sh
python3 -I scripts/migrate/persona-cutover.py \
  --source-name '<previous-name>' --home '<explicit home>'
```

Automation is intentionally narrow:

1. Move a real `~/.<previous-name>` to `~/.persona` and a real `~/.config/<previous-name>` to `~/.config/persona`, using an atomic exclusive rename. Missing stores are not initialized. Cross-filesystem rename fails rather than copying silently.
2. For an old real directory with a new symlink pointing exactly to it, remove the new alias and move the real directory. For a new real directory with an old symlink pointing exactly to it, remove the old alias. Journal the original link for rollback. Two real directories, even empty ones, are conflicts. External/dangling/cyclic aliases and symlink ancestors are refused; no copying, merging or overwriting occurs.
3. Rename previous `personad`-equivalent, link and WeChat **regular filenames** in the moved runtime's `bin/` to `personad`, `persona-link` and `personad-wechat` (including `.exe` variants). The node is a `personad node` invocation, not a separately required node artifact. It does **not** convert binary bytes or install a new version. Both filenames existing, a binary symlink, or a hard-linked managed file is a refusal. Preserve these old executable objects outside active paths before installing the frozen new artifacts.
4. Make runtime/config roots 0700 and recognized credential files 0600: hub/manager/link/relay/tailcat/APNs config, the default `relay.secret`, machine UID, encryption/handle keys, TLS identities, launcher settings and build config. Other extension secrets and Windows ACLs need manual inspection. Never regenerate a key to fix a permission problem.
5. In hub/manager/build JSON, change only `plugins[].id`, `extensions[].id`, and `look.id`. Extension IDs with the input product namespace followed by `-` or `d-` become the corresponding Persona namespace; unrelated IDs remain unchanged. Extension config contents, personal names, notes, messages, credentials and app registration IDs are untouched. Duplicate resulting IDs are refused.
6. Move `personas/<old Visor ID>` to `personas/persona-visor`, map other previous-namespace avatar directories to the Persona namespace, edit only `persona.json`'s `id` and `app_icon_fallback`, and leave Mochi unchanged. Move namespace-qualified extension `installed/`, `data/` and `tools/` ID directories without rewriting their data. Installed extension manifests, signatures, dependency declarations and permission digests still require reviewed replacement/re-signing and permission confirmation before enabling them. No signature is forged by the tool.

7. With repeatable `--managed-harness-json '<explicit JSON path>'`, inspect only an operator-approved existing harness JSON below the selected home. Move the exact `mcpServers[<previous-name>]` key to `mcpServers.persona`; move controlled old uppercase namespace keys in root `env` and that MCP server's `env` to `PERSONA_*`; convert the leading `mcp__<previous-name>__` namespace in `permissions.allow`, `ask` and `deny` and exact product-name tokens in the tool identifier (`persona_settings`, `ask_persona`). Keep category, wildcard, argument constraints, order and every argument value unchanged. MCP/env target-key collisions and exact rule collisions are refused, never merged. Unrecognized objects, symlink paths and duplicate input paths are refused. This does not execute the config or edit command strings, tokens, messages or app IDs. Exact command/root **values** remain a reviewed manual step before start. A harness overlapping an automatic config edit must be converted in a separate stopped-services transaction.
8. With repeatable `--claude-config-dir '<explicit config dir>'`, move Claude Code's conversation directories for folders under the previous root (`<config dir>/projects/<key>`, see "Agent conversation history") to the keys of the same folders under `~/.persona`, as journaled renames. Where the new key already holds conversations, merge: move each entry it lacks, recurse into directories present in both, then retire what is left of the previous directory (only byte-identical copies) into the private journal, so no second copy stays behind. A file that differs in the two directories is refused; the tool never picks one. An old key that is a symlink to the new key is removed. Keys a sibling of the root shares (`~/.<previous-name>-tools/x` has the same key as `~/.<previous-name>/tools-x`), keys Claude Code shortens (over 200 characters) and config directories inside a moved root are refused.

Example plan, still read-only:

```sh
python3 -I scripts/migrate/persona-cutover.py \
  --source-name '<previous-name>' --home '<explicit home>' \
  --managed-harness-json '<explicit home>/.agent/settings.json'
```

Add that same explicit path option to the approved apply invocation to perform the typed edits with private byte backups and journal recovery. Never use this example path to infer where a real harness is installed; inventory its actual config directory and each project's settings.

Application requires explicit assertions, **only after a future production approval or inside a sandbox**:

```sh
python3 -I scripts/migrate/persona-cutover.py \
  --source-name '<previous-name>' --home '<explicit home>' \
  --apply --approve --services-stopped --backup-verified \
  --journal '<explicit home>/.persona-cutover-<unique id>'
```

The tool holds an exclusive per-home lock, preflights every supported action, backs up binary/edited-JSON bytes, and writes a reversible intent journal before each mutation. Journal directory is 0700; journal and backup files are 0600. It does not make a complete copy of the knowledge base or database: `--backup-verified` asserts independent backups already exist. No service-stop check can be inferred from this flag. The operator must stop all writers, including background Agents and extension children.

Apply is not auto-resumed after a failure. Inspect the journal and roll back or repair under the stopped-services barrier. Atomic journal writes and intent states permit interrupted-operation recovery; this is not a promise of filesystem/power-loss recovery without the independent snapshot. A crash can leave `.persona-cutover.lock`, `rollback.lock`, or an uncommitted private temporary file. Confirm no migration process remains, inspect the private journal, then explicitly remove only that transaction's stale lock/temp before recovery. Never bypass a live lock.

## Exact database migration

Server persistence has two migration sets and may use separate hub/manager databases. Run the transaction **once per affected database**, not just once on the hub machine. The tool emits SQL for the existing `public` schema, checks expected column types and complete registry membership, and skips absent role tables. An unexpected schema is a stop condition, not an excuse to point the script at a different database.

**Which database holds which table.** `hub_settings` and `devices` are the hub's (`migrations/hub/`); `plugins`, `plugin_secrets`, `plugin_audit`, `settings` and `node_sessions` are the built-in manager's (`migrations/manager/`). Once the built-in manager has moved its data into a database of its own ([managers.md](../assistant/managers.md) ("The built-in manager's database")), those five live there: a second database on the hub's Postgres server named by `database_name` in `managers/assistant/manager.json` under the data directory (`<hub database>_manager` by default), or the server `database_url` there names. The hub's database keeps its copy of the five as they were at the move, for a build from before it, until `personad assistant drop-shared-tables --yes`; nothing reads that copy.

So run the transaction against the hub's database **and** against the manager's own database. The SQL tells them apart by `manager_moved` (`direction='out'` in the hub's database after the move): there it leaves the manager tables as they are, migrates `hub_settings` and `devices`, and ends with `committed for the hub tables only` and a reminder to run it against the manager's database too. `supported ID fields and stored session folders verified` is printed only by a run that migrated the manager tables. Run against the frozen copy alone, an earlier version of this transaction succeeded and verified every field while the manager's data kept the previous IDs and folders (#1686). Check the stored folders where they are read:

```sh
psql -X --no-password --dbname='service=persona-cutover-manager' \
  -c "SELECT count(*) FROM node_sessions WHERE cwd LIKE '%/.<previous-name>/%' OR cwd LIKE '%/.<previous-name>'"
```

The server lane compared original/new SQL DDL and Store queries and found no product-brand table/column/index/constraint rename to apply. `Session.started_by_persona` is a derived wire JSON field, not a database column; the stored node-session field is `node_sessions.started_at` (`migrations/manager/0418_node_sessions_started.sql`). `schema_migrations` stores version/name/part, not a source checksum. Do not invent an `ALTER started_by` or guess schema identifiers from a protocol rename. Fresh-database tests alone are still not upgrade acceptance: rehearse the real original migrations, the typed cutover transaction, and reads through the new Store in an isolated migrated database. The SQL's column/type checks do not replace that Store upgrade test.

| Existing table / file | Exact supported data | Deliberately untouched |
| --- | --- | --- |
| `plugins` | Namespace-qualified `id`, `manifest.id`, `manifest.contributes.looks[].id` / `app_icon_fallback` for typed previous-namespace built-in look IDs | `config`, user text, version, enabled state, grants, remote configuration and dates |
| `plugin_secrets` | `plugin` ownership key | `name`, sealed ciphertext and encryption key |
| `plugin_audit` | `plugin` ownership key | Historical `detail`, actor and other user data |
| `settings`, `hub_settings` | For `key='look'`, `value.id`; delete `key='hub.extensions_report'` as a disposable stale cache | Other settings and arbitrary JSON fields |
| `node_sessions` | `cwd` equal to or below `<home>/.<previous-name>` for each listed home, rewritten to the same folder under `<home>/.persona` | Every other folder, session IDs, panes, launchers, tasks and dates; Windows folders |
| `devices` | `prefs.look.package`: exact Visor ID or previous-namespace built-in IDs, preserving `/revision` | `look.design`, push tokens/topics, device IDs, credentials, opaque `relay_grant` and other preferences; preserving a grant is not proof it works after the AAD change |
| `hub.json`, `manager.json`, build config | The typed fields listed in the filesystem section | Secret values, personal values, real registrations and arbitrary path strings |
| User runtime directory | Roots, known credentials, avatar manifest IDs, extension ID directories | Knowledge-base messages, external application data, arbitrary JSON and shell files |

A node resumes a stored session in the folder `node_sessions.cwd` records. Left under the previous root, the main conversation restarts in the old folder (through any alias left behind) and Claude Code writes its transcript under the old folder's key again (#1533). The transaction rewrites the folders below `<home>/.<previous-name>` for `--home` as given and for each repeatable `--session-home '<home>'`: list the home of every machine whose sessions are stored in that database (the manager's own, once it moved its data), as the node there spells it (`$HOME`). Homes must be absolute POSIX paths; Windows folders are rewritten by hand.

```sh
python3 -I scripts/migrate/persona-cutover.py \
  --source-name '<previous-name>' --home '<hub home>' \
  --session-home '<another machine home>' --print-database-sql
```

### Mandatory relay-renewal acceptance

`devices.relay_grant` and client-stored relay grants are base64url nonce+ciphertext authenticated under the relay's AAD. The new relay cannot open grants sealed with the previous AAD even if their bytes, APNs topic and encryption key are unchanged. The maintenance SQL and local fixture conversion preserve opaque grants unchanged; they do not clear, re-seal, validate or print them. Nonempty grants do not block the preceding typed-ID transaction, but their successful renewal is a **mandatory post-start acceptance condition**.

The existing `postRelay` path in `server/internal/hub/push_transport.go` renews an invalid grant after an HTTP 401: `renewGrant` posts the saved APNs token/environment/topic to the matching relay's `/v1/register`, stores the fresh grant, then retries the push. This is generic credential renewal, not an old-brand/AAD fallback: the new relay still refuses the old ciphertext. After the global barrier, use only the new relay/hub/clients and verify that this 401→register→persist→successful retry path works, or have the new client re-register and deliver a fresh grant explicitly. Verify persistence, subsequent push success without another invalid-grant failure, and that a stale client does not overwrite the renewed credential. `TestRelayRenewsAnInvalidGrant` is the server's isolated renewal regression; actual push delivery still needs its own approved acceptance.

Keep normal relay delivery gated until controlled verification succeeds; if token/topic data is unavailable, the relay is unreachable, renewal fails, or any push is lost, cutover acceptance remains blocked. Preserve APNs tokens/topics, trusted pairing and the relay's **own** 32-byte grant secret: the implementation defaults to `relay.secret`, or uses `relay.json.secret_path`. It is not necessarily the hub's `secret.key`. Do not add old-AAD decoding, clear existing grants, or implement offline re-sealing for this migration. Restoring the old binary requires the old grants/secret as part of the whole-fleet rollback. Ordinary extension secret ciphertext with unchanged AES-GCM domain remains preserved by the registry transaction.

For a shared/external relay, record its owner and upgrade dependencies explicitly. A private fleet's cutover does not authorize replacing a relay used by other old clients; coordinate that complete dependent-client set or keep relay disabled until it can be migrated. The publisher/relay is part of the protocol barrier, not an unchanged third-party endpoint.

Unknown setting keys, extension-specific ID fields, environment names, custom directory paths, cached schema fields and client preferences are **manual blockers** until their owners define an exact mapping and test it. This tool does not claim to migrate every arbitrary JSON value. Do not use global substitution on a dump, user messages, prompts, permissions, accounts or a home directory.

Generate the reviewed transaction without connecting or writing a file:

```sh
python3 -I scripts/migrate/persona-cutover.py \
  --source-name '<previous-name>' --home '<sandbox home>' --print-database-sql
```

After future explicit authorization, save that output privately (use `umask 077`) and have the operator execute it using an already reviewed libpq service, not credentials on the command line:

```sh
# MANUAL, FUTURE APPROVED WINDOW ONLY: the tool itself never executes this.
psql -X --no-password --dbname='service=persona-cutover' \
  --file='<private reviewed transaction.sql>'
```

The script uses psql quoted-literal binding for the required source name, `ON_ERROR_STOP`, a transaction, bounded lock/statement timeouts and exclusive table locks. It refuses an old/new registry-ID collision. Because the existing secret foreign key has `ON DELETE CASCADE` without an update cascade, it copies each renamed parent record, moves secret/audit ownership, then deletes the emptied old parent; it never deletes secrets to recreate them. Before COMMIT it verifies all supported fields have zero old IDs and checks sealed secret rows are unchanged. It prints only verification counts, not tokens or configuration contents. A connection failure with unknown commit outcome requires querying the verification fields before retrying; do not infer rollback from a transport error.

For offline JSON rehearsal, place an exported fixture at `<sandbox home>/.persona-cutover-records.json` with `{"format":1,"tables":{…}}`, where table keys and row shapes match the table above. Add `--records '<sandbox home>/.persona-cutover-records.json'` to plan/apply. This changes only the local fixture and journals its original bytes; it is not the live database migration. Stale-cache deletion is performed by SQL, not by the fixture converter.

Database/container names are a separate operator step. The volume holds the built-in manager's database too (`jarvis_manager`, [managers.md](../assistant/managers.md) ("The built-in manager's database")): it moves with the volume, and its `manager.json` keeps its name (`database_name`), so rename it only together with that key. Stop the old Postgres/container before moving its persistent volume; record the Postgres major version and PGDATA mount layout. Docker cannot rename a volume in place: create the target named volume, restore a verified stopped-volume snapshot, preserve ownership/ACLs, and confirm it is the populated volume before starting one database container. Preserve the rollback volume offline. A container rename alone does not change Compose project/volume labels; review Compose resource mappings to prevent it creating an empty second store. Never run `db.sh` against a missing target during migration. With an administrative connection to another database, use reviewed `ALTER DATABASE … RENAME TO …` / `ALTER ROLE … RENAME TO …` for product-owned names and update each affected DSN structurally without changing passwords, ports or unrelated names. These operations are not emitted by the row-migration tool. No old and new Postgres instances may write copies of the same dataset.

## Maintenance-window sequence: every machine

1. **Before downtime:** save the frozen artifacts and the previous binaries with checksums; back up real runtime/config roots (preserving symlinks/modes), service definitions, client preference exports, TLS/encryption keys and all relevant databases. Use a version-compatible `pg_dump -Fc` per database plus `pg_dumpall --globals-only` for cluster roles. Save private outputs with 0600 under a 0700 backup directory. Rehearse restore in a disposable cluster and compare row counts/identity values. A dump file existing is not backup verification.
2. **Drain:** pause updates, scheduled work and outgoing integrations. Resolve or explicitly preserve pending sends/approvals/drafts. Close all clients and stop extension/Agent/harness children that can write configuration or transcripts. Record a maintenance start deadline and a rollback deadline; every wait has a bounded timeout and a verdict.
3. **Stop on each machine:** Linux disables/stops the actual old socket **as well as** service, node/link units, timers and other activators. Stop socket first to prevent an incoming request reactivating the hub; wait for processes to exit. Mac boots out old LaunchAgents/Daemons in the actual GUI/user/system domain and disables login items. Windows disables and ends the old task and stops its node/connector child processes. Docker disables restart policies and stops hub/managers/extensions; postpone database stop until row migration/backup is complete. Verify process trees, open files and listening ports, not only the service-manager state. Do not use `restart-idle` as this stopping barrier.
4. **Migrate data:** run filesystem plan/apply per default-home machine. Resolve unsupported custom paths manually without merging. Run the reviewed database transaction on every affected stopped-writer database, inspect its zero-old-ID counts, then perform approved infrastructure renames. Preserve machine UID, hub ID, pairing credentials, database contents, knowledge base and cryptographic keys. Move the agents' conversation project directories for folders under the moved root to the keys of their new folders in the same apply (`--claude-config-dir`, next section) before anything starts.
5. **Convert configuration as inert data:** update unit/plist/task references, environment files, login shell exports, launchers, hooks, project/user harness settings and MCP registrations. Remove the previous uppercase environment namespace rather than exporting both. Convert exact permission rules `mcp__<previous-name>__<tool>` to `mcp__persona__<tool>`, including brand tokens in tool identifiers (`persona_settings`, `ask_persona`), while preserving allow/ask/deny meaning and argument constraints; never widen a wildcard or grant a new capability. Collapsing previously distinct rules is a refusal requiring an explicit policy review. Update product-managed prompt/tool references and instruction paths, not arbitrary user prose. Export/backup/review settings JSON; do not source/eval shell configuration or run imported task actions. Recreate stopped sessions later to discard inherited values. External extension permission/signature changes require explicit approval.
6. **Install without starting:** put all frozen new artifacts in the single target paths; remove old active filenames/wrappers/aliases and old service definitions/enabled links. Stage new Linux units with matching `Service=`/`Wants=` socket references, correct ListenStream and only new environment keys; `daemon-reload` but do not start. Validate Mac plist text with `plutil -lint`; do not bootstrap it yet. Keep Windows tasks disabled. Reinstall/re-sign verified extension manifests and update owned dependencies/runtime loaders; no mixed old packages remain enabled.
7. **Global barrier:** compare every machine/client's artifact version and mapping worksheet. Check the supported persisted fields and exact product-owned config/key/path namespaces for zero active old identities. Any unreachable/unconverted participant stays blocked. Do not mistake the repository text scan for installed-state verification. Registered application IDs and preserved historical user content are explicitly not rewritten.
8. **Start in order, only after all participants are converted:** one database, matching relay with push delivery still gated, hub, managers, nodes/link/runtime extensions, then the matching clients. Verify fresh relay grants through existing 401 renewal or new-client registration before enabling normal relay delivery; do not replay old-domain sealed payloads. Enable only new activators. Verify auth, HTTPS/WSS and IPv6/direct access with the existing trusted TLS key/pin; moving a certificate must not replace its private key. Confirm one database and one runtime store, stable UID/hub/pairing, expected avatar, Inbox/outbox behavior and MCP permissions. Exercise both ends of every protocol and ensure old routes/names fail rather than silently forwarding.
9. **Accept or roll back:** observe through the agreed deadline. Record actual commands, versions and results, including skipped Mac/real-device checks. Do not delete the offline rollback artifacts until the user accepts the cutover and retention requirements are met.

### Agent conversation history

Claude Code keeps each folder's conversations (transcripts, subagent and tool-result files, `memory/`) in `<config dir>/projects/<key>`, where the key is the folder's absolute path with every character other than ASCII letters and digits replaced by `-`. The assistant runs in `<root>/assistant` and the health space in `<root>/health`, so moving the root moves their key too: after the move Claude Code looks under the key for `~/.persona/assistant`, finds nothing, and the assistant's whole history seems gone, the main conversation included, although every file is still on disk.

On every machine, for every Claude Code config directory listed in the inventory (`~/.claude` and each `CLAUDE_CONFIG_DIR` in use):

1. Make sure nothing writes to them: no agent process holds a transcript in them open, and the assistant is idle.
2. Pass the directory to the filesystem plan and apply with `--claude-config-dir` (automated step 8). The plan lists every move; the apply journals them and `--rollback` moves them back.
3. The tool moves, it does not copy. A copy left under the old key is where a session restarted in the old folder keeps writing, while new sessions write under the new key: the two copies of one conversation diverge, and each reader shows whichever it picks (#1533). For the same reason the database transaction rewrites `node_sessions.cwd`.
4. A refusal (a file that differs in both directories, a sibling's key, a shortened key) means a hand merge: compare the transcripts, keep the one with every line, and run the plan again.

Other agents' per-folder state is not automated; move it the same way by hand.

This is separate from the folder each transcript records it ran in. Transcripts keep their recorded path (user history is not rewritten); the server must still count a conversation recorded in the previous assistant directory as the assistant's (#163).

## Operator command templates (not executed in this task)

These are examples for the **future approved window**, not a script to paste blindly. Fill variables from the private, reviewed worksheet and run only the commands matching that machine's role. Do not put addresses, credentials or actual machine names in an issue/PR.

### Linux native hub / node / connector

```sh
# Inventory first: each variable names an actually installed unit, not a guessed label.
systemctl --user cat "$OLD_SOCKET" "$OLD_HUB" "$OLD_NODE" "$OLD_LINK"
# Stop socket activation before the hub; disable any inventoried timers separately.
systemctl --user disable --now "$OLD_SOCKET"
systemctl --user disable --now "$OLD_HUB" "$OLD_NODE" "$OLD_LINK"
# Verify stopped states and the configured listener; do not kill unrelated owners.
systemctl --user is-active "$OLD_SOCKET" "$OLD_HUB" "$OLD_NODE" "$OLD_LINK"
lsof -nP -iTCP:"$HUB_PORT" -sTCP:LISTEN
```

`is-active` must report inactive/failed for the old set; its nonzero exit is expected during this check. Record the actual result. The filesystem plan/apply above moves the default roots; review every external environment file and update **values** pointing into a moved root as well as variable **names**. The harness JSON tool deliberately does not rewrite command/argument strings or arbitrary path values: stage their exact canonical binary and root paths manually from the reviewed mapping, preserving unrelated values and real app IDs.

After the all-machine barrier, install/review only the new unit definitions and then start the units belonging to this machine:

```sh
systemctl --user daemon-reload
systemctl --user enable --now personad.socket personad.service
systemctl --user enable --now personad-node.service persona-link.service
systemctl --user is-active personad.socket personad.service
systemctl --user status personad-node.service persona-link.service --no-pager
```

A hub-only machine does not enable node/link, and a node-only machine does not enable the hub. Service owners/system-wide installations use their inventoried domain rather than changing every command to root.

### macOS and Windows

```sh
# Mac: use the actual launchd domain and original plist path from the worksheet.
launchctl bootout "$LAUNCH_DOMAIN" "$OLD_PLIST"
plutil -lint "$NEW_PLIST"
# Only after the global barrier:
launchctl bootstrap "$LAUNCH_DOMAIN" "$NEW_PLIST"
launchctl print "$LAUNCH_DOMAIN/$NEW_LABEL"
```

On Windows, export the existing task XML to a private backup, then `Disable-ScheduledTask -TaskName $OldTask` and `Stop-ScheduledTask -TaskName $OldTask`. Inspect/stop its owned child process tree. Register the reviewed new XML under `Persona Link`, keep it disabled through data/config conversion, and enable/start it only after the barrier. `Get-ScheduledTaskInfo -TaskName 'Persona Link'` must confirm its result. Task names/accounts/actions and Windows ACLs come from the private worksheet; never execute an imported XML action merely to discover it.

### Post-start acceptance

```sh
personad version
persona-link version
curl --fail --silent --show-error --max-time 10 "$HUB_URL/api/v1/health"
claude mcp list
```

Capture these outputs privately **when the approved cutover actually runs**. The health version must match the frozen artifact, the MCP registration must be `persona` and expose the new fully qualified tool namespace without expanded grants, and each computer must return its existing UID. Verify TLS/WSS/pins, pairing, both avatars, a read-only phone tool, scheduled tasks and Inbox. Use a dedicated test conversation for checks that send messages. Confirm prior short links fail and relay grant renewal succeeds before normal delivery resumes. This task has not executed any of these live commands; no live success output is claimed.

## Rollback: the entire fleet, never one live participant

Any data loss, identity divergence, protocol rejection among new peers, permission expansion, invalid signature, failed client upgrade or unresolved mapping blocks acceptance. Keep all clients/services/writers stopped while recovering.

The filesystem journal reverses only its own directory/filename/permission/typed-JSON operations:

```sh
python3 -I scripts/migrate/persona-cutover.py \
  --source-name '<previous-name>' --home '<explicit home>' \
  --rollback --approve --services-stopped \
  --journal '<explicit home>/.persona-cutover-<unique id>'
```

It checks identities and refuses to overwrite occupied original paths, modified JSON or replaced binaries. If new artifacts/manual edits were installed, first undo those external steps from verified backups. Preserve the original executable objects (not just their bytes) when staging the new artifacts so the tool can recognize them on rollback. Binary-byte backups are a secondary manual recovery aid, not permission to force the journal. If identities no longer match, use the independent complete snapshot and manual path plan; do not weaken checks or edit the journal to force a merge. Rollback restores original permission modes, even if they were weaker; review private credentials before restarting.

A filesystem rollback **does not undo committed SQL, database/role/volume renames, service changes, signing, or client preference updates**. Restore each database to its verified pre-window snapshot on a stopped isolated target, restore cluster role names/grants and DSNs, switch back to the offline prior volume, restore service/env/MCP/client exports, and reinstall the same previous binary/version on **every** participant. If the prior binary cannot accept systemd `LISTEN_FDS`, keep both new and old socket activation disabled and restore the previous self-listening unit before starting it. Do not blue-green-switch back. The filesystem rollback moves the agent conversation project directories back to their old keys and restores retired copies from the journal; the database snapshot brings back the old `node_sessions.cwd`. Only after the whole fleet is old and consistent may the previous service set be started. Historical directory aliases may exist again in that rollback state; they are not retained in the accepted Persona-only state.

## Reproducible verification and current limits

```sh
python3 -I scripts/test/test_persona_cutover.py -v
```

Tests allocate fresh temporary homes; they cover plan-without-writes, all approval gates, unchanged secret/message/app-ID bytes, both same-directory alias orientations, external/dangling/cyclic aliases, real-directory/binary/config conflicts, exclusive no-clobber rename, hard links, journals/locks, interrupted apply and recovery, typed config edits, Visor versus Mochi, extension data directory moves, conversation project directory moves, merges, refusals and rollback, offline registry/preference fixtures and SQL emission. When `initdb`, `pg_ctl` and `psql` are on PATH, additional tests create their **own disposable Postgres cluster with TCP disabled**, exercise SQL success/retry/conflict/schema failure/hub-only cases and stored session folders, and stop that cluster with a timeout. They never read a live database URL. Missing tools are reported as skips, not SQL execution success.

The server lane also verified `TestPersonaCutoverSQL` and `TestPersonaCutoverRealSchemaStore` in a newly owned isolated Postgres database: all six emitted-SQL fixtures passed (typed IDs, other built-in looks/revisions, unchanged opaque grants, retry/idempotence, conflict rollback, schema rejection and hub-only operation), and the real historical-DDL/new-Store upgrade test passed without replaying historical migrations. This is isolated database acceptance, not production, Mac or real-device acceptance. Local missing-tool skips remain accurately reported by the standalone Python command.

The Linux filesystem rehearsal is not Mac launchd, Windows task/ACL, Docker-volume, live Postgres, TLS, client upgrade, Apple screenshot, or real-device acceptance. The production prohibition remains until Mac verification and explicit user approval; report each missing verification instead of filling it with inference.
