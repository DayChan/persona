package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The manager's secrets sealed with its secret.key (internal/secretkey), where they are in its database: the built-in
// manager's key rotation (internal/assistant keyrotate.go) re-seals every one of them. TestSealedColumnsAreListed
// fails for a column of the manager set that looks sealed and is not listed here; the packages that keep sealed values
// in settings check their keys and fields against sealedSettings.

// sealedTable is a table with sealed columns: its primary key, and the columns.
type sealedTable struct {
	table string
	key   []string
	cols  []string
}

var sealedTables = []sealedTable{
	{"mail_accounts", []string{"id"}, []string{"password_enc", "oauth_enc"}}, // internal/mail
	{"connector_secrets", []string{"connector"}, []string{"sealed"}},         // internal/extmgr connectors
	{"plugin_secrets", []string{"plugin", "name"}, []string{"sealed"}},       // internal/extension registry
	{"plugin_markets", []string{"id"}, []string{"token_sealed"}},             // internal/extmgr extension markets
}

// SealedSetting is a settings value (jsonb) with sealed strings in it: a list of objects (List) or one object, and the
// fields of each object that are sealed.
type SealedSetting struct {
	Key    string
	List   bool
	Fields []string
}

var sealedSettings = []SealedSetting{
	{Key: "launchers.managed", List: true, Fields: []string{"key_sealed", "headers_sealed", "env_sealed"}}, // internal/assistant storedLauncher
	{Key: "launchers.machine_keys", List: true, Fields: []string{"key_sealed"}},                            // internal/assistant machineKey
	{Key: "bots.feishu", Fields: []string{"secret_enc"}},                                                   // internal/extmgr FeishuBotSettings
	{Key: "mail.microsoft_client", Fields: []string{"enc"}},                                                // internal/mail sealedSetting
}

// SealedSettings are the settings values that hold sealed strings (the packages that write them check theirs is here).
func SealedSettings() []SealedSetting { return sealedSettings }

// SealedColumns are the sealed columns by table ("mail_accounts.password_enc").
func SealedColumns() []string {
	var out []string
	for _, t := range sealedTables {
		for _, c := range t.cols {
			out = append(out, t.table+"."+c)
		}
	}
	return out
}

// ResealOutcome is what a re-seal did with one value.
type ResealOutcome int

const (
	// ResealKept: already sealed with the key the re-seal is for; left as it is.
	ResealKept ResealOutcome = iota
	// ResealDone: opened with an old key and sealed again with the new one.
	ResealDone
	// ResealUnreadable: no key it has opens it; left as it is.
	ResealUnreadable
)

// ResealReport counts a re-seal's values; Unreadable names where the unreadable ones are (table.column or
// settings key.field, never a value).
type ResealReport struct {
	Kept, Resealed int
	Unreadable     []string
}

// SealingKey is the record of which key the manager's secrets in this database are sealed with (sealing_key).
type SealingKey struct {
	Fingerprint string
	// Previous is the fingerprint of the key it replaced.
	Previous  string
	RotatedAt time.Time
	// Age is how long ago it was rotated, by the database's clock.
	Age time.Duration
	// PreviousDropped is when the old key was deleted; zero while it is kept.
	PreviousDropped time.Time
}

// ErrHubTables refuses a re-seal in a database that has the hub's tables too: one the hub shares, whose manager rows
// an older build switched back opens with the hub's key.
var ErrHubTables = errors.New("this database has the hub's tables too")

// ErrResealRefused: a value of a shape the re-seal does not know (refused before anything was committed).
var ErrResealRefused = errors.New("a sealed value of an unexpected shape")

// ErrCommitUnknown: the re-seal's COMMIT failed on the way (a connection cut): it may have been committed or not.
// SealingKeySettled tells which.
var ErrCommitUnknown = errors.New("the re-seal's commit did not answer")

// ErrSealingKeyChanged refuses a re-seal whose record of the key is no longer what the rotation read: another process
// rotated meanwhile (its advisory lock lost), and sealing over its values would leave them sealed with a key it drops.
var ErrSealingKeyChanged = errors.New("another process changed the key the secrets are sealed with")

