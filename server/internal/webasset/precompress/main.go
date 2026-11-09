// Command precompress keeps the built web client's hashed assets gzipped, as the hub sends them
// (webasset.Precompress): `go run ./internal/webasset/precompress <dir>`, from server/, on the copy that go build
// embeds (internal/hub/webdist), after the web client was copied there and its privacy was checked.
package main

import (
	"fmt"
	"os"

	"github.com/nwverse/persona/server/internal/webasset"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./internal/webasset/precompress <web client dir>")
		os.Exit(2)
	}
	dir := os.Args[1]
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		fmt.Fprintf(os.Stderr, "precompress: %s is not a directory\n", dir)
		os.Exit(2)
	}
	st, err := webasset.Precompress(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "precompress:", err)
		os.Exit(1)
	}
	fmt.Printf("precompress: %d files, %d -> %d bytes\n", st.Files, st.Before, st.After)
}
