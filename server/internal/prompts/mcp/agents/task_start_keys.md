Type into the terminal tab a task's agent did not start in, while its computer keeps that tab and no agent runs there: text as is (no enter, at most 32 characters), then keys by name ("enter", "esc", "ctrl+c", "y", "n", "up", "down"…, at most 8). Use it to answer the question the tab's shell waits on, or to get past where the agent's screen stopped; read the tab first with task_start_screen and again after. The keys, the text and your reason are kept with the task's history. It starts nothing: start the agent again with task_retry.

```json
{
  "type": "object",
  "required": ["todo_id", "reason"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the task's id, as the 督办 message gives it"
    },
    "text": {
      "type": "string",
      "maxLength": 32,
      "description": "text to type as is, before the keys"
    },
    "keys": {
      "type": "array",
      "maxItems": 8,
      "items": { "type": "string" },
      "description": "keys to press after the text, by name"
    },
    "reason": {
      "type": "string",
      "description": "why these keys, in the user's language, one sentence"
    }
  }
}
```
