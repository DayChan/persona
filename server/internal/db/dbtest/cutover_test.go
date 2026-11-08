package dbtest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/secretkey"
)

// TestPersonaCutoverSQL runs the migration lane's complete psql fixtures against
// a freshly owned database. It never uses the shared test database for schema work.
func TestPersonaCutoverSQL(t *testing.T) {
	base := os.Getenv(Env)
	container := os.Getenv("PERSONA_TEST_POSTGRES_CONTAINER")
	if base == "" || container == "" {
		t.Skip("set PERSONA_TEST_DATABASE_URL and PERSONA_TEST_POSTGRES_CONTAINER for isolated SQL verification")
	}
	for _, program := range []string{"docker", "python3"} {
		if _, err := exec.LookPath(program); err != nil {
			t.Skip(program + " unavailable")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	own, cleanup, err := Own(ctx, base)
	if err != nil {
		t.Fatal("create isolated SQL database:", err)
	}
	defer cleanup()
	if !ownedBy(own) {
		t.Fatal("SQL schema verification requires a freshly owned database, not a shared fallback")
	}
	parsed, err := url.Parse(own)
	if err != nil {
		t.Fatal("parse isolated database")
	}
	connection, err := pgx.Connect(ctx, own)
	if err != nil {
		t.Fatal("connect to isolated SQL database")
	}
	var port string
	if err := connection.QueryRow(ctx, "SHOW port").Scan(&port); err != nil {
		t.Fatal("read isolated server port:", err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatal("invalid isolated server port")
	}
	_ = connection.Close(ctx)
	password, _ := parsed.User.Password()
	fixture, err := filepath.Abs("../../../../scripts/test/test_persona_cutover.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fixture); os.IsNotExist(err) {
		t.Skip("migration fixtures not included in this checkout")
	}
	home := t.TempDir()
	const python = `import importlib.util, pathlib, subprocess, sys, unittest, os
spec = importlib.util.spec_from_file_location("persona_cutover_tests", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
home = pathlib.Path(sys.argv[2])
def cli_sql(source, homes=()):
    extra = [arg for other in homes for arg in ("--session-home", str(other))]
    result = subprocess.run([sys.executable, "-I", str(module.SCRIPT), "--source-name", source,
                             "--home", str(home), "--print-database-sql", *extra],
                            capture_output=True, text=True, timeout=30, check=True)
    return result.stdout
module.CUTOVER.database_sql = cli_sql
class OwnedDatabaseTests(module.DatabaseSandboxTests):
    __unittest_skip__ = False
    __unittest_skip_why__ = ""
    @classmethod
    def setUpClass(cls):
        cls.root = home
    @classmethod
    def run_program(cls, args, input=None, timeout=30, check=True):
        if args[0] != "psql":
            raise AssertionError("only psql may run against the owned database")
        command = ["docker", "exec", "-i", "-e", "PGHOST", "-e", "PGPORT", "-e", "PGUSER",
                   "-e", "PGPASSWORD", "-e", "PGDATABASE", os.environ["PERSONA_TEST_POSTGRES_CONTAINER"],
                   "psql", "-X", "--no-password", "-v", "ON_ERROR_STOP=1", "-At"]
        result = subprocess.run(command, input=input, text=True, capture_output=True, timeout=timeout)
        if check and result.returncode:
            raise AssertionError("owned database SQL failed: " + result.stdout + result.stderr)
        return result
suite = unittest.defaultTestLoader.loadTestsFromTestCase(OwnedDatabaseTests)
result = unittest.TextTestRunner(verbosity=2).run(suite)
sys.exit(0 if result.wasSuccessful() else 1)
`
	cmd := exec.CommandContext(ctx, "python3", "-I", "-c", python, fixture, home)
	cmd.Env = identity.Environment(os.Environ(), "PGHOST=127.0.0.1", "PGPORT="+port, "PGUSER="+parsed.User.Username(),
		"PGPASSWORD="+password, "PGDATABASE="+parsed.Path[1:], "PGSERVICE=", "PGSERVICEFILE=", "PGPASSFILE=", "PGOPTIONS=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated cutover SQL fixtures: %v\n%s", err, output)
	}
	t.Logf("isolated cutover SQL fixtures:\n%s", output)
}

// The historical DDL has no product-branded identifiers. Populate its unchanged
// schema with pre-cutover IDs, then verify the current Store can read it.
func TestPersonaCutoverRealSchemaStore(t *testing.T) {
	base, container := os.Getenv(Env), os.Getenv("PERSONA_TEST_POSTGRES_CONTAINER")
	if base == "" || container == "" {
		t.Skip("isolated PostgreSQL cutover inputs not configured")
	}
	for _, program := range []string{"docker", "python3"} {
		if _, err := exec.LookPath(program); err != nil {
			t.Skip(program + " unavailable")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	own, cleanup, err := Own(ctx, base)
	if err != nil {
		t.Fatal("create owned Store database:", err)
	}
	defer cleanup()
	if !ownedBy(own) {
		t.Fatal("refusing shared fallback for Store cutover verification")
	}
	database, err := db.Open(ctx, own, db.AllSets)
	if err != nil {
		t.Fatal("create frozen schema:", err)
	}
	database.Close()
	conn, err := pgx.Connect(ctx, own)
	if err != nil {
		t.Fatal("connect owned Store database")
	}
	defer conn.Close(context.Background())
	keyPath := filepath.Join(t.TempDir(), "secret.key")
	box := secretkey.New(keyPath)
	sealed, err := box.Seal("synthetic credential")
	if err != nil {
		t.Fatal(err)
	}
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `
INSERT INTO plugins (id,source,version,manifest,config) VALUES
 ('previous-apps','installed','1.0.0','{"id":"previous-apps","name":"previous","contributes":{"looks":[{"id":"custom","app_icon_fallback":"previous"}]}}','{"user_message":"previous"}');
INSERT INTO plugin_audit (plugin,action,actor,detail) VALUES ('previous-apps','install','fixture','{"text":"previous"}');
INSERT INTO settings (key,value) VALUES ('look','{"id":"previous","updated_at":"2026-01-01"}');
INSERT INTO hub_settings (key,value) VALUES ('look','{"id":"persona"}'),('hub.extensions_report','{"cached_id":"previous-apps"}');
INSERT INTO devices (device_id,prefs,topic,relay_grant) VALUES ('fixture','{"look":{"package":"previous/r1","design":"visor"}}','com.example.previous','opaque-fixture-grant');
`)
	if err != nil {
		t.Fatal("seed unchanged pre-cutover schema:", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO plugin_secrets (plugin,name,sealed) VALUES ('previous-apps','token',$1)`, sealed); err != nil {
		t.Fatal(err)
	}
	var migrationCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	var port string
	if err := conn.QueryRow(ctx, "SHOW port").Scan(&port); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(own)
	if err != nil {
		t.Fatal("parse owned Store database")
	}
	password, _ := parsed.User.Password()
	home := t.TempDir()
	// A stored session's folder under the previous root moves with it; any other folder stays (#1533).
	if _, err := conn.Exec(ctx, `INSERT INTO node_sessions (machine_id,session_id,cwd) VALUES
 ('fixture','assistant',$1),('fixture','project','/srv/code/app')`, home+"/.previous/assistant"); err != nil {
		t.Fatal("seed stored sessions:", err)
	}
	script, err := filepath.Abs("../../../../scripts/migrate/persona-cutover.py")
	if err != nil {
		t.Fatal(err)
	}
	cli := exec.CommandContext(ctx, "python3", "-I", script, "--source-name", "previous", "--home", home, "--print-database-sql")
	sql, err := cli.Output()
	if err != nil {
		t.Fatal("generate exact cutover SQL:", err)
	}
	apply := func() {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", "exec", "-i", "-e", "PGHOST", "-e", "PGPORT", "-e", "PGUSER", "-e", "PGPASSWORD", "-e", "PGDATABASE", container, "psql", "-X", "--no-password", "-v", "ON_ERROR_STOP=1")
		cmd.Env = identity.Environment(os.Environ(), "PGHOST=127.0.0.1", "PGPORT="+port, "PGUSER="+parsed.User.Username(), "PGPASSWORD="+password, "PGDATABASE="+strings.TrimPrefix(parsed.Path, "/"))
		cmd.Stdin = bytes.NewReader(sql)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("apply cutover only to owned Store database: %v\n%s", err, output)
		}
	}
	apply()
	apply()
	current, err := db.Open(ctx, own, db.AllSets)
	if err != nil {
		t.Fatal("open current Store after cutover:", err)
	}
	defer current.Close()
	records, err := current.Extensions(ctx)
	if err != nil || len(records) != 1 || records[0].ID != "persona-apps" {
		t.Fatalf("Store registry after cutover: %+v %v", records, err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(records[0].Manifest, &manifest); err != nil || manifest["id"] != "persona-apps" || manifest["name"] != "previous" {
		t.Fatal("typed manifest identity or user text changed", err)
	}
	secrets, err := current.ExtensionSecrets(ctx, "persona-apps")
	if err != nil || secrets["token"] != sealed {
		t.Fatal("sealed credential bytes changed", err)
	}
	if plain, err := box.Open(secrets["token"]); err != nil || plain != "synthetic credential" {
		t.Fatal("current secret box could not open unchanged credential", err)
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("secret.key changed", err)
	}
	audits, err := current.ExtensionAudits(ctx, "persona-apps", 10)
	if err != nil || len(audits) != 1 || audits[0].Plugin != "persona-apps" {
		t.Fatal("Store audit links lost", err)
	}
	var look struct {
		ID string `json:"id"`
	}
	if ok, err := current.Setting(ctx, "look", &look); err != nil || !ok || look.ID != "persona-visor" {
		t.Fatal("Store Visor choice lost", err)
	}
	devices, err := current.Devices(ctx)
	if err != nil || len(devices) != 1 || devices[0].RelayGrant != "opaque-fixture-grant" || devices[0].Topic != "com.example.previous" {
		t.Fatalf("Store device opaque fields changed: %+v %v", devices, err)
	}
	var prefs struct {
		Look struct {
			Package string `json:"package"`
		} `json:"look"`
	}
	if err := json.Unmarshal(devices[0].Prefs, &prefs); err != nil || prefs.Look.Package != "persona-visor/r1" {
		t.Fatal("Store device package lost", err)
	}
	folders := map[string]string{}
	rows, err := conn.Query(ctx, "SELECT session_id, cwd FROM node_sessions WHERE machine_id='fixture'")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, cwd string
		if err := rows.Scan(&id, &cwd); err != nil {
			t.Fatal(err)
		}
		folders[id] = cwd
	}
	if rows.Err() != nil || folders["assistant"] != home+"/.persona/assistant" || folders["project"] != "/srv/code/app" {
		t.Fatalf("stored session folders after cutover: %v %v", folders, rows.Err())
	}
	var after int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&after); err != nil || after != migrationCount {
		t.Fatal("historical migrations replayed after cutover", err)
	}
}
