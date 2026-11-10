List the folders the user's inbox items are filed under: each `/`-separated path (`work/alerts/engine`, every segment a level) with its item count and open count, and whether automatic filing by the triage model is on. Give `folder` to also list the items filed in that folder and below, or `unfiled: true` for the items in no folder; each line has the item's id (what inbox_move takes), source, title, sender and whether it is done. Call it before inbox_move, to reuse a folder in use rather than open a near-duplicate.

```json
{
  "type": "object",
  "properties": {
    "folder": {
      "type": "string",
      "description": "a folder path: also list the items filed there and below"
    },
    "unfiled": {
      "type": "boolean",
      "description": "also list the items in no folder"
    }
  }
}
```
