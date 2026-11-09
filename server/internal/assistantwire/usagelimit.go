package assistantwire

import (
	"regexp"
	"strings"
	"sync"
)

// usageLimitLine is the line Claude Code writes itself when a subscription's limit refused a request: "You've hit
// your session limit · resets 4:20pm (Asia/Shanghai)", "You've hit your weekly limit · resets Sep 28, 4pm (…)". The
// limit is named within a few words, before any full stop: "You've hit your target of 90%. Next, the rate limit…" is
// a reply. Compiled on first use, not whenever a program that links the contract starts (a hook, #1308). A contract
// depends on the standard library only, so not internal/lazyre.
var usageLimitLine = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`^You(?:'|’)ve hit your [^·\n.]{0,40}\blimit\b`)
})

// UsageLimitText: text is Claude Code's line for a subscription usage limit (only as the start of the text: a reply
// may quote one). transcript.UsageLimitText calls this, so the line has one definition; CutOff uses it too.
func UsageLimitText(text string) bool {
	return usageLimitLine().MatchString(strings.TrimSpace(text))
}
