Approve a source's history import plan, as the selection reads it now, and start or resume the import (it also clears a stop). Only when the user has seen the plan (kb_history_plan) and explicitly says to start or resume; never on your own. After a stop, check kb_history_status first and tell the user why it stopped; `slow` keeps the slowest pace for the first two hours after the import starts again, the safe choice after a stop.

```json
{
  "type": "object",
  "required": ["source"],
  "properties": {
    "source": {
      "type": "string",
      "description": "connector id from kb.history.sources"
    },
    "slow": {
      "type": "boolean",
      "description": "the slowest pace for the first two hours of calls"
    }
  }
}
```
