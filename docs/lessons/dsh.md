# Lessons: DSH and runtime integration

Pitfalls hit while integrating DSH (`docs/integrations/dsh.md`) and node's runtime support (`server/internal/node/runtimes.go`, `integrations/runtime-sdk`, spec in `docs/integrations/runtimes.md`).

- **DSH approvals / questions are a responder chain, not a broadcast** (`approval/request` and `user-questions/request` are Cordis waterfalls: they run in registration order and stop at whoever doesn't call `next()`).
  The browser link in the chain only withdraws its card when the request's own `signal` ends; the plugin can't withdraw it on the browser's behalf. So the phone and the Web GUI can't "both pop up, first answer wins". The plugin can only go to the phone first,
  and call `next()` once the phone hands it back to the computer or it expires. The plugin needs `prepend: true` to get ahead of the browser (the browser only registers its listener when it connects).
- **Not every `user/message` is from the user**: only `source.kind = user` is user input. `time-context`, `skill-catalog`, `runtime-context` and
  `agent-instructions` are context DSH injects every turn; `agent-message` / `subagent-settled` are messages from sub-agents. A message queued in `agent/inbox/spliced`
  gets recorded again as a `user/message` when it enters the turn; showing both duplicates it.
- **DSH's seq starts at 0**, while Persona's seq 0 means "nothing yet" (the zero value of `since_seq` / `before_seq`): map it to DSH seq + 1.
- **A DSH session log is a sequence of independent zstd frames** (one frame per write batch). `zlib.zstdDecompressSync` on the whole file only decodes the first frame, so it looks like "the log has only one line".
  To read a log by hand, split it at the frame magic `28 b5 2f fd` and decode frame by frame. The plugin doesn't read logs; it goes through `sessionController.page`.
- **Machines without herdr never used to push the session list**: `pollAgents` did a straight `continue` when the host (herdr) didn't respond, so runtime sessions (and everything except the assistant) were listed only once, on connect.
  Now a herdr failure still builds the list and pushes changes.
- **A runtime must record the connection before `runtime.hello` returns** (the SDK already does this): node may call in (`session.watch`) right after it answers hello, and whatever the callback pushes needs somewhere to go.
- **`dsh web` started from an Agent's shell can't use DSH's own sandbox** (macOS doesn't allow sandbox-exec inside sandbox-exec), so every bash tool call reports
  `SANDBOX_UNAVAILABLE`. The end-to-end script gives the test instance a "no sandbox, but ask for approval" preset (`sandbox: danger-full-access, approval: ask`).
