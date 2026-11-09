package assistant

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/pgdocker"
)

// The built-in manager's own database (docs/server/server.md "The default manager's own database"). It used to keep its state
// in the hub's database (hub.json's database_url); now it has one of its own (provisionOwn). Its data moves there once,
// at a start, before it serves anything. What says it did is in the databases themselves: its own records the copy
// that filled it, in the same transaction as the data ('in'); the hub's that the data left ('out'). Its manager.json
// says where its own is (and that it moved, managerconf.DatabaseMovedKey, which only saves a look). A start that finds
// its own database filled runs there and never copies over it; one that finds the data left the hub's database for
// one it does not know does not start; once moved it never runs on the hub's database again. Until the copy went
// through, the hub's database stays the one in use, and the next start tries again.

// ownContainerID is the name its container and volume go by (persona-mgr-<id>-pg): the built-in manager's id and a
// short hash of its data directory, so two hubs or two users on one Docker never share one.
// _PERSONA_ASSISTANT_DB_ID says another (trial runs).
func ownContainerID(home string) string {
	if v := os.Getenv("_PERSONA_ASSISTANT_DB_ID"); v != "" {
		return v
	}
	sum := sha256.Sum256([]byte(home))
	return assistantwire.AssistantID + "-" + hex.EncodeToString(sum[:4])
}

// ownDatabaseName is the second database's name in the hub's Postgres server: the hub's database's name and
// _manager, so two hubs on one server never share one.
func ownDatabaseName(shared string) string {
	name := db.DatabaseName(shared) + "_manager"
	if len(name) > 63 { // Postgres' identifier length
		sum := sha256.Sum256([]byte(name))
		name = name[:46] + "_" + hex.EncodeToString(sum[:4]) + "_manager"
	}
	return name
}

func databaseMovedAt(moved time.Time) string {
	// Operational diagnostics identify an absolute instant, regardless of the hub's local zone.
	return moved.UTC().Format(time.RFC3339)
}

// moveWait is how long the move waits for other processes to leave the manager's tables in the shared database.
var moveWait = time.Minute

// provisioner makes sure the built-in manager's own database is there (made the first time, started every time) and
// says where it is (URL set); kept is what manager.json has of it, shared the hub's database. created: it was made
// now (one that had its data and was lost comes back empty).
type provisioner func(ctx context.Context, home, shared string, kept managerconf.OwnDatabase) (own managerconf.OwnDatabase, created bool, err error)

// errMovedElsewhere: the data left the hub's database for a database this data directory does not know.
var errMovedElsewhere = errors.New("the built-in manager's data moved out of the hub's database into a database this data directory does not know: restore its managers/assistant/manager.json")

