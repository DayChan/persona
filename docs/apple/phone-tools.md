# Phone data tools

`apple/Persona/PhoneTools/`: wraps iPhone capabilities as MCP tools so Claude on a computer can operate phone data. Toggles and authorization are under Settings › Connectors › This iPhone (called "Phone data" before 2026-11-01).

Route: Claude calls a `phone_*` tool → `personad mcp` → node.sock → hub → an online device (iPhone, Android, or Mac, WebSocket `tool_call`) →
the app queries/writes locally → `tool_result` returns along the same route.

Tool descriptions and schemas are registered by the app (iOS `apple/Persona/PhoneTools/`, Android `android/platform/phonetools/` (`PhoneToolCatalog.kt`)), not in the server's `server/internal/prompts/mcp/` (`docs/assistant/prompts.md`, "MCP tools").
Descriptions and parameters for tools with the same name must match exactly on both platforms (the hub merges tools from all phones by name; `PhoneToolCatalogTest` compares against the catalog exported by iOS).

Mail tools `mail_*` use the same `personad mcp`, but execute on the hub without going through the phone; see "Mail tools (mail_*)" below.

## Registration and toggles

| Entry | Purpose |
| --- | --- |
| `PhoneToolRegistry.all` | All tools (ignoring toggles), used by tests and settings |
| `PhoneToolRegistry.enabledSpecs()` | Only user-enabled data sources; sent in `register_phone` after connecting to the hub |
| `PhoneToolRegistry.call(name:args:confirm:)` | Executes one call, never throws: failure becomes `isError=true` + explanation |
| `PhoneToolRegistry.authorizationStatus(_:)` / `requestAccess(_:)` | Displays status and requests permission in settings |

Each data source (contacts / calendar / reminders / location / health / photos / device / clipboard / notifications) can be disabled individually in settings. Disabled sources
no longer appear in `enabledSpecs()`, so the model cannot see their tools, and calls are rejected too (preventing old sessions from continuing to call them).

The table above is for iOS (Mac uses the same code). **Android** uses `PhoneToolRegistry.kt` in `android/platform/phonetools/`, also with `all`, `enabledSpecs()`, and `call(…)`; all calls pass through this single gate:

- **Toggles** are under Settings › Phone data (Android has no "Connectors" group), one per data source (contacts / calendar / location / health / photos / clipboard / device information / notifications, no reminders). All default to off and are stored separately per pairing (`PhoneToolSettings`).
- **Foreground restrictions** are declared in the tool catalog: `PhoneToolCatalog.kt` marks `phone_location_current` and the two clipboard tools with `foreground = true`. When the app is not in the foreground, `enabledSpecs()` omits them and calls are rejected, telling the model to ask the user to open Persona and retry. Moving to the background during execution also interrupts the call.
- **Hub registration** is handled by `PhoneToolsRuntime.kt`: send `register_phone` after connecting. When toggles, foreground/background state, or the persistent service change (permissions are also rechecked on every return to the foreground), recalculate `enabledSpecs()` and resend only if it changed. Send an empty list on disconnect or unpairing. Tools are offered only while the app is in the foreground or the persistent service is enabled; with neither, none are offered.
- Before execution, check toggles, permissions, foreground state, and online status in order; writes requiring confirmation (only event creation) recheck after the user taps Allow. Duplicate delivery of the same `call_id` returns the previous result without executing again.

See `docs/android/android.md` ("App shell and direct page navigation," the Settings › Phone data item; "Phone data tools aligned with iOS (2026-11-02)") and `android/platform/phonetools/README.md` for wiring and details.

## Tool catalog

