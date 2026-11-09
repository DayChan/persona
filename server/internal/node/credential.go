package node

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Credentials the hub reads from this machine for a connector (docs/plans/plan-connectors.md "4 · Credentials"). Only the hub
// asks — it is a hub → node RPC, the node socket of hooks and MCP does not route here — and nothing is logged.

var ghHost = lazyre.New(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)

// ghToken is `gh auth token` for host on this machine, without prompting.
func ghToken(ctx context.Context, host string) (map[string]any, error) {
	if host == "" {
		host = "github.com"
	}
	if !ghHost.MatchString(host) {
		return nil, &rpc.Error{Message: "bad host"}
	}
	gh, err := ghLookPath()
	if err != nil {
		return nil, &rpc.Error{Code: proto.CodeNoGh, Message: ghMissing(err)}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gh, "auth", "token", "--hostname", host)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	token := strings.TrimSpace(string(out))
	if err != nil || token == "" {
		// gh says "no oauth token found for <host>" both when it is not signed in and when the token is in a keychain
		// this session cannot open; the hub's message covers both.
		why := firstLine(stderr.String())
		if why == "" && err != nil {
			why = err.Error()
		}
		return nil, &rpc.Error{Code: proto.CodeNotSignedIn, Message: "gh auth token: " + why}
	}
	return map[string]any{"token": token}, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
