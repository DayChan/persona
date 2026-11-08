package kb

import (
	"slices"
	"time"
)

// AnchorSpan is how many messages an anchor import keeps on each side of an anchor.
const AnchorSpan = 50

// AnchorFilter picks what an anchor import keeps from a conversation read backwards (newest to oldest), for sources
// that cannot search for the anchors: each anchor (a message Anchor says is one: the owner's own, one that mentions
// the owner) with the Span messages newer than it and the Span older. Windows that overlap or touch merge by
// themselves; what falls between them is counted, never kept, and marked by one omission line (OmissionLine) in
// each gap, including the gap before the newest window. Nothing is marked after the oldest window until Flush, and
// a conversation without any anchor gets no line at all (no chat is made out of nothing).
//
// It holds at most Span messages, and its state (State) is plain data to keep between pages, so an import resumes
// mid-window after a restart.
type AnchorFilter struct {
	Anchor func(SourceMessage) bool
	Span   int // 0: AnchorSpan
	st     AnchorFilterState
}

// AnchorFilterState is an AnchorFilter between pages (HistoryState.Filter).
type AnchorFilterState struct {
	// Ring: the messages newer than the one read last that are not kept (yet), newest first, at most Span: they are
	// kept if an anchor comes within Span.
	Ring []SourceMessage `json:"ring,omitempty"`
	// Older: how many more messages are kept after the last anchor.
	Older int `json:"older,omitempty"`
	// Omitted and OmittedAt: the messages of the current gap left out so far, and the time of the oldest of them.
	Omitted   int       `json:"omitted,omitempty"`
	OmittedAt time.Time `json:"omitted_at,omitzero"`
	// Windows: anchors' windows kept so far (merged ones count once). Read, Dropped: messages pushed, and left out.
	Windows int `json:"windows,omitempty"`
	Read    int `json:"read,omitempty"`
	Dropped int `json:"dropped,omitempty"`
}

// clean is the state without what must not be written to disk: senders' identity hints are raw and kept in memory
// only (kbwire.SourceMessage), so buffered messages are kept without them.
func (st AnchorFilterState) clean() AnchorFilterState {
	if len(st.Ring) == 0 {
		return st
	}
	st.Ring = slices.Clone(st.Ring)
	for i := range st.Ring {
		st.Ring[i].SenderHandles = nil
	}
	return st
}

// NewAnchorFilter is a filter that picks anchors with anchor, resuming from st (nil: a new import).
func NewAnchorFilter(anchor func(SourceMessage) bool, span int, st *AnchorFilterState) *AnchorFilter {
	f := &AnchorFilter{Anchor: anchor, Span: span}
	if st != nil {
		f.st = *st
		f.st.Ring = slices.Clone(st.Ring)
	}
	return f
}

func (f *AnchorFilter) span() int { return or(f.Span, AnchorSpan) }

// State is the filter's state to keep between pages (with the buffered messages' identity hints left out).
func (f *AnchorFilter) State() AnchorFilterState { return f.st.clean() }

// Push takes the next page read backwards: pages come newest first, but each page's messages oldest first (as
// sources return a page). It returns what to write: the kept messages and the omission lines of the gaps that
// closed, oldest first (WriteHistory takes them in any order, but within a minute this order is the one written).
func (f *AnchorFilter) Push(page []SourceMessage) []SourceMessage {
	var out []SourceMessage // newest first while it is built
	for i := len(page) - 1; i >= 0; i-- {
		m := page[i]
		f.st.Read++
		switch {
		case f.Anchor != nil && f.Anchor(m):
			out = f.closeGap(out, true)
			out = append(out, f.st.Ring...)
			out = append(out, m)
			f.st.Ring = nil
			f.st.Older = f.span()
			f.st.Windows++
		case f.st.Older > 0:
			out = append(out, m)
			f.st.Older--
		default:
			f.st.Ring = append(f.st.Ring, m)
			if len(f.st.Ring) > f.span() {
				f.omit(f.st.Ring[0])
				f.st.Ring = slices.Delete(f.st.Ring, 0, 1)
			}
		}
	}
	slices.Reverse(out)
	return out
}

// Flush ends the conversation (its start, or the text retention, was reached): what is still buffered is left out,
// and the gap after the oldest window gets its omission line.
func (f *AnchorFilter) Flush() []SourceMessage {
	for _, m := range f.st.Ring {
		f.omit(m)
	}
	f.st.Ring = nil
	f.st.Older = 0
	return f.closeGap(nil, f.st.Windows > 0)
}

func (f *AnchorFilter) omit(m SourceMessage) {
	f.st.Omitted++
	f.st.Dropped++
	f.st.OmittedAt = m.At // read newest to oldest: the last one left out is the oldest
}

// closeGap ends the current gap, appending its omission line when mark says a window borders it (Flush of a
// conversation without anchors writes nothing).
func (f *AnchorFilter) closeGap(out []SourceMessage, mark bool) []SourceMessage {
	if f.st.Omitted > 0 && mark {
		// The oldest message left out stamps the line: it sorts after the older window (same batch, written in order)
		// and before the newer one, which shares its minute only when the whole gap does.
		out = append(out, OmissionLine(f.st.OmittedAt, f.st.Omitted))
	}
	f.st.Omitted, f.st.OmittedAt = 0, time.Time{}
	return out
}
