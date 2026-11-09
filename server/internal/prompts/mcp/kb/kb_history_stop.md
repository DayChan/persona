Stop the history import now: it makes no further call until the user resumes it (the call in flight, if any, finishes; nothing is cancelled). Use it whenever the user says to stop or pause importing history.

```json
{
  "type": "object",
  "properties": {
    "reason": {
      "type": "string",
      "description": "why, in a few words, as the user said it; shown in the status, never written to the log"
    }
  }
}
```