// SealingKey is the record of the key the secrets are sealed with; nil before the first rotation.
func (d *DB) SealingKey(ctx context.Context) (*SealingKey, error) {
	var k SealingKey
	var dropped *time.Time
	var age float64
	err := d.pool.QueryRow(ctx, `SELECT fingerprint, previous, rotated_at, previous_dropped_at,
		extract(epoch FROM now() - rotated_at)::float8 FROM sealing_key`).
		Scan(&k.Fingerprint, &k.Previous, &k.RotatedAt, &dropped, &age)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if dropped != nil {
		k.PreviousDropped = *dropped
	}
	k.Age = time.Duration(age * float64(time.Second))
	return &k, nil
}

// MarkPreviousDropped records that the old key was deleted.
func (d *DB) MarkPreviousDropped(ctx context.Context) error {
	_, err := d.pool.Exec(ctx, `UPDATE sealing_key SET previous_dropped_at = now()`)
	return err
}

// HasHubTables is whether this database has any of the tables only the hub's set makes.
func (d *DB) HasHubTables(ctx context.Context) (bool, error) {
	return hasAny(ctx, d.pool, hubOnlyTables())
}

// hubOnlyTables are the hub set's tables the manager set does not make too (sent_messages is in both).
func hubOnlyTables() []string {
	mine := ManagerTables()
	var out []string
	for _, t := range HubTables() {
		if !slices.Contains(mine, t) {
			out = append(out, t)
		}
	}
	return out
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func hasAny(ctx context.Context, q queryRower, tables []string) (bool, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM unnest($1::text[]) AS t(name) WHERE to_regclass(quote_ident(t.name)) IS NOT NULL`, tables).Scan(&n)
	return n > 0, err
}

// ResealSecrets passes every sealed value of the manager's in this database through fn and writes back what it
// changed, in one transaction; with rec, the same transaction records it as the key they are sealed with now
// (sealing_key), so the values and the record go together. expect is the fingerprint the record must have when it
// starts ("" for none yet), checked under a lock of the record's table held to the end: ErrSealingKeyChanged when
// another process changed it. A value is "" when unset: those are skipped. ErrHubTables in a database the hub shares.
// A settings value of an unexpected shape is an error, not skipped: a re-seal that cannot see all of them must not let
// the old key go.
func (d *DB) ResealSecrets(ctx context.Context, fn func(sealed string) (string, ResealOutcome, error), expect string, rec *SealingKey) (ResealReport, error) {
	var rep ResealReport
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return rep, err
	}
	defer tx.Rollback(context.Background())
	if hub, err := hasAny(ctx, tx, hubOnlyTables()); err != nil {
		return rep, err
	} else if hub {
		return rep, ErrHubTables
	}
	// Bounded: an orphaned re-seal (its client gone, the server not told) is ended within a couple of minutes, and a
	// re-seal waits that long at most for one.
	if _, err := tx.Exec(ctx, `SET LOCAL idle_in_transaction_session_timeout = '2min'`); err != nil {
		return rep, err
	}
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '3min'`); err != nil {
		return rep, err
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE sealing_key IN EXCLUSIVE MODE`); err != nil {
		return rep, err
	}
	var now string
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT fingerprint FROM sealing_key), '')`).Scan(&now); err != nil {
		return rep, err
	}
	if now != expect {
		return rep, ErrSealingKeyChanged
	}
	count := func(where string, o ResealOutcome) {
		switch o {
		case ResealKept:
			rep.Kept++
		case ResealDone:
			rep.Resealed++
		case ResealUnreadable:
			rep.Unreadable = append(rep.Unreadable, where)
		}
	}
	for _, t := range sealedTables {
		if err := resealTable(ctx, tx, t, fn, count); err != nil {
			return rep, fmt.Errorf("%s: %w", t.table, err)
		}
	}
	for _, s := range sealedSettings {
		if err := resealSetting(ctx, tx, s, fn, count); err != nil {
			return rep, fmt.Errorf("settings %s: %w", s.Key, err)
		}
	}
	if rec != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO sealing_key (one, fingerprint, previous, rotated_at) VALUES (true, $1, $2, now())
			ON CONFLICT (one) DO UPDATE SET fingerprint = $1, previous = $2, rotated_at = now(), previous_dropped_at = NULL`,
			rec.Fingerprint, rec.Previous); err != nil {
			return rep, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		// Every error before this one left nothing committed; this one may have.
		return rep, fmt.Errorf("%w: %w", ErrCommitUnknown, err)
	}
	return rep, nil
}

// SealingKeySettled is SealingKey once no re-seal is open: it waits (up to wait) for one that holds the record's
// table, which commits or ends meanwhile. After ErrCommitUnknown it tells whether that re-seal went through; an error
// when it cannot be told.
func (d *DB) SealingKeySettled(ctx context.Context, wait time.Duration) (*SealingKey, error) {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL lock_timeout = '%dms'`, wait.Milliseconds())); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE sealing_key IN SHARE MODE`); err != nil {
		return nil, err
	}
	var fp string
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT fingerprint FROM sealing_key), '')`).Scan(&fp); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if fp == "" {
		return nil, nil
	}
	return d.SealingKey(ctx)
}

