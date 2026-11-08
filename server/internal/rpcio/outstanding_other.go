//go:build !linux

package rpcio

import "io"

// OutstandingQuery does not measure peer consumption on this platform, so an
// owned transport gets no progress signal and is never retired for a stall
// (fail-safe). Darwin's small socket buffers might make the accepted count
// adequate, but that is unverified; Windows pipes have no equivalent query here.
func OutstandingQuery(io.Writer) func() (int64, bool) { return nil }
