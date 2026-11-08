package rpcio

import (
	"io"
	"testing"
)

func TestOutstandingQueryUnavailableWriter(t *testing.T) {
	if OutstandingQuery(io.Discard) != nil {
		t.Fatal("writer without a descriptor acquired a consumption signal")
	}
}
