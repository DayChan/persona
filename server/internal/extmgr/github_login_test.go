package extmgr

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/github"
)

// extensionGitHub is an extension's connector that took the "github" id over (connector/proc in production): its
// account comes only through its status.
type extensionGitHub struct{ status connector.Status }

func (extensionGitHub) ID() string   { return "github" }
func (extensionGitHub) Name() string { return "GitHub" }
func (extensionGitHub) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus}}
}
func (c extensionGitHub) Status(context.Context) connector.Status { return c.status }

// The persona tiers read the GitHub account from whichever connector holds the id: an extension's counts as the hub's
// own does, through its status, instead of reading as "known, not signed in" (the lowest tier).
func TestGitHubLoginFromTheConnectorHoldingTheID(t *testing.T) {
	for _, c := range []struct {
		name  string
		conn  connector.Connector
		login string
		known bool
	}{
		{"no GitHub connector", nil, "", true},
		{"the extension's, signed in", extensionGitHub{connector.Status{State: connector.StateOK, Account: " octocat "}}, "octocat", true},
		{"the extension's, signed out", extensionGitHub{connector.Status{State: connector.StateNotSignedIn}}, "", true},
		{"the extension's, not said yet", extensionGitHub{}, "", false},
		{"the hub's own", signedInGitHub{github.New(github.Config{}, "Mac", nil)}, "", true},
	} {
		h := newTestManager(t)
		h.connectors = connector.NewRegistry(nil)
		if c.conn != nil {
			if err := h.addConnector(c.conn); err != nil {
				t.Fatal(err)
			}
		}
		if login, known := h.githubLogin(context.Background()); login != c.login || known != c.known {
			t.Errorf("%s: (%q, %v), want (%q, %v)", c.name, login, known, c.login, c.known)
		}
	}
}
