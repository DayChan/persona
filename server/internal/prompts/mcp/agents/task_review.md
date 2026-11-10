Review a task you handed out from this conversation (its todo id), after you checked its work yourself: each done_when criterion with its evidence (a PR link and that it merged, CI, a screenshot, what the live system answers) or what is missing. accepted closes the task (its agent is closed once it stays still); rework sends what is missing back to the same agent (resumed if it was closed) — at most 2 rounds, a third goes to the user instead; needs_user marks it waiting for the user's decision (its conclusion card asks them; without one, ask them yourself: ask_user, or notify_user if they are away, as the answer says); cancelled ends it (a duplicate, no longer wanted): its agents are closed and archived, and their waiting cards end. A leader conversation's needs_user also goes to the main conversation. From a conversation other than the main one, on a task it does not supervise, only accepted and cancelled apply: send what is missing to its agent with agent_send. Use it on every task a 督办 message says waits for review; don't accept on the agent's word alone. On a supervised task, accepted and needs_user send the user one conclusion card in Lark: the verdict (done, cannot be done, or needs your decision), reason (one sentence), link (one), and for needs_user your recommend, which the user takes by answering "OK"; so give outcome, reason and link with accepted, and reason and recommend with needs_user, and don't push the same result again. A needs_user decision may carry a deadline: when it passes unanswered, a reversible one takes your recommend by default (recorded on the to-do as taken at the deadline), and one that cannot be undone is only reminded again; without a deadline it waits as before.

```json
{
  "type": "object",
  "required": ["todo_id", "verdict"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the task's todo id (todo_…), from the 督办 message or the context snapshot"
    },
    "verdict": {
      "type": "string",
      "enum": ["accepted", "rework", "needs_user", "cancelled"]
    },
    "checks": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["criterion", "met"],
        "properties": {
          "criterion": {"type": "string", "description": "one done_when criterion (or what the task was for, when it had none)"},
          "met": {"type": "boolean"},
          "evidence": {"type": "string", "description": "what shows it is met (a link, a command's output), or what is missing"}
        }
      },
      "description": "each criterion, met or not, with its evidence or gap"
    },
    "note": {
      "type": "string",
      "description": "rework: what the agent is to do (Chinese, concrete); needs_user: what the user is to decide; cancelled: why; accepted: the result in a line"
    },
    "outcome": {
      "type": "string",
      "enum": ["done", "cannot"],
      "description": "accepted: done (the work is done or the answer is yes), or cannot (the task found it cannot be done); default done"
    },
    "reason": {
      "type": "string",
      "description": "accepted and needs_user: the conclusion card's one sentence of why (what the user is to decide, for needs_user), in the user's language; default the note's first line"
    },
    "link": {
      "type": "string",
      "description": "accepted and needs_user: the one link to the evidence (the PR, the page, the document); default the task's PR, a link in the note or checks, or the agent's conversation"
    },
    "recommend": {
      "type": "string",
      "description": "needs_user: the option you recommend, in a line; the user's \"OK\" takes it and it goes to the task's agent"
    },
    "deadline": {
      "type": "string",
      "description": "needs_user, optional: until when the decision waits, a duration (\"4h\", \"90m\") or an RFC 3339 time, 1 minute to 30 days ahead; needs recommend and reversible"
    },
    "reversible": {
      "type": "boolean",
      "description": "with deadline: whether taking recommend can be undone later. false for deleting, sending anything to other people, spending money or changing production: those are never taken by default"
    }
  }
}
```
