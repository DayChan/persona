# Paseo engine

[Paseo](https://paseo.sh) is a multi-Agent manager: a persistent daemon plus desktop, phone, and web apps. It runs Claude Code, Codex, and various ACP Agents
through their SDKs, without terminals, and has its own workspaces (projects and worktrees). In Persona, Paseo sits alongside herdr, Herdr App, and Orca
as a computer's **engine** (`docs/integrations/engines.md`). When a computer uses Paseo, Agents shows that computer's Paseo Agents; creating chats from the phone,
resuming work, and answering cards all happen in Paseo.

The integration differs from the other three: herdr / Herdr App / Orca are terminal-based, and node reads their panes. Paseo has no terminal; node connects through a **bridge**.
The bridge is a runtime (`docs/integrations/runtimes.md`), speaking Paseo's WebSocket protocol on one side and Persona's runtime protocol on the other. Node runs it itself; no separate installation is needed.

```
 Phone / web / Mac ── hub ── node ──runtime.sock── bridge (run by node, integrations/paseo)──WebSocket── Paseo daemon ── Claude Code / Codex / ACP…
                              └── reads transcripts in ~/.claude (Agents running Claude Code)
```

## How to use it

1. Run Paseo on the computer: open the Paseo desktop App, or run `paseo daemon start`.
2. In the App, open Settings › Machines, select the computer, and change "Engine" to **Paseo**. You can also choose it at the engine step during pairing.
3. Wait a few seconds; the Paseo Agents appear in Agents.

Nothing else needs to be installed. The bridge requires Node.js 22 or newer. Node uses Paseo's own copy: the node used by the CLI version, or the desktop App's binary,
run with `ELECTRON_RUN_AS_NODE=1`. A plain `node` always runs the bridge without that variable, even when Node itself was started from an Electron host (such as an editor's extension host) and inherited it.

## Which sessions appear

- **Listed Agents**: all unarchived Paseo Agents, up to 300. Running Agents and those waiting for you are always included.
  An Agent visible in Paseo's own App is also visible in Persona; archived Agents are omitted.
- **Session IDs**:
  - An Agent running Claude Code (including custom providers with `extends: "claude"`) uses its Claude session ID, the same ID as that chat in herdr or "Not running."
    An Agent created by Paseo itself has no session ID before its first turn, so it is not listed yet.
  - Other Agents use `paseo-<agent id>`.
  - Agents created or taken over through Persona store their ID in the `persona.session` label, which stays unchanged thereafter,
    even if the Claude session changes (`/clear`).
- **Content**:
  - **Agents running Claude Code**: content is read directly from the Claude transcript on that computer, exactly as for herdr sessions: tool cards, sub-Agents, background tasks,
    compaction, and "what it is doing" are all available. The same transcript does not produce an extra "Not running" row. If the transcript has not been written yet (the first turn has just started), or node does not recognize its configuration directory,
    content uses Paseo's timeline until the transcript becomes available, then switches automatically.
  - **Other Agents**: content comes from Paseo's timeline, converted to the App's rendering format. Commands, file reads / writes, diffs, search, web access, sub-Agents, and to-dos use Claude Code tool names.
- **State**:

  | In Paseo | In Persona |
  | --- | --- |
  | Pending request (permission, question, plan) | Needs you |
  | `running`, `initializing` | Working |
  | `idle` / `error`, marked for attention by Paseo (`requiresAttention`: finished / errored and not yet viewed in Paseo) | Unread (Seen once viewed in Persona); the final message for an error is its cause |
  | `idle` / `error`, viewed in Paseo | Seen |
  | Older Paseo (no `requiresAttention`): `idle` after a turn, `error` | Persona tracks Unread / Seen itself |
  | `idle`: has not run yet | Idle |
  | `closed`: not running, process exited | Not running (Paseo reloads it when a message is sent) |

  Read state is one-way, as with herdr (#2831): viewed in Paseo means Seen in Persona; views in Persona are tracked by hub and not written back to Paseo.

- **Spaces and slots** (#2784, #2802): two levels, matching Paseo's sidebar.
  - **A space is a Paseo project**: a repository's main checkout and its worktrees form one group in the App, named after the project.
    The space ID is Paseo's project-record ID: the `projectKey` field in the location information Paseo gives the Agent contains this ID (not the project's own key).
  - **Separate worktree project records are merged into the main repository**: when only a worktree directory is given to Paseo, it creates a separate project record for it
    (named after the worktree directory). These records are grouped with the main repository's project by its path (`checkout.mainRepoRoot`, compared after resolving symlinks).
    If Persona does not yet know the main repository's project (it has no Agents), the record forms its own group, named after the main repository directory.
  - **A slot is a Paseo workspace**: a worktree, or a task in the same directory. Its name is the workspace title; if absent, the branch name; if that is absent too, the directory name.
  - Agents without project information from Paseo keep the existing behavior: their workspace is their space, with no slot.
- **Repository** (#2840): Agents in Git repositories carry `repo_root` (the main repository directory for a worktree, or the checkout itself for a main checkout). "By repository" on all three clients uses it to group
  Paseo worktrees under the main repository. Paseo worktrees live at `~/.paseo/worktrees/<project hash>/<name>`; the path alone does not identify the repository.
- **Streaming**: while the phone is viewing a session, the bridge subscribes to that Agent's timeline and shows text as it is written. Unviewed sessions have no bridge subscription. If a session changes Agents (the original closes and another resumes it), the bridge subscribes to the new one. Non-Claude sessions use the new history to make the phone refetch the whole page.

## Capabilities

| Capability | Paseo | Notes |
| --- | --- | --- |
| List sessions and show state | ✅ | Pushed immediately when Paseo's catalog changes |
| Send messages and images | ✅ | While an Agent is working, messages steer the current turn (Paseo's `steer`, like typing while Claude Code works); resending the same message delivers it only once |
| Interrupt | ✅ | |
| Permission, question, and plan cards | ✅ | Both the phone and Paseo App can answer; the first answer wins. Answering in Paseo removes the phone's card. Tapping "Return to computer" on the phone leaves the answer to the Paseo App |
| Create sessions from the phone | ✅ | Each available Paseo provider is a launcher, with its models, reasoning effort, and permission modes (permission modes: Claude Code's five; for providers on Codex, `bypassPermissions` starts Paseo's `full-access`, and the others leave Paseo's default). Workspace selection: use the workspace explicitly named in the form if it exists in Paseo; otherwise, if the directory is exactly a worktree, use that worktree's workspace (prefer one under the main-repository project, then the most recent), matching Paseo's own directory-based lookup. For a main checkout or ordinary directory, Paseo creates a new workspace under that project |
| Resume a "Not running" Claude session | ✅ | Paseo imports the chat without changing its session ID. If Paseo already has it, reuse it, restoring it first if archived. Reject if another claude process outside Paseo is using it, to prevent two processes writing at once |
| Rename | Backend support | Changes the Agent's title in Paseo. "Rename" on iOS, Android, and web currently appears only for terminal sessions; rename Paseo sessions in Paseo for now |
| Close | Backend support | Archives the Agent in Paseo, stopping it and removing it from the list. Like Rename, the App's Close button currently appears only for terminal sessions |
| Change permission mode and reasoning effort | ✅ | Claude Code's five modes; on Codex, `bypassPermissions` switches to Paseo's `full-access` and a Codex agent in `full-access` reports `bypassPermissions`; reasoning effort includes only the levels supported by the model |
| Keystrokes, screen reading, live terminal, phone takeover | ❌ | Paseo has no terminal |
| Phone tools (`phone_*`, `mail_*`) | ❌ | Not yet injected into Paseo Agents |

Login-shell launchers (those in `~/.zshrc`) still appear in the creation form. If selected, use the Paseo provider with the same name if one exists;
otherwise use Paseo's default provider (its built-in Claude Code provider if enabled, otherwise the first provider running Claude Code).

## How the bridge runs

- **Bridge code**: bundled in personad as runtime package `persona-paseo-runtime` (`internal/runtimepkg`, generated from `integrations/` at build time).
  When the engine switches to Paseo, node writes it beside `runtime.sock` at `runtime-packages/persona-paseo-runtime/<hash>/`, then runs:
  `node bridge.mjs --socket <runtime.sock> --paseo-home <Paseo home>`.
- **Which Node.js**: the first version 22 or newer, in this order:
  1. `PERSONA_NODEJS`
  2. The one used by the Paseo daemon (the process in `paseo.pid`)
  3. `node` on PATH
  4. Common Homebrew, `/usr/local`, volta, and nvm installation locations
- **Exit and shutdown**:
  - After the bridge exits, node runs it again with exponential backoff starting at 1 second, capped at 1 minute. After 5 minutes of stable operation, the interval resets to 1 second.
  - When switching away from this engine, node stops it: SIGTERM to its process group, then SIGKILL after 3 seconds.
  - Node keeps the bridge's stdin open. If node suddenly disappears (is killed), stdin closes and the bridge exits. This avoids leaving an old bridge holding the runtime ID and blocking the new node's bridge.
- **Logs**: bridge output is written line by line to node's log, prefixed with `paseo bridge:`.
- **node.info**: Paseo appears available in the engine list when the bridge is connected, with Paseo's version. It also appears available if the daemon responds even though the bridge has not connected;
  the bridge startup failure reason goes in `note` (displayed below the engine option in the App).

## Finding Paseo

- **Address**: use only the address in `paseo.pid` under Paseo home (`PASEO_HOME`, default `~/.paseo`), just as Paseo's own CLI does. Never fall back to a default port,
  since another home's daemon might be listening there.
  - No `paseo.pid`: Paseo is not running.
  - `paseo.pid` exists but has no address yet: Paseo is starting.
  - Retry later in either case.
- **Password**: if Paseo has a password, log in using `local-credential` in its home (as Paseo's CLI does). With no password, Paseo skips the check.
- **Declared connection capabilities**: the bridge connects as a `cli` client and declares `owned_subscriptions`, receiving only replies to its own requests and messages from its own subscriptions, not broadcasts.
  It subscribes to three things:
  - Agent catalog
  - Pending requests and provider changes
  - Timeline of the Agent being viewed
- **Paseo restart (for example, an upgrade)**: the bridge reconnects every second; measured recovery is about 1.5 seconds. Sessions remain in the list during this time.
- **Paseo stopped for over 30 seconds**: the bridge leaves node, and the engine shows as not detected. Claude Code sessions fall back to "Not running," with transcripts preserved.
  Sending a message then reports that Paseo is not connected; nothing is sent.

## Mapping

- **timeline → items**: seq uses Paseo timeline's seq. Content is assigned to seq as follows:
  - Messages, thinking, notifications: the row where they start.
  - Tool calls: the call uses its start row; the result uses its completion row.
  - Text being written is delivered only as streaming text, becoming an item once complete.
  - If Paseo rewrites history (rewind), `history` changes and the App refetches the whole page.
- **Tool names**:

  | Paseo detail | App rendering |
  | --- | --- |
  | `shell` | `Bash` |
  | `read` | `Read` |
  | `edit` | `Edit` |
  | `write` | `Write` |
  | `search` | `Grep` / `Glob` / `WebSearch` |
  | `fetch` | `WebFetch` |
  | `sub_agent` | `Task` |
  | `plan` | `ExitPlanMode` |
  | `todo` | `TodoWrite` |
  | Other | Original name and unmodified parameters |

- **Cards**:
  - Paseo `question` → question card. Answers for Claude Code use the original question text as the key; other Agents use the `header` from Paseo's card.
  - `plan` → plan card:
    - Approval selects Paseo's `implement`, then switches to the mode selected on the phone. If the previous mode was bypass / auto, use `implement_resume` to return to it.
    - Rejection sends the feedback as the denial reason.
  - Everything else → permission card:
    - If Claude Code provides a suggested rule, the card offers "Always allow," stating which rule will be added and where it applies.
    - Denial includes the reason you entered.
  - Card IDs are derived from Paseo request IDs, reconnecting to the original card after a bridge restart.
  - Plan requests Claude Code gives Paseo contain no plan body (`ExitPlanMode` input is empty). The bridge reads the body from that tool call in the timeline.
- **Create**: use Paseo's `agent.create`. The bridge chooses the Agent ID and includes the form's selected model, permission mode, and reasoning effort. The first message is `initialPrompt`.
  - Creating from a space places it in that Paseo workspace.
  - If Paseo does not finish creation within 25 seconds, return the session row first and keep waiting in the background.
- **Take over**: use Paseo's `import_agent`, choosing the provider used by the most recent Agent running Claude Code on that computer.
  Set the `persona.session` label to the original session ID; archived Paseo Agents are restored.

## Out of scope and known limitations

- No terminal: no keystrokes, screen reading, live terminal view, or phone takeover. The App hides these entry points based on session capabilities.
- Phone tools (`phone_*`, `mail_*`) are not yet injected into Paseo Agents.
- All three clients show "Rename" and "Close" based on whether the session has a terminal. Paseo sessions have none, so these actions are currently hidden. Node and the bridge already support both operations;
  the App can show them by capability once session `capabilities` includes rename / close.
- Connect only to the local Paseo daemon. Paseo's relay and its other connected hosts are not integrated. Paseo on another computer uses that computer's own node.
- The bridge knows only workspaces that still have Agents. After all Agents in a worktree are archived, creating another session in that directory from Persona leaves the bridge unable to find its workspace,
  and Paseo creates another project record (Paseo's creation request can create only a new worktree, not select an existing one). The App still groups it with the main repository.
- Images in Paseo's timeline are not displayed. Claude Code sessions are unaffected; their images come from the transcript.
- If Codex history is enabled for the machine, the same Codex chat in Paseo may also appear as an extra read-only Codex history row.
- Paseo's own plugins, scheduled tasks, and hub execution are not integrated.

## Verification

- Unit tests: `cd integrations/paseo && npm test`. Use a fake Paseo daemon (`test/fake-paseo.js`) and the SDK's fake node to test lists, content,
  streaming, sending, cards, creation, takeover, mode changes, Paseo restart, and disconnection.
- End to end: first `cd server && go generate ./internal/runtimepkg && go build -o /tmp/personad ./cmd/personad`, then run
  `PERSONAD=/tmp/personad CLAUDE_BIN=<a Claude Code executable> node integrations/paseo/e2e/run.mjs`. There are 29 checks; `paseo` must be installed locally.
  - Start a real Paseo daemon with its own home and an unused port. The provider uses `extends: "claude"`; the model is a scripted
    Anthropic API in `e2e/model.mjs`.
  - Start a real `personad node --stdio`, with the SDK test tools acting as hub. After setting the engine to Paseo, node runs the bridge itself.
  - Test from creation through takeover after closing.
  - Use only temporary directories, without touching the active local Paseo or `~/.claude`.
  - Add `KEEP=1` to retain temporary directories, or `DEBUG=1` to print the bridge lines from node's log.
- Local testing (2026-11-04, Paseo 0.14.0, Claude Code 2.1.294):
  - All 29 end-to-end checks passed.
  - Read-only validation against a real active Paseo: the bridge connected to a fake node, allowing only read requests. Every available provider became a launcher; all Claude Code sessions pointed to their own transcripts,
    most of which were found locally (the missing ones were in configuration directories unknown to node, so their content used the timeline). No write requests were sent.

## Troubleshooting

- **Engine shows "Not detected"**: Paseo is not running, or `paseo.pid` is not in the home node expects. For a custom home, set `PASEO_HOME` for personad.
- **Engine is available, but the note says "Paseo is running, but Persona cannot connect to it: …"**: the bridge cannot start; the rest gives the reason, commonly a missing Node.js 22.
  Set `PERSONA_NODEJS` to a node executable version 22 or newer.
- **Sessions appear, but content uses Paseo's format rather than Claude's transcript**: node did not find the transcript, for example because the provider uses a different
  `CLAUDE_CONFIG_DIR`. Add that directory to the machine's `machines[].claude_config_dirs` in hub.json (`docs/operations/config.md`).
- **Detailed logs**: look for lines beginning with `paseo bridge:` in node's log.
