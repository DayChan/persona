package transcript

import (
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// pagePrompt counts a user question, not a model turn awakened by Persona. The
// hub adds injected metadata after paging, so older transcripts need the same
// template recognition here. Explicit user/session provenance protects quoted
// notification text from being mistaken for an injected prompt.
func pagePrompt(it proto.Item) bool {
	if it.Role != "user" || it.Kind != proto.KindText || it.Queued || it.Wake || it.Injected != nil {
		return false
	}
	return it.ClientMsgID != "" || it.From != "" || prompts.ParseInjected(it.Text) == nil
}