// builtinDatabase is the database the built-in manager runs on: its own once its data is there, else (no own database
// can be made yet, the move must wait or failed before its copy went through) the one it shares with the hub:
// onShared. An error: it must not start (its data is in a database that is not up, or one it does not know).
func builtinDatabase(ctx context.Context, home, shared string, provision provisioner, logf func(string, ...any)) (url string, onShared bool, err error) {
	kept := managerconf.ReadOwnDatabase(home)
	if !kept.Moved.IsZero() {
		own, _, err := provision(ctx, home, shared, kept)
		if err == nil {
			err = hasItsData(ctx, own.URL) // one lost and made again has no record of the copy
		}
		if err != nil {
			// Its data went there: the hub's database's copy is stale, and running on it would split the two.
			return "", false, fmt.Errorf("its own database (where its data moved %s) is not up: %w", databaseMovedAt(kept.Moved), err)
		}
		if changed(kept, own) {
			own.Moved = kept.Moved
			if err := managerconf.WriteOwnDatabase(home, own); err != nil {
				logf("database: its own database moved (%v): its manager.json could not keep the new address; this start runs there all the same", err)
			}
		}
		return own.URL, false, nil
	}
	hasData, err := db.HasManagerData(ctx, shared)
	if err != nil {
		logf("database: the hub's database: %v: on it for now; tried again at the next start", err)
		return shared, true, nil
	}
	own, _, err := provision(ctx, home, shared, kept)
	if err != nil {
		if hasData && movedOut(ctx, shared) {
			return "", false, errMovedElsewhere
		}
		logf("database: no database of its own yet (%v): on the hub's for now; tried again at the next start", err)
		return shared, true, nil
	}
	if changed(kept, own) { // kept before anything goes there: a later try finds it again
		if err := managerconf.WriteOwnDatabase(home, own); err != nil {
			logf("database: its own database is ready, but its manager.json could not keep where (%v): on the hub's for now; tried again at the next start", err)
			return shared, true, nil
		}
	}
	record := func(how string) (string, bool, error) {
		own.Moved = time.Now()
		if err := managerconf.WriteOwnDatabase(home, own); err != nil {
			logf("database: %s, but its manager.json could not say so (%v): the next start finds it there all the same", how, err)
		}
		return own.URL, false, nil
	}
	switch err := hasItsData(ctx, own.URL); {
	case err == nil: // filled by an earlier start (one from another data directory, or that could not write manager.json)
		return record("its own database already has its data")
	case !errors.Is(err, errNotItsData):
		logf("database: its own database: %v: on the hub's for now; tried again at the next start", err)
		return shared, true, nil
	}
	if !hasData && movedOut(ctx, shared) { // its old tables dropped there (drop-shared-tables), the record kept
		return "", false, errMovedElsewhere
	}
	if !hasData { // a fresh install: nothing to copy, and the hub's database gets none of the manager's tables
		if err := markFresh(ctx, own.URL); err != nil {
			logf("database: its own database: %v: on the hub's for now; tried again at the next start", err)
			return shared, true, nil
		}
		return record("a fresh install")
	}
	if movedOut(ctx, shared) {
		return "", false, errMovedElsewhere
	}
	rep, err := moveData(ctx, shared, own, logf)
	switch {
	case errors.Is(err, errAlreadyMoved):
		return record("its own database already has its data")
	case errors.Is(err, errMovedElsewhere):
		return "", false, err
	case err != nil:
		logf("database: moving its data into its own database: %v: on the hub's for now; tried again at the next start", err)
		return shared, true, nil
	}
	n := int64(0)
	for _, c := range rep.Rows {
		n += c
	}
	where := "a second database in the hub's Postgres (" + own.Name + ")"
	if own.Container != "" {
		where = "its own Postgres (Docker container " + own.Container + ")"
	}
	logf("database: moved its data (%d tables, %d rows) out of the hub's database into %s, once; the hub's keeps its copy for a build from before", len(rep.Rows), n, where)
	return record("moved its data")
}

// changed: own is another database than manager.json keeps (the hub's server's address is derived at each start, and
// not kept).
func changed(kept, own managerconf.OwnDatabase) bool {
	return own.Container != kept.Container || own.Name != kept.Name || (own.Container != "" && own.URL != kept.URL)
}

// errNotItsData: the database is the manager's own, not filled yet.
var errNotItsData = errors.New("not filled yet")

// errAlreadyMoved: another start filled it meanwhile.
var errAlreadyMoved = errors.New("filled by another start")

// hasItsData: the database at url is filled with the manager's data (its copy went through, db.MovedIn); errNotItsData
// when not yet.
func hasItsData(ctx context.Context, url string) error {
	d, err := db.Open(ctx, url, db.ManagerSet)
	if err != nil {
		return err
	}
	defer d.Close()
	_, ok, err := d.MovedIn(ctx)
	if err == nil && !ok {
		err = errNotItsData
	}
	return err
}

// markFresh records in its own database that it is its data's from the start (a fresh install).
func markFresh(ctx context.Context, url string) error {
	d, err := db.Open(ctx, url, db.ManagerSet)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.MarkMovedIn(ctx, "fresh install")
}

// movedOut: the hub's database at shared says the manager's data left it (db.MovedOut), or cannot tell (an error
// counts as moved: running on a copy that may be stale is what must not happen). Read without migrating.
func movedOut(ctx context.Context, shared string) bool {
	ok, err := db.ManagerMovedOut(ctx, shared)
	return ok || err != nil
}

