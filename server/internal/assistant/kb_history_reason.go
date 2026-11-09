package assistant

import (
	"maps"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// historyReason is why something happened in the history import (a stop, a pause, a slowdown, the end of a run): a
// code and its arguments, kept as they are in STOP, status.json and the log, and put into words only when a person
// reads it (assistant-kb_history.reason_<code>, in their language). Args hold only codes, numbers, method names and
// chat hashes; Text is words that came from a person (kb_history_stop, a STOP written by hand) or from the source
// itself (Pace.Reason), shown as they are and never written to the log.
type historyReason struct {
	Code string            `json:"code,omitempty"`
	Args map[string]string `json:"args,omitempty"`
	Text string            `json:"text,omitempty"`
}

// reason is a historyReason of code with args given as key, value pairs.
func reason(code string, kv ...string) historyReason {
	r := historyReason{Code: code}
	for i := 0; i+1 < len(kv); i += 2 {
		if r.Args == nil {
			r.Args = map[string]string{}
		}
		r.Args[kv[i]] = kv[i+1]
	}
	return r
}

// String is the reason for the server's own log: its code and arguments, never Text.
func (r historyReason) String() string {
	var b strings.Builder
	b.WriteString(r.Code)
	for _, k := range slices.Sorted(maps.Keys(r.Args)) {
		b.WriteString(" " + k + "=" + r.Args[k])
	}
	return b.String()
}

// logged is the reason as the import's log keeps it: without Text.
func (r historyReason) logged() historyReason {
	r.Text = ""
	return r
}

// historyReasonText is r in l's words. A code without words of its own (a newer version's, say) reads as itself.
func historyReasonText(l i18n.Lang, r historyReason) string {
	if r.Code == "" && r.Text != "" {
		return r.Text
	}
	key := "assistant-kb_history.reason_" + r.Code
	if r.Code == "" || !i18n.Has(key) {
		s := r.String()
		if r.Text != "" {
			s += ": " + r.Text
		}
		return s
	}
	if r.Text != "" && i18n.Has(key+"_text") { // the same reason, with a person's or the source's own words
		key += "_text"
	}
	args := i18n.Args{"text": r.Text}
	for k, v := range r.Args {
		args[k] = v
	}
	return i18n.T(l, key, args)
}
