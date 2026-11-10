Read the last 30 lines of the terminal tab a task's agent did not start in, while its computer keeps that tab (a 督办 message says until when): the question the tab's shell waits on, or where the agent's own screen stopped. Only for a task of this conversation whose start is being remedied; what it shows stays here, it is not kept with the task.

```json
{
  "type": "object",
  "required": ["todo_id"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the task's id, as the 督办 message gives it"
    },
    "reason": {
      "type": "string",
      "description": "why you look, in the user's language (optional; kept with the task's history)"
    }
  }
}
```
