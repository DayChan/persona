package transcript

import (
	"bytes"

	"github.com/nwverse/persona/server/internal/proto"
)

// ObserveModelFallback follows only refusal fallback state in a transcript scan that otherwise skips ordinary
// conversation lines. Once a fallback is active, prompts and replies can clear it, so marker-only scans must read
// those lines too. The scratch parser keeps the same command, sidechain and turn rules without changing the scan's
// other metadata. TurnError travels with its code so a reader can match it against a separately read tail. Call
// ParseLine instead on lines that the scan already parses.
func (m *Meta) ObserveModelFallback(line []byte) {
	if m.ModelFallback == nil && !bytes.Contains(line, []byte("model_refusal_")) {
		return
	}
	scratch := Meta{Sidechain: m.Sidechain, ModelFallback: m.ModelFallback, TurnError: m.TurnError, TurnErrorCode: m.TurnErrorCode}
	ParseLine(0, line, &scratch)
	if scratch.ModelFallbackObserved {
		m.ModelFallback, m.ModelFallbackObserved = scratch.ModelFallback, true
	}
	m.TurnError, m.TurnErrorCode = scratch.TurnError, scratch.TurnErrorCode
}

// claudeModelRefusal keeps even unknown refusal records visible. Only a known fallback with a target model is
// evidence that Claude Code changed models; an absent or new subtype must not invent such a switch.
func (m *Meta) claudeModelRefusal(subtype string, line []byte, it proto.Item) []proto.Item {
	var fields struct {
		OriginalModel string `json:"originalModel"`
		FallbackModel string `json:"fallbackModel"`
		Category      string `json:"apiRefusalCategory"`
		Scope         string `json:"scope"`
		Explanation   string `json:"apiRefusalExplanation"`
	}
	// Malformed or future fields still leave the original record in the notice.
	err := unmarshalLine(line, &fields)
	r := &proto.ModelRefusal{Subtype: subtype, OriginalModel: fields.OriginalModel, FallbackModel: fields.FallbackModel,
		Category: fields.Category, Scope: fields.Scope, Explanation: fields.Explanation, Raw: string(line)}
	it.ModelRefusal, it.Text = r, r.Raw
	if err == nil && subtype == "model_refusal_fallback" && r.FallbackModel != "" {
		m.ModelFallback, m.ModelFallbackObserved = r, true
	}
	return []proto.Item{it}
}

func (m *Meta) clearModelFallback() {
	m.ModelFallback, m.ModelFallbackObserved = nil, true
	if m.TurnErrorCode == proto.TurnErrorModelFallbackUnavailable {
		m.TurnErrorCode = ""
	}
}

// A real reply tells us which model ran. Synthetic error replies cannot clear a fallback merely by naming
// <synthetic>, and a successful reply from the fallback keeps session-scoped evidence for later turns.
func (m *Meta) observeReplyModel(model string) {
	if m.ModelFallback == nil || m.ModelFallback.FallbackModel != model {
		m.clearModelFallback()
	}
}
