# Lessons: transcript parsing, synchronization, and pagination

Pitfalls, unusual behavior, and the trade-offs made at the time (originally in "Lessons" in `CLAUDE.md`; see the index in `docs/lessons/README.md`). Add new lessons to the relevant section, or start a section if none fits.

## Display provenance is not a model role (#2130)

Persona's `conversation_send` relay is model input with role `user`, not the user's own words. Its existing `from`
field belongs to runtime peer messages, not Persona conversation ids. Prefer structured source metadata; an older-server
fallback must require the complete leading delivery envelope and exclude app-sent `client_msg_id` messages. Do not
classify arbitrary clock/clipboard prefixes or source titles. Preserve unknown source kinds as external provenance,
bump the item cache version, and carry the provenance into Watch's derived text model as well as the shared chat view.
Group labels after feed and date insertion; adjacent rows with the same title but different conversation ids are not
the same speaker. `JSONValue.string` coerces booleans and numbers for generic UI: provenance fields must pattern-match
actual JSON strings, or malformed metadata can become an invented source instead of protecting a user quotation.
Use synthetic conversations for screenshots, not the user's real main conversation.

- A session's "last edited" time must be the timestamp of its last real conversation entry (user / assistant / tool): Claude Code appends recap, ai-title,
  mode, and other lines to idle sessions, refreshing the file's mtime. Leave the timestamp blank for sessions whose transcript cannot be found; do not fall back to now (it would always show "Just now").
- Queued messages: transcript queue-operation entries (enqueue / dequeue / remove / popAll) reconstruct the queue precisely. In the TUI, ↑ brings **all** queued messages back into the input field.
  ctrl+u clears only the current line (for multiple lines, use ctrl+u + backspace one line at a time). Long text sent through herdr bracketed paste is wrapped in `<pasted_content id=…>`; strip it before display.
- Messages entered while Claude is working (from a computer or phone) are "absorbed mid-turn" and stored in the transcript as `attachment/queued_command` (commandMode=prompt,
  origin.kind=human), not as user messages. The parser must handle them separately, or they will not appear on the phone.
