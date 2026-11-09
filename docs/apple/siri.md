# Deeper Siri integration (Ask Persona, personal Agents, Control Center)

Building on `docs/apple/intents.md` (Ask Claude / status / approve first / open session), this adds Siri entry points for the assistant and personal Agents. Every intent connects directly to the hub in the background:
it reads `Keychain.serverConfig` afresh each time (the hub address + device token saved during pairing). The App does not need to be in the foreground, and no WebSocket is required.

## Intents

| Intent | Title | Purpose | Run mode |
| --- | --- | --- | --- |
| `AskPersonaIntent` | Ask Persona | Sends a question to the assistant's main chat (`main` from `GET /api/v1/assistant`; if none exists, opens one with `POST /api/v1/assistant/conversations` and `main:true`). Polls `items?after_seq=` for up to about 24 seconds, waits until the session is no longer working, then reads the assistant's text aloud and displays a card (question + full answer). If the answer is slow, says "Sent to Persona. It's still thinking." | Background |
| `TalkToPersonaIntent` | Talk to Persona | Starts a duplex voice chat when CarPlay is connected (`docs/apple/carplay.md`). Otherwise asks the user to connect CarPlay first and does not start. | Background |
| `AgentTaskIntent` | Give an Agent a Task | `POST /api/v1/agents/sessions/{id}/messages`, with a new UUID for `clientId` each time (hub deduplication + outbox). Choose a session through `PersonalAgentEntity`; if none is selected, use the first in the list with `canSend`. | Background |
| `AgentsStatusIntent` | Personal Agent Unread | `GET /api/v1/agents/sessions`: reports how many sessions have how many unread messages, listing the first three; if none are unread, reports the three most recent. | Background |
| `AddTodoIntent` | New To-Do | `POST /api/v1/todos` (`{"title", "notes"?}`, see `docs/assistant/todos.md`), in the App's currently selected hub group (`hub.group`, the same as the To-dos page; falls back to the first group if it is missing). Reads "To-do saved: '…'" and returns the title for use in subsequent Shortcuts actions. If an old hub lacks the to-do endpoint (404), asks the user to update the hub first. | Background |
| `OpenPersonalAgentIntent` | Open Personal Agent | Tapping a personal Agent session in Spotlight opens that conversation directly (Agents › Personal Agents › session; back goes to the personal Agents list; an open settings sheet is closed first). `AppModel.openPersonalAgent` is handed to `ChatsView`, which, as with `openChat`, pushes it once the list has appeared and the navigation stack is still. | Foreground |
| `OpenPersonaAssistantIntent` / `OpenPersonaAgentsIntent` | Open Persona Assistant / Agents | Intents behind the controls for Control Center, the Lock Screen, and the Action Button. | Foreground |
| `StartPersonaTypingIntent` | Type to Persona | Behind the Type to Persona control: opens the main chat (`AppModel.openTypingConversation`; with no main chat yet, the new conversation page) and leaves a one-shot request (`TypingRequest`) that the chat's composer takes once its page is in front and the App is active, focusing its field so the keyboard comes up. An ask no composer took within 30 seconds is dropped. | Foreground |

`PersonalAgentEntity` is an `IndexedEntity`: whenever the Agents page finishes loading, it calls `CSSearchableIndex.default().indexAppEntities(...)`
and `PersonaShortcuts.updateAppShortcutParameters()`, so Spotlight can find sessions and Siri phrases containing session names can match them.

## Phrases (`PersonaShortcuts`, available immediately after installation, no manual setup)

