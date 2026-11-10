package manager

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// cmdCapabilities inspects this executable without loading configuration or joining a hub.
func cmdCapabilities(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("manager capabilities", flag.ContinueOnError)
	fs.SetOutput(out)
	asJSON := fs.Bool("json", false, "print capabilities as JSON")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return errUsage
	}
	caps := []string{assistantwire.CapabilityConfigOwn, assistantwire.CapabilityAgentSessionArchives, assistantwire.CapabilityMachinesOwn}
	if *asJSON {
		return json.NewEncoder(out).Encode(struct {
			Capabilities []string `json:"capabilities"`
		}{caps})
	}
	for _, capability := range caps {
		if _, err := fmt.Fprintln(out, capability); err != nil {
			return err
		}
	}
	return nil
}
