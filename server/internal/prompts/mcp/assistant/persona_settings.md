Read and change Persona's own settings for the user: their inbox sorting rules, how their inbox items link to their tasks, 关于我 (what you know about them), your default launcher (model), their mail accounts, which Claude Code config directories each machine keeps out of Agents, and each extension's settings (extension.<id>: what Settings › Extensions shows, like turning one of its features on). Start with action list (what there is, what each takes); get reads one. set / add / remove put a card on the user's phone that says what changes (before → after) and nothing changes until they approve; what the card needs filled in, like a mail password, the user types there, so never ask for passwords in the conversation. Change only what the user asked for.

```json
{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["list", "get", "set", "add", "remove"],
      "description": "default list"
    },
    "key": {
      "type": "string",
      "description": "the setting, from list (inbox.rules, mail.accounts, extension.<id>…)"
    },
    "value": {
      "description": "set: the new value; add: the item (each setting's list entry says what)"
    },
    "id": {
      "type": "string",
      "description": "remove: which item (from get)"
    }
  },
  "additionalProperties": false
}
```
