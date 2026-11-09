# Health space (#2691)

Health-related chats are split out of the assistant's main chat into a separate **Health** space: its own entry point, its own chat directory, its own memory.
The first version is modeled on ChatGPT Health and starts with the hub and the web (#2789); the iOS and Mac entry points then reuse the same APIs (#2901). Android does not have it yet.

## Why split it out

Health information is more private than work information, and longer-lived: allergies, medications, checkup results need to stay useful once recorded, but they should not show up in the context of work chats,
nor be read by the main chat's tools or written into the knowledge base. Inside the main chat, memory, tools and the chat list are all shared and cannot be separated.
With a separate directory, Claude Code's per-directory auto memory, CLAUDE.md and chat list separate naturally.

## What we borrow from ChatGPT Health

| What ChatGPT Health does | What Persona v1 does | Later |
| --- | --- | --- |
| A separate Health entry in the sidebar (desktop) / menu (phone), opening an independent space ([1]) | A separate **Health** entry in the web sidebar, `/health`; **Health** in the iOS and Mac assistant sidebar | Health entry on Android |
| Health home: recent data and trends, synced records, suggested questions, past health chats, connected accounts ([2]) | `/health` page: a one-line privacy note, data sources (Apple Health / Health Connect via the phone), suggested questions, past health chats, health memory | Recent trend cards on the home page (using the structured cards from #886) |
| Health memory is stored separately and does not flow back into regular chats; users can view and delete it ([3]) | The Health space has its own directory and its auto memory belongs only to it; viewable and deletable on the web; the main chat's tools cannot see health chats | Give the Health space only health-related tools |
| Talking about health in a regular chat suggests moving to Health ([4]) | Not done | Suggest "Continue in Health" when the main chat touches on health |
| Connected data is not used for training, with extra encryption and isolation ([5]) | All data stays on your own hub and computers; `kb_update` from a health chat is rejected (blocked in code); not sending to other chats relies on chat tools only accepting the same space; push and `phone_notify` notifications carry no health content; handing to an Agent, sending email and Lark each need confirmation on the phone ("outbound") | More data sources: medical records, checkup report uploads, heart rate / HRV from recordings (#1950) |
| Does not replace a doctor's diagnosis and treatment | CLAUDE.md states: no diagnosis, no prescribing, no changing prescriptions; on danger signs, the first sentence tells the user to seek medical care | |

Sources:

1. https://www.foxnews.com/tech/chatgpt-health-promises-privacy-health-conversations
2. https://chat.openai.com/health
3. https://livemint.com/technology/tech-news/openai-launches-a-dedicated-experience-for-medical-questions-in-chatgpt-heres-how-it-works-11767866024939.html
4. https://www.thetechoutlook.com/new-release/software-apps/openai-introduces-a-dedicated-space-for-health-conversations-in-chatgpt-chatgpt-health/
5. https://openai.com/index/health-in-chatgpt/

## How directories and memory are isolated

- **Directory**: health chats run in `<data dir>/health` on the assistant's machine (default `~/.persona/health`), alongside the assistant's own `<data dir>/assistant`.
  When the hub asks node to prepare the directory (`assistant.prepare`), the relative path it passes is `.persona/health`.
  `identity.HealthDir`, `identity.SpaceHealth`, `identity.SpaceOf` are in `server/internal/identity/paths.go`; `IsAssistantDir` also holds for the health directory,
  so health chats, like assistant chats, are "assistant chats" (`assistant: true`) and use the same tools, authorization and questions.
- **CLAUDE.md**: the Health space has its own `server/internal/prompts/assistant/health_claude_md.md` (golden: `server/internal/assistant/testdata/prompts/health_claude_md.golden`):
  talk only about health; read data with `phone_health_summary`, record data with `phone_health_write_sample` (confirmed on the phone); do not write to the knowledge base or send health content to other chats; when the user wants to share with someone (doctor, coach, family), help send it, with the send confirmed on the phone;
  no diagnosis, no prescribing; on danger signs, tell the user to seek medical care first; when discussing numbers, attach a chart card by default ("Charts" below). Connector and plugin skills are installed the same way as in the assistant directory, but the instructions plugins give the assistant (`plugins.Instructions`) are not added to the health CLAUDE.md: those were written for the main chat's work scenarios, and v1 deliberately leaves them out.
- **Memory**: Claude Code's auto memory is per working directory (`~/.claude/projects/<encoded dir>/memory/`). Only health chats read and write the health directory's memory; it is not in the main chat's context, and vice versa.
- **Files** (#3282): the main chat's Claude can read files on the computer with Read / Bash; separate directories do not mean unreadable. So the main chat (not the Health space's assistant chat) starts with deny rules:
  Read and Edit may not touch the health directory or its Claude project directory (memory, transcripts), `server/internal/node/healthdeny.go`; the CLI gets them via `--disallowedTools`, the Agent SDK via `permissions.deny` in settings.
  Deny rules override every allow: even after tapping **Always Allow in Session** or switching to bypass mode, the files cannot be read (tested on Claude Code 2.1.318).
  **Always Allow in Session** for file tools in the main chat also only allows the directory of that file (`hooks.go` `fileRuleScope`), instead of opening the whole tool.
  **Not blocked**: Bash can build the path in any number of ways (`cd .. && cat he*`), so matching command text cannot catch all of them; blocking it fully needs Claude Code's sandbox or OS-level isolation. When the main chat runs on Codex, this rule does not exist yet either.
  The memory API with `?space=health` reads and writes exactly this memory.
- **node**: node recognizes the Claude project directory that corresponds to the health directory (`isAssistantProject` in `server/internal/node/sessions_offline.go`), and treats health chat transcripts as assistant chats, same as the assistant's.
- **Which space**: in the session list the hub sends, every health chat carries `space: "health"` (`proto.Session.Space`, filled from cwd in `server/internal/hub/hub.go` and `server/internal/hub/assistant_link.go`);
  the assistant's own chats do not have this field. To decide which space a chat belongs to, the assistant looks at `space` first and falls back to the directory (`spaceOf` in `server/internal/assistant/health.go`).

## API

For the exact protocol, see the "Assistant" table in `docs/protocol/protocol.md`.

| Endpoint | How the Health space uses it |
| --- | --- |
| `POST /api/v1/assistant/conversations` | With `"space":"health"`, starts a new chat in the health directory (writing the health CLAUDE.md first). Combined with `"main":true` it returns 400 (the Health space has no main chat); an unknown `space` returns 400 |
| `GET /api/v1/assistant?space=health` | Lists the Health space's chats; `cwd` is the health directory. Without `space` it lists only the assistant's own chats, and health chats no longer appear. `?archived=1` works the same; an unknown `space` returns 400 |
| `GET /api/v1/assistant/memory?space=health` | The Health space's memory `{"files":[…],"dir"}` |
| `PUT / DELETE /api/v1/assistant/memory/{name}?space=health` | Edit or delete one memory in the Health space |
| `sessions` push, session list | Health chats carry `space: "health"` |

Once a chat is open it behaves like any other assistant chat: sending, archiving, renaming and compacting all use the existing endpoints (`/api/v1/assistant/conversations/{id}/…`, `/api/v1/sessions/{mid}/{sid}/…`) without `space`.

## Tool isolation

The `conversation*` tools in a chat only see chats in the same space (`server/internal/assistant/conversations.go`: `spaceConversation`, `ownSpaceConversation`, `selfSpace`):

- `conversations_list`: in a health chat it lists only health chats; in an assistant chat only assistant chats.
- `conversation_read`, `conversation_rename`, `conversation_archive`, `conversation_send`: a target in another space is treated as not found.
- `conversation_new`: a new chat started from a health chat stays in the Health space.
- Calls not made from a chat (no caller session id) count as the assistant's own space.

- `kb_update` from a health chat fails outright: health information does not go into the knowledge base (`assistant/tools.go` `runTool`). Knowledge base reads (`kb_search` etc.) work as usual.
- Scheduled follow-ups and scheduled tasks created in a health chat (`schedule_followup`, `schedule_routine`) record their space (`Task.space`); when one fires and needs a new chat, it opens in the Health space.
- When the calling chat's space cannot be determined (the assistant's machine cannot list it for the moment), it is treated as the main space: it can only reach main-space chats, and health chats can never be listed or read. The worst case is a health chat temporarily seeing the main space's list; health content is never exposed elsewhere the other way round.
- `list_followups` lists only this space's scheduled tasks; editing with `schedule_routine` and an id, or deleting with `delete_routine` / `cancel_followup`, treats tasks in another space as not found.
- Extensions' skills and gated commands reach both spaces (there is no per-space contribution). Every assistant process carries its space in `PERSONA_SPACE` (`health` / `main`, #6003, `docs/extensions/extensions.md` "Assistant"), so a command meant for health data alone can refuse in the main space; a `cd` into the health directory does not change it.
- All other tools (phone tools, reminders, mail, Lark, Agents…) exist in both spaces: health information can be sent to whoever the user wants; the rules are under "Outbound" below.

## Outbound (#3194)

The boundary is not "health information may not leave" but "it may not leave without the user knowing". Emailing checkup results to a doctor, or sending them over Lark to a fitness coach or family, is legitimate and still works.
The decision always depends on the chat's space (the trusted session directory), never on a model judging "does this sentence count as health".

| Exit | In a health chat | Code |
| --- | --- | --- |
| Push: turn ended, `notify_user`, plan waiting for you to start, scheduled task failed | Only says "New in your health space. Open Persona to see it.", without the body, chat title or task name | `hub/machine.go` `doneAlert`, `assistant/tools.go`, `tasks.go`, `schedule.go` |
| Phone local notification: `phone_notify` | The hub replaces the title and body with the same localized notice as `notify_user` before forwarding to the phone; no health content reaches the notification | `assistant/fleet_tools.go` `mcpCall`, `assistant/health.go` `healthNotice` |
| Push for question / authorization cards | The lock screen likewise only says **Health**; the card content is viewed in the App | `hub/push_payload.go` `interactionPayloadIn` (looks at `ix.Cwd`) |
| Lark bot, plugin channels | Forwarded `notify_user` only says there is a new message; answers in a scheduled task's chat are not mirrored; "all cards" and "all questions" exclude health chat cards (the chat the channel itself is driving works as usual) | `assistant/feishu_cards.go`, `feishu_push.go`, `channels_plugin.go` |
| `agent_send` / `agent_start` | Confirmed on the phone every time; the card states that once handed over, the record on the other side is not deleted with the Health space | `assistant/health.go` `confirmOutbound` |
| `task_plan` | Never starts automatically; always waits for the user to tap start on the plan card | `assistant/tasks.go` |
| `mail_send` and connector write operations | Confirmed on the phone every time; gate allow rules do not let it through (deny still rejects) | `connector.ConfirmEach`, `callConnector` in `extmgr/connectors.go` |
| Lark write operations (lark-cli, command gate) | Confirmed on the phone every time; allow rules do not let it through, and there is no **Always Allow in Session**; reads work as usual (deny still rejects). Both entrances hold it: the gated command (`larkCheck`) and the node's gate proxy, which a script can call without the command: a health process's proxy address lets a write through only with the one-time grant `larkCheck` leaves once the phone confirmed it. Both decide the space from a record made when the conversation's process is started, not from the directory a command runs in (a `cd` moves it); a hook directory that is itself a health space counts too, so a mix-up can only ask more | `node/larkgate.go` `larkCheck`, `node/gateproxy.go` `ServeHTTP` |

The health CLAUDE.md directs the model to use `notify_user` for notices. Both notification tools use the existing Health title and generic message in English, Simplified Chinese or Japanese.
Explicit reminders created with `phone_reminders_create` keep the user-requested text and require confirmation on the phone. Memory and chats within the same space need no confirmation.

Known limit: a gate proxy address does not know who calls it. A process in a health conversation could in principle learn the address of another conversation (another process of the same user has an environment that can be read), the same kind of gap as the Bash one under "How directories and memory are isolated": closing it needs the sandbox or OS-level isolation (see "Later").

## Web entry point

- A separate **Health** item in the sidebar opens `/health`.
- Home: a one-line privacy note (chats and memory stay in the Health space, data is on your own hub, not a substitute for a doctor), data sources (Apple Health / Health Connect, via the phone's `phone_health_summary` / `phone_health_write_sample`; Persona must be open on the phone and authorized),
  suggested questions (tap one to start a health chat with it), past health chats, health memory (view, delete).
- Tapping a chat opens `/health/<id>`, the same chat page as other assistant chats.
- Health chats no longer appear in the main chat's history list.


Verification image removed; see the associated PR.

Verification image removed; see the associated PR.

## iOS and Mac entry points (#2901)

- Entry: a **Health** row below **Knowledge** in the assistant sidebar (in the new shell's drawer it sits between **Knowledge** and **Machines**; in the Mac split-view list it is below **Scheduled Tasks**), pushing the Health page (`apple/Persona/Views/Assistant/HealthView.swift`, shared by iOS and Mac).
- Health page: a one-line note, suggested questions (tap one to start a health chat with it), health chats (`GET /api/v1/assistant?space=health`, merged with pushed ones carrying `space: "health"`; swipe / right-click to archive),
  health memory (pushes the memory page `AssistantMemoryView(space: "health")`; view, edit and delete all carry `?space=health`), data sources, privacy. At the bottom is an input box; sending does `POST /api/v1/assistant/conversations` with `"space":"health"`, never set as the main chat.
- Health chats that are started or tapped open in the Persona page's chat, the same chat page as other assistant chats; go back to the Health page via **Health** in the sidebar.
- The sidebar's **Recent**, **Archived** and search do not list health chats (`AssistantLists` splits by `Session.assistantSpace`; health ones are in `AppModel.healthConversations`).
- Screenshot arguments: `-tab assistant -openAssistantPage health` (new shell: `-openShellPage health`). Unit tests: `PersonaTests/HealthModelTests`.

## Charts (#3140)

Health answers are mostly a set of numbers, and a chart is faster to read than text. In the second version the Health space attaches a `persona-card` **by default** when discussing numbers (the chart cards from #886; format and rendering in `sdk/go/wire/personacard.go`), and each client draws it as best it can:

| Content | Card | Example |
| --- | --- | --- |
| Per-day trends over the last 7 days (or longer): steps, sleep, weight, resting heart rate | `line` | Steps over the last 7 days |
| Calories per meal (comparing a few items, calories burned per workout) | `bar` | Calories per meal today |
| Daily calorie breakdown (how much each part takes of one total) | `stacked_bar` | Today's calories: breakfast / lunch / dinner / remaining |
| A single key metric | `stat` | Steps today |
| Details (each food, each item on a lab report) | Markdown table | |

- **Instructions**: in the Health space's CLAUDE.md (`server/internal/prompts/assistant/health_claude_md.md`, "Charts by default when discussing numbers"): pick per the table above, put the conclusion and key numbers in the text with the card after it, at most three cards at a time, and do not fill days with no data with 0.
  `TestHealthClaudeMDTeachesCharts` checks that all four card types have examples and that every example is a card all clients can draw.
- **Web**: a health chat is the assistant's chat page (`ChatView`); code blocks are drawn as charts via `ui/AssistantCard.tsx` as before, with no separate implementation. What each client draws is governed by the cases in `schema/extension/examples/persona-card/` (web: `lib/assistant-view.test.ts`).
- **Lark**: cards cannot draw charts, and the JSON in a code block is not an answer either, so before sending, the bot rewrites each `persona-card` as a line or two of numbers (`stat` as one bold number, `line` / `bar` as a ▁▃█ sparkline plus the individual numbers, `stacked_bar` as a small two-column table); see `docs/extensions/bots.md` ("Cards").

To see it: in the default mock hub (`python3 scripts/mock/mock_hub.py`, numbers are made up), the Health space's three chats "Sleep this week", "Step goal" and "What I ate today" all have charts.

Verification image removed; see the associated PR.

Verification image removed; see the associated PR.

## Checkup reports (#4546)

Checkup reports are usually PDFs dozens of pages long. If only the chat history remains after reading one, there is no way to compare across years, so the original and an item-by-item record both stay in the health directory.

- **Upload**: attach a PDF or another file in the input box of the web Health page (home and health chats); it goes to the assistant's machine via `POST /api/v1/machines/{mid}/files?dest=health`
  (`proto.UploadDestHealth`). node stores it in the health directory's `reports/` (`originalname-YYYYMMDD-HHMMSS.ext`, 0700 / 0600, never expires; the computer's manager's health space: `health-<id>/` for another manager than the built-in one, `docs/assistant/managers.md` ("Directories on a computer"). Reports uploaded before the computer moved to another manager stay in the previous manager's space there: the new owner does not list them, and they show again if the computer moves back), and the path is inserted into the input box and sent with the message.
  Images are still sent as images (Claude looks at them directly) and are not saved to disk. The main chat cannot read `reports/`: it is inside the health directory denied by #3282.
- **Record**: the health chat reads the original and writes a same-named `.md` next to it: report date (the exam date printed on the report), institution, type, a table of "Item | Result | Unit | Reference range | Abnormal",
  values copied as in the original and "illegible" where unreadable, ending with the doctor's conclusions and advice. Photos sent directly in the chat get a record the same way. The procedure is in the health CLAUDE.md ("Data").
- **Compare**: when asked about "the past few years" or "compared with last year", it reads all records under `reports/`, merges the same item ("triglycerides" and "TG") and compares by date, drawn as a `line` card; originals without a record get one first.
- **No approval every time**: the health chat's `--allowedTools` includes `Edit(//<health dir>/reports/**)` (`healthReportRules` in `node/healthdeny.go`), so writing records does not pop a confirmation on the phone;
  everywhere else in the health directory still asks according to the chat's permission mode. Claude Code matches only `Read` and `Edit` rules by path; `Edit(path)` covers every file-modifying tool, and `Write(path)` never matches,
  so tapping **Always Allow** on the phone for Write / MultiEdit / NotebookEdit also records `Edit(<dir>/**)`.
- Tested (Claude Code 2.1.318, two fake reports): both records were copied correctly item by item, the date was the exam date on the report, "TG" and "LDL-C" were matched with last year's "triglycerides" and "low-density lipoprotein", and the answer had three `line` cards.
- iOS / Mac (#4587): files picked via the Health page's input box (both interface styles) and via **Files** in a health chat's input box, as well as files dragged in on Mac, all carry `dest=health` (`FileUploads.start(…, dest:)`);
  photos are still sent as images, and videos still go to the temporary directory. A hub / node older than #4546 returns 400 / "unknown upload destination"; the App reports the upload failed, which goes away after updating the hub.
- **Entry point (#4656)**: with only the input box's `+`, it is not obvious you can upload, so the Health page has an explicit **Upload a Checkup Report**: on the web below the home input box, on iOS / Mac at the front of **Try Asking**.
  Tapping it picks a PDF or images (images are also uploaded as files, with the original kept in `reports/` as well). Once everything is uploaded, a health chat starts automatically (`views/health/reports.ts`, `FileUploads.startAll`) with the paths in the first message;
  it still starts if you leave the page mid-upload. To test the whole path: iOS launch argument `-showReportUpload <file name>` (on the mock hub, look for `POST …/files?dest=health` and `POST /api/v1/assistant/conversations`).
- **View and delete (#4998)**: **Checkup Reports** on the Health page lists reports in `reports/` (`server/internal/node/healthreports.go`): an original and its same-named `.md` record count as one report; record-only reports (photos sent in chat) count too.
  View the structured record, open the original or delete. Deletion is permanent and removes both original and record; **content already mentioned in chats and health memory is not deleted with them**, as the confirmation explains. Names must identify a single regular file in `reports/`; symlinks, directories and `../` are rejected.
  On the web it is on the home page (`views/health/HealthReports.tsx`); iOS / Mac use a separate page (`HealthReportsView.swift`). An old node returns 409 `node_outdated`, which the page displays. APIs live in `server/internal/devices/healthreports.go`, forwarded by hub (`devicewire.Routes`).
  Screenshot arguments: `-openReportRecord YES` (open the first report's record directly), `-showReportDelete YES` (show delete confirmation directly), both starting from the reports page.
- Not yet: Android (no Health space entry point yet).

## Not in v1

- Android entry point: will reuse the APIs above.
- Packaging the diet-advice extension: another Agent is doing it.
- Suggesting moving to the Health space when the main chat touches on health.
- Migrating existing health content discussed in the main chat: not migrated; old memory stays in the assistant directory.

## Health trends on iOS (#3272)

- `apple/Persona/Views/Health/HealthTrendsSection.swift`: **Health Trends**, steps (`bar`), sleep and weight (`line`; `stat` when there is only one day) over the last 7 / 30 days, drawn as `persona-card` with the same renderer as cards in health answers (`AssistantViewCard`).
- Data is read from Apple Health directly on the phone (`HealthTool.daily`, the same queries as `phone_health_summary`), not via the hub. If health permission has not been requested, it shows **Allow Access**; if it cannot read, it explains where to turn it on.
- It is just a component; the Health space entry point and past chats are in #2901, whose Health home page includes this section directly.
- Screenshots: `-showHealthTrends sample` (the simulator has no health data, so sample data is used), `empty`, `ask`, `YES` (reads the device).

## Later

- A Health tab on the phone (#2901), with **Health Trends** above on its home page.
- Suggest "Continue in Health" when the main chat touches on health.
- diet-advice push notifications go into the Health space.
- The main chat reading the health directory via Bash or via Codex: block it fully with a sandbox or OS-level isolation (after #3282).
- More data sources: medical records, checkup report upload on Android (web and iOS: see "Checkup reports" above), heart rate / HRV from recordings (#1950).

## How to verify

- Server (inside `server/`): `go test ./internal/assistant -run Health` (`server/internal/assistant/health_test.go`: starting health chats, separate lists, `main` and unknown `space` rejected, tools do not cross spaces, outbound, golden),
  `go test ./internal/hub -run Health` (push without body), `go test ./internal/connector -run ConfirmEach`, `go test ./internal/node -run LarkGateHealth`, `go test ./internal/identity` (health directory, `SpaceOf`, `IsAssistantDir`).
  After changing `health_claude_md.md`, update the golden and review the diff (`docs/assistant/prompts.md`). Charts: `go test ./internal/assistant -run HealthClaudeMDTeachesCharts`, `go test ./internal/feishubot -run PersonaCards`.
- Web: `cd web && npm test && npm run build`.
- iOS / Mac: `PersonaTests/HealthModelTests` (lists and memory carry `space`, merging pushes, sidebar does not list health chats, new chats carry `space`); build `Persona` and `PersonaMac`.
