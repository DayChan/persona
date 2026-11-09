package extmgr

import (
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Log keeps a line (host.Observer), and hands it to whoever follows the log.
func (s *extensionStats) Log(source, line string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l := LogLine{Seq: s.next, At: s.now(), Source: source, Text: s.redactLocked(cut(line, logLineMost))}
	s.next++
	if len(s.logs) < logLinesKept {
		s.logs = append(s.logs, l)
	} else {
		s.logs[l.Seq%logLinesKept] = l
	}
	for ch := range s.subs {
		select {
		case ch <- l:
		default: // a follower that does not keep up misses lines rather than hold the extension up
		}
	}
}

func (s *extensionStats) redactLocked(line string) string {
	if s.redact != nil {
		line = s.redact(line)
	}
	return redactSecrets(line)
}

func cut(s string, most int) string {
	if len(s) <= most {
		return s
	}
	i := most
	for i > 0 && !utf8RuneStart(s[i]) {
		i--
	}
	return s[:i] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// Lines are the kept lines after seq since (all when since < 0), oldest first, those with q in them (case
// insensitive), at most limit of the latest.
func (s *extensionStats) Lines(since int64, q string, limit int) (lines []LogLine, next int64) {
	if s == nil {
		return []LogLine{}, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q = strings.ToLower(q)
	lines = []LogLine{}
	first := s.next - int64(len(s.logs))
	for seq := max(first, since+1); seq < s.next; seq++ {
		l := s.logs[seq%logLinesKept] // while not full, a line's seq is its index
		if q != "" && !strings.Contains(strings.ToLower(l.Text), q) && !strings.Contains(l.Source, q) {
			continue
		}
		lines = append(lines, l)
	}
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines, s.next
}

// follow hands each new line to ch until stop.
func (s *extensionStats) follow(ch chan LogLine) (stop func()) {
	s.mu.Lock()
	if s.subs == nil {
		s.subs = map[chan LogLine]struct{}{}
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// secretPatterns find what looks like a credential in a line: it is replaced before the line is kept.
var secretPatterns = []*lazyre.Regexp{
	lazyre.New(`(?i)((?:authorization|cookie)["']?\s*[:=]\s*["']?)(?:(?:bearer|basic|token)\s+)?[^\s"',;&]{6,}`),
	lazyre.New(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`),
	lazyre.New(`(?i)((?:token|secret|password|passwd|api[_-]?key|access[_-]?key)["']?\s*[:=]\s*["']?)[^\s"',;&]{6,}`),
	lazyre.New(`(?i)([?&](?:token|key|secret|access_token|sig)=)[^\s&"']+`),
}

// redactSecrets is line with what looks like a credential in it replaced by "***".
func redactSecrets(line string) string {
	for _, re := range secretPatterns {
		line = re.ReplaceAllString(line, "${1}***")
	}
	return line
}

// hostLogf is the log the host writes to: the hub's only (journal), the extension's own through Observe.
func (ps *extensions) hostLogf() func(string, ...any) {
	if ps.journal != nil {
		return ps.journal
	}
	return ps.logf
}

// redactOwnSecrets is line without the hub's own token in it.
func (h *Manager) redactOwnSecrets(line string) string {
	if h.cfg != nil && len(h.cfg.Token) >= 8 {
		line = strings.ReplaceAll(line, h.cfg.Token, "***")
	}
	return line
}
