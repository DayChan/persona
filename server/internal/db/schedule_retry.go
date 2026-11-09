package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	scheduleKey     = "assistant.tasks"
	scheduleRunsKey = "assistant.runs"
	originsKey      = "assistant.origins"
)

// scheduleOwnerLock is the session advisory lock key for the schedule retry owner.
const scheduleOwnerLock int64 = 0x706d7363686564

// ReadSchedule reads the task list and its row version from the same settings
// table as Setting. A legacy null value is returned unchanged: it means the list
// has not yet been migrated. Revision zero is reserved for a missing row.
func (d *DB) ReadSchedule(ctx context.Context) (tasks json.RawMessage, revision int64, found bool, err error) {
	return d.readScheduleList(ctx, scheduleKey)
}

// ReadScheduleRuns reads the run history and its row version. A missing row
// has revision zero; an existing legacy null is returned unchanged.
func (d *DB) ReadScheduleRuns(ctx context.Context) (json.RawMessage, int64, bool, error) {
	return d.readScheduleList(ctx, scheduleRunsKey)
}

func (d *DB) readScheduleList(ctx context.Context, key string) (tasks json.RawMessage, revision int64, found bool, err error) {
	err = d.pool.QueryRow(ctx, `SELECT value, xmin::text::bigint FROM `+d.settingsTable()+` WHERE key = $1`, key).Scan(&tasks, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if !scheduleJSON(tasks, true) || revision == 0 {
		return nil, 0, false, errors.New("invalid stored schedule")
	}
	return tasks, revision, true, nil
}

func scheduleJSON(raw json.RawMessage, allowNull bool) bool {
	raw = bytes.TrimSpace(raw)
	return json.Valid(raw) && ((len(raw) > 0 && raw[0] == '[') || (allowNull && bytes.Equal(raw, []byte("null"))))
}

// CompareSchedule writes a task array only if the row still has the observed
// revision. Every ordinary settings update changes xmin, including old writers.
func (d *DB) CompareSchedule(ctx context.Context, revision int64, tasks json.RawMessage) (bool, error) {
	return d.compareScheduleList(ctx, scheduleKey, revision, tasks)
}

// CompareScheduleRuns writes a run history array if its row version matches.
// Ordinary settings writes change that version, including writes by older builds.
func (d *DB) CompareScheduleRuns(ctx context.Context, revision int64, runs json.RawMessage) (bool, error) {
	return d.compareScheduleList(ctx, scheduleRunsKey, revision, runs)
}

func (d *DB) compareScheduleList(ctx context.Context, key string, revision int64, tasks json.RawMessage) (bool, error) {
	if revision < 0 || !scheduleJSON(tasks, false) {
		return false, errors.New("invalid schedule revision or task array")
	}
	var sql string
	if revision == 0 {
		sql = `INSERT INTO ` + d.settingsTable() + ` (key, value, updated_at) VALUES ($1, $2::jsonb, now()) ON CONFLICT (key) DO NOTHING`
	} else {
		sql = `UPDATE ` + d.settingsTable() + ` SET value = $2::jsonb, updated_at = now() WHERE key = $1 AND xmin::text::bigint = $3`
	}
	args := []any{key, []byte(tasks)}
	if revision != 0 {
		args = append(args, revision)
	}
	tag, err := d.pool.Exec(ctx, sql, args...)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ReadOrigins reads the legacy origin map and its row version without changing the JSON format.
func (d *DB) ReadOrigins(ctx context.Context) (json.RawMessage, int64, bool, error) {
	var raw json.RawMessage
	var revision int64
	err := d.pool.QueryRow(ctx, `SELECT value, xmin::text::bigint FROM `+d.settingsTable()+` WHERE key = $1`, originsKey).Scan(&raw, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	trimmed := bytes.TrimSpace(raw)
	if !json.Valid(trimmed) || (!bytes.Equal(trimmed, []byte("null")) && (len(trimmed) == 0 || trimmed[0] != '{')) || revision == 0 {
		return nil, 0, false, errors.New("invalid stored origins")
	}
	return raw, revision, true, nil
}

// CompareOrigins replaces the origin map only if its original row version still matches.
func (d *DB) CompareOrigins(ctx context.Context, revision int64, raw json.RawMessage) (bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if revision < 0 || !json.Valid(trimmed) || len(trimmed) == 0 || trimmed[0] != '{' {
		return false, errors.New("invalid origins revision or map")
	}
	var sql string
	if revision == 0 {
		sql = `INSERT INTO ` + d.settingsTable() + ` (key, value, updated_at) VALUES ($1, $2::jsonb, now()) ON CONFLICT (key) DO NOTHING`
	} else {
		sql = `UPDATE ` + d.settingsTable() + ` SET value = $2::jsonb, updated_at = now() WHERE key = $1 AND xmin::text::bigint = $3`
	}
	args := []any{originsKey, []byte(raw)}
	if revision != 0 {
		args = append(args, revision)
	}
	tag, err := d.pool.Exec(ctx, sql, args...)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// TryScheduleOwner tries to hold the exclusive session lock on a dedicated
// pool connection. ctx governs the entire ownership lifetime, not just the
// acquisition: cancellation releases the lock. For an orderly shutdown, use
// an ownership context that stays live until all workers have stopped and been
// joined, then release or cancel it. done closes after loss, cancellation, or
// release; it does not imply the caller's workers have stopped. On unexpected
// loss, stop the workers when done closes. A lost lock is never retaken.
func (d *DB) TryScheduleOwner(ctx context.Context) (done <-chan struct{}, release func(), acquired bool, err error) {
	c, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	var got bool
	err = c.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, scheduleOwnerLock).Scan(&got)
	if err != nil {
		// The server may have acquired the lock even if its reply was lost.
		closeScheduleConn(c.Conn())
		c.Release()
		return nil, nil, false, err
	}
	if !got {
		c.Release()
		return nil, nil, false, nil
	}
	if err := ctx.Err(); err != nil {
		closeScheduleConn(c.Conn())
		c.Release()
		return nil, nil, false, err
	}
	ownerCtx, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer c.Release()
		defer closeScheduleConn(c.Conn()) // session close releases the lock; never return it locked to the pool
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ownerCtx.Done():
				return
			case <-ticker.C:
				pingCtx, stop := context.WithTimeout(ownerCtx, time.Second)
				err := c.Ping(pingCtx)
				stop()
				if err != nil {
					return
				}
			}
		}
	}()
	return finished, func() { cancel(); <-finished }, true, nil
}

func closeScheduleConn(c *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.Close(ctx)
}
