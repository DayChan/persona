Save one attachment of a message (a PDF, CSV, xlsx, zip…) as a file on this computer, the one you run on, and return its path. The id is the one mail_read lists after the attachment. Files go under the Persona data directory's attachments/mail folder, in the subfolder dir if given; an existing file is never overwritten (the new one gets a numbered name). At most 25 MB. Does not mark the message as read. Read, unzip or move the saved file with your own tools afterwards.

```json
{
  "type": "object",
  "properties": {
    "account_id": {
      "type": "string"
    },
    "uid": {
      "type": "integer"
    },
    "mailbox": {
      "type": "string",
      "description": "Mailbox name, default INBOX"
    },
    "attachment_id": {
      "type": "string",
      "description": "The id mail_read lists after the attachment"
    },
    "dir": {
      "type": "string",
      "description": "Subfolder to save into, relative (e.g. bills/2026-10); no absolute paths, '..' or hidden folders"
    }
  },
  "required": ["account_id", "uid", "attachment_id"],
  "additionalProperties": false
}
```
