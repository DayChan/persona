package proc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/rpc"
)

type catalogProgram struct{ *exampleProgram }

func (p *catalogProgram) Manifest() connector.Manifest {
	m := p.exampleProgram.Manifest()
	m.Capabilities = append(m.Capabilities, connector.CapCatalog)
	return m
}
func (*catalogProgram) Catalog(context.Context) ([]connector.CatalogSource, error) {
	return []connector.CatalogSource{{ID: "notices", Name: "Notices", Tools: []connector.CatalogTool{{Name: "list", Description: "Read notices", InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}}}, nil
}
func (*catalogProgram) CatalogCall(_ context.Context, q connector.CatalogQuery) (string, error) {
	if string(q.Args) == `{"limited":true}` {
		return "", &rpc.Error{Code: "rate_limited", Message: "Wait", Data: json.RawMessage(`{"retry_after":12.5}`)}
	}
	return q.Source + ":" + q.Tool + ":" + string(q.Args), nil
}
func TestProgramCatalog(t *testing.T) {
	for _, plugin := range []bool{false, true} {
		t.Run(map[bool]string{false: "connector protocol", true: "plugin protocol"}[plugin], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var c *Conn
			if plugin {
				c = linkedAs(t, &memSecrets{}, (&catalogProgram{&exampleProgram{}}).Manifest(), "PERSONA_PROC_KIND=catalog")
			} else {
				c = New(program(t, &memSecrets{}, "PERSONA_PROC_KIND=catalog"))
			}
			go c.Run(ctx)
			waitUntil(t, "catalog program started", func() bool { return c.Status(ctx).State == connector.StateNotSignedIn })
			sources, err := connector.ReadCatalog(ctx, c)
			if err != nil || len(sources) != 1 || sources[0].ID != "notices" {
				t.Fatalf("%+v %v", sources, err)
			}
			q := connector.CatalogQuery{Source: "notices", Tool: "list", Args: json.RawMessage(`{"pageToken":"next"}`)}
			if text, err := c.CatalogCall(ctx, q); err != nil || text != `notices:list:{"pageToken":"next"}` {
				t.Fatalf("%q %v", text, err)
			}
			q.Tool = "send"
			var re *rpc.Error
			if _, err := c.CatalogCall(ctx, q); !errors.As(err, &re) || re.Code != "not_read_only" {
				t.Fatalf("a missing tool: %v", err)
			}
			// Exercise the receiving adapter directly, independently of the proxy's admission.
			var out serve.CallResult
			if err := c.call(ctx, serve.MCatalogCall, q, &out); !errors.As(err, &re) || re.Code != "not_read_only" {
				t.Fatalf("program admitted a missing tool: %v", err)
			}
			q.Tool = "list"
			q.Args = json.RawMessage(`{"limited":true}`)
			if _, err := c.CatalogCall(ctx, q); !errors.As(err, &re) || re.Code != "rate_limited" || string(re.Data) != `{"retry_after":12.5}` {
				t.Fatalf("rate limit changed: %v", err)
			}
		})
	}
}
