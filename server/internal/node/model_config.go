package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/proto"
)

// modelExecution is non-secret per-turn execution metadata supplied by the hub.
type modelExecution struct {
	Model       string
	Effort      string
	UseEffort   bool
	Fingerprint string
	Ultracode   *bool `json:"-"` // explicit TUI start request; never a default or execution fingerprint
	// Runtime: the assistant's runtime, proto.AssistantRuntimeAgentSDK or "" (the CLI; "cli" reads as ""). Omitted
	// when empty so a fingerprint from before #2455 stays the same.
	Runtime string `json:",omitempty"`
	IsMain  bool   `json:",omitempty"`
}

// check refuses a model that is not a model id (proto.ValidModelID, #1644) before anything runs; the error's text is
// what the manager reads as a refused request (assistant startRequestErr), so it stays "invalid model".
func (m modelExecution) check() error {
	if m.Model != "" && !proto.ValidModelID(m.Model) {
		return errors.New("invalid model")
	}
	return validModes("", m.Effort)
}

// executionFingerprint also detects a managed provider changed before the next turn (any of its fields, the #2455 ones
// included), and the runtime changed: another process, resumed on the same conversation.
func (n *Node) executionFingerprint(launcher string, m modelExecution) string {
	if m.Runtime == proto.AssistantRuntimeCLI {
		m.Runtime = ""
	}
	raw, _ := json.Marshal(m)
	if l, ok := n.managedCatalog(launcher); ok { // as named: the same before and after a withheld one's pull (its Rev)
		v, _ := json.Marshal(l)
		raw = append(raw, v...)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func executionOption(options []modelExecution) modelExecution {
	if len(options) > 0 {
		return options[0]
	}
	return modelExecution{}
}

// assistantModel is the launcher and model the hub runs conversation id's next message on (as a message from the phone
// would be), for a message the node sends it by itself (recoverAssistantTasks). Started on the model it last ran, the
// conversation's next message from the phone would need another process (#1554). Nothing when the hub can't say (an
// older hub): the conversation's own then.
func (n *Node) assistantModel(ctx context.Context, id string) (string, modelExecution) {
	var res struct {
		Launcher         string `json:"launcher"`
		Model            string `json:"model"`
		Effort           string `json:"effort"`
		ModelFingerprint string `json:"model_fingerprint"`
		Runtime          string `json:"runtime"`
		IsMain           bool   `json:"is_main"`
	}
	if n.peer == nil || n.peer.Call(ctx, "assistant.model", map[string]any{"session_id": id}, &res) != nil {
		return "", modelExecution{}
	}
	return res.Launcher, modelExecution{Model: res.Model, Effort: res.Effort, UseEffort: res.ModelFingerprint != "", Fingerprint: res.ModelFingerprint, Runtime: res.Runtime, IsMain: res.IsMain}
}
