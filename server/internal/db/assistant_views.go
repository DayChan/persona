package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	AssistantViewsMost = 200
	assistantViewBytes = 64 << 10
)

var (
	ErrAssistantViewNotFound = errors.New("assistant view not found")
	ErrAssistantViewLimit    = errors.New("assistant conversation has reached its live card limit")
	ErrAssistantViewVersion  = errors.New("assistant view version changed")
)

type AssistantViewRow struct {
	ID           string          `json:"id"`
	Conversation string          `json:"conversation"`
	MachineID    string          `json:"machine_id"`
	View         json.RawMessage `json:"view"`
	Version      int64           `json:"version"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// CreateAssistantView atomically stores a card and its original position in the conversation feed.
// A conversation-scoped lock makes the limit hold across concurrent requests and hub processes.
func (d *DB) CreateAssistantView(ctx context.Context, v AssistantViewRow, feed FeedRow) error {
	if err := checkAssistantViewRow(v); err != nil {
		return err
	}
	if v.Version != 1 || v.CreatedAt.IsZero() || v.UpdatedAt.IsZero() || feed.ID == "" || feed.Conversation != v.Conversation ||
		feed.Kind != "view" || feed.Ref != v.ID || !feed.At.Equal(v.CreatedAt) {
		return errors.New("a new assistant view needs version 1 and a matching view feed item")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "assistant_views:"+v.Conversation); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM assistant_views WHERE conversation = $1`, v.Conversation).Scan(&count); err != nil {
		return err
	}
	if count >= AssistantViewsMost {
		return ErrAssistantViewLimit
	}
	if _, err := tx.Exec(ctx, `INSERT INTO assistant_views (id, conversation, machine_id, view, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, v.ID, v.Conversation, v.MachineID, []byte(v.View), v.Version, v.CreatedAt, v.UpdatedAt); err != nil {
		return err
	}
	body := feed.Body
	if len(body) == 0 {
		body = json.RawMessage(`{}`)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO assistant_feed (id, conversation, at, kind, ref, body)
		VALUES ($1, $2, $3, $4, $5, $6)`, feed.ID, feed.Conversation, feed.At, feed.Kind, feed.Ref, []byte(body)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func checkAssistantViewRow(v AssistantViewRow) error {
	if v.ID == "" || v.Conversation == "" || v.MachineID == "" {
		return errors.New("assistant view id, conversation and machine are required")
	}
	return checkAssistantViewJSON(v.View)
}

func checkAssistantViewJSON(view json.RawMessage) error {
	if len(view) > assistantViewBytes {
		return fmt.Errorf("assistant view has at most %d bytes", assistantViewBytes)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(view, &object); err != nil || object == nil {
		return errors.New("assistant view must be a JSON object")
	}
	return nil
}

const assistantViewColumns = `id, conversation, machine_id, view, version, created_at, updated_at`

func scanAssistantView(row pgx.Row) (*AssistantViewRow, error) {
	var v AssistantViewRow
	err := row.Scan(&v.ID, &v.Conversation, &v.MachineID, &v.View, &v.Version, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAssistantViewNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// AssistantView requires its conversation, so an id from another conversation never grants access.
func (d *DB) AssistantView(ctx context.Context, conversation, id string) (*AssistantViewRow, error) {
	return scanAssistantView(d.pool.QueryRow(ctx, `SELECT `+assistantViewColumns+` FROM assistant_views WHERE conversation = $1 AND id = $2`, conversation, id))
}

func (d *DB) AssistantViews(ctx context.Context, conversation string) ([]AssistantViewRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+assistantViewColumns+` FROM assistant_views WHERE conversation = $1 ORDER BY created_at, id`, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssistantViewRow{}
	for rows.Next() {
		v, err := scanAssistantView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// AssistantViewKey names one stored state of a card without its contents.
type AssistantViewKey struct {
	ID      string
	Version int64
	// Xmin is the transaction that wrote this row version. Any rewrite of the row changes it, including one that keeps
	// Version (a migration, a manual repair), so a cached copy is checked against both.
	Xmin uint32
}

// AssistantViewKeyed is a card together with the key of the row version it was read from.
type AssistantViewKeyed struct {
	AssistantViewRow
	Xmin uint32
}

// AssistantViewKeys lists the conversation's cards without reading view, so Postgres never detoasts their contents.
func (d *DB) AssistantViewKeys(ctx context.Context, conversation string) ([]AssistantViewKey, error) {
	rows, err := d.pool.Query(ctx, `SELECT id, version, xmin FROM assistant_views WHERE conversation = $1`, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssistantViewKey{}
	for rows.Next() {
		var k AssistantViewKey
		if err := rows.Scan(&k.ID, &k.Version, &k.Xmin); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// AssistantViewsByID reads the named cards of one conversation; an id of another conversation reads nothing.
// Ids that no longer exist are left out.
func (d *DB) AssistantViewsByID(ctx context.Context, conversation string, ids []string) ([]AssistantViewKeyed, error) {
	out := []AssistantViewKeyed{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT `+assistantViewColumns+`, xmin FROM assistant_views WHERE conversation = $1 AND id = ANY($2)`, conversation, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v AssistantViewKeyed
		if err := rows.Scan(&v.ID, &v.Conversation, &v.MachineID, &v.View, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.Xmin); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpdateAssistantView changes only the contents and version, never its owner or feed position.
// expected is required: callers can retry after reading the winner instead of overwriting an unseen update.
func (d *DB) UpdateAssistantView(ctx context.Context, conversation, id string, expected int64, view json.RawMessage, at time.Time) (*AssistantViewRow, error) {
	if id == "" || conversation == "" {
		return nil, errors.New("assistant view id and conversation are required")
	}
	if err := checkAssistantViewJSON(view); err != nil {
		return nil, err
	}
	if expected < 1 || expected >= 9007199254740991 || at.IsZero() {
		return nil, errors.New("assistant view update needs a safe positive version and a timestamp")
	}
	v, err := scanAssistantView(d.pool.QueryRow(ctx, `UPDATE assistant_views SET view = $4, version = version + 1, updated_at = $5
		WHERE conversation = $1 AND id = $2 AND version = $3 RETURNING `+assistantViewColumns, conversation, id, expected, []byte(view), at))
	if !errors.Is(err, ErrAssistantViewNotFound) {
		return v, err
	}
	if _, err := d.AssistantView(ctx, conversation, id); err != nil {
		return nil, err
	}
	return nil, ErrAssistantViewVersion
}
