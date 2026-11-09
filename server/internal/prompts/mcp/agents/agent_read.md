Read the recent conversation of one agent (from agents_list): the user's messages, what the agent said, tool calls in short form. `turns` = how many recent exchanges (default 2, max 10). Use it to answer the user's follow-ups about work an agent or task already did (its conclusion, a PR link, why it stopped) instead of asking the agent with agent_send, which makes it take another turn (a closed agent is resumed first) while the user waits.

```json
{
  "type": "object",
  "required": ["machine", "session_id"],
  "properties": {
    "machine": {
      "type": "string"
    },
    "session_id": {
      "type": "string",
      "description": "full id or its first 8 characters"
    },
    "turns": {
      "type": "integer"
    }
  }
}
```
