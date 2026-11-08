//go:build !unix

package node

import (
	"errors"
	"os"
)

// Synchronous inherited handles are not an interruption capability. Until a
// platform-specific owned, cancellable stdio adapter exists, fail before I/O;
// generic Serve remains available with its explicit legacy limitations.
func openOwnedStdio(_, _ *os.File) (*ownedRPCTransport, error) {
	return nil, errors.New("node: interruptible exclusive stdio is unsupported on this platform")
}
