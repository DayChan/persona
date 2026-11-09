package proc

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
)

// Catalog reads the program's current sources, retaining the original JSON for
// validation before decoding can erase a malformed optional field.
func (c *Conn) Catalog(ctx context.Context) ([]connector.CatalogSource, error) {
	var raw json.RawMessage
	if err := c.call(ctx, serve.MCatalog, struct{}{}, &raw); err != nil {
		return nil, err
	}
	if err := wire.CheckConnectorCatalogJSON(raw); err != nil {
		return nil, err
	}
	var out connector.CatalogResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Sources, nil
}

// CatalogCall uses the same read-only admission as in-process connectors. The
// small direct adapter prevents admission from recursively calling itself.
func (c *Conn) CatalogCall(ctx context.Context, q connector.CatalogQuery) (string, error) {
	return connector.CallCatalog(ctx, catalogCaller{c}, q)
}

type catalogCaller struct{ *Conn }

func (c catalogCaller) CatalogCall(ctx context.Context, q connector.CatalogQuery) (string, error) {
	var out serve.CallResult
	err := c.call(ctx, serve.MCatalogCall, q, &out)
	return out.Text, err
}
