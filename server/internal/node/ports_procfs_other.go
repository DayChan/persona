//go:build !linux

package node

import "errors"

// procfsProcs is Linux's process listing (ports_procfs_linux.go); macOS runs ps, Windows takes a Toolhelp snapshot.
func procfsProcs(string) (map[int]proc, error) { return nil, errors.New("no /proc process table here") }