| Tool | Permission required | Confirmation | Description |
| --- | --- | --- | --- |
| `phone_contacts_search {query, limit?}` | Contacts | No | Fuzzy search by name, company, job title, email, or phone number (numbers may contain spaces/dashes); returns name, company, job title, labeled phone numbers and emails, and birthday (`--MM-DD` if no year) |
| `phone_calendar_events {start?, end?, query?, limit?}` | Calendar (full access) | No | Defaults to midnight today through 7 days later; returns id, title, start/end, all-day flag, calendar name, location, first 300 characters of notes, and attendees |
| `phone_calendar_create_event {title, start, end, location?, notes?, all_day?, calendar?}` | Calendar | **Yes** | Creates an event, selects a calendar by name, falls back to the default calendar |
| `phone_reminders_list {list?, include_completed?, limit?}` | Reminders | No | Defaults to incomplete only; returns id, title, due date, completion, priority, list, and first 300 characters of notes |
| `phone_reminders_create {title, due?, notes?, list?, priority?, location?}` | Reminders | **Yes** | Creates a reminder; priority 0/1–4/5/6–9. `location {title, latitude, longitude, radius_meters?, trigger: arrive\|leave}` makes it go off on arriving at or leaving a place (#6564, "Reminders by place" below) |
| `phone_reminders_update {id, title?, due?, notes?, location?}` | Reminders | **Yes** | Changes only the fields given; `due: null` removes the due date, `location: null` stops it going off at a place |
| `phone_reminders_complete {id}` | Reminders | **Yes** | Marks complete (using the id returned by `phone_reminders_list`) |
| `phone_reminders_delete {id}` | Reminders | **Yes** | Deletes a reminder the user no longer needs |
| `phone_location_current {}` | Location (while in use) | No | One-shot location, waits up to 15 seconds; returns latitude/longitude, accuracy, altitude, time, time zone, and reverse-geocoded address |
| `phone_location_search {query, limit?}` | Location | No | Finds a place by name or address (MapKit): name, address, latitude, longitude, up to 10 |
| `phone_health_summary {metrics?, days?}` | Health (read) | No | Defaults to steps over the last 7 days; metrics steps / distance / active_energy / heart_rate / resting_heart_rate / sleep / weight |
| `phone_health_write_sample {metric, value?, start, end?}` | Health (write) | **Yes** | Writes one sample: steps (integer) / distance (meters) / active_energy (kilocalories) / weight (kilograms, one instant) / sleep (an asleep interval, no value); intervals at most 24 hours, not in the future; one sample at a time, no bulk import or deletion |
| `phone_photos_recent {limit?, days?, screenshots_only?}` | Photos (read) | No | Recent photo/video metadata: local id, time, type, dimensions, screenshot/favorite flags, duration, latitude/longitude |
| `phone_photo_get {id, max_size?}` | Photos (read) | No | Returns the JPEG image itself (default longest edge 1280, quality 0.7) + metadata |
| `phone_device_info {}` | None | No | Model identifier, iOS version, battery and charging state, Low Power Mode, storage, network type, time zone, language, thermal state |
| `phone_clipboard_get {}` | None (system paste prompt) | No | Reads clipboard text |
| `phone_clipboard_set {text}` | None | No | Replaces clipboard text |
| `phone_notify {title, body, delay_seconds?}` | Notifications | No | Local notification, optionally delayed up to 24 hours |
| `phone_notifications_list {limit?}` | None | No | **Android only**: Persona's own currently displayed notifications (id, title, body, time), newest first, default 50, maximum 100; excludes confirmation-card notifications and cannot see other apps' notifications |

### Reminders by place (#6564)

"Remind me to buy milk on the way home": the assistant writes a reminder with a location alarm (`EKAlarm.structuredLocation` and `proximity`), and iOS watches the circle itself, like a reminder made in the Reminders app. Persona needs no background location and no "Always" permission; the alarm goes off on the phone and the watch even when Persona is closed. A location alarm goes off once.

- **When it goes off is the assistant's call** (`server/internal/prompts/assistant/claude_md.md`): something to do on the way is leaving where the user set out from; something to do once there is arriving. It says which it chose, and `phone_reminders_update` changes it.
- **Places** (home, office) are the user's saved places, below; the assistant reads them with `list_places` and never takes a place's coordinates from memory.
- **No duplicates**: the assistant lists reminders first and changes the matching one. "Done" completes it and "not needed" deletes it.
- The radius defaults to 150 m (100–2000): fixes indoors are often 50–100 m off, so a smaller circle misses.
- Not yet: going off near any shop of a kind ("when I pass a supermarket"), and Android, which has no reminder tools.

### Saved places (#1598)

The places reminders by place go off at belong to the user, not to the assistant's memory: **Settings › Assistant › Saved Places** lists them and lets the user add, change and delete them (`SavedPlacesView`, `SavedPlacesModel`). A place is a name (what the user calls it in a chat), an address, coordinates and the reminder's radius (150 m by default, 100–2000). In the editor it is found by searching an address or a name (MapKit, `LocationTool.find`, the same search as `phone_location_search`) or from the current location, and a map shows the circle.

- **Kept by the manager** in its settings (`assistant.places`, `server/internal/assistant/places.go`), per manager. `GET/POST /api/v1/assistant/places`, `PUT/DELETE /api/v1/assistant/places/{id}` (`docs/protocol/protocol.md`); names are unique (case aside), writes need full credentials.
- **The assistant** reads them with `list_places` before a reminder by place. When a place is missing, it finds it with the user (`phone_location_current` there, or `phone_location_search` and the user's pick) and keeps it with `save_place`, which adds the place or changes the one of the same name; so whatever it uses, the user sees and can correct.
- Changing a saved place does not move the reminders already set: each reminder keeps the coordinates it was made with.

Tools by platform:

- **iOS**: everything above except `phone_notifications_list`.
- **Mac** (`PersonaMac`): no health tools (Mac has no health data, `PhoneToolRegistry.healthTools`); otherwise the same as iOS.
- **Android**: no three reminder tools or `phone_health_write_sample`; adds `phone_notifications_list`. `phone_location_current` is available only while the app is in the foreground.

Results are consistently pretty-printed JSON text (`ToolContent.text`), with all timestamps in ISO 8601 including the local time-zone offset; date fields use
`yyyy-MM-dd`. `phone_photo_get` also includes a `ToolContent.image`.

## Confirmation flow

Writes (create event, create reminder, complete reminder, write health data) carry a `confirmation` closure; before execution, the app shows a confirmation card (Android only confirms event creation, since it lacks the other three write tools), with a summary such as "Create event ‘…’ 14:00 → 15:00"
or "Write health: weight 72.5 kg". If the user denies, the tool returns `isError=true` with `The user declined on the phone.`. Confirmation covers only one call;
if Claude calls again, it asks again.

- **One at a time** (#4456, `PhoneConfirmations`): a write that comes while another is on screen waits behind it and shows once that one is answered.
  Each answer goes to the confirmation it names: dismissing the dialog denies only the one it showed, and an answer counts once.
- **How long the hub waits** (`phoneConfirmWait` in `server/internal/hub/hub.go`, #4427): a confirmed write waits up to 30 minutes, other phone calls 2 minutes.
  The clock starts when the hub sends the `tool_call` to the phone (after waking it, if it had to), not when the dialog shows: a write queued behind another
  has less of its 30 minutes left when its turn comes, and the hub may have stopped waiting already. An approval after that is still carried out on the phone,
  and the agent was told to check before calling again. Claude Code's own limit for a silent MCP call is also 30 minutes; `personad mcp` sends progress
  while the call waits, so the hub's reply is the one the agent gets (#1498, `docs/protocol/protocol.md` "MCP tools").
- **A result while the socket is down** (#4456, `ToolResultOutbox`): Allow tapped just as the app comes back from the background can land before the socket
  has reconnected. The result is kept (only tool results, at most 16 per hub group) and sent once that group connects again; the hub matches it to the call
  by `call_id` whatever connection it comes on. One older than 30 minutes is dropped: the hub no longer waits for it.

Reads do not interrupt the user, but every call leaves a `tool_call` / `tool_result` record in the session.

`phone_clipboard_set` and `phone_notify` also change things on the phone, but do not show confirmation. Confirmation happens in the app; the hub's `connector_gate` does not govern phone tools. `mail_send` confirmation is requested by the hub under different rules; see the next section.

## Mail tools (mail_*)

Mail tools are supplied by the hub's mail connector (`mailConnector` in `server/internal/extmgr/mail_connector.go`, with descriptions and parameters in `server/internal/prompts/mcp/mail/`). They connect to IMAP / SMTP on the hub, without going through the phone.
Both the assistant and Agents can use them (connector `AgentTools: mail_*`). See `docs/extensions/mail.md` for accounts and credentials.

| Tool | Confirmation | Description |
| --- | --- | --- |
| `mail_accounts {}` | No | Lists configured email accounts (id, name, address, provider); call before search or sending to get `account_id` |
| `mail_search {account_id?, mailbox?, query?, limit?, before_uid?}` | No | Searches mail, returning uid, subject, sender, time, and summary; omitting `account_id` searches all accounts' inboxes; `mailbox` defaults to INBOX; `limit` defaults to 20, maximum 200; `before_uid` pages backward. Gmail uses Gmail search syntax; other providers use server-side full-text search. If server search is unavailable, the hub matches subject, sender, and recipients in a recent batch (not body), returning `before_uid` for backward pagination |
| `mail_read {account_id, uid, mailbox?}` | No | Reads one message in full: headers, plain text, HTML, attachment list; does not mark read |
| `mail_send {account_id, to, cc?, bcc?, subject, text, html?, in_reply_to?, references?}` | **Yes** (hub asks) | Sends mail; `to` / `cc` / `bcc` are arrays of `{name?, email}`; for replies, set `in_reply_to` to the original Message-ID and `references` to its original References plus that ID |

`mail_send` confirmation is separate from phone writes: it is a connector write passing through the hub gate (`server/internal/connector/gate.go`).
If no rule matches, the default is confirm: the hub sends an approval card to the phone ("Send mail to …: subject"), returning `The user declined mail_send.` if denied;
`connector_gate` rules in the manager's manager.json can set allow (send without asking) or deny (reject outright). Writes in health chats always ask; allow rules do not bypass confirmation (`docs/assistant/health.md`).

## Permissions

System permissions are requested at two points: when the user taps "Authorize" in settings (`PhoneToolRegistry.requestAccess`), or on the first actual use of a tool.
Notifications are an exception: `phone_notify` displays its own permission prompt on the first call. Other tools encountering "Not requested" only return an error and tell the model to ask the user to authorize;
they do not show system dialogs mid-execution (health only requests read permission again after a grant, to add newly introduced types). Previously denied permissions can only
be changed in iOS Settings.

| Data source | Info.plist key | Authorization |
| --- | --- | --- |
| Contacts | `NSContactsUsageDescription` | `requestAccess(for: .contacts)`, full or limited |
| Calendar | `NSCalendarsFullAccessUsageDescription` | iOS 17+ `requestFullAccessToEvents()` |
| Reminders | `NSRemindersFullAccessUsageDescription` | iOS 17+ `requestFullAccessToReminders()` |
| Location | `NSLocationWhenInUseUsageDescription` | `requestWhenInUseAuthorization()` |
| Health | `NSHealthShareUsageDescription`, `NSHealthUpdateUsageDescription` (+ entitlement) | `requestAuthorization(toShare: five write types, read: …)` |
| Photos | `NSPhotoLibraryUsageDescription` | `.readWrite`, possibly limited (only user-selected photos are readable) |
| Notifications | None (runtime authorization) | `requestAuthorization([.alert, .sound, .badge])` |

Health writes write only the one sample confirmed on the phone: the confirmation summary and written data come from the same argument parsing (`HealthSample`, values to at most two decimal places; what the card shows is what gets written). Invalid arguments make the card say "Invalid parameters; nothing will be written." Samples are marked as manually entered (`HKMetadataKeyWasUserEntered`). Unlike read access, the system reports write permission to the app: users who previously granted only read access (before writes existed) see another system health permission prompt after confirming their first write. Denial returns an error telling them to enable access in iOS Settings › Health › Data Access & Devices › Persona.

Health has a hard iOS limitation: **the system never tells an app whether read permission was granted** (to prevent information leakage). Settings therefore show whether permission has
been requested; denial appears as empty query results. The tool description explains this, so the model cannot infer anything beyond "no data" from empty results.

Android's `phone_health_summary` (implemented in #278, omitted when #632 merged into the integration branch, rebuilt in #820) reads Health Connect; its name, description, and parameters match iOS exactly (including
"read permission is never reported to the app," because the hub stores a single tool catalog). The difference: Health Connect tells the app which read permissions were granted,
so settings can show "Only some data allowed," and unauthorized metrics appear in `not_granted`; if none are granted, return an error rather than an empty result.
Health Connect is built into Android 14 and later; on 8–13 it must be installed separately (usually absent on phones without Google services), in which case the tool returns "This phone has no Health Connect."

## Implementation notes

- Connection reuse per account does not apply: each tool creates its own query. EventKit's `EKEventStore`, `CNContactStore`,
  and `HKHealthStore` are created on demand to avoid passing non-Sendable objects across actors.
- Convert all network/system callbacks to async with `withCheckedContinuation` + a one-shot `SingleResume` wrapper, ensuring callbacks triggered multiple times
  (Photos degraded image → original, HealthKit error callbacks) do not resume twice.
- Blocking or delegate-based operations such as location, network state, and contact enumeration live in helpers outside MainActor
  (`OneShotLocation`, `NetworkProbe`, `ContactsTool.fetch` use background queues).
- EventKit objects are not Sendable; reminders are converted to value-type `ReminderRow` in the callback before being returned.
- Image scaling uses `UIGraphicsImageRenderer`, keeping the longest edge within `max_size` (default 1280, maximum 2048).

## iOS limitations

- **No API to read SMS / iMessage / call history** (`MessageUI` can write, not read).
- **No API to read Notes, WeChat, or other apps' data**. The only routes are "screenshot + `phone_photo_get` to inspect the image" or the user manually pasting into the clipboard.
- **The app must be connected to the hub**: the hub can send `tool_call` only to a phone that has sent `register_phone`. In the foreground or just after entering the background, the WebSocket
  remains available to receive calls; when iOS suspends the iPhone app, the hub first sends a silent push to wake it in the background and waits up to 20 seconds for connection (`hub.go`, `wakePhone`). Only the most recently registered device is woken; if it was woken within 30 seconds, skip the push but still wait 20 seconds. Hub push configuration is required; Android is not woken.
  If waking fails, return an error asking the model to have the user open Persona and retry. A call waits at most 2 minutes for the phone (a confirmed write 30 minutes, see "Confirmation flow"); timeout also returns an error.
- **Background location is unavailable**: only "While Using" permission is requested, so location fails outside the foreground.
- **Clipboard reads trigger a system prompt**: when reading content copied in another app, iOS asks "Allow Paste?" and the user must allow it.
- **Health data is aggregated by day only**: no individual-record queries; steps/energy are totals, heart rate is daily average/minimum/maximum, and sleep is daily time asleep
  (the `asleep` types, excluding "In Bed").
- **Photos access may be limited**: if the user grants only selected photos, `phone_photos_recent` returns only those.
- **Notifications can be sent only when the app has execution time**: delayed notifications are scheduled by the system and arrive on time, but the call itself must succeed, meaning the app is connected to the hub or can be woken.
- Returned content (including mail/photo metadata) enters Claude's context and is therefore given to the model for processing.
