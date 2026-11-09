Hand the user a plan of one or more tasks for agents on their computers, instead of starting agents yourself. Use it for any work that takes an agent (code, research in a repo, anything longer than a few tool calls); one task per independent piece of work. Check machines_list for current machine resources and availability before choosing a machine. Fill in every field from what you know (the knowledge base, agents_list: which machine works in which directory, the user's habits); the user sees each choice as a chip they can change, with your one-line reason. Read-only tasks, tasks the user asked for themselves (user_requested), and kinds the user told you to start without asking, start at once; the rest wait as a card for the user's 开始. Either way this returns at once: don't wait, carry on. Each queued task is a todo (todo id) whose progress shows in your context snapshot; send to a task's agent with agent_send. Quick things you can do with a tool yourself (a reminder, an event) are not tasks. A task stays on its requested machine by default (machine_policy=fixed), including retries. An unavailable launcher fails there; do not reroute machine-local work. Use machine_policy=flexible only when the task may run on another computer: a failed health check can then move it, and its why says so. The response confirms queued dispatch, not a successful Agent start; inspect the task outcome. From the main conversation you supervise what you hand out: give each task done_when (results you can check) and expect_minutes; you get a 督办 message when one needs you, and you close it with task_review, not by repeating its agent's words. The answer lists work already going on that looks like the same task: check it before you let both go on. A todo this conversation opened that has no agent yet (one opened from a Lark topic: the message that handed it to you names its id) is the task's own: give that id as the task's todo_id and the agent works on it, instead of a new todo being made; any other todo_id is refused with why.

```json
{
  "type": "object",
  "required": [
    "tasks"
  ],
  "properties": {
    "tasks": {
      "type": "array",
      "minItems": 1,
      "maxItems": 8,
      "items": {
        "type": "object",
        "required": [
          "title",
          "prompt",
          "machine",
          "cwd",
          "readonly",
          "user_requested",
          "why"
        ],
        "properties": {
          "title": {
            "type": "string",
            "description": "the task in a short line the user reads on the card (Chinese, under 20 characters)"
          },
          "prompt": {
            "type": "string",
            "description": "the agent's first message: everything it needs to do the task without asking you"
          },
          "machine": {
            "type": "string",
            "description": "machine id; fixed target by default"
          },
          "machine_policy": {
            "type": "string",
            "enum": ["fixed", "flexible"],
            "description": "fixed (default): keep this machine through preview, dispatch and retries; fail there if unavailable. flexible: explicitly permit health-based placement on another computer, only for work that is portable"
          },
          "cwd": {
            "type": "string",
            "description": "the project directory: absolute path or ~/…"
          },
          "launcher": {
            "type": "string",
            "description": "launcher name; empty: the default"
          },
          "model": {
            "type": "string",
            "description": "model id (an alias such as opus, or a gateway's own id; letters, digits and . _ - / : @ [ ] only, or a leading ~ as in ~vendor/model-latest); empty: the launcher's default"
          },
          "permission": {
            "type": "string",
            "enum": [
              "default",
              "acceptEdits",
              "plan",
              "bypassPermissions"
            ],
            "description": "permission mode; empty: the default for writable tasks. readonly=true always uses plan, overriding this field"
          },
          "ultracode": {
            "type": "boolean",
            "description": "true for a large task (many files, checks from several angles): Claude Code runs every substantial step as a multi-agent workflow, slower and costlier; false for small or quick work. Claude Code agents only (2.1.284 or later): any other agent, or an older Claude Code, fails to start. Never put the word in the prompt or title instead"
          },
          "readonly": {
            "type": "boolean",
            "description": "true when the task only reads (looks things up, reads code, does not change project files). Forces permission=plan, including retries and card edits; a launcher that cannot honor plan mode is refused"
          },
          "user_requested": {
            "type": "boolean",
            "description": "true when the user asked for this very task in their latest message (told you to do it, to have an agent do it, to go ahead and start); false when it is your own idea or inferred from something they did not ask for"
          },
          "why": {
            "type": "string",
            "description": "one line (Chinese) on why this machine, directory and model"
          },
          "done_when": {
            "type": "array",
            "maxItems": 6,
            "items": {
              "type": "string"
            },
            "description": "what done means: results you will check before you accept it (Chinese, one each), e.g. 「PR 合并到 master」「浅色 / 深色截图贴进 PR」「线上 hub 已部署并验证」; the agent is told them"
          },
          "expect_minutes": {
            "type": "integer",
            "minimum": 0,
            "description": "about how long the task should take, in minutes; a task with no progress for 1.5× that (at least 20 minutes) counts as stuck"
          },
          "todo_id": {
            "type": "string",
            "description": "optional: the todo this task runs on instead of a new one, when this conversation opened it and no agent is on it yet (the id the Lark-topic message gave you); at most one task per todo; leave it out for a task with no such todo"
          },
          "tier": {
            "type": "string",
            "description": "An Agent preset: one of the tiers the snapshot's 模型档位 line lists for the profile in use, each with what it is for; pick the one whose description fits the work. Resolved when the task actually starts, with the profile in use then. Explicit launcher/model/effort override its values."
          },
          "effort": {
            "type": "string",
            "description": "Explicit effort override; omit to use the tier or purpose default."
          }
        }
      }
    }
  }
}
```
