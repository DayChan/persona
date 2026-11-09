How the history import is doing, per source: its state (running, resting, paused, stopped, done, waiting for approval or for the search index), whether its plan is approved, chats done / to go / paused / in error / unreadable with messages read and written, calls and messages in the last hour, the last slowdown, pause and stop with their reasons, and the last call. Read-only.

```json
{
  "type": "object",
  "properties": {
    "source": {
      "type": "string",
      "description": "one connector id; omitted: all configured sources"
    }
  }
}
```
