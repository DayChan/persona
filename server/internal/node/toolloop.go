package node

import (
	"bytes"
	"os"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// toolLoopScan keeps the incremental detector and its file position together.
type toolLoopScan struct {
	detector transcript.LoopDetector
	offset   int64
	size     int64
	mtime    time.Time
	skipping bool
}

// Bound both tail reconstruction and the largest complete line we parse.
const toolLoopReadLimit = 8 << 20

// toolLoopOf is called only for running sessions. Reads are incremental and
// bounded; a gap too large to reconstruct safely starts a fresh tail scan.
func (n *Node) toolLoopOf(id string) *proto.ToolLoop {
	path := n.transcriptPath(id)
	if path == "" {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	n.mu.Lock()
	m := n.metas[path]
	n.mu.Unlock()
	if m.toolLoop.size != st.Size() || !m.toolLoop.mtime.Equal(st.ModTime()) {
		updated := readToolLoop(path, st, m)
		n.mu.Lock()
		current := n.metas[path]
		if current.toolLoop.size == m.toolLoop.size && current.toolLoop.mtime.Equal(m.toolLoop.mtime) {
			current.toolLoop = updated.toolLoop
			n.metas[path] = current
		}
		m = current
		n.mu.Unlock()
	}
	return m.toolLoop.detector.Candidate()
}

func readToolLoop(path string, st os.FileInfo, m quickMeta) quickMeta {
	off := m.toolLoop.offset
	d := m.toolLoop.detector.Clone()
	skipping := m.toolLoop.skipping
	rewritten := st.Size() < m.toolLoop.size || st.Size() == m.toolLoop.size && !st.ModTime().Equal(m.toolLoop.mtime)
	tail := rewritten || st.Size()-off > toolLoopReadLimit
	if tail {
		d = transcript.LoopDetector{}
		off = max(st.Size()-toolLoopReadLimit, 0)
		skipping = off > 0
	}
	f, err := os.Open(path)
	if err != nil {
		// A failed read must retry from a safe boundary, just like a short read.
		m.toolLoop = toolLoopScan{}
		return m
	}
	defer f.Close()
	if tail && off > 0 {
		var previous [1]byte
		if n, _ := f.ReadAt(previous[:], off-1); n == 1 && previous[0] == '\n' {
			skipping = false
		}
	}
	buf := make([]byte, st.Size()-off)
	n, _ := f.ReadAt(buf, off)
	if n != len(buf) {
		// The file changed during the read. Discard the old position too, so
		// the next poll reconstructs history from the bounded tail.
		m.toolLoop = toolLoopScan{}
		return m
	}
	if skipping {
		// Discard the rest of a partial line, including across later reads.
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			off += int64(len(buf))
			m.toolLoop = toolLoopScan{detector: d, offset: off, size: st.Size(), mtime: st.ModTime(), skipping: true}
			return m
		}
		off += int64(i + 1)
		buf = buf[i+1:]
		skipping = false
	}
	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		d.Line(buf[:i])
		off += int64(i + 1)
		buf = buf[i+1:]
	}
	m.toolLoop = toolLoopScan{detector: d, offset: off, size: st.Size(), mtime: st.ModTime(), skipping: skipping}
	return m
}
