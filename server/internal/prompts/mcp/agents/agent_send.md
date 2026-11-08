Send a message to an agent as the user would type it (answers its question, tells it to continue, gives new instructions). Queued if the agent is working. Only send what the user asked for. An agent that is not running any more (a finished task's agent is closed after a few idle minutes) is resumed first, in the same session with the same machine, directory and launcher, so you can keep working with it. A text of only /exit or /quit closes a running agent and its terminal tab (the session can still be resumed later) and does nothing to one that is not running; a finished task's agent is closed for you, so there is no need to.

When a dialog holds the agent's terminal the send is refused (dialog_open) with the dialog's first line, and whether it is only a notice that Enter closes ("Enter to continue", no options). Send again with dismiss_notice true to close such a notice first. Confirmations, permissions and pickers are never closed for you: tell the user, or answer them with interaction_answer.

When you hand new work to an agent that runs already (one the user opened, a long-lived one that passes work on), give task from the main conversation: once the text is sent the agent is your supervised task, as agent_supervise makes one (a todo with the text as its notes, the agent on it, done_when, a card in the conversation; 督办 messages, task_review), so the work is tracked instead of only said. Put the criteria in the text too: the agent is told only what the text says. An agent already on one of your tasks takes more of it under the same title (no second task; done_when is updated); a different title is refused, so other work is not filed under that task. An agent on the user's own todo or another conversation's task is refused. Refused means nothing was sent. If the send fails, no task is made. Leave task out for a reply, a nudge or a decision on work it already has.

```json
{
  "type": "object",
  "required": ["machine", "session_id", "text"],
  "properties": {
    "machine": {
      "type": "string"
    },
    "session_id": {
      "type": "string"
    },
    "text": {
      "type": "string"
    },
    "dismiss_notice": {
      "type": "boolean",
      "description": "Press Enter first to close a notice that only says \"Enter to continue\" (no options). Any other dialog still refuses the send."
    },
    "task": {
      "type": "object",
      "description": "new work: make it your supervised task first (main conversation only)",
      "required": ["title"],
      "properties": {
        "title": {
          "type": "string",
          "description": "the task in a short line (Chinese, under 20 characters)"
        },
        "done_when": {
          "type": "array",
          "maxItems": 6,
          "items": { "type": "string" },
          "description": "results you can check when it says it is done (a PR merged, a sheet's link, what the live system answers)"
        },
        "expect_minutes": {
          "type": "integer",
          "description": "how long it should take from now; longer without progress counts as stuck"
        }
      }
    }
  }
}
```
