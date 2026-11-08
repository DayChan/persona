//go:build linux

package rpc

import (
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpcio"
)

func TestConsumedProgressUnixPartialRead(t *testing.T) {
	writer, reader := socketPair(t)
	defer writer.Close()
	defer reader.Close()
	if err := reader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	counted := &ProgressWriter{W: writer}
	progress := ConsumedProgress(counted.Accepted, rpcio.OutstandingQuery(writer))
	if progress == nil {
		t.Fatal("unix socket measurement unavailable")
	}
	if _, err := counted.Write(make([]byte, progressPiece)); err != nil {
		t.Fatal(err)
	}
	accepted, first := counted.Accepted(), progress()
	// Partial reads may not release queue memory. Completing one bounded send
	// buffer must, without another Write or a change in accepted bytes.
	for range progressPiece / 256 {
		if _, err := io.ReadFull(reader, make([]byte, 256)); err != nil {
			t.Fatal(err)
		}
		_ = progress()
	}
	if counted.Accepted() != accepted || progress() <= first {
		t.Fatal("complete buffer consumption was invisible without a Write return")
	}
}

func TestConsumedProgressPipePartialRead(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	counted := &ProgressWriter{W: writer}
	progress := ConsumedProgress(counted.Accepted, rpcio.OutstandingQuery(writer))
	if progress == nil {
		t.Fatal("pipe measurement unavailable")
	}
	if _, err := counted.Write(make([]byte, progressPiece)); err != nil {
		t.Fatal(err)
	}
	first := progress()
	if _, err := io.ReadFull(reader, make([]byte, 256)); err != nil {
		t.Fatal(err)
	}
	if progress() <= first {
		t.Fatal("partial pipe consumption was invisible without a Write return")
	}
}

func TestConsumedProgressRejectsTCPAcknowledgements(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialTimeout("tcp", listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var accepted atomic.Int64
	if ConsumedProgress(accepted.Load, rpcio.OutstandingQuery(client)) != nil {
		t.Fatal("TCP acknowledgement was treated as application consumption")
	}
}
