package managerupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// Authority is a fresh read of the selected manager's own persisted identity.
// It is not activity evidence, and cannot by itself authorize an update.
type Authority struct {
	ManagerID  string    `json:"manager"`
	Main       string    `json:"main"`
	MachineID  string    `json:"machine_id"`
	Revision   string    `json:"revision"`
	ObservedAt time.Time `json:"observed_at"`
}

// ReadAuthority avoids the display conversation cache and db.Open (which applies
// migrations). The original config's ownership/hash binds this single read to
// the explicit independent manager. No schema, setting or service is changed.
func (c *Controller) ReadAuthority(ctx context.Context, p Plan) (Authority, error) {
	var out Authority
	if err := p.validateShape(); err != nil {
		return out, err
	}
	if c.UID != p.UID {
		return out, errors.New("authority reader UID differs from manager owner")
	}
	path := filepath.Join(p.Dir, "manager.json")
	for _, item := range []string{p.Dir, path} {
		if err := canonical(item); err != nil {
			return out, err
		}
	}
	if c.FileCheck == nil {
		return out, errors.New("authority file ownership verifier unavailable")
	}
	if err := c.FileCheck(path, p.UID); err != nil {
		return out, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != p.ConfigSHA256 {
		return out, errors.New("authority manager config identity changed")
	}
	var config struct {
		ID          string `json:"id"`
		DatabaseURL string `json:"database_url"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return out, errors.New("invalid authority manager config")
	}
	if config.ID != p.ManagerID || config.DatabaseURL == "" {
		return out, errors.New("authority manager identity or database unavailable")
	}
	options, err := pgx.ParseConfig(config.DatabaseURL)
	if err != nil {
		// Connection parser/driver errors can contain a DSN; never emit those.
		return out, errors.New("invalid authority database connection configuration")
	}
	// One fixed, parameter-free read needs no prepared statements or schema work.
	options.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	options.RuntimeParams["default_transaction_read_only"] = "on"
	options.RuntimeParams["application_name"] = "persona-manager-update-authority"
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	connection, err := pgx.ConnectConfig(ctx, options)
	if err != nil {
		return out, errors.New("authority database connection unavailable")
	}
	var main, machine, rowVersion string
	var changed, observed time.Time
	err = connection.QueryRow(ctx, `SELECT value->>'main', value->>'machine_id', xmin::text, updated_at, clock_timestamp()
		FROM public.settings WHERE key = 'assistant'`).Scan(&main, &machine, &rowVersion, &changed, &observed)
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	closeErr := connection.Close(closeCtx)
	closeCancel()
	if err != nil || closeErr != nil {
		return out, errors.New("complete authority settings read unavailable")
	}
	current, err := hashFile(path)
	if err != nil {
		return out, err
	}
	if current != p.ConfigSHA256 {
		return out, errors.New("authority manager config changed during read")
	}
	return authorityRecord(p.ManagerID, main, machine, rowVersion, changed, observed)
}

func authorityRecord(manager, main, machine, version string, changed, observed time.Time) (Authority, error) {
	valid := func(value string) bool {
		return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value &&
			strings.IndexFunc(value, unicode.IsControl) < 0
	}
	if !idPattern.MatchString(manager) || !valid(main) || !valid(machine) || !valid(version) || changed.IsZero() || observed.IsZero() {
		return Authority{}, errors.New("persisted main, machine or authority revision unavailable")
	}
	data, err := json.Marshal([]any{manager, main, machine, version, changed.UTC()})
	if err != nil {
		return Authority{}, err
	}
	digest := sha256.Sum256(data)
	return Authority{ManagerID: manager, Main: main, MachineID: machine,
		Revision: hex.EncodeToString(digest[:]), ObservedAt: observed.UTC()}, nil
}