- **The fake model gets eaten by title generation**: after the first message, DSH's web profile uses the same provider to generate a title. When scripting a turn with `llm-mock-server`,
  answer requests with `tools` (the Agent's own) separately from those without (side calls like the title), or the scripted tool calls get consumed by the title request and the order falls apart.
- **`blocked` makes the hub fabricate a terminal card** (found 2026-11-01 when merging the Codex source into master): for a `blocked` session with no matching card, the hub reads the screen
  and fabricates a "terminal is waiting for you" card. While a DSH question waits for someone in its own web page, the session is `blocked` and `session.screen` returns an empty screen, so an extra blank terminal card shows up on the phone.
  Now sources without terminal capability (`capabilities.terminal` false) never get one; the manager makes these cards (`assistant.syncScreenInteractions` in `assistant/fleet_ix_screen.go`). An end-to-end test with the harness acting as hub can't catch hub-side or manager-side problems like this;
  when adding such behavior, add a case to that side's tests (`hub/runtime_test.go`, or `assistant/fleet_cards_test.go`, e.g. `TestNoScreenCardWithoutATerminal`).
- **A card without `agent` is drawn as Claude's**: iOS / Android / web all offer "Always Allow in Session" on approval cards without `agent`. DSH can only treat that as "allow once", so pressing it still asks again.
  node tags runtime cards with `agent` / `agent_label`, and the apps only offer "always allow" on Claude's cards or cards with `always_label`.
- **The list is not ownership** (2026-11-01): the plugin only lists recent top-level sessions (last 7 days, at most 80). node used to decide ownership from "the current list" only, so a DSH session that dropped off the list
  (an older one, or one the plugin forgot after DSH restarted) was treated as a Claude session not running on this machine and got `claude --resume`d into herdr. Old DSH session ids are plain uuids,
  same as Claude's, so they pass id validation; new sessions (`session-…`) fail first with `invalid session id`. Now node remembers which runtime listed which sessions and asks the runtime first about any it never listed,
  and the plugin keeps listing sessions the phone has touched (`e2e/older-session.mjs`: list cap 1, restart DSH, then message a session that dropped off the list).
- **Separate parents from children before counting sessions** (same day; misjudged once): of the 2,000+ directories under `~/.dsh/sessions`, 98% are sessions spawned by sub-agents / workflows (session header
  `origin: "subagent"` or a `parentSession`). Top-level sessions number only a few dozen, and only a handful in the last 7 days. The plugin never listed child sessions, and the cap of 80 was nowhere near reached;
  the "DSH session that can't send" on the phone at the time was actually **a Claude session opened in the Claude desktop app** under the `DSH-Claw` directory (`docs/lessons/agents.md`).
  To find out "why a session behaves like this", get its id and source first; don't reason from totals. The session header is the first line of a zstd-compressed jsonl (`session.v4.jsonl.zstd`; older ones are `v2` / unversioned).
- **`runtime.sock` followed the hub connection, so one disconnect cost a minute and a half** (2026-11-01, #224): the resident node created a new Node per hub connection, and `runtime.sock` plus the runtime connections
  were created and torn down with it. On a node restart or hub reconnect: launchd brings node up in about 10 s, the hub reconnects via tailcat in a few tens of seconds, the plugin only reappears once the hub is connected to the socket,
  and its retry backs off to 30 s. Total: a minute and a half, during which opening a DSH session reported transcript not found. Now the socket and runtime connections belong to the resident node process (`RuntimeHost`)
  and survive a hub connection change; the SDK retries every second while the socket file exists. End-to-end: `runtime-sdk/e2e/resident.mjs` (real `--attach`, disconnect and reconnect the hub, restart the resident node).
- **pi-web only loads extensions once a session is open** (2026-11-01, measured while integrating pi): one pi-web process runs all sessions; the extension module loads once and the factory runs once per session,
  but nothing loads before the first session opens. Relying on the extension alone, Persona sees no pi sessions at all after pi-web restarts. So pi has a separate companion process for sessions on disk
  (`docs/integrations/pi.md`). When two runtimes list the same session, node picks the one currently running it: pi session files have no lock, and "last to connect wins" would send the phone's messages to the second process writing it.
- **SDK timers don't keep the process alive; a standalone runtime must do it itself** (2026-11-01, pi companion process): the SDK's reconnect and push timers are all `unref()`ed, because it usually lives inside the DSH or pi process
  and shouldn't hold up the host's exit. The pi-web companion is a standalone process that is only a runtime; as soon as it disconnects (node not up yet, restarting, too old) the event loop is empty, the process `exit 0`s and launchd keeps relaunching it.
  The standalone process keeps its own `setInterval` (`companion.mjs`).
- **An intentional disconnect must block the socket's close callback** (same day): after deliberately disconnecting from a too-old node, the `close` callback saw no peer and scheduled a reconnect in 1 s, overriding the "wait for node to restart" timer;
  the resident node's log showed connected / left twice a second. The unit test passed by luck (it only checked that it wasn't connected at one moment); it only shows up when you count the hellos the old node receives (`hellos` in `fake-node.js`).
- **Live text must accumulate even when nobody watches** (2026-11-05, #3609): the `text` of `session.delta` is the whole text of the message so far, but the plugin used to append chunks only while `rt.watching(id)`,
  so a phone opening the session mid-message saw only what came after. Only "send or not" depends on the watch; "record or not" does not (the Paseo bridge always worked this way).
- **pi in herdr** (2026-11-01, measured with herdr 0.9.5 installed locally): a pi pane's `HERDR_PANE_ID` is the `pane_id` from `agent list` (`w1:p1`). herdr's pi integration reports
  `agent_session {kind: "path"}`, whose value is the session file; the uuid at the end of the file name is pi's session id (node used to accept only `kind: "id"`, so pi was always dropped). herdr's CLI only answers
  `server_not_running` when there is no server and doesn't start one itself. node adds `/opt/homebrew/bin` to PATH, so an old node can still find herdr's CLI after it's installed with brew. `herdr integration install claude`
  changes the hooks in `~/.claude`; don't install it casually on a machine where someone is using Claude. For real-device checks use `XDG_CONFIG_HOME=<temp dir> herdr --session <name> server`, and stop it by name.
- **When a test hangs, check whether something was never started** (same day): the companion process's unit test didn't call `host.start()`, so `await n.ready` never resolved; `npm test` hung for three minutes and left a temp directory behind when killed.
  Wrap unit test runs in a timeout too (`perl -e 'alarm 300; exec @ARGV' npm test`).

- **DSH plugin management can't be called from a session driven by the phone** (2026-11-02): when enabling `persona-dsh-runtime` in plugin management, DSH's hot reload uses `AsyncLocalStorage` to mark "inside a transaction",
  and the socket the plugin creates inside the transaction carries that mark into every later callback. Every turn started by a message coming in through the plugin (sent from the phone) is treated as "still inside the transaction", so `plugin_manager`'s
  `set_bundle` always reports `HMR transactions cannot be nested`, and sending a sub-agent to call it fails the same way (it inherits the same context). Messages sent from the DSH web page are unaffected.
  Plugins installed by link aren't in hot reload's watched directory either, so swapping files doesn't reload them. So the plugin updates itself (`docs/integrations/dsh.md` ("Auto-update")) instead of relying on plugin management.
- **Re-importing the same path in the same process gets the old code** (same day): Node's ESM cache is keyed by URL, even if the file changed. When the plugin reloads inside DSH (plugin management off and on again),
  the `lib/dsh.js` imported earlier is still the first copy, and the new version is swapped in for nothing. So the loader loads from `<store>/<hash>/`, and the bundled copy is copied there first too: when the content changes, the path changes.
  The loader's unit test found this (change the bundled file, start another loader in the same process, and it still runs the old version).
- **When a pi extension sends itself a `/command`, a command that doesn't exist becomes a message to the model** (2026-11-02, pi auto-update): pi only gives extensions `reload()` inside a command context, so to switch versions the extension
  registers a command and runs it on itself with `pi.sendUserMessage("/persona-reload", {expandPromptTemplates: true})`. pi looks for an extension command first and, if none is found, hands the string to the model as a user message,
  so only send it to sessions that really registered the command (an old pi without `registerCommand` doesn't switch). pi-web rejects reload while it is replying; wait until between turns.
