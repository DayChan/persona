Set up a recurring routine: every day (or on some weekdays) at a time, Persona starts a new conversation with `prompt`, you do the work, and the user gets a push with your answer (with `push: "asked"`, only what you send with notify_user). Use for "每天 9 点给我早报", "每周五下午起草周报". For one-off reminders use schedule_followup. Pass `id` (from list_followups) to change an existing routine: only the fields you give change; `enabled: false` pauses it. A routine can take `until`, an instant after which it stops (RFC 3339 or 30m / 2h / 1d, not a bare clock time): Persona then turns it off and records a skipped run instead of running. Omit `until` to keep an existing routine's end time; null clears it. Times are in the user's time zone.

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "to change an existing routine"
    },
    "title": {
      "type": "string",
      "description": "short name shown in the app, e.g. 早报"
    },
    "prompt": {
      "type": "string",
      "description": "what to do each time, self-contained"
    },
    "time": {
      "type": "string",
      "description": "HH:MM, 24-hour"
    },
    "weekdays": {
      "type": "array",
      "items": {
        "type": "integer",
        "minimum": 1,
        "maximum": 7
      },
      "description": "1 = Monday … 7 = Sunday; omit or empty = every day"
    },
    "enabled": {
      "type": "boolean"
    },
    "push": {
      "type": "string",
      "enum": ["every", "asked"],
      "description": "every (default for a new task): each run's answer is pushed to the user; asked: only what a run sends with notify_user, for a task that watches something (it then runs in its own conversation). Omit to keep an existing task's"
    },
    "until": {
      "type": ["string", "null"],
      "description": "optional end time (RFC 3339 or 30m / 2h / 1d). After it, Persona turns the task off and records a skipped run instead of running. Omit to keep an existing task's end time; null clears it. Ignored for a one-off."
    }
  }
}
```
