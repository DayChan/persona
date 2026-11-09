# /agent in Lark

> **Since 2026-11-04 (#1520, extensions step 3, 3c)**: the whole path that connects Lark through the Lark gateway (the App's `/api/v1/lark/…`, the Lark sources of the Inbox and knowledge base, Moments,
> the assistant's `ol` gate and credentials, /agent, personal Agents) is no longer in the open-source repository; it is provided by the extension that connects Lark (`docs/extensions/extensions.md`, `docs/plans/plan-extensions.md` ("Step 3")).
> The behavior described below is provided by the extension unchanged; the hub source files mentioned here (`hub/lark*.go`, the `internal/lark` client…) have been deleted. The open-source version keeps only the official lark-cli, the Lark bot and Lark Mail.

In any Lark conversation (group chat, direct chat, a topic in a topic group, a thread reply in a regular group) or in a document comment, when **I send** a message starting with `/agent`, the hub immediately starts an Agent to handle it.
When it finishes, it replies to that message as me (inside the topic if it was in a topic), ending with a quoted line "> 🤖 AI 代答，仅供参考" ("AI-written reply, for reference only"; extension setting `agent_reply_suffix`, the default since version 0.11.1 of the Lark extension, #866; if the Agent wrote this line itself it is removed first, so only one remains). (Requested 2026-10-31: turn the "listen for /Agent …" that I used to start by hand in herdr
into a system feature. That listener itself ran inside a chat that already had context; here a request can come from any group, so the hub has to gather the context first and then hand it to the Agent.)

The code lives in the Lark extension (formerly the hub's lark_agent.go); configuration is the extension's `agent`, `agent_launcher`, `agent_machine` (formerly `lark_agent` in `hub.json`).

## What counts as a request

When the Lark extension's Agent feature is enabled, either of these triggers can make a chat message a request:

- **Prefix**: after stripping leading @mentions, my message starts with the configured prefix (`agent_prefix`, default `/agent`, case-insensitive),
  followed by a space, newline, colon or nothing (`/agents` does not count). The text after the prefix is the request and may be empty;
  the Agent works out what to do from the context and the quoted message.
- **Self-mention**: I @mention myself in my own chat message. The mention's user ID must match the signed-in account's user ID;
  a display name or plain text that looks like an @mention is not proof of identity. The self-mention is removed from the request text,
  which may be empty so the Agent can use the conversation and quoted message as context.
- **Independent switches**: `agent_trigger_prefix` and `agent_trigger_self_mention` both default to `true`. Turning either off leaves the other available;
  turning both off prevents new chat requests through either trigger. Changing `agent_prefix` changes only the prefix, not the self-mention trigger.
- **Only my own messages** (`isMine`: sent by a person, not a bot, not recalled): someone else sending `/agent` or @mentioning me does not trigger anything,
  even if their display name matches mine. The Agent runs with all my permissions: Lark, mail, all my computers.
- A message containing both the prefix and a self-mention is handled **once**, not as two requests. The self-mention and any matched prefix are removed from the request text.
- A request from either enabled trigger in the same topic continues the same Agent conversation; the existing `/stop` behavior is unchanged.
- Only live events are used (the Lark gateway's `message.upserted` on `/agent/v1/events`, which carries the whole message); messages from before the hub started, or older than 10 minutes, are ignored,
  so restarts and redelivery do not answer old messages again. Each message is handled once (the Lark gateway resends the same message when it is edited or its topic gets a new reply).
- Messages ending with the current suffix, the default suffix "🤖 AI 代答，仅供参考" or the old "🤖" are skipped as Agent replies, even if they contain either trigger;
  a request that merely quotes these words is handled normally.

Document comments still use the prefix trigger, controlled by `agent_trigger_prefix`; self-mentions trigger requests only in chat messages.

## Context handed to the Agent (`larkAgentContext`)

This is the biggest difference from the listener in herdr: a request can come from anywhere and the Agent starts knowing nothing, so the hub gathers context first and puts it in the first message (`<persona-context>` tags):

| Content | Source |
| --- | --- |
| Location: direct chat / group / topic, conversation name, feed_id, request message id and time | Conversation meta in the knowledge base |
| Messages in the topic (when the request is in a topic), otherwise the 40 messages before the request (`context_messages`) | Lark gateway `messages/:id/thread`, `conversations/:feedId/messages?beforePosition=` |
| The message the request quotes | The message's `replyTo` |
| Lark documents linked from the request, the quoted message and the last 10 messages (at most 3, 8000 characters each) | `documents/read?format=markdown` |
| This conversation's profile, and the profiles of the first 3 projects it is linked to | Knowledge base `~/.persona/data` |

Each chat log line is "month-day hour:minute name: text"; the request line is marked "← request"; the total stays within 30,000 characters (newest kept). For anything missing the Agent looks it up itself (lark_*, kb_*, reading documents, checking Agents).

## How it shows in the App (#876)

In the first message to the Agent, **only the sentence at the end, outside all `<persona-context>` tags, is what the user wrote** (the request after `/agent`, or the prefix itself if nothing was written);
the instructions for the Agent (`kind="instructions"`) and every context item in the table above are inside tags. The bubbles on web, iOS / Mac and Android show only that sentence,
and the tags collapse into one line next to the bubble, "Context · N items", folded by default; expanding shows a list of titles only, and tapping an item shows its body (with links that can be opened); the watch shows "[N context items]".

- Chats from before 2026-11-03 use the old format (instructions, `## 请求`, `## 上下文`, `## 怎么做` all outside the tags): transcript parsing (`legacyLarkAgent` in `transcript/contexts.go`) rewrites them into the new format, so old sessions display the same way. iOS's `ChatCache` was bumped to `chats-v6` for this.
- Context often runs to tens of thousands of characters, and user messages are truncated to 24 KB when paging: truncation only trims each block's body (`lightContexts`); the user's sentence and every block's title stay whole, and nothing is cut in the middle of a tag.

## How it runs

- Like the Inbox's AI twin, it is a headless Agent in the assistant's directory (`assistant.send`), so it has all the assistant's tools. New requests open with `purpose: "lark.agent"`: configure its provider / launcher, model, and effort under **Settings › Model configuration › Lark /agent** (`docs/assistant/model-config.md`). With no newly saved override, or after restoring following, it inherits **Agents** (`agent.new`), not the main chat or old `lark_agent.launcher` / extension launcher defaults. Retired per-purpose records remain inert; only new user saves use the independent override namespace (#5921). The resolved launcher, model, and effort are passed to the node before it starts the conversation. Existing topic conversations retain their running settings; saving applies to new `/agent` conversations. The chat appears in the history of the "Persona" tab, where you can open it to see what it looked up; no "done" notification is pushed when it finishes (the result is the reply in Lark).
- On receiving a request it first reacts to the message with the "OnIt" emoji, and switches it to "DONE" after replying.
- The Agent submits its answer with the MCP tool `lark_agent_reply` (accepted only from the chat started for that request); the hub sends it with the Lark gateway's `POST /messages/:id/replies`
  (`editorType: markdown`, `replyMode: thread` inside a topic). If the turn ends before the Agent's first reply without calling the tool, the hub sends its last reply paragraph.
- **Long tasks and later progress (#3192)**: `lark_agent_reply` can be called many times during the request record's retention period (3 days). The first call replies to the original message as above;
  every later call (progress, final result) goes into a topic rooted at the request (if the request was already in a topic, into that topic), so the group is not flooded;
  if the Lark gateway refuses to open a topic there (`REPLY_IN_THREAD_*`), it falls back to replying with a quote of the original message. When there is still no reply after 30 minutes, it is no longer stopped:
  it replies "正在工作中…" ("Working on it…") as a topic under the original message, and all later replies go into that topic. The old cutoff, "not finished within N minutes, stopping here", is gone.
  After the first reply, a turn that ends without calling the tool sends nothing (whether and what to send is the Agent's call). The prompt asks it to send only meaningful progress and results.
- **Dispatched tasks**: the extension opens the chat with `report_tasks: true`. Tasks this chat dispatches with `task_plan` / `agent_start` behave like the main chat's
  "quiet tasks" (`docs/assistant/unified-entry.md`): no separate pushes; finished, waiting for the user, interrupted, failed to start are all delivered as task notices (`Reporting` in `prompts/assistant/task_notice.md`)
  into this /agent chat (queued after the current turn if it is busy); it then sends the result to the topic with `lark_agent_reply`.
  The registration lives in `assistant.task_reporters` in the settings table (14 days) and survives hub restarts.
- **Stopping (#3308)**: sending `/stop` (also `/停止`, `/停`, optionally preceded by an @mention) in the request's topic, or quoting the request (or its reply), stops that request,
  even right after it was sent while the context is still being read. Only my own messages count (as with `/agent`, the request can only be mine, so the requester is me); others' are ignored, with no "not allowed" reply (replying that to a colleague in my name would be odd).
  The extension calls `POST /api/v1/assistant/conversations/{id}/stop`: the hub interrupts the turn running in this chat and the Agents of its still-running dispatched tasks (plan cards waiting for the user's answer or not yet started are left alone;
  nothing runs there without the user), drops queued task notices, and from then on drops its task notices outright (not redirected to the main chat) until the user sends this chat another message (continuing in the App, or sending `/agent` again in the topic to continue this chat).
  The request is recorded as `stopped`, no longer tracked or replied to (`lark_agent_reply` is refused too), and "OnIt" is removed; under the `/stop` message it replies "已停止" ("Stopped") with a list of what was stopped (this request's handling, the names of dispatched tasks);
  if nothing was running it replies "已经结束了" ("Already finished"). Both carry the footer, with the same rules as replies. The "Working on it" message ends with how to stop; nothing extra is sent when a request starts, to avoid an extra message every time.
  Stopping this chat with the stop button in the App / web (`POST /api/v1/sessions/{mid}/{sid}/interrupt`): the hub tells the assistant (`session.user_stopped`), which also stops dispatched tasks and records it as stopped from the App;
  the extension reads `GET /api/v1/assistant/reporters` on each scan, records the request as `stopped`, says "已在 Persona 里停止" ("Stopped in Persona") in the topic, and does not send the half-finished last paragraph.
  On an old hub without `…/stop` (404), the extension falls back to only interrupting this chat. `/agent` in document comments cannot be stopped this way yet.
- Sending `/agent` again in the same topic continues the same chat (`--resume`), so it remembers what was said before.
- The prompts live in the extension (formerly the hub's prompts/lark, `chat_agent.md` / `doc_agent.md` and their `*_context.md`); they tell it to **make no promises on my behalf**: the reply goes out in my name, so it only states what has been done and writes what has not as suggestions. (On the first real use, 2026-10-31,
  it wrote "I'll take down the orchestrator in this old lane" in a reply, without having done it.)
- Records are stored in `lark_agent.runs` in the settings table (3 days).

## Configuration

Model selection is stored by the manager through `GET` / `PUT /api/v1/model-config/purposes/lark.agent` (PATCH remains compatible). Read the current revision, save the selected fields, and read back `override` and `effective`. To follow Agents again, send `provider: null`, `model: null`, and `effort: null` with the current revision. This does not authorize sending Lark messages or alter the extension's trigger and reply settings.

The legacy trigger configuration below illustrates the pre-extension shape; its `launcher` field no longer selects a model. Current trigger and machine settings belong to the Lark extension.

```json
"lark_agent": {
  "enabled": true,
  "launcher": "<your strongest launcher>",
  "prefix": "/agent",
  "context_messages": 40
}
```

## /agent in document comments (in the extension; formerly the hub's lark_agent_docs.go)

- My own comments produce no live event and do not reach the document notification center. Writing a comment means opening the document, which moves it to the top of Lark's "Recent",
  so the hub reads `documents/recent` every 20 seconds: for documents with activity in the last 15 minutes (at most 5 per run) it reads their comments (Base goes through `documents/base/comments`).
- A comment reply of mine (the Lark gateway's `mine`, or an author id equal to mine) that starts with the prefix after stripping leading @mentions, is within 10 minutes and was written after the hub started is a request; each comment reply id is handled once.
- Context: document type, title, link, comment id; the text the comment highlights; the discussion in this comment up to the request; the document body (Markdown, within 20,000 characters).
- The reply goes into the same comment (`documents/comments/:id/replies`). Comments are plain text, so Markdown symbols the Agent wrote are stripped first (`plainText`), and a final line "🤖 AI 代答，仅供参考" is added (plain text, no quote marker); comment replies ending with it or the old "🤖" are not treated as requests.
  Writing `/agent` again in the same comment continues the previous chat. The Agent may not edit the document body; if a change is needed, it gives the wording in the reply.
- Assumption: writing a comment makes the document's activityAt newer (inferred before the user tested it). Requests are picked up within about 20 seconds at the latest.

## Extension takeover (#1484)

Once the extension that connects Lark through the Lark gateway is enabled, /agent is handled by the extension: the hub's own live events and /agent stop, and `lark_agent_reply` gives way to the extension's connector tool of the same name (with one extra parameter `request`: the request's message / comment id).
The extension uses the hub's public API: `POST /api/v1/assistant/conversations` with `purpose: "lark.agent"` and the configured machine (`machine_id`) to open a chat, `POST /api/v1/sessions/{m}/{s}/messages` to continue the same topic,
and `GET /api/v1/kb/chats/{id}/context` to get what the knowledge base knows about this conversation / project. Configuration lives in the extension (`agent`, `agent_launcher`, `agent_machine`); `lark_agent` in hub.json no longer takes effect.

## Not done yet

(Chatting with the Persona main chat from Lark at any time, or answering questions by tapping cards in Lark: that is the Lark bot, `docs/extensions/bots.md`.)

- Someone else's `/agent` (answering for others) is never taken. Answering others goes through guest conversations instead (`docs/assistant/assistant.md` ("Guest conversations"), #5195): a Lark extension's auto replies, off by default and turned on in its settings, with only `lark_agent_reply`.
