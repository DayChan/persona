package node

import (
	"io"
	"sync/atomic"

	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/rpcio"
)

// ownedRPCTransport carries an owner's explicit interruption capability, not a
// guess based on Writer/Closer/File type. interrupt must promptly unblock active
// I/O, including I/O through the response wrapper. Borrowed Serve streams do not
// acquire ownership. Only the stdio initializer and connection owner construct it.
// Its writes go out in pieces counted in accepted; outstanding queue data then
// provides a progress token, not a consumed-byte count (rpc.ConsumedProgress).
// Both owners' writers are byte streams (pipes, sockets, binary websocket conns),
// so pieces are safe. Stdio pipes or socketpairs and accepted unix connections are
// measured on Linux; TCP acknowledgements, writers without a descriptor and other
// platforms do not provide consumption signals, so they are not retired for a
// stall. measure, if set, replaces that signal (in-memory test transports).
type ownedRPCTransport struct {
	io.Reader
	io.Writer
	interrupt func()
	accepted  atomic.Int64
	measure   func() int64
}

func (t *ownedRPCTransport) Write(b []byte) (int, error) {
	return rpc.WriteCounted(t.Writer, b, &t.accepted)
}

// newConnectionPeer retains ownership before the update-response wrapper hides
// it. Unknown and borrowed streams keep the legacy, non-hard-interrupt contract.
func newConnectionPeer(w, transport io.Writer) *rpc.Peer {
	peer := rpc.New("n", w)
	peer.Cancels = map[string]bool{"mcp.call": true}
	if owned, ok := transport.(*ownedRPCTransport); ok {
		peer.Interrupt = owned.interrupt
		peer.Progress = owned.measure
		if peer.Progress == nil {
			peer.Progress = rpc.ConsumedProgress(owned.accepted.Load, rpcio.OutstandingQuery(owned.Writer))
		}
	}
	return peer
}
