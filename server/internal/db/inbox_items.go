package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- inbox ----

const putInboxItemSQL = `INSERT INTO inbox_items (id, data, updated_at, done_at) VALUES ($1, $2, $3, $4)
	ON CONFLICT (id) DO UPDATE SET data = $2, updated_at = $3, done_at = $4`

// PutInboxItem stores one inbox item (the manager's inbox, assistant/inbox.go, owns the JSON).
func (d *DB) PutInboxItem(ctx context.Context, id string, data []byte, updatedAt time.Time, doneAt *time.Time) error {
	_, err := d.pool.Exec(ctx, putInboxItemSQL, id, data, updatedAt, doneAt)
	return err
}

// InboxRow is one inbox item for PutInboxItems.
type InboxRow struct {
	ID        string
	Data      []byte
	UpdatedAt time.Time
	DoneAt    *time.Time
}

// PutInboxItems stores inbox items in one round trip and one transaction: when one fails none is stored.
func (d *DB) PutInboxItems(ctx context.Context, rows []InboxRow) error {
	if len(rows) == 0 {
		return nil
	}
	// A bare pgx.Batch is only a pipeline of autocommit statements, so a connection failure midway can leave some
	// rows stored; the explicit transaction makes the all-or-nothing contract real.
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var b pgx.Batch
	for _, r := range rows {
		b.Queue(putInboxItemSQL, r.ID, r.Data, r.UpdatedAt, r.DoneAt)
	}
	if err := tx.SendBatch(ctx, &b).Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteInboxSource removes a source's inbox items for good (the user deleted its data): ids (the items the manager
// holds) and every other row whose item has that source, the done ones kept as history included. It returns the ids
// of the rows it deleted.
func (d *DB) DeleteInboxSource(ctx context.Context, source string, ids []string) ([]string, error) {
	rows, err := d.pool.Query(ctx, `DELETE FROM inbox_items WHERE id = ANY($1) OR data->>'source' = $2 RETURNING id`, ids, source)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// PurgeInboxItems deletes the items done before doneBefore (the history kept is over) and says how many went.
func (d *DB) PurgeInboxItems(ctx context.Context, doneBefore time.Time) (int64, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM inbox_items WHERE done_at < $1`, doneBefore)
	return tag.RowsAffected(), err
}

// InboxItems returns the open items and those done since doneSince, newest first; older done items stay stored (as
// history, DoneInboxItems) until PurgeInboxItems.
func (d *DB) InboxItems(ctx context.Context, doneSince time.Time) ([][]byte, error) {
	rows, err := d.pool.Query(ctx, `SELECT data FROM inbox_items WHERE done_at IS NULL OR done_at >= $1 ORDER BY updated_at DESC`, doneSince)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[[]byte])
}

// DoneInboxItemsByID returns the done items stored under ids (the history, whether the manager holds them or not); an
// id with no row, or an open one, has none.
func (d *DB) DoneInboxItemsByID(ctx context.Context, ids []string) ([][]byte, error) {
	rows, err := d.pool.Query(ctx, `SELECT data FROM inbox_items WHERE id = ANY($1) AND done_at IS NOT NULL`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[[]byte])
}

// InboxDoneQuery is one page of the done items (DoneInboxItems).
type InboxDoneQuery struct {
	// Before and BeforeID are where the page starts: the items done before Before, or at Before with an id below
	// BeforeID (the last item of the page before). Zero Before: from the newest.
	Before   time.Time
	BeforeID string
	Limit    int
	// Connectors includes the connectors' items (an item with a connector block, GitHub's…).
	Connectors bool
}

// The done items, newest first. The order and the cursor are (done_at, id), not done_at alone: a bulk action files
// many items at one instant, and a page ending among them would repeat or skip the rest. inbox_items_open (done_at
// first) serves both: an index scan backwards from the cursor, the ties sorted by id as they come.
const (
	doneInboxItemsSQL = `SELECT id, data, updated_at, done_at FROM inbox_items
	WHERE done_at IS NOT NULL AND ($1 OR jsonb_typeof(data->'connector') IS DISTINCT FROM 'object')
	ORDER BY done_at DESC, id DESC LIMIT $2`
	doneInboxItemsBeforeSQL = `SELECT id, data, updated_at, done_at FROM inbox_items
	WHERE (done_at, id) < ($3, $4) AND ($1 OR jsonb_typeof(data->'connector') IS DISTINCT FROM 'object')
	ORDER BY done_at DESC, id DESC LIMIT $2`
)

// DoneInboxItems returns one page of the done items kept (q), newest done first.
func (d *DB) DoneInboxItems(ctx context.Context, q InboxDoneQuery) ([]InboxRow, error) {
	var rows pgx.Rows
	var err error
	if q.Before.IsZero() {
		rows, err = d.pool.Query(ctx, doneInboxItemsSQL, q.Connectors, q.Limit)
	} else {
		rows, err = d.pool.Query(ctx, doneInboxItemsBeforeSQL, q.Connectors, q.Limit, q.Before, q.BeforeID)
	}
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (InboxRow, error) {
		var r InboxRow
		err := row.Scan(&r.ID, &r.Data, &r.UpdatedAt, &r.DoneAt)
		return r, err
	})
}
