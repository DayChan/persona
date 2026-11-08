Archive a coding agent's session (from agents_list) when the user asks to archive or tidy up agents: it leaves the Agents list for Archived in the app, nothing is deleted, and agent_unarchive brings it back (never remove a session any other way). Persona also asks the agent's own tool to archive it where that tool has an archive (Codex); the result says whether it did (native: done / pending / unsupported). Without close the agent itself is not closed: one open on its computer stays open. With close, its agent is also closed on its computer the way a finished task's agent is (its tab and process go, its transcript stays, and the next message to it resumes it): only when the user asks to close it too, and the user confirms on the phone first (declined: nothing changes). A busy agent is refused and nothing changes: working, waiting for an answer or background work, and for close also a /loop, queued messages or a server it runs. When the close does not happen the session stays archived and still open on the computer, and the result says why: tell the user exactly that, not that it was closed. Not for Persona conversations (conversation_archive).

```json
{
  "type": "object",
  "required": ["machine", "session_id"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "the machine's id or name, as agents_list shows it"
    },
    "session_id": {
      "type": "string",
      "description": "the agent's session id, or its first 8 characters"
    },
    "close": {
      "type": "boolean",
      "description": "also close its agent on the computer (asks the user on the phone first); default false: archive only"
    }
  }
}
```
