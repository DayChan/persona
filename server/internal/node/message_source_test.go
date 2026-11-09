package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Node RPC's source field already names a screen source. Provenance must use a
// distinct optional request key so older nodes still decode automated sends.
func TestMessageSourceRequestCompatibility(t *testing.T) {
	n := &Node{state: newState()}
	source := &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "conversation-1", Title: "Research"}
	params, err := json.Marshal(map[string]any{"text": prompts.MessageSourceText("Read the result.", source), "message_source": source})
	if err != nil {
		t.Fatal(err)
	}
	// Unknown method exercises the real common decoder without launching a model,
	// reading a transcript, or touching a user's runtime configuration.
	_, err = n.handle(context.Background(), "source.schema_test", params)
	if err == nil || !strings.Contains(err.Error(), "unknown method") {
		t.Fatalf("provenance broke common request decoding: %v", err)
	}
	bad, _ := json.Marshal(map[string]any{"source": source})
	_, err = n.handle(context.Background(), "source.schema_test", bad)
	if err == nil || !strings.Contains(err.Error(), "bad params") {
		t.Fatal("regression control did not detect the pre-existing string source field")
	}
}
