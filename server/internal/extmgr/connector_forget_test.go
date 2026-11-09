package extmgr

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
)

type forgetConnector struct {
	feedItemsConnector
	id      string
	sources []string
}

func (c *forgetConnector) ID() string { return c.id }
func (c *forgetConnector) Manifest() connector.Manifest {
	m := c.feedItemsConnector.Manifest()
	m.ArchiveSources = c.sources
	return m
}

func TestConnectorDeletionForwardsOnlyOwnedArchiveSources(t *testing.T) {
	for _, endpoint := range []string{"archive", "data"} {
		t.Run(endpoint, func(t *testing.T) {
			h := newTestManager(t)
			h.store = newKeepStore()
			h.connectors = connector.NewRegistry(nil)
			for _, c := range []connector.Connector{
				&forgetConnector{id: "example", sources: []string{"posts", "other", "shared"}},
				&forgetConnector{id: "other", sources: []string{"shared"}},
			} {
				if err := h.addConnector(c); err != nil {
					t.Fatal(err)
				}
			}
			// Disabling another connector does not release ownership of its saved content.
			if err := h.setConnectorEnabled(t.Context(), "other", false); err != nil {
				t.Fatal(err)
			}
			a := forgettingAssistant(h)
			code, out := call(t, h.routes(), "DELETE", "/api/v1/connectors/example/"+endpoint, h.cfg.Token,
				`{"archive_sources":["other","unrelated"]}`)
			if code != 200 || out["posts"] != 2.0 || out["items"] != 3.0 {
				t.Fatalf("delete: %d %v", code, out)
			}
			got := a.got(assistantwire.MethodKBForget)
			if len(got) != 1 {
				t.Fatalf("kb.forget requests: %s", got)
			}
			var p assistantwire.KBForget
			if err := json.Unmarshal(got[0], &p); err != nil {
				t.Fatal(err)
			}
			if p.Source != "example" || !slices.Equal(p.ArchiveSources, []string{"posts"}) {
				t.Fatalf("deletion scope: %+v", p)
			}
			// Deletion filtering must not alter the manifest or ingestion's permitted source names.
			if got := h.archiveSources(h.connectorByID("example")); !slices.Equal(got, []string{"posts", "shared"}) {
				t.Fatalf("ingestion sources changed: %v", got)
			}
		})
	}
}
