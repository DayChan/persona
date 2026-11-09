package extmgr

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
)

// extensionLark stands in for a plugin's connector "lark": an inbox that records replies.
type extensionLark struct {
	things []connector.InboxThing
	got    []connector.InboxReply
}

func (*extensionLark) ID() string   { return "lark" }
func (*extensionLark) Name() string { return "飞书" }
func (*extensionLark) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox}, Kinds: []string{"dm", "doc"}}
}
func (c *extensionLark) Collect(context.Context) ([]connector.InboxThing, error) {
	return c.things, nil
}
func (c *extensionLark) Reply(_ context.Context, r connector.InboxReply) error {
	c.got = append(c.got, r)
	return nil
}

// A plugin's connector takes the id lark (the hub has none of its own any more); gone with the plugin.
func TestExtensionTakesOverLark(t *testing.T) {
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	h.registerConnectors()
	if h.connectors.Get("lark") != nil {
		t.Fatal("the hub has a Feishu connector of its own")
	}
	p := &extensionLark{}
	h.yieldOwnConnector("lark")
	if err := h.addConnector(p); err != nil {
		t.Fatal(err)
	}
	if h.connectors.Get("lark") != p {
		t.Fatal("the plugin's is the one now")
	}
	h.removeExtensionConnector("lark")
	if h.connectors.Get("lark") != nil {
		t.Fatal("still there after the plugin went")
	}
	h.yieldOwnConnector("mail") // not migratable: would stay (and this hub has none)
	if h.hasOwnConnector("lark") {
		t.Fatal("no Feishu connector of the hub's own")
	}
}

// GitHub moves out of the hub (#2830): an extension's connector github takes the hub's place while it is there, the
// hub's own is kept aside (it still checks the computers' gh logins for Settings › Connectors › GitHub) and comes back
// after.
func TestExtensionTakesOverGitHub(t *testing.T) {
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	h.registerConnectors()
	own := h.githubConnector()
	if own == nil {
		t.Fatal("the hub has its own GitHub connector")
	}
	h.yieldOwnConnector("github")
	if h.connectors.Get("github") != nil || h.githubConnector() != nil {
		t.Fatal("set aside for the extension's")
	}
	if h.ownGitHub() != own || !h.hasOwnConnector("github") {
		t.Fatal("the hub's own is kept: its sign-in and settings stay when the extension goes")
	}
	h.restoreOwnConnector("github")
	if h.githubConnector() != own {
		t.Fatal("back to the hub's own")
	}
}
