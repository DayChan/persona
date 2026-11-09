package lazyre

import (
	"regexp/syntax"
	"slices"
	"strings"
	"unicode/utf8"
)

// A gated pattern (NewGated) answers MatchString by first looking for the literals every match starts with: a large
// unanchored alternation of sayings costs the regexp engine every branch at every position of the text, about half a
// microsecond a byte, while a text holding none of the literals cannot match at all. The literals come from the
// pattern itself, so the gate never disagrees with the pattern.

// gateMost is how many literals a gate looks for at most; a pattern with more is matched without one.
const gateMost = 256

// NewGated is New for a pattern run often on texts it seldom matches: MatchString, ReplaceAllString,
// ReplaceAllLiteralString and ReplaceAllStringFunc first check the text holds one of the literals every match starts
// with (zero-width assertions aside), and give the text back as it is when it holds none. A pattern some match of which may
// start with anything (a class, a repeat, case folding) has no gate and matches as New's.
func NewGated(src string) *Regexp {
	r := &Regexp{src: src, gated: true}
	if inTest {
		r.Regexp()
	}
	return r
}

// Gated reports whether r has a gate: it was made with NewGated and every match of it starts with a literal.
func (r *Regexp) Gated() bool {
	r.Regexp()
	return r.gate != nil
}

// MayMatchLowered is false only when strings.ToLower(s) cannot match r: s is ASCII and, its letters lower-cased as it is
// read, holds none of the literals every match starts with. A caller that matches lower-cased texts (r.MatchString of
// strings.ToLower(s)) then lowers, which copies, only the texts that may match. True for any other s, and for a pattern
// without a gate.
func (r *Regexp) MayMatchLowered(s string) bool {
	r.Regexp() // makes the gate
	return r.gate == nil || r.gate.openLowered(s)
}

// gateLiterals is the gate of src: strings one of which every match starts with, none inside another; nil when
// there is none to look for.
func gateLiterals(src string) []string {
	re, err := syntax.Parse(src, syntax.Perl)
	if err != nil {
		return nil
	}
	set, _ := prefixes(re.Simplify())
	if len(set) == 0 || len(set) > gateMost || slices.Contains(set, "") {
		return nil
	}
	// Shortest first, and one that holds a shorter one dropped: a text holding it holds the shorter one too.
	slices.SortFunc(set, func(a, b string) int { return len(a) - len(b) })
	var out []string
	for _, s := range set {
		if !slices.ContainsFunc(out, func(o string) bool { return strings.Contains(s, o) }) {
			out = append(out, s)
		}
	}
	return out
}

// prefixes is a set of strings one of which every match of re starts with, and whether each is a whole match of re
// (so what follows re in a concatenation may lengthen them). A part it cannot tell about ends the strings there.
func prefixes(re *syntax.Regexp) ([]string, bool) {
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 {
			return []string{""}, false
		}
		return []string{string(re.Rune)}, true
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return []string{""}, true
	case syntax.OpCapture:
		return prefixes(re.Sub[0])
	case syntax.OpCharClass:
		var out []string
		for i := 0; i+1 < len(re.Rune); i += 2 {
			for c := re.Rune[i]; c <= re.Rune[i+1]; c++ {
				if len(out) == 8 {
					return []string{""}, false
				}
				out = append(out, string(c))
			}
		}
		if len(out) == 0 {
			return []string{""}, false
		}
		return out, true
	case syntax.OpAlternate:
		var out []string
		whole := true
		for _, sub := range re.Sub {
			set, w := prefixes(sub)
			out, whole = append(out, set...), whole && w
		}
		return out, whole
	case syntax.OpConcat:
		out := []string{""}
		for _, sub := range re.Sub {
			set, whole := prefixes(sub)
			if len(out)*len(set) > gateMost {
				return out, false
			}
			next := make([]string, 0, len(out)*len(set))
			for _, a := range out {
				for _, b := range set {
					next = append(next, a+b)
				}
			}
			if out = next; !whole {
				return out, false
			}
		}
		return out, true
	}
	return []string{""}, false
}

// gate is what a gated pattern looks for in a text before matching it: one of lits.
type gate struct {
	lits []string
	// first is lits by their first byte, and second, for each first byte, the set of bytes that follow it in those
	// lits (a bit per byte; every byte for a one-byte literal): for a gate with many, one pass over the text's bytes,
	// comparing a literal only where the text has its first two bytes.
	first  *[256][]string
	second *[256][4]uint64
}

// fewLits is how many literals a gate looks for one at a time (strings.Contains is fast for each).
const fewLits = 8

// newGate is src's gate; nil when it has none.
func newGate(src string) *gate {
	lits := gateLiterals(src)
	if lits == nil {
		return nil
	}
	g := &gate{lits: lits}
	if len(lits) > fewLits {
		g.first, g.second = new([256][]string), new([256][4]uint64)
		for _, l := range lits {
			g.first[l[0]] = append(g.first[l[0]], l)
			if len(l) == 1 {
				g.second[l[0]] = [4]uint64{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}
			} else {
				g.second[l[0]][l[1]>>6] |= 1 << (l[1] & 63)
			}
		}
	}
	return g
}

// openLowered: strings.ToLower(s) may hold one of the gate's literals. For an ASCII s that text is s with A-Z turned
// into a-z, byte for byte, so this is open on it with each byte lower-cased as it is read, and nothing is copied. Other
// runes may lower into other bytes: an s holding one is taken as open.
func (g *gate) openLowered(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return true
		}
	}
	if g.first == nil {
		for _, l := range g.lits {
			for i := 0; i+len(l) <= len(s); i++ {
				if hasLoweredPrefix(s[i:], l) {
					return true
				}
			}
		}
		return false
	}
	for i := 0; i < len(s); i++ {
		b := lowerASCII(s[i])
		ls := g.first[b]
		if ls == nil {
			continue
		}
		if i+1 < len(s) {
			if c := lowerASCII(s[i+1]); g.second[b][c>>6]&(1<<(c&63)) == 0 {
				continue
			}
		}
		for _, l := range ls {
			if hasLoweredPrefix(s[i:], l) {
				return true
			}
		}
	}
	return false
}

// hasLoweredPrefix: the ASCII s, lower-cased, starts with l.
func hasLoweredPrefix(s, l string) bool {
	if len(s) < len(l) {
		return false
	}
	for j := 0; j < len(l); j++ {
		if lowerASCII(s[j]) != l[j] {
			return false
		}
	}
	return true
}

// lowerASCII is c as strings.ToLower turns an ASCII byte.
func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// open: s holds one of the gate's literals, so the pattern may match it.
func (g *gate) open(s string) bool {
	if g.first == nil {
		for _, l := range g.lits {
			if strings.Contains(s, l) {
				return true
			}
		}
		return false
	}
	// A literal starts on a rune's first byte, which is never a continuation byte: looking at every byte finds every
	// place one can start, in invalid UTF-8 too.
	for i := 0; i < len(s); i++ {
		ls := g.first[s[i]]
		if ls == nil {
			continue
		}
		if i+1 < len(s) {
			if c := s[i+1]; g.second[s[i]][c>>6]&(1<<(c&63)) == 0 {
				continue
			}
		}
		for _, l := range ls {
			if strings.HasPrefix(s[i:], l) {
				return true
			}
		}
	}
	return false
}
