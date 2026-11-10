package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- inbox ----

const putInboxItemSQL = `INSERT INTO inbox_items (id, data, updated_at, done_at) VALUES ($1, $2, $3, $4)
	ON CONFLICT (id) DO UPDATE SET data = $2, updated_at = $3, done_at = $4`

// PutInboxItem stores one inbox item (the manager's inbox, assistant/inbox.go, owns the JSON). data is stored as json,
// the bytes as written (0471: every reader unmarshals them). It must still be valid JSON with no \u0000 in a string, as
// jsonb required: Postgres computes the source column from it, and that refuses the write.
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

// deleteInboxSourceSQL finds a source's rows through inbox_items_source: source is the item's, kept by Postgres (0471),
// so the delete reads no document. data->>'source' read every row's JSON (the done ones kept as history included).
const deleteInboxSourceSQL = `DELETE FROM inbox_items WHERE id = ANY($1) OR source = $2 RETURNING id`

// DeleteInboxSource removes a source's inbox items for good (the user deleted its data): ids (the items the manager
// holds) and every other row whose item has that source, the done ones kept as history included. It returns the ids
// of the rows it deleted.
func (d *DB) DeleteInboxSource(ctx context.Context, source string, ids []string) ([]string, error) {
	rows, err := d.pool.Query(ctx, deleteInboxSourceSQL, ids, source)
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

// inboxItemsSQL has no ORDER BY: the manager keys what it loads by id, so the order was never used, and the sort cost
// the start read a Sort node over every row.
const inboxItemsSQL = `SELECT data FROM inbox_items WHERE done_at IS NULL OR done_at >= $1`

// InboxItems hands each open item, and each one done since doneSince, to each, in no particular order; older done
// items stay stored (as history, DoneInboxItems) until PurgeInboxItems. raw is the row's data in the driver's read
// buffer, valid only during that call: each copies what it keeps (json.Unmarshal does), so the rows are never all
// held as bytes at once. An error from each stops the read and is returned.
func (d *DB) InboxItems(ctx context.Context, doneSince time.Time, each func(raw []byte) error) error {
	rows, err := d.pool.Query(ctx, inboxItemsSQL, doneSince)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows.RawValues()[0]); err != nil {
			return err
		}
	}
	return rows.Err()
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
// many items at one instant, and a page ending among them would repeat or skip the rest. inbox_items_open is
// (done_at, id) (0471) and serves both: an index scan backwards from the cursor, in the page's order. data is json
// (0471), hence json_typeof; a build from before 0471 still runs its jsonb_typeof through the migration's shim.
const (
	doneInboxItemsSQL = `SELECT id, data, updated_at, done_at FROM inbox_items
	WHERE done_at IS NOT NULL AND ($1 OR json_typeof(data->'connector') IS DISTINCT FROM 'object')
	ORDER BY done_at DESC, id DESC LIMIT $2`
	doneInboxItemsBeforeSQL = `SELECT id, data, updated_at, done_at FROM inbox_items
	WHERE (done_at, id) < ($3, $4) AND ($1 OR json_typeof(data->'connector') IS DISTINCT FROM 'object')
	ORDER BY done_at DESC, id DESC LIMIT $2`
)

// InboxDay is how many done items one calendar day has (DoneInboxDays).
type InboxDay struct {
	Day   string `json:"day"` // YYYY-MM-DD in the zone asked for
	Count int    `json:"count"`
}

// doneInboxDaysSQL counts the done items kept per calendar day in the zone $1, newest day first. done_at is timestamptz,
// so AT TIME ZONE gives the day's wall-clock time there. With the connectors' items ($2) it reads only done_at, from
// inbox_items_open; without them, each done row's connector, as the history's pages do.
const doneInboxDaysSQL = `SELECT to_char(done_at AT TIME ZONE $1, 'YYYY-MM-DD') AS day, count(*) FROM inbox_items
	WHERE done_at IS NOT NULL AND ($2 OR json_typeof(data->'connector') IS DISTINCT FROM 'object')
	GROUP BY day ORDER BY day DESC`

// DoneInboxDays counts the done items kept per calendar day in zone (an IANA name the caller has checked), newest
// day first; days with none are left out.
func (d *DB) DoneInboxDays(ctx context.Context, zone string, connectors bool) ([]InboxDay, error) {
	rows, err := d.pool.Query(ctx, doneInboxDaysSQL, zone, connectors)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (InboxDay, error) {
		var day InboxDay
		err := row.Scan(&day.Day, &day.Count)
		return day, err
	})
}

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
