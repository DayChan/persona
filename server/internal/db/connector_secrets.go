package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ---- connector secrets ----

// ConnectorSecret is a connector's sealed secret; false when it has none.
func (d *DB) ConnectorSecret(ctx context.Context, connector string) (string, bool, error) {
	var sealed string
	err := d.pool.QueryRow(ctx, `SELECT sealed FROM connector_secrets WHERE connector = $1`, connector).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return sealed, err == nil, err
}

func (d *DB) PutConnectorSecret(ctx context.Context, connector, sealed string) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO connector_secrets (connector, sealed, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (connector) DO UPDATE SET sealed = $2, updated_at = now()`, connector, sealed)
	return err
}

func (d *DB) DeleteConnectorSecret(ctx context.Context, connector string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM connector_secrets WHERE connector = $1`, connector)
	return err
}