func resealTable(ctx context.Context, tx pgx.Tx, t sealedTable, fn func(string) (string, ResealOutcome, error), count func(string, ResealOutcome)) error {
	cols := append(append([]string{}, t.key...), t.cols...)
	rows, err := tx.Query(ctx, `SELECT `+strings.Join(cols, ", ")+` FROM `+t.table+` FOR UPDATE`)
	if err != nil {
		return err
	}
	var all [][]string
	for rows.Next() {
		vals := make([]string, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return err
		}
		all = append(all, vals)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	set := make([]string, len(t.cols))
	for i, c := range t.cols {
		set[i] = fmt.Sprintf("%s = $%d", c, i+1)
	}
	where := make([]string, len(t.key))
	for i, k := range t.key {
		where[i] = fmt.Sprintf("%s = $%d", k, len(t.cols)+i+1)
	}
	update := `UPDATE ` + t.table + ` SET ` + strings.Join(set, ", ") + ` WHERE ` + strings.Join(where, " AND ")
	for _, vals := range all {
		keys, sealed := vals[:len(t.key)], vals[len(t.key):]
		changed := false
		for i, v := range sealed {
			if v == "" {
				continue
			}
			nv, o, err := fn(v)
			if err != nil {
				return err
			}
			count(t.table+"."+t.cols[i], o)
			if nv != v {
				sealed[i], changed = nv, true
			}
		}
		if !changed {
			continue
		}
		args := make([]any, 0, len(vals))
		for _, v := range sealed {
			args = append(args, v)
		}
		for _, k := range keys {
			args = append(args, k)
		}
		if _, err := tx.Exec(ctx, update, args...); err != nil {
			return err
		}
	}
	return nil
}

func resealSetting(ctx context.Context, tx pgx.Tx, s SealedSetting, fn func(string) (string, ResealOutcome, error), count func(string, ResealOutcome)) error {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1 FOR UPDATE`, s.Key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var objs []map[string]json.RawMessage
	if s.List {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil
		}
		if err := json.Unmarshal(raw, &objs); err != nil {
			return fmt.Errorf("%w: not a list of objects: %v", ErrResealRefused, err)
		}
	} else {
		var o map[string]json.RawMessage
		if err := json.Unmarshal(raw, &o); err != nil {
			return fmt.Errorf("%w: not an object: %v", ErrResealRefused, err)
		}
		objs = []map[string]json.RawMessage{o}
	}
	changed := false
	for _, o := range objs {
		for _, f := range s.Fields {
			fv, ok := o[f]
			if !ok || bytes.Equal(bytes.TrimSpace(fv), []byte("null")) {
				continue
			}
			var v string
			if err := json.Unmarshal(fv, &v); err != nil {
				return fmt.Errorf("%w: %s is not a string: %v", ErrResealRefused, f, err)
			}
			if v == "" {
				continue
			}
			nv, out, err := fn(v)
			if err != nil {
				return err
			}
			count("settings "+s.Key+"."+f, out)
			if nv != v {
				b, err := json.Marshal(nv)
				if err != nil {
					return err
				}
				o[f], changed = b, true
			}
		}
	}
	if !changed {
		return nil
	}
	var v any = objs
	if !s.List {
		v = objs[0]
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	// updated_at stays: the value means the same as before.
	_, err = tx.Exec(ctx, `UPDATE settings SET value = $2 WHERE key = $1`, s.Key, bytes.TrimSpace(buf.Bytes()))
	return err
}

// SealedValues are every sealed value of the manager's in this database ("" ones aside), read without locking anything:
// a running manager's check whether any still needs sealing again (keyrotate.go) before it takes the rows' locks.
func (d *DB) SealedValues(ctx context.Context) ([]string, error) {
	var out []string
	for _, t := range sealedTables {
		rows, err := d.pool.Query(ctx, `SELECT `+strings.Join(t.cols, ", ")+` FROM `+t.table)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			vals := make([]string, len(t.cols))
			ptrs := make([]any, len(vals))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return nil, err
			}
			for _, v := range vals {
				if v != "" {
					out = append(out, v)
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for _, s := range sealedSettings {
		for _, f := range s.Fields {
			q := `SELECT coalesce(value->>$2, '') FROM settings WHERE key = $1 AND jsonb_typeof(value) = 'object'`
			if s.List {
				q = `SELECT coalesce(e.elem->>$2, '') FROM settings s, jsonb_array_elements(s.value) AS e(elem)
					WHERE s.key = $1 AND jsonb_typeof(s.value) = 'array' AND jsonb_typeof(e.elem) = 'object'`
			}
			rows, err := d.pool.Query(ctx, q, s.Key, f)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var v string
				if err := rows.Scan(&v); err != nil {
					rows.Close()
					return nil, err
				}
				if v != "" {
					out = append(out, v)
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// keyRotationLock is the advisory lock a rotation of the manager's secret.key holds: "persona key rotate".
const keyRotationLock int64 = 0x706b726f74

// ErrKeyRotationBusy: another process held the key rotation's lock for longer than a start waits.
var ErrKeyRotationBusy = errors.New("another process is rotating the manager's secret.key")

// KeyRotationLock is the key rotation's lock, held on a connection of its own.
type KeyRotationLock struct{ rel func() }

// LockKeyRotation takes the key rotation's lock, waiting up to wait for another process to let it go
// (ErrKeyRotationBusy).
func (d *DB) LockKeyRotation(ctx context.Context, wait time.Duration) (*KeyRotationLock, error) {
	c, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		var got bool
		if err := c.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, keyRotationLock).Scan(&got); err != nil {
			c.Release()
			return nil, err
		}
		if got {
			return &KeyRotationLock{rel: func() {
				_, _ = c.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, keyRotationLock)
				c.Release()
			}}, nil
		}
		if !time.Now().Before(deadline) {
			c.Release()
			return nil, fmt.Errorf("%w (waited %s)", ErrKeyRotationBusy, wait)
		}
		select {
		case <-ctx.Done():
			c.Release()
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Release lets the lock go.
func (l *KeyRotationLock) Release() { l.rel() }

// OtherManagers counts the connections to this database of a manager that are not this pool's (application_name
// AppManager, or a name that starts with it): another manager process. One started before the key rotation may still
// seal with the old key, so the rotation keeps that key while there is one. Not seen: a process with no connection
// open at that moment, and one whose database_url sets another application_name (the rotation's grace covers the
// first; docs/assistant/managers.md "Its own secret.key").
func (d *DB) OtherManagers(ctx context.Context) (int, error) {
	var n int
	err := d.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
		AND coalesce(backend_type, 'client backend') = 'client backend' AND pid <> pg_backend_pid() AND NOT (pid = ANY($1::int4[]))
		AND (application_name = $2 OR application_name LIKE $2 || '%')`, d.pids.list(), AppManager).Scan(&n)
	return n, err
}
