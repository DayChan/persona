The Claude Code / Codex agents running on the user's computers (herdr), with their state: needs_you (waiting for an answer / approval), working, unread (finished, result not looked at), idle. Each line has machine, session id, project, title, last update and the last thing the agent said. Known Claude Code switch states appear as ultracode=on/off; an omitted state is unknown. ultracode=on means every substantive task runs a multi-agent dynamic workflow, independently of thinking effort. Let it finish and wait for workflow results without nudging it. Much faster and more accurate than ssh + herdr. At the end: the launchers that failed their last health check, by machine (agent_start and task_plan move off them).

A line under an agent is something waiting for the user: a question lists its options and the interaction id to answer it with (interaction_answer), an approval is the user's to give in the app.

```json
{
  "type": "object",
  "properties": {
    "state": {
      "type": "string",
      "enum": ["active", "needs_you", "working", "unread", "idle", "archived", "all"],
      "description": "default active = needs_you + working + unread; archived: only the archived sessions (agent_unarchive restores them); the others leave archived ones out, all lists them marked archived"
    },
    "machine": {
      "type": "string"
    },
    "query": {
      "type": "string",
      "description": "filter by title / project / directory"
    }
  }
}
```
