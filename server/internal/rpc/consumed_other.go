//go:build !linux

package rpc

import "io"

// unreadQuery: consumption by the peer is not measured on this platform, so an
// owned transport here never gets a progress signal and is never retired for a
// stall (fail-safe). Darwin's small socket buffers might make the accepted count
// adequate, but that is unverified; Windows pipes have no equivalent query here.
func unreadQuery(io.Writer) func() (int64, bool) { return nil }