// moveData copies the manager's tables from the shared database into its own, while nothing else uses them there,
// once it made sure there is room for them; then the shared database records that they left.
func moveData(ctx context.Context, shared string, own managerconf.OwnDatabase, logf func(string, ...any)) (db.CopyReport, error) {
	from, err := db.Open(ctx, shared, db.ManagerSet)
	if err != nil {
		return db.CopyReport{}, fmt.Errorf("the hub's database: %w", err)
	}
	defer from.Close()
	to, err := db.Open(ctx, own.URL, db.ManagerSet)
	if err != nil {
		return db.CopyReport{}, fmt.Errorf("its own database: %w", err)
	}
	defer to.Close()
	if err := roomFor(ctx, from, to, own, logf); err != nil {
		return db.CopyReport{}, err
	}
	unlock, err := from.LockManagerMove(ctx, moveWait)
	if err != nil {
		return db.CopyReport{}, err
	}
	defer unlock()
	// Looked at again under the lock: another start may have moved it while this one waited.
	if _, ok, err := to.MovedIn(ctx); err != nil || ok {
		if err == nil {
			err = errAlreadyMoved
		}
		return db.CopyReport{}, err
	}
	if _, ok, err := from.MovedOut(ctx); err != nil || ok {
		if err == nil {
			err = errMovedElsewhere
		}
		return db.CopyReport{}, err
	}
	rep, err := db.CopyTables(ctx, from, to, logf)
	if err != nil {
		return rep, err
	}
	peer, _ := to.Identity(ctx)
	if err := from.MarkMovedOut(ctx, peer); err != nil {
		logf("database: copied, but the hub's database could not record that its data left (%v): a start that does not know its own database could take the stale copy", err)
	}
	return rep, nil
}

// roomFor refuses the copy when the file system its own database keeps its files on has not room for it (the
// tables' size half again, and some to spare); when that cannot be told (a server elsewhere, no rights to see it), it
// says so and lets the copy go on.
func roomFor(ctx context.Context, from, to *db.DB, own managerconf.OwnDatabase, logf func(string, ...any)) error {
	need, err := db.CopySize(ctx, from, to)
	if err != nil {
		return err
	}
	need = need*3/2 + 256<<20
	dir := ""
	if own.Container != "" { // its volume's directory on this machine (Docker's own data directory)
		volume := strings.TrimSuffix(own.Container, "-pg") + "-data"
		if out, err := pgdocker.RunCommand(ctx, nil, "docker", "volume", "inspect", "-f", "{{.Mountpoint}}", volume); err == nil {
			dir = strings.TrimSpace(string(out))
		}
	} else if local(own.URL) { // a Postgres on this machine (one in a container keeps its files where this cannot see)
		dir, _ = to.DataDirectory(ctx)
	}
	free, ok := freeBytes(dir)
	if dir == "" || !ok {
		logf("database: the copy needs about %d MB; the free space where its own database keeps its files could not be told: going on", need>>20)
		return nil
	}
	if free < need {
		return fmt.Errorf("not enough free space for the copy: about %d MB needed, %d MB free at %s", need>>20, free>>20, dir)
	}
	return nil
}

// local: the database at url is on this machine (its files on a file system here).
func local(url string) bool {
	u, err := neturl.Parse(url)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "" || host == "localhost" || strings.HasPrefix(host, "/") || host == "127.0.0.1" || host == "::1"
}

// Where a built-in manager without a database of its own yet gets one (manager.json's database_location; the default
// until it is decided for everyone): LocationHubServer, a second database in the hub's Postgres server (one server,
// one backup); LocationContainer, a Postgres container of its own (as a manager made in the app has), or the hub's
// server where there is no Docker.
const (
	LocationHubServer = "hub_server"
	LocationContainer = "container"
)

// defaultLocation is where one goes when manager.json does not say.
var defaultLocation = LocationHubServer