- Ask Persona: "问 Persona" (Ask Persona), "用 Persona 问一下" (Ask with Persona), "Ask Persona", "Ask Persona a question" → Siri then asks "What do you want to ask Persona?"
- Give an Agent a Task: "用 Persona 让 Agent 去做" (Have Persona give an Agent a task), "让 Persona 的 <会话名> 去做" (Have Persona's <session name> do it), "Persona 交给 Agent" (Persona, hand it to an Agent), "Have Persona agent do something", "Send a task to <session name> with Persona"
- Personal Agent Unread: "Persona 个人 Agent 未读" (Persona personal Agent unread), "Persona Agent 有什么新消息" (What's new from Persona Agents), "Check Persona agents"
- New To-Do: "用 Persona 新建待办" (Create a to-do with Persona), "Persona 记个待办" (Persona, note a to-do), "Persona 加个待办" (Persona, add a to-do), "Add a to-do in Persona", "New Persona to-do" → Siri then asks "What's the to-do?"; notes can only be entered in the Shortcuts App.
- The existing Ask Claude / status actions each gain an English phrase.

`String` parameters cannot appear in phrases (see `docs/apple/intents.md`), so Siri asks for the question / task contents after the invocation phrase.

## Action Button and Control Center

- Action Button: **Settings › Action Button › Shortcut**; select "Ask Persona", "Give an Agent a Task", or "New To-Do" directly (App Shortcuts appear automatically).
- Control Center / Lock Screen: the `PersonaWidgets` extension adds three `ControlWidget`s: `AskPersonaControl` and `PersonaAgentsControl` open
  the App's assistant page / Agents page, `TypeToPersonaControl` opens the main chat with the keyboard up (for when dictation is not accurate enough).
  Their intents live in `LiveActivity/PersonaControlIntents.swift` (compiled into both App and extension; `perform` changes `AppModel` only in the App).
- Control kinds are the identity iOS saves with a placed control: a kind the extension stops registering leaves that control broken. Ask Persona and
  Persona Agents keep the kinds every TestFlight build shipped (`LegacyNames.controlAskKind` / `controlAgentsKind`). The interim kinds
  `persona.control.ask` / `persona.control.agents` were also registered for a while (builds from 2026-11-07), which listed each control twice
  in the gallery; they are no longer registered. WidgetKit has no way to keep a kind working while leaving it out of the gallery
  (`ControlWidgetConfiguration` only has `displayName`, `description`, `promptsForUserConfiguration` and `pushHandler`, and a widget bundle
  can only leave widgets out by OS version), so a control placed from one of those entries has to be added again.

## Not implemented / deferred

- Apple Intelligence assistant schemas such as `@AppIntent(schema: .assistant...)`: none fits "ask a private assistant" (existing schemas cover domains such as mail / photos / books), so they are not integrated.
- A personal Agent's Spotlight result carries no hub group: with several hub groups connected, the opened conversation uses the App's selected group, and a session in another group shows an error on its page.
- Ask Persona has a 24-second answer limit: long tasks cannot be read in full; view them in the App's main chat.
- For long chats in the car, see `docs/apple/carplay.md` (Siri is only the entry point; the chat uses duplex voice).
- Controls can only open the App. Asking directly from a control would require App Intents text input, which controls do not support.

## Verification on a phone

1. After installing through TestFlight / Xcode, open the App once (and pair it), then visit **Agents › Personal Agents** once so sessions are indexed.
2. Say "Ask Persona" to Siri, then ask a question: it should read the answer and show the full text on a card; the App's main chat should contain this turn.
3. Say "Have Persona agent do something", choose a session, and describe the task: it should reply "Sent to '…'"; that session in the Lark gateway should receive the message. The same task must not be duplicated (clientId).
4. Say "Check Persona agents".
5. Say "Add a to-do in Persona", then give a title: it should reply "To-do saved: '…'", and the App's To-dos page should contain the new entry.
6. These actions should appear under **Shortcuts App › Persona** and be selectable for the Action Button; **Control Center › Add a Control › Persona** should contain three controls, each once (Ask Persona, Persona Agents, Type to Persona); Type to Persona should open the main chat with the keyboard up.
7. Searching for a personal Agent's session name in Spotlight should find it; tapping it opens that conversation directly, and back in the top-left returns to the personal Agents list. Try once each with the settings sheet open, with the App in the background, and with it not running.

Unit tests: `PersonaTests/SiriIntentsTests.swift` (answer extraction, polling completion, spoken text, session selection, unread text), `PersonaTests/IntentsTests.swift` (new to-do text, old hubs, the hub group read).
After compilation, `Persona.app/Metadata.appintents/extract.actionsdata` should contain the new actions and autoShortcuts.
