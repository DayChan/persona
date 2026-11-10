# Meeting minutes (offline meetings)

An in-person meeting or hallway discussion becomes a recording, a transcript, and structured minutes. This is not a new recording product: it is the recordings already in the knowledge base (#3235, [recording.md](../apple/recording.md), [knowledge.md](./knowledge.md) "Recordings"), with a minutes document instead of a short summary when the recording is a meeting.

The iPhone and Apple Watch entry points, the Live Activity, and showing minutes inside the app are #2694. This document is the contract they follow. Mac recording is #2704 and uses the same fields.

## Why the recordings we already have don't produce minutes

The knowledge base currently holds no recordings at all, so nothing is being captured. In order:

1. **The entry is too deep to remember.** Recording starts at Knowledge › Recordings, the last kind capsule, then a language choice, then start. Nobody does that while walking into a room. A one-tap entry is the highest priority, and it belongs to #2694 (an in-app entry, an App Intent, a Control Center control, a Live Activity).
2. **The summary is not minutes.** `prompts/kb/recording.md` produces at most 600 Chinese characters under "what this is / key points / to-dos and open questions". It has no decisions, no "who does what by when", no place, and its points are not pinned to transcript timestamps. A transcript over 24k characters keeps only its head and tail, so the middle of a long meeting is dropped.
3. **The calendar is ignored.** A Lark meeting or event in the same time window already has a title (`meetings`, `events`), and a recording never reads it. (Those items carry only a title and a start time: no end, place or attendees.)
4. **A failed sync looks like a connection problem.** With Knowledge off or read-only, recording sync says it can't reach the hub and retries forever (#1915). The user thinks it was captured.

## End to end

1. The user starts a **meeting recording** (#2694: one tap, Siri, the Action button, or Control Center). A recording started the old way, from Knowledge › Recordings, stays an ordinary recording.
2. The phone (or the watch, relayed through the phone) records in segments and transcribes **on device**, exactly as today. Audio never leaves the device.
3. The phone uploads the transcript so far with `PUT /api/v1/recordings/{id}`, now also saying whether this is a meeting and, if it knows, which calendar event it is. `complete: false` until recording and transcription are both finished.
4. Once `complete: true` and the recording is a meeting, the assistant generates **minutes** with the model it already uses for knowledge-base compression. An ordinary recording still gets the short summary it gets today.
5. Minutes are stored inside the recording's directory. Deleting the recording deletes them. When they are ready, the user gets one notification.
6. Action items are **suggested to-dos**. Nothing is created until the user confirms.
7. The web Knowledge › Recordings detail shows the minutes; the iOS detail does the same (#2694). Asking the assistant "what were the minutes of the meeting just now" resolves through `kb_get` / `kb_search`.

## Who does what

| | iOS / watch (#2694) | Hub (the assistant) |
| --- | --- | --- |
| Capture and transcription | On device, segmented, as today | Never sees audio |
| Marking it a meeting | Sends `kind` and, when it has one, the calendar hint | Trusts the phone; also treats a recording as a meeting when it overlaps a calendar item in the knowledge base, or when the user asks for minutes |
| Calendar | Reads the on-device calendar for the current event's title, attendees and place | Reads `meetings` / `events` already in the knowledge base for the title (they have no attendees) |
| Minutes | Displays them; does not generate them | Generates, stores, regenerates on request |
| To-dos | Offers the suggested ones for confirmation | Creates a to-do only for the action items the user confirmed |

## Field contract

Added to [protocol.md](../protocol/protocol.md) ("Recording sync"). Everything else about an upload is unchanged, and a client that sends none of the new fields behaves exactly as before.

On `PUT /api/v1/recordings/{id}`:

```jsonc
{
  "kind": "meeting",            // optional: "meeting" or "note" (default "note")
  "calendar": {                 // optional, only when the device matched an event
    "title": "Design review",
    "attendees": ["Ada", "Bo"],
    "location": "Room 3"
  }
}
```

- `kind` is `meeting` when the user explicitly started a meeting recording, or when the device matched an event happening now. Anything else, including omitted, is `note`.
- `calendar` is whatever the device already knows. It is a hint, not the source of truth: the hub prefers a `meetings` or `events` item in the knowledge base whose time overlaps the recording. The device does not send a calendar event id, because the hub cannot see the device's calendar. Other keys in `calendar` are ignored; a title or location over 200 characters, or more than 100 attendees or one over 100 characters, is `400`.
- The phone keeps owning the transcript. These fields travel with the full-state upload, so a later upload replaces them like everything else.

Stored on the recording, and returned by `GET /api/v1/kb/recordings/{id}` inside `detail.meta` (alongside the fields already there):

| Field | Meaning |
| --- | --- |
| `kind` | `meeting` or `note` |
| `calendar` | The hint above, when one was sent |
| `meeting` | The matched knowledge-base item, when one was found: `{kind, id, name}` (`kind` is `meetings` or `events`) |
| `minutes_state` | `pending` (a meeting whose minutes for the current revision are not written yet: waiting for the transcript, the model, or regenerating), `ready`, `failed`, or absent when the recording is not a meeting (minutes it got while it was one stay in `detail.minutes`) |
| `minutes_asked` | `true` once the user asked for minutes (`POST …/minutes`), which makes the recording a meeting whatever its `kind` |

`GET /api/v1/recordings` (reconciliation) carries the same `minutes_state` on each recording when it is set, so the phone knows when to fetch a detail.

The minutes themselves are `detail.minutes`, present once minutes have been written. While newer ones are `pending` or `failed` (a re-transcription, a regenerate), the previous minutes stay; their `revision` is then below `meta.revision`:

```jsonc
{
  "version": 2,                      // increases each time the minutes are rewritten; sent back when confirming to-dos
  "revision": 7,                     // the recording revision they were made from
  "generated_at": "2026-11-10T07:12:03Z",
  "title": "Design review",
  "when": "2026-11-10 14:00–15:10",  // in the user's time zone
  "where": "Room 3",                 // omitted when nobody said and no calendar has it
  "attendees": ["Ada", "Bo"],        // omitted when not known; never invented
  "points": [
    {"text": "The export path drops entries over 4 MB.", "at": 312.0, "who": "Ada"}
  ],
  "decisions": [
    {"text": "Ship the cap as-is for this release.", "at": 1504.5}
  ],
  "actions": [
    {"id": "a1", "who": "Bo", "what": "Add the regression test.", "by": "Friday", "due_date": "2026-11-13", "at": 2100.0, "todo_id": ""}
  ],
  "open": [
    {"text": "Whether the cap should be per entry or per recording.", "at": 2408.0}
  ],
  "partial": false,                  // true when the transcript was too long and its end was not read; omitted when false
  "markdown": "# Design review\n…"    // the same minutes as Markdown with clock times: what Copy / Share send, and minutes.md on disk
}
```

- `at` is seconds from the start of the recording, the same clock as `segments[].start`, so a client can jump straight to that part of the transcript. Every point, decision, action and open question carries one.
- `who` is omitted unless the transcript or the calendar actually supports it. There is no speaker diarization, so a name appears only when someone was addressed by name or is the only attendee who could have said it. Otherwise the field is absent, not guessed.
- `by` is the deadline as spoken ("Friday", "before the 20th"), not a parsed date. Absent when none was said. `due_date` (`YYYY-MM-DD`) is added only when the spoken deadline resolves to one date unambiguously, counting from the meeting day.
- `id` names an action within one `version` (`a1`, `a2`, …). `todo_id` is set once the user confirmed it as a to-do.
- Sections with nothing in them are omitted, not filled with "none".
- `points`, `decisions`, `actions` and `open` keep the transcript's order.

Reading it back:

- `GET /api/v1/kb/recordings/{id}` returns `detail.minutes` and the meta fields above. `detail.summary` stays the short summary, for a meeting as well, so existing clients keep working.
- `kb_get` on a recording prints the minutes (`minutes.md`: the same items, each at its clock time) before the transcript, and `kb_search` matches the minutes text as well as the transcript, so "the minutes of the meeting just now" resolves without a new tool.

Regenerating: `POST /api/v1/recordings/{id}/minutes` asks for a fresh generation. It also works on a recording that is not a meeting (an older app, the watch, a forgotten mark), which becomes one (`minutes_asked`). `202 {"state":"pending"}` when it is queued, `409` when the transcript is not `complete`, `404` when there is no such recording. It does not change the transcript or the revision.

Confirming suggested to-dos: `POST /api/v1/recordings/{id}/minutes/todos` with `{"version": 2, "actions": ["a1", "a3"]}` creates one Persona to-do per listed action and writes its `todo_id` back. The title is `what`, prefixed with `who: ` when someone was named; the due date is `due_date` when set; the notes name the meeting, its date and the item's clock time. It is an ordinary to-do (no task, no Agent).
`200 {"todos": [Todo], "minutes": Minutes}`. An action that already has a `todo_id` is not created again, and a retry after a lost response creates nothing new. `409 minutes_stale` when `version` is not the current one (refetch and ask again); `404` no such recording, no minutes, or an unknown action; `400` an empty `actions`; `403` from a device that cannot write to-dos.

## What the minutes must contain

The theme (the title), when and where, attendees and speakers only when known, the points discussed, the decisions, the action items (who, what, by when), and the open questions. Each of those carries the transcript timestamp it came from. Nothing the transcript and the calendar don't support is added: a misrecognized word is read in context, but an unspoken decision is not invented.

## How the hub generates them

- For a meeting: `kind: meeting`, a calendar item in the knowledge base overlapping a recording of at least 5 minutes, or `minutes_asked`. Only once `complete: true` and the current revision has no minutes yet. The same round that summarizes ordinary recordings does it, at most a few per pass, with the model configuration the assistant already uses (`kb.compress`). No new service. An upload that completes a recording wakes that round, so minutes start within seconds.
- A long transcript is handled in segments: consecutive chunks in time order, each chunk reduced to candidate points, decisions, actions and questions with their timestamps, then one merge pass that deduplicates and orders them. The middle of a meeting is no longer dropped on the floor. Chunks are sized to fit the model's context with room for the instructions.
- When a `meetings` or `events` item overlaps the recording's time window (it starts at most 15 minutes before the recording and before its end; the closest start wins, events before meetings), it becomes `meeting` and its name names the recording unless the user typed a title or the phone sent `calendar.title`. Attendees and place come only from the phone's `calendar` hint or from the transcript.
- Every item must cite a line the model actually read; items whose `at` matches no transcript line are dropped, and so is a `due_date` that is not a real date from the meeting day to a year after it.
- The minutes are written next to the recording (`minutes.json`, and `minutes.md` for search, in its directory) and removed by the same delete and the same retention sweep that remove the recording.
- On success the user gets a single push naming the meeting, with the number of decisions and suggested to-dos; it has a stable id per recording and `version`, so a restart does not repeat it. A tap opens Persona's main chat, as reminders do; the Lark bot and channels with pushes on get the same text with the recording's web link. Opening the recording straight from a push needs a new push field and is later work.
- No model configured, or the model paused: the minutes stay `pending` and the next round tries again. An answer that cannot be used (not JSON, refused, nothing citable) counts as a failure; after three for one revision they are `failed` until a new revision or a regenerate. Failures do not notify.
- Each action item is offered as a **suggested to-do** the user confirms (`POST …/minutes/todos` above). An unconfirmed suggestion stays only a suggestion. None are created automatically.

## Privacy

Unchanged by default. Transcription stays on device, only text is uploaded, audio never leaves the phone, and recordings still expire after `recording_days` (default 14).

### Needs a decision: cloud transcription and speaker diarization

On-device transcription of a Chinese meeting is good enough for a single speaker and degrades with crosstalk, distance and accents, and it cannot say who spoke. Two options, **neither implemented**:

| | On device (today) | Cloud or hub transcription, with diarization |
| --- | --- | --- |
| Privacy | Audio never leaves the phone | Audio, or a high-quality recording of it, leaves the phone for a third party or the hub machine |
| Chinese meeting quality | Usable for one speaker nearby; poor across a table | Meaningfully better, and can label speakers |
| Cost and dependency | None | A new service, or a model the hub does not run today, plus the bandwidth of an hour of audio |
| Failure | Already handled: no text, no upload | A new failure mode for every meeting |

Until that is decided, minutes name a speaker only when the words themselves or the calendar make it plain.

## Phases

1. **This document and the field contract**, merged first so #2694 and #2704 can build against it.
2. **Hub**: generate minutes for meetings, store and delete them with the recording, notify once, suggest to-dos and create the confirmed ones, use an overlapping calendar item, and answer `kb_get` / `kb_search` (`kb_get` prints the minutes before the transcript; `kb_list recordings` shows each recording's start and whether it has minutes). Unit tests with a fake model.
3. **Web**: the Knowledge › Recordings detail renders the minutes, with a light and a dark screenshot. Android follows in its own issue.
4. **iOS** (#2694) and **Mac** (#2704) send `kind` and `calendar` and render the same `detail.minutes`.
5. **Later**: a push that opens the recording; a "meeting starting, record it?" nudge from the knowledge base's calendar; attendees, end and place on connector calendar items.

Out of scope until the decision above: uploading audio, cloud transcription, speaker diarization, and creating to-dos without confirmation.