// provisionOwn is provisioner on this machine: its container (pgdocker.ProvisionManagerDB: made the first time,
// started every time), or a second database in the hub's Postgres server. One it has stays the kind and the one it is.
func provisionOwn(ctx context.Context, home, shared string, kept managerconf.OwnDatabase) (managerconf.OwnDatabase, bool, error) {
	switch {
	case kept.Name != "":
		return provisionSecond(ctx, shared, kept, kept.Name)
	case kept.Container != "":
		return provisionContainer(ctx, home, kept)
	}
	if cmp.Or(kept.Location, defaultLocation) == LocationContainer {
		own, created, err := provisionContainer(ctx, home, kept)
		if err == nil || !(errors.Is(err, pgdocker.ErrNoDocker) || errors.Is(err, pgdocker.ErrDockerDenied) || errors.Is(err, pgdocker.ErrDockerDown)) {
			return own, created, err
		}
	}
	return provisionSecond(ctx, shared, kept, ownDatabaseName(shared))
}

// provisionContainer is its Postgres container: the one manager.json names, else the one named for this data
// directory (made now, or by an earlier start: its own; one with the manager's data is taken as it is, never copied
// over: CopyTables refuses).
func provisionContainer(ctx context.Context, home string, kept managerconf.OwnDatabase) (managerconf.OwnDatabase, bool, error) {
	id := ownContainerID(home)
	if kept.Container != "" {
		name, ok := managerContainerID(kept.Container)
		if !ok {
			return kept, false, fmt.Errorf("database_container %q is not one a manager makes", kept.Container)
		}
		id = name
	}
	password := ""
	if u, err := neturl.Parse(kept.URL); err == nil && kept.URL != "" {
		password, _ = u.User.Password()
	}
	url, created, err := pgdocker.ProvisionManagerDB(ctx, id, pgdocker.ManagerDBOptions{Password: password, Ping: pingDatabase})
	if err != nil {
		return kept, false, err
	}
	return managerconf.OwnDatabase{URL: url, Container: pgdocker.ManagerContainer(id), Location: kept.Location}, created, nil
}

// managerContainerID is the manager id in a database container a manager makes (pgdocker.ManagerContainer:
// persona-mgr-<id>-pg); ok is false for any other name.
func managerContainerID(container string) (id string, ok bool) {
	id, prefixed := strings.CutPrefix(container, "persona-mgr-")
	id, suffixed := strings.CutSuffix(id, "-pg")
	if !prefixed || !suffixed || id == "" {
		return "", false
	}
	return id, true
}

// provisionSecond is the second database name in the hub's Postgres server (made when its role may: CREATE
// DATABASE), its address the hub's with that name.
func provisionSecond(ctx context.Context, shared string, kept managerconf.OwnDatabase, name string) (managerconf.OwnDatabase, bool, error) {
	second, err := db.WithDatabase(shared, name)
	if err != nil {
		return kept, false, err
	}
	created, err := db.EnsureDatabase(ctx, second)
	if err != nil {
		return kept, false, fmt.Errorf("a second database in the hub's Postgres could not be made: %w", err)
	}
	return managerconf.OwnDatabase{URL: second, Name: name, Location: kept.Location}, created, nil
}

func pingDatabase(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	return c.Close(ctx)
}

// shareCheckEvery is how often a manager on the hub's database makes sure it still holds the move lock's share.
var shareCheckEvery = 30 * time.Second

// holdShare keeps share held while ctx lasts: its connection lost (the server restarted), it is taken again; the data
// moved meanwhile (a move by another process while it was not held), it stops the manager (stop): running on would
// write to a copy no longer in use.
func holdShare(ctx context.Context, share *db.ShareHold, shared string, stop context.CancelFunc, logf func(string, ...any)) {
	t := time.NewTicker(shareCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := share.Check(ctx); err != nil {
			logf("database: the move's lock share on the hub's database: %v (taken again at the next check)", err)
			continue
		}
		if out, err := db.ManagerMovedOut(ctx, shared); err == nil && out { // a can't-tell is looked at again next time
			logf("database: its data moved into its own database while this one ran on the hub's: stopping; the next start runs there")
			stop()
			return
		}
	}
}
