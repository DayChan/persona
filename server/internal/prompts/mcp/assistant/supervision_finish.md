Finish handling a supervision wake-up without cluttering the user's main conversation. Use visibility="silent" when no user decision or useful result needs to be shown, with a short summary of what you checked or changed; then end the turn. The complete turn stays available in the expandable supervision record. Use visibility="user" to explicitly show the turn without sending a notification. notify_user, ask_user, and task_review user conclusion cards also promote the turn. A promoted turn cannot be made silent again. This tool is only for supervision-triggered turns, not normal user messages.

```json
{
  "type": "object",
  "required": ["visibility", "summary"],
  "properties": {
    "visibility": {"type": "string", "enum": ["silent", "user"]},
    "summary": {"type": "string", "minLength": 1, "maxLength": 1000}
  },
  "additionalProperties": false
}
```
