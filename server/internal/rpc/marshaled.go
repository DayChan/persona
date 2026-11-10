package rpc

// Marshaled is params exactly as json.Marshal wrote them: compact, HTML characters, U+2028 and U+2029 escaped, valid
// UTF-8 only. Notify puts them into its line as they are. As json.RawMessage the envelope's encoder checks and
// escapes them once more, finds nothing to change, and copies them twice on the way (a computer's session list,
// each time it changes). Bytes from anywhere else (a runtime's push, a file) go as json.RawMessage, checked as before.
type Marshaled []byte

// marshaledLine is the line json.Marshal and a newline give for an envelope of event and params, in one buffer of
// its size: {"event":<event>,"params":<params>}. False when event is empty (the envelope leaves it out) or has a
// byte the encoder may escape, or params is empty (an error): the encoder writes those.
func marshaledLine(event string, params Marshaled) ([]byte, bool) {
	if event == "" || len(params) == 0 {
		return nil, false
	}
	for i := 0; i < len(event); i++ {
		if c := event[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			return nil, false
		}
	}
	const head, mid, tail = `{"event":"`, `","params":`, "}\n"
	b := make([]byte, 0, len(head)+len(event)+len(mid)+len(params)+len(tail))
	b = append(append(append(b, head...), event...), mid...)
	return append(append(b, params...), tail...), true
}

// MarshalJSON is m as it is, as json.RawMessage's: params of a Call or a nested value are not written as base64.
func (m Marshaled) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	return m, nil
}
