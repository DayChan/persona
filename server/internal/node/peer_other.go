//go:build !linux && !darwin

package node

import "net"

// peerPID: not known here. No command line counts as the assistant (cliCall); an MCP server must name a session the
// node knows as the assistant's (mcpAssistantOf).
func peerPID(net.Conn) (int, error) { return 0, errNoPeer }
