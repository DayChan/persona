package node

import (
	"crypto/sha256"
	"os"
	"sync"

	"github.com/nwverse/persona/server/internal/transcript"
)

// A resident node (attach.go) makes a new Node for every hub connection. Each new Node read every listed transcript
// whole again for its background tasks (bgscan.go): on a real corpus that was about half of a reconnect's CPU and a
// quarter of a gigabyte read, and the first list after the reconnect lacked those sessions' background state until
// the big reads were done. The scans are carried from one connection's Node to the next instead, and each is checked
// on its first use on the new connection: it goes on from its offset only when the path is still the file it read
// (os.SameFile: device and inode on Unix) and that file still starts with the bytes it read (bgHeadLen of them at
// most, hashed). Anything else starts it over from the beginning, as on a node's first connection.

// bgCarry holds a resident node's background-task scans between two connections' Nodes.
type bgCarry struct {
	mu    sync.Mutex
	scans map[string]*bgScan
}

// adoptBgScans gives n the scans the last connection's Node handed over: none on the first connection, nor when that
// Node had not ended yet (Attach waits for it 10 s at most). Called before Serve, while nothing else runs on n.
func (n *Node) adoptBgScans(c *bgCarry) {
	c.mu.Lock()
	scans := c.scans
	c.scans = nil
	c.mu.Unlock()
	if len(scans) == 0 {
		return
	}
	for _, sc := range scans { // each one's lock without n.mu: n.mu is never held while taking a bgScan's lock
		sc.mu.Lock()
		sc.seen.carried = true
		sc.mu.Unlock()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.bgScans == nil {
		n.bgScans = map[string]*bgScan{}
	}
	for p, sc := range scans {
		if n.bgScans[p] == nil {
			n.bgScans[p] = sc
		}
	}
}

// handOverBgScans leaves n's scans for the next connection's Node, once n's Serve has returned. Something of n still
// running reads into a map of its own from then on, so a scan is never in two Nodes' maps.
func (n *Node) handOverBgScans(c *bgCarry) {
	n.mu.Lock()
	scans := n.bgScans
	n.bgScans = map[string]*bgScan{}
	n.mu.Unlock()
	c.mu.Lock()
	c.scans = scans // a Node that ends later than the next one began leaves its scans for the one after that
	c.mu.Unlock()
}

// bgHeadLen: how many of a transcript's first bytes a scan hashes to know the file again on the next connection.
const bgHeadLen = 4 << 10

// bgSeen is the file a scan read, to tell on a later connection whether the path still holds it.
type bgSeen struct {
	carried bool        // handed over from the last connection's Node and not checked since
	file    os.FileInfo // the file read, from its open handle (os.SameFile)
	headLen int64       // min(offset, bgHeadLen): how many of its first bytes head is of
	head    [sha256.Size]byte
}

// note records f (st its Stat) as the file a scan has read up to off. It reads the file's head only when that is not
// the same file, or the scan's offset is still within the head; a scan that goes on adds nothing to read.
func (s *bgSeen) note(f *os.File, st os.FileInfo, off int64) {
	n := min(off, bgHeadLen)
	if s.file != nil && os.SameFile(s.file, st) && s.headLen == n {
		return
	}
	head, ok := headSum(f, n)
	if !ok {
		s.file, s.headLen = nil, 0 // not known again: the next connection starts the scan over
		return
	}
	s.file, s.headLen, s.head = st, n, head
}

// checkCarried is a carried scan's first use on the new connection (caller holds sc.mu): it keeps its offset and meta
// only when f, the path opened now (st its Stat), is the file it read, not shorter than what it read, and starts with
// the same bytes. f nil: the path could not be read. A scan whose big first read still runs is checked once it is done.
func (sc *bgScan) checkCarried(f *os.File, st os.FileInfo) {
	if !sc.seen.carried || sc.pending {
		return
	}
	sc.seen.carried = false
	if f != nil && sc.seen.file != nil && os.SameFile(sc.seen.file, st) && st.Size() >= sc.off {
		if head, ok := headSum(f, sc.seen.headLen); ok && head == sc.seen.head {
			return
		}
	}
	sc.off, sc.meta, sc.seen = 0, transcript.Meta{}, bgSeen{}
}

// headSum hashes the first n bytes of f (at most bgHeadLen); false when f has fewer.
func headSum(f *os.File, n int64) ([sha256.Size]byte, bool) {
	var buf [bgHeadLen]byte
	if n > bgHeadLen {
		n = bgHeadLen
	}
	if got, _ := f.ReadAt(buf[:n], 0); int64(got) != n {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256(buf[:n]), true
}
