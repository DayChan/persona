Show or change what a source's history import reads. What is read of each chat comes from the knowledge base's selection (the same rules as daily ingestion; per-chat settings are changed here with `set`): the whole chat, only the user's own messages (with the messages around them) and mentions of the user, or nothing. Without `set` or `back_days` it refreshes the source's chat list and lists the chats grouped that way, largest first, with each chat's id and any per-chat setting of the user's. `set` changes per-chat settings, which apply to daily ingestion too: a chat by its id or exact name (a name two chats share is refused with their ids, a name no chat has gets close names; then nothing is changed); `read: false` never reads it, `read: true, store: true` keeps the whole chat (for `days`, or as the rules say), `read: true, store: false` only the user's messages and mentions, `reset: true` back to the rules. `back_days` limits how far back the import goes (null: as far as the rules keep messages). A change, or a refreshed list, that reads more than the user approved takes the approval back, so the import needs kb_history_resume again; reading less does not. Listing or changing never starts the import: show the user the list, apply the exceptions they name, show the result again, and only when they say to start call kb_history_resume. Needs `kb.history.enabled` in manager.json.

```json
{
  "type": "object",
  "properties": {
    "source": {
      "type": "string",
      "description": "connector id from kb.history.sources; omitted: every configured source (required with set or back_days)"
    },
    "set": {
      "type": "array",
      "description": "per-chat settings to change, all or none",
      "items": {
        "type": "object",
        "required": ["chat"],
        "properties": {
          "chat": {
            "type": "string",
            "description": "the chat's id, or its exact name"
          },
          "read": {
            "type": "boolean",
            "description": "false: never read it (required unless reset)"
          },
          "store": {
            "type": "boolean",
            "description": "true: keep the whole chat; false: only the user's messages and mentions"
          },
          "days": {
            "type": ["integer", "null"],
            "description": "with store: how many days its lines are kept (null: as the rules say)"
          },
          "reset": {
            "type": "boolean",
            "description": "remove the chat's setting: the rules decide again"
          }
        }
      }
    },
    "back_days": {
      "type": ["integer", "null"],
      "description": "how many days back the import goes for this source; null: as far as the rules keep messages"
    }
  }
}
```
