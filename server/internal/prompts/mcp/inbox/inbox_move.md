Move inbox items into a folder, as the user would from the inbox: `items` are their ids (from inbox_folders, or the id of an item handed to this conversation), `folder` a `/`-separated path (`work/alerts/engine`; a new path makes the folder), or `""` to take them out of every folder. Moved items stay where they are put: the triage model no longer files them. An unknown id or a path with no name in it moves nothing. Move only what the user asked for.

```json
{
  "type": "object",
  "required": ["items", "folder"],
  "properties": {
    "items": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "description": "inbox item ids"
    },
    "folder": {
      "type": "string",
      "description": "the folder path, or \"\" for no folder"
    }
  }
}
```