- Newer Claude Code versions write narration between tool calls into nonempty `thinking` blocks, not `text`. The UI must not hide all thinking indiscriminately.
  A live preview must also survive a cold load: `fold_work` hides those original blocks, so folds carry a bounded `latest_thinking` preview without changing their range or counts (#6319).
- A new session (a headless assistant or a newly launched herdr Agent) has no transcript until claude writes its first line. The phone requests a backlog as soon as it subscribes to the hub. `session.items`
  must return an empty list rather than not found for these "starting" sessions (`startingSession`). Once watch sees the file appear, it must first replay the lines already written, or the chat page briefly flashes an error.
  A rename is the same: the Feishu /agent names its conversation right after starting it, and the custom-title line had nowhere to go, so the node
  answered "transcript … not found" and the manager kept no title. The node now keeps the title of a running assistant conversation and writes it once the
  transcript appears; if renamed again meanwhile, the newest title wins; if the process ends first, the title is dropped (`retitle`, #5866).
- Only a response paginated from the newest end knows "whether there are earlier messages" (`has_more`). Subscription catch-up with since_seq used to send `has_more: false`;
  if it arrived before the first HTTP page, the App assumed it had reached the start of history and stopped upward scrolling at the earliest loaded message (2026-10-26). The hub now omits this field in catch-up,
  and the App treats its absence as unknown. A batch that does not cover the earliest local message must not set it to false either. Reproduce with `-showScrollToOldest YES`, scrolling all the way up while logging.
- A runtime rewatch's eight-page replay cap needs the same `more_after` recovery marker as the hub's HTTP backlog, propagated through the live `session.items` path too. Without it, contiguous batches look complete and a reconnect leaves everything after page eight missing until another append (#1621).
- When the chat page is "not following," the list freezes at the messages present at that moment (`frozen`, so content stays still while scrolling up). Only a **change** in the bottom sentinel's visibility enables following. If the landing point (the last read position,
  or the start of an unread reply) is on the last screen, landing already puts the list at the bottom. The sentinel stays visible and never changes; later replies are all held back, with no "Back to latest" either (2026-10-31, TestFlight 50/51:
  "stuck on the message I sent"). Rule: if the list is at the newest end and no finger is down, it must follow (`landed` / `contentArrived`). Run `ChatFollowTests` when changing follow logic.
- In sessions with many screenshots, one page of earlier messages is 1–5 MB (images embedded as base64). Decode pagination on a background thread, and prefetch the previous page as soon as the earliest dozen or so lines appear.
- Do not implement background sync by "subscribing to every session": subscriptions make the node refresh transcripts every 250ms and read terminals every 400ms (herdr process calls). Use an IM-style push/pull combination instead:
  the hub pushes only each session's `rev` (transcript byte count), and the App pulls increments with `after_seq` (`docs/apple/ios.md`, background sync). The full session list is about 130 KB;
  do not resend it for rev changes—use a separate `revs` event. The Lark gateway has a real-time event WebSocket (`/agent/v1/events`, `message.upserted` /
  `conversation.upserted`); Lark pushes come from there.
- Validate changes to transcript parsing or the sync protocol against a real corpus (`docs/archive/integrations/msg-render-verification.md`). `corpus_test.go` compares the old and new parsers file by file;
  `continuity_corpus_test.go` simulates lost pushes, gap filling, and rewrites using the App's rules. Keep the corpus only in `~/persona-corpus`. Active session files grow while being read, so take a snapshot before comparing.
  To view real sessions in the simulator App, use a local test hub (fake HOME, no herdr connection, separate database), not the production hub.
- Cold-opening a large transcript parses only a tail window (`transcript/window.go`). A line's parsed result depends on preceding lines through just three things: background task notifications already displayed
  (duplicates are dropped), the previous "! cmd" input, and Codex recording the same item twice. If a parser change introduces "cross-line state," the window's starting point must also be able to reconstruct it
  (`seeded` / `seeNotifications`), or windowed and full reads will disagree. `TestFixtureWindowedEqualsFull` and the corpus's `TestCorpusWindowed` catch this.
- User images take two paths: ordinary messages contain user image blocks; messages sent while Claude is working are "absorbed mid-turn" as `attachment/queued_command`, with images in the prompt's block array
  (before 2026-10-29, the parser took only text blocks, so bubbles showed no images). In App turns awakened by background tasks (`pickAnswerAcrossWakes`), keep the user's images together with their message in the original position, not in the work trace.
- A long chat's slow first screen is not caused by the hub / node (cold read: 0.12 s), but by the page being too large: one long Codex session's last two turns contained 1412 entries and 1.08 MB, with just 6 reply entries; the rest were tools and thinking already collapsed in the UI.
  The App requests `fold_work`. Pages read backward from the end replace each work segment with one `kind: fold` entry (counts and ranges only). Expanding a work row fetches it with `from_seq` / `through_seq`
  (`docs/protocol/protocol.md`, "Folded work"). `after_seq` catch-up and live pushes are not folded. Neither are tool_use entries still running or tool_result entries whose other half is outside the page, or they cannot be paired.
- Claude Code writes `&gt;`, `&lt;`, and `&amp;` itself into the summary / event / result of background task notifications (`<task-notification>`); decode them during parsing. For background commands without a description,
  the summary is the whole command (a heredoc can span dozens of lines). Do not use the summary as notice text: the node parses it into `Item.task`, and the App renders a single tool-style row (`TaskNoteRow`).
  Monitor events carry only task-id; map them back to the call using the tool_result `Monitor started (task <id>` (`TaskIndex`).
- The App merges its on-disk chat cache by item id. If the hub changes the contents of a type of item (for example, adding fields to notices), new pages do not overwrite old cached items. When the item structure changes,
  bump the `ChatCache` directory version (`chats-v3`).
- Backward pagination has a parsing budget, but must not return an empty page while earlier content remains. An empty page + `has_more: true` leaves the App with no seq to fetch next, making earlier history permanently inaccessible (large Codex rollouts can have tens of MB of lines that produce no entries between two turns).
- Parsing `<persona-context>` must not assume all attributes are present or in a fixed order (2026-11-03, #876). iOS once recognized it only when all three of `kind`, `title`, and `url` were present. Lark /agent contexts had no `url`, so the tag went unrecognized, leaving the whole prompt and large blank areas in the bubble.
  Truncating user messages for pagination must also avoid cutting through tags: a cut in the middle leaves an unclosed `<persona-context`, which every client displays as body text (`lightContexts` in `transcript/contexts.go` trims only the body of each block).

## A tail window may contain no words at all (2026-11-04, #1163)

When listing offline sessions, the node reads only the last 384 KB of a transcript (`quickTail`) to get its title and last words. Claude Code now writes an entire `api-request-blob` on one transcript line, 300–500 KB per line;
a single line can fill the tail window. If the last turn stops between tool calls (thinking → tool_use → tool_result), the tail contains no text at all. `scanOffline` used to treat every session with
"neither a title nor last words" as a hook probe and skip it—even a kept main chat. The main chat disappeared from every list, the App and web could not find it, and the input field became completely unresponsive.
Now it skips only small files that have been read in full (no larger than the tail window) and genuinely contain no output. The main chat is listed regardless of its tail contents. Before "filtering" based on a tail, remember that one large data line can fill it.

The same window also lost the last words of online sessions (#1583): after the Agent said something and then made dozens of tool calls, tool results, hook receipts (`async_hook_response`), and request lines quickly exceeded 384 KB.
`last_text` became empty, leaving task cards and main-chat snapshots showing "No activity yet" for 7 minutes. `readQuickMetaSince` now reuses the previously read text when the window contains none;
if there is no previous value (the node has just started), it searches backward in segments (`lastWordsBefore`, up to 8 MB). For any "last X" value found only in the tail, consider that it may already be outside the window.
- **A Claude Code transcript is a tree** (`uuid` / `parentUuid`): rewinding (`/rewind`) or editing and resending with ↑ writes the next message as a sibling of an earlier one. Rewound turns remain in the file. See
  `transcript/branch.go` (two messages from the user under the same parent, with an assistant reply to the earlier one → the earlier message's entire subtree produces no items) and `docs/protocol/protocol.md` ("Rewind to a message");
  **pressing Esc to cancel a message before any reply, then sending again, also writes the same pair of siblings** (#3036; observed: two identical messages under the same "[Request interrupted by user]" line, with no reply to the first).
  This is not a rewind. Older versions treated any "two siblings" as a rewind, added `.r1` to `history`, made all clients discard and refetch local content, and removed the interrupted turn from the chat too.
  To detect a rewind, check whether the earlier message received a reply. Persona's own rewind does not rely on this inference (`File.Rewound` is marked by the node).
  Do not walk backward from the end along parentUuid (parallel tool results are normal branches, and `compact_boundary` has a null parent). Abandoning a branch changes only the `history` token (`.r<n>`),
  not the item structure, so there is no need to bump the App's `ChatCache` directory version. Clients already discard and refetch local content when the token changes. Validate against a real corpus:
  build one old and one new binary with `go test -c ./internal/transcript`, compare item counts file by file with `-test.run 'TestCorpus$' -corpus <directory> -out <file>`, then run `TestCorpusWindowed`.

## A subagent's report is a tool call now (2026-11-06, #5886)

Since Claude Code 2.1.318 the Agent tool always launches the subagent async ("Async agent launched successfully", even when asked for the foreground), and the subagent reports by calling `SubagentHandback` with its report as `message`.
The parent gets the report on a hidden user line (`isMeta: true`, `origin: {kind: "peer", from: <agent id>, handback: true, handbackReportText}`), and the agent's `<task-notification>` only points at it ("This agent's report was delivered to you as a message from … (its SubagentHandback call)").
Read the old way, the report was nowhere: the hidden line was skipped, the notice's detail was the pointer, the subagent's `last_text` was its first words, and the hand-back counted as a step and was its `last_tool` ("Calling SubagentHandback").
`transcript` now reads the call as the subagent's last words (a text item, `LastText`; its "Report delivered to your caller." result is not shown) and gives the parent's notice the handed-back report as its detail (`reportFor`; a pointer with no report known is dropped).
When Claude Code changes how something reports back, record a real session and look at what is hidden (`isMeta`) as well as what is shown.

Claude Code 2.1.319 adds two more shapes (#6381). A hand-back that arrives while the session works is a `queued_command` attachment with the same peer origin, so it was drawn as a message "from general-purpose" with Claude Code's `[Subagent hand-back]` framing; it now goes to the notification like the hidden line.
A subagent that waits on its own background work gets an interim notification ("This agent has not reported yet … SubagentHandback"), and the notification that ends it names only the subagent, no `<tool-use-id>`.
The apps find a task's card by `tool_use_id`, else by the id in the launch receipt, and the 2.1.319 receipt's first sentence says "including the agentId below": their pattern took "below" for the id. So the report stayed off the card, which kept the interim text as its result.
The node now gives such a notification the call an earlier notification of the task named (`noteCall`), drops the interim text, and seeds both, with the reports handed back, into windows read from a file's end (`window.go`), so a cold open of a big file gives the same notices.

## Work opened in the App folded again by the same page (2026-11-07, #6038)

As a chat opens, the latest page can arrive twice: once over HTTP (`loadInitial`, when the socket is not up yet) and once in the socket's subscribe backlog, both with `fold_work`. With "open all work" on, the first fold row fetched its steps (`AppModel.unfold`) before the second page came; that page carried the same fold, `dropFolded` took the steps out again and the fold came back under the same row id.
`FoldedWorkRow` fetches in `.task(id: attempt)`, which had already run for that id, so the row stayed on "Loading N steps…" (about 1 run in 3 on the mock hub). `ChatStore` now remembers the folds it opened (`opened`): a page with the same fold over the same or a shorter range leaves the work open, a fold that reaches further folds it again, and starting over (`reset`, `replace`) forgets them.
A view that fetches once per id cannot recover when the store puts the same id back: the store must not undo what the view already did.
## A queued message is taken in at the next tool boundary, not at the end of the turn (measured Claude Code 2.1.2xx; re-check)

- See also "Queued messages" in `docs/integrations/claude-integration.md` (#6088, measured on 2.1.319), which also covers "send now".
- Extending the "absorbed mid-turn" entry above: Enter while Claude is working queues the text, and Claude takes it in as soon as the current tool call returns: the
  transcript gets it as `attachment/queued_command` (`commandMode=prompt`, `origin.kind=human`) and the queue-operation line
  records `reason: "absorbed_mid_turn"`. It is not held until the turn ends, so a message sent during a long turn can be
  half-way through the reply before the phone shows it.
- Esc stops only the tool call that is running. The queue survives and starts the next turn, so "stop" and "the queued
  message" are independent; after an interrupt, look for the queued message becoming a new turn rather than assuming the
  interrupt cancelled it. The parser must not treat that later turn as a new user message typed after the stop.

## A herdr started from inside a Claude session makes its panes write no transcript (measured Claude Code 2.1.2xx; re-check)

- Claude Code sets `CLAUDE_CODE_CHILD_SESSION` for the processes it starts. A herdr that someone launches from inside a
  Claude session therefore passes it to every pane, and Claude in those panes treats itself as a child session and writes no
  `~/.claude/projects/…/<session id>.jsonl`. The Agent then appears in lists with no transcript: no title, no last words, and
  a chat that stays empty while the terminal is clearly working.
- `CLAUDE_CODE_FORCE_SESSION_PERSISTENCE=1` overrides it and writes the transcript again. Set it on the Agent command (like
  `HERDR_AGENT=claude`) rather than exporting it into the pane shell, and only when the parent environment has the child flag:
  an agent started normally must keep whatever its launcher decided.
- Symptom to recognize: a pane whose Claude works and herdr reports an id, and a `sessions/<pid>.json` that names a session
  id whose `projects/…/<id>.jsonl` never appears. (The pid file has no transcript path; node derives it from the session id —
  `transcriptPath` in `node/transcript_files.go` looks for `<config dir>/projects/*/<id>.jsonl`.) Check the node's own environment
  for the child flag first (`/proc/<pid>/environ`, on macOS `KERN_PROCARGS2`), then the pane shell's.
