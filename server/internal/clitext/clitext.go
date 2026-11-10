// Package clitext is what every program's command line shares (personad, persona-node, persona-hub,
// persona-manager, persona-link): a usage mistake, the program's name in what it prints, and untrusted text made inert
// before it is printed (Inert). It depends on nothing of the server, so a program links it without the code of the
// others (docs/server/server.md "Programs").
package clitext

import (
	"os"
	"path/filepath"
	"strings"
)

// UsageError is a usage mistake: main prints it as it is (no log timestamp) and exits 2.
type UsageError string

func (e UsageError) Error() string { return string(e) }

// CommandName is the program's name in what it prints: personad, persona-node, persona-hub or persona-manager when
// run under that name (personad is the compatibility entry, docs/operations/rename.md), otherwise personad.
func CommandName() string {
	switch n := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"); n {
	case "personad", "persona-node", "persona-hub", "persona-manager":
		return n
	}
	return "personad"
}

// Named is a usage text (written with personad) with the program's name as it was run.
func Named(text string) string {
	return strings.ReplaceAll(text, "personad ", CommandName()+" ")
}

// IsHelpArg: -h or --help.
func IsHelpArg(a string) bool { return a == "-h" || a == "--help" }
