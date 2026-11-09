package connector

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Catalog is an optional collection of subscribable read-only sources. Find it
// through ReadCatalog and dispatch through CallCatalog, which also serve older
// connectors' declared ReadTools without exposing any Actions.
type Catalog interface {
	Catalog(context.Context) ([]CatalogSource, error)
	CatalogCall(context.Context, CatalogQuery) (string, error)
}

// Catalog payloads share the schema-generated SDK types with extension authors.
type (
	CatalogSource      = wire.ConnectorCatalogSource
	CatalogTool        = wire.ConnectorCatalogTool
	CatalogAnnotations = wire.ConnectorCatalogAnnotations
	SubscriptionRecipe = wire.ConnectorSubscriptionRecipe
	SubscriptionFields = wire.ConnectorSubscriptionFields
	CatalogResult      = wire.ConnectorCatalog
)

type CatalogQuery struct {
	Source string          `json:"source"`
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args,omitempty"`
}

// ReadCatalog validates the catalog a connector declares. Without that optional
// capability, its existing read tools form one source whose stable id is the
// connector id, with their <id>_ prefix removed from the short names.
func ReadCatalog(ctx context.Context, c Connector) ([]CatalogSource, error) {
	sources := []CatalogSource{}
	if catalog, ok := As[Catalog](c); ok {
		var err error
		sources, err = catalog.Catalog(ctx)
		if err != nil {
			return nil, err
		}
	} else if tools, ok := As[ReadTools](c); ok {
		source := CatalogSource{ID: c.ID(), Name: c.Name(), Tools: []CatalogTool{}}
		actionNames := map[string]bool{}
		if actions, ok := As[Actions](c); ok {
			for _, action := range actions.Actions() {
				actionNames[action.Name] = true
			}
		}
		for _, t := range tools.Tools() {
			if actionNames[t.Name] {
				return nil, notReadOnly()
			}
			if !strings.HasPrefix(t.Name, c.ID()+"_") {
				return nil, errors.New("read tool lacks its connector prefix")
			}
			source.Tools = append(source.Tools, CatalogTool{Name: strings.TrimPrefix(t.Name, c.ID()+"_"),
				Description: t.Description, InputSchema: t.InputSchema, ReadOnly: true})
		}
		sources = append(sources, source)
	}
	if sources == nil {
		sources = []CatalogSource{}
	}
	if err := wire.CheckConnectorCatalog(CatalogResult{Sources: sources}); err != nil {
		if errors.Is(err, wire.ErrCatalogNotReadOnly) {
			return nil, notReadOnly()
		}
		return nil, err
	}
	return sources, nil
}

// CallCatalog re-reads the current declaration before dispatch. Unknown sources
// or tools and contradictory read-only hints never reach the underlying call.
// Upstream coded errors (including rate_limited and its data) pass through.
func CallCatalog(ctx context.Context, c Connector, q CatalogQuery) (string, error) {
	sources, err := ReadCatalog(ctx, c)
	if err != nil {
		if errors.Is(err, wire.ErrCatalogNotReadOnly) {
			return "", notReadOnly()
		}
		return "", err
	}
	found := false
	for _, s := range sources {
		for _, t := range s.Tools {
			if s.ID == q.Source && t.Name == q.Tool {
				found = true
			}
		}
	}
	if !found {
		return "", notReadOnly()
	}
	if len(q.Args) == 0 {
		q.Args = json.RawMessage(`{}`)
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(q.Args, &args) != nil || args == nil {
		return "", errors.New("catalog.call args must be an object")
	}
	if catalog, ok := As[Catalog](c); ok {
		return catalog.CatalogCall(ctx, q)
	}
	tools, ok := As[ReadTools](c)
	if !ok {
		return "", notReadOnly()
	}
	return tools.Call(ctx, c.ID()+"_"+q.Tool, q.Args)
}

func notReadOnly() error {
	return &rpc.Error{Code: "not_read_only", Message: "The source and tool must be in the read-only catalog"}
}
