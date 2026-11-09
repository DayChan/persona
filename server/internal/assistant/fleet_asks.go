package assistant

import (
	"sync"

	"github.com/nwverse/persona/server/internal/transcript"
)

// asksMemo keeps what transcript.AskOf found in each session's last words, by machine and session, while those words
// stay the same. A machine's list is decorated for every change the manager publishes, and on a machine listing
// thousands of sessions nearly all are offline transcripts whose node did not read their question (no AsksRead) and
// whose last words did not change since the list before: reading them again was most of the time the list took.
type asksMemo struct {
	mu       sync.Mutex
	machines map[string]*askBook
	askOf    func(string) string // transcript.AskOf when nil (tests count the reads)
}

// askBook is one machine's: each session's last words and what they ask, stamped with the round of the latest
// complete list that read them. A complete list forgets the rows it did not read (sessions gone, or read by their node).
type askBook struct {
	round uint64
	rows  map[string]askRow
}

type askRow struct {
	text, asks string
	round      uint64
}

// askReader reads what sessions ask for one decorated list of a machine. Its zero value reads every text afresh.
type askReader struct {
	m     *asksMemo
	b     *askBook
	round uint64 // the complete list's round; 0 for a subset of the list, which forgets nothing
}

func (m *asksMemo) reader(machine string, complete bool) askReader {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.machines[machine]
	if b == nil {
		if m.machines == nil {
			m.machines = map[string]*askBook{}
		}
		b = &askBook{rows: map[string]askRow{}}
		m.machines[machine] = b
	}
	r := askReader{m: m, b: b}
	if complete {
		b.round++
		r.round = b.round
	}
	return r
}

// of is what text, session sid's last words, asks: transcript.AskOf(text), read again only when the words changed.
// The lock is held for the lookup only, never while AskOf runs.
func (r askReader) of(sid, text string) string {
	if r.b == nil {
		return transcript.AskOf(text)
	}
	r.m.mu.Lock()
	row, ok := r.b.rows[sid]
	if ok && row.text == text {
		// The list's own copy of the words from now on, so the one of a list replaced since is not kept alive.
		r.b.rows[sid] = askRow{text: text, asks: row.asks, round: r.stamp()}
		r.m.mu.Unlock()
		return row.asks
	}
	askOf := r.m.askOf
	r.m.mu.Unlock()
	if askOf == nil {
		askOf = transcript.AskOf
	}
	asks := askOf(text)
	r.m.mu.Lock()
	r.b.rows[sid] = askRow{text: text, asks: asks, round: r.stamp()}
	r.m.mu.Unlock()
	return asks
}

// stamp is the round a row read now belongs to: the list's own, or for a subset the latest complete list's. Under mu.
func (r askReader) stamp() uint64 {
	if r.round != 0 {
		return r.round
	}
	return r.b.round
}

// done ends a complete list: the rows it did not read are forgotten, unless a later complete list started meanwhile
// (that one forgets instead).
func (r askReader) done() {
	if r.round == 0 {
		return
	}
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if r.b.round != r.round {
		return
	}
	for sid, row := range r.b.rows {
		if row.round != r.round {
			delete(r.b.rows, sid)
		}
	}
}
