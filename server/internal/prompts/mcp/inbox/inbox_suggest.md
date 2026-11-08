Hand in your suggestion for the inbox event this conversation handles (one at a time: a new one replaces the last), and the event's recap. It waits in the user's inbox; nothing goes out until they confirm, and then the program does exactly what it says (you never send it yourself). `kind`: `reply` — one message to send, `text` in the user's voice, ready to go out as it is (no blanks to fill in like 【…】: leave out what you could not find and say so in `note`; for a Feishu sheet / Base item `changes` proposes moving rows' status: ref, field and one of the values the item lists); `agent` — an Agent to start, `text` its instruction (`machine` and `cwd` optional); `conclusion` — you dealt with it, `text` the conclusion, `outcome` `fyi` (worth knowing) or `done` (nothing to do). `handback: true` for one the user has to judge: their confirmation comes back here as an instruction instead of being carried out. `note`: one or two sentences on what you found and where. `recap`: the event's background, progress and where it is stuck (up to 600 characters), every time. `related`: ids of inbox items in other chats or sources this one is about. With only `recap` (no `kind`) it just updates the recap. Call it at the end of each turn. In a conversation the user handed an inbox item to (its context block names the item's id), pass that id as `item` and call it when you have something to write back to the item; it refuses an item not handed to this conversation or one the user already filed: answer in the chat then.

```json
{
  "type": "object",
  "properties": {
    "item": {
      "type": "string"
    },
    "kind": {
      "type": "string",
      "enum": ["reply", "agent", "conclusion"]
    },
    "text": {
      "type": "string"
    },
    "note": {
      "type": "string"
    },
    "handback": {
      "type": "boolean"
    },
    "outcome": {
      "type": "string",
      "enum": ["fyi", "done"]
    },
    "recap": {
      "type": "string"
    },
    "machine": {
      "type": "string"
    },
    "cwd": {
      "type": "string"
    },
    "related": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "changes": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["ref", "field", "value"],
        "properties": {
          "ref": {
            "type": "string"
          },
          "field": {
            "type": "string"
          },
          "value": {
            "type": "string"
          }
        }
      }
    }
  }
}
```
