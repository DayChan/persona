Start a task's agent again after it did not start: in the same tab while its computer keeps it (after task_start_keys answered what stopped it), otherwise in a new tab (a start that failed for good is reopened). At most 3 times per task; the hub never retries by itself. It returns at once; whether the agent came up arrives as a 督办 message. Only for a task of this conversation; your reason is kept with the task's history.

```json
{
  "type": "object",
  "required": ["todo_id", "reason"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the task's id, as the 督办 message gives it"
    },
    "reason": {
      "type": "string",
      "description": "why it should start now, in the user's language, one sentence"
    }
  }
}
```
