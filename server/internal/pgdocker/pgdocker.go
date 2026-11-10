// Package pgdocker is a Postgres of one's own in Docker: one container, one volume, its port published on 127.0.0.1.
// `personad hub setup` makes the hub's with it (persona-postgres, scripts/deploy/db.sh's), and `personad manager
// install` a manager's (docs/assistant/managers.md, "Database"). It only runs docker; what to tell the user is the caller's.
package pgdocker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Image is the Postgres image the containers run.
const Image = "postgres:18-alpine"

// ServerArgs follow Image in docker run: the container's command, postgres with the settings Persona's databases
// get beyond PostgreSQL's defaults. The first change to a page after a checkpoint writes the whole page to the WAL,
// and on a personal hub that is most of the WAL of a write to a random page: wal_compression=lz4 halves those page
// images for a little CPU (Image is built with lz4), and checkpoint_timeout=15min (PostgreSQL's is 5min) writes them
// a third as often, for a longer crash recovery. scripts/deploy/db.sh passes the same, and compose its own defaults
// (docs/operations/docker.md "Disk writes").
var ServerArgs = []string{"postgres", "-c", "wal_compression=lz4", "-c", "checkpoint_timeout=15min"}

// User and Database are the role and database a container is made with.
const (
	User     = "persona"
	Database = "persona"
)

// Runner runs a program and returns its combined output; env is added to the environment (the password goes
// there, not on a command line others can read).
type Runner func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)

// Container is one Postgres container and its volume.
type Container struct {
	Name, Volume string
	// User and Database are the role and database its Postgres was made with; "" is the package's (User, Database).
	// An existing container keeps its own: one made before the rename has jarvis (#6149), read with Identity.
	User, Database string
	Port           int // the host port it is published on (127.0.0.1:<Port>) when it is made
	Run            Runner
	Sleep          func(time.Duration)
	Wait           time.Duration // how long Start waits for Postgres to answer
}

// Why Docker can't be used (CheckDocker).
var (
	ErrNoDocker     = errors.New("docker is not installed")
	ErrDockerDenied = errors.New("this user may not use docker")
	ErrDockerDown   = errors.New("docker is not running")
)

// CheckDocker says whether docker is there and answers: nil, ErrNoDocker, ErrDockerDenied or ErrDockerDown.
func CheckDocker(ctx context.Context, run Runner, lookPath func(string) (string, error)) error {
	if _, err := lookPath("docker"); err != nil {
		return ErrNoDocker
	}
	if out, err := run(ctx, nil, "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
		if strings.Contains(strings.ToLower(string(out)), "permission denied") {
			return ErrDockerDenied
		}
		return ErrDockerDown
	}
	return nil
}

// StepError is a docker command that failed while making the container: Step is "volume" or "run".
type StepError struct {
	Step   string
	Output string
}

func (e *StepError) Error() string { return e.Step + ": " + e.Output }

// Exists: there is a container by this name (running or not).
func (c *Container) Exists(ctx context.Context) bool {
	out, err := c.Run(ctx, nil, "docker", "ps", "-a", "--filter", "name=^/"+c.Name+"$", "--format", "{{.Names}}")
	return err == nil && strings.TrimSpace(string(out)) == c.Name
}

// Create makes the volume and the container with password (it starts it; Start waits for it).
func (c *Container) Create(ctx context.Context, password string) error {
	if out, err := c.Run(ctx, nil, "docker", "volume", "create", c.Volume); err != nil {
		return &StepError{Step: "volume", Output: FirstLine(string(out))}
	}
	// postgres 18 keeps PGDATA under /var/lib/postgresql/<major>/docker, so mount the parent (db.sh). The password
	// goes through the environment (-e NAME with no value), not the command line others can read.
	args := []string{"run", "-d", "--name", c.Name, "--restart", "unless-stopped",
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", c.Port), "-v", c.Volume + ":/var/lib/postgresql",
		"-e", "POSTGRES_USER=" + c.user(), "-e", "POSTGRES_PASSWORD", "-e", "POSTGRES_DB=" + c.database(), Image}
	args = append(args, ServerArgs...)
	if out, err := c.Run(ctx, []string{"POSTGRES_PASSWORD=" + password}, "docker", args...); err != nil {
		return &StepError{Step: "run", Output: FirstLine(string(out))}
	}
	return nil
}

// Password is the password the container was made with (its environment).
func (c *Container) Password(ctx context.Context) (string, error) {
	env, err := c.env(ctx)
	if err != nil {
		return "", err
	}
	if v := env["POSTGRES_PASSWORD"]; v != "" {
		return v, nil
	}
	return "", errors.New("no POSTGRES_PASSWORD")
}

// Identity sets User and Database to the ones the container was made with, from its environment (POSTGRES_USER;
// POSTGRES_DB, else that user, as the postgres image reads them). Every container made here has them; without them
// the package's stay.
func (c *Container) Identity(ctx context.Context) error {
	env, err := c.env(ctx)
	if err != nil {
		return err
	}
	if u := env["POSTGRES_USER"]; u != "" {
		c.User, c.Database = u, u
	}
	if d := env["POSTGRES_DB"]; d != "" {
		c.Database = d
	}
	return nil
}

// env is the container's POSTGRES_* environment.
func (c *Container) env(ctx context.Context) (map[string]string, error) {
	out, err := c.Run(ctx, nil, "docker", "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", c.Name)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && strings.HasPrefix(k, "POSTGRES_") {
			env[k] = v
		}
	}
	return env, nil
}

func (c *Container) user() string {
	if c.User != "" {
		return c.User
	}
	return User
}

func (c *Container) database() string {
	if c.Database != "" {
		return c.Database
	}
	return Database
}

// URL is the container's database at port on this machine, as its own user.
func (c *Container) URL(password string, port int) string {
	return urlFor(c.user(), c.database(), password, port)
}

// ErrNotPublished: the container's 5432 is published on no host port (on the host's network, or a container another
// command made on a Docker network of its own).
var ErrNotPublished = errors.New("its port 5432 is not published on this machine")

// HostPort is the host port the container's 5432 is published on (127.0.0.1:<port>); ErrNotPublished when there is
// none.
func (c *Container) HostPort(ctx context.Context) (int, error) {
	out, err := c.Run(ctx, nil, "docker", "port", c.Name, "5432/tcp")
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if _, p, err := net.SplitHostPort(strings.TrimSpace(line)); err == nil {
				if n, err := strconv.Atoi(p); err == nil && n > 0 {
					return n, nil
				}
			}
		}
	}
	return 0, fmt.Errorf("%s: %w", c.Name, ErrNotPublished)
}

// portTaken: docker start failed because the port the container publishes is someone else's now.
func portTaken(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "port is already allocated") || strings.Contains(s, "address already in use")
}

// URL is a new container's database at port on this machine.
func URL(password string, port int) string { return urlFor(User, Database, password, port) }

func urlFor(user, database, password string, port int) string {
	return fmt.Sprintf("postgres://%s@127.0.0.1:%d/%s?sslmode=disable", url.UserPassword(user, password).String(), port, database)
}

// Start starts the container and waits until Postgres in it answers (db.sh's 30 seconds). dbURL is the address it was
// given ("" when there is none yet): a container on the host's network (--network host, where publishing a port does
// not work) listens on that URL's port inside, not 5432, as db.sh handles it.
func (c *Container) Start(ctx context.Context, dbURL string) error {
	if out, err := c.Run(ctx, nil, "docker", "start", c.Name); err != nil {
		return errors.New(FirstLine(string(out)))
	}
	inner := "5432"
	if out, err := c.Run(ctx, nil, "docker", "inspect", "-f", "{{.HostConfig.NetworkMode}}", c.Name); err == nil &&
		strings.TrimSpace(string(out)) == "host" && dbURL != "" {
		if u, err := url.Parse(dbURL); err == nil && u.Port() != "" {
			inner = u.Port()
		}
	}
	deadline := time.Now().Add(c.Wait)
	for {
		// Over TCP: while a new container initializes, its temporary server answers on the Unix socket only, then
		// restarts; a socket check would pass in between.
		if _, err := c.Run(ctx, nil, "docker", "exec", c.Name, "pg_isready", "-h", "127.0.0.1", "-p", inner, "-U", c.user(), "-d", c.database()); err == nil {
			return nil
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return &NotReadyError{Seconds: int(c.Wait.Seconds())}
		}
		c.Sleep(time.Second)
	}
}

// NotReadyError: Postgres in the container did not answer within Seconds.
type NotReadyError struct{ Seconds int }

func (e *NotReadyError) Error() string { return fmt.Sprintf("%d 秒内没有就绪", e.Seconds) }

// Stop stops the container (its data stays in the volume).
func (c *Container) Stop(ctx context.Context) error {
	if out, err := c.Run(ctx, nil, "docker", "stop", c.Name); err != nil {
		return errors.New(FirstLine(string(out)))
	}
	return nil
}

// Remove removes the container and its volume, with everything in it.
func (c *Container) Remove(ctx context.Context) error {
	if c.Exists(ctx) {
		if out, err := c.Run(ctx, nil, "docker", "rm", "-f", c.Name); err != nil {
			return errors.New(FirstLine(string(out)))
		}
	}
	if out, err := c.Run(ctx, nil, "docker", "volume", "rm", c.Volume); err != nil && !strings.Contains(string(out), "no such volume") {
		return errors.New(FirstLine(string(out)))
	}
	return nil
}

// FirstLine is the first line of a command's output.
func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// ManagerContainer and ManagerVolume are manager id's database container and volume.
func ManagerContainer(id string) string { return "persona-mgr-" + id + "-pg" }
func ManagerVolume(id string) string    { return "persona-mgr-" + id + "-data" }

// ManagerDBOptions are how ProvisionManagerDB reaches docker and the database; zero values are this machine's.
type ManagerDBOptions struct {
	// Password is the one the container was made with, when the caller kept it (in the URL it stored); "" for a new
	// container (one is generated) or to read it back from an existing container.
	Password string
	Run      Runner
	LookPath func(string) (string, error)
	FreePort func() (int, error)                         // a free port on 127.0.0.1 for a new container
	Ping     func(ctx context.Context, url string) error // nil: Postgres answering in the container is enough
	Sleep    func(time.Duration)
	Wait     time.Duration // how long to wait for it to answer (default 30 s)
}

// ProvisionManagerDB makes sure manager id has its own Postgres in Docker (ManagerContainer, ManagerVolume, published on
// 127.0.0.1) and returns its URL, password included, for the caller to keep (manager.json; for the default manager its
// own config). Idempotent: an existing container is started and reused with the password it was made with. It writes
// no file and never touches the hub's database. Without docker: ErrNoDocker, ErrDockerDenied or ErrDockerDown (wrapped).
func ProvisionManagerDB(ctx context.Context, managerID string, o ManagerDBOptions) (databaseURL string, created bool, err error) {
	if o.Run == nil {
		o.Run = RunCommand
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.FreePort == nil {
		o.FreePort = FreeLoopbackPort
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.Wait == 0 {
		o.Wait = 30 * time.Second
	}
	if err := CheckDocker(ctx, o.Run, o.LookPath); err != nil {
		return "", false, err
	}
	pg := &Container{Name: ManagerContainer(managerID), Volume: ManagerVolume(managerID), Run: o.Run, Sleep: o.Sleep, Wait: o.Wait}
	password := o.Password
	if !pg.Exists(ctx) {
		if password == "" {
			password = RandomPassword()
		}
		if pg.Port, err = o.FreePort(); err != nil {
			return "", false, err
		}
		if err := pg.Create(ctx, password); err != nil {
			return "", false, fmt.Errorf("make the database container %s: %w", pg.Name, err)
		}
		created = true
	} else {
		// its own role and database, not today's names: one made before the rename has jarvis (#6149)
		if err := pg.Identity(ctx); err != nil {
			return "", false, fmt.Errorf("the database container %s is there but its settings can't be read: %w", pg.Name, err)
		}
		if password == "" {
			if password, err = pg.Password(ctx); err != nil {
				return "", false, fmt.Errorf("the database container %s is there but its password can't be read: %w", pg.Name, err)
			}
		}
	}
	err = pg.Start(ctx, "")
	if err != nil && !created && portTaken(err) {
		// Its port was free when it was made and is taken now (it was stopped meanwhile): made again on another,
		// over the same volume and with the same password; the caller keeps the new URL.
		if out, rmErr := pg.Run(ctx, nil, "docker", "rm", "-f", pg.Name); rmErr != nil {
			return "", false, fmt.Errorf("the port of the database container %s is taken, and it could not be removed to move it: %s", pg.Name, FirstLine(string(out)))
		}
		if pg.Port, err = o.FreePort(); err != nil {
			return "", false, err
		}
		if err := pg.Create(ctx, password); err != nil {
			return "", false, fmt.Errorf("make the database container %s again on port %d: %w", pg.Name, pg.Port, err)
		}
		err = pg.Start(ctx, "")
	}
	if err != nil {
		return "", created, fmt.Errorf("the database container %s did not come up: %w", pg.Name, err)
	}
	port, err := pg.HostPort(ctx)
	if err != nil {
		return "", created, fmt.Errorf("%w: it is not one `personad manager install` made; remove it (docker rm -f %s) or give --database-url", err, pg.Name)
	}
	databaseURL = pg.URL(password, port)
	if o.Ping == nil {
		return databaseURL, created, nil
	}
	deadline := time.Now().Add(o.Wait)
	for {
		err := o.Ping(ctx, databaseURL)
		if err == nil {
			return databaseURL, created, nil
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return "", created, fmt.Errorf("the database container %s is up but does not answer: %w", pg.Name, err)
		}
		o.Sleep(time.Second)
	}
}

// RandomPassword is a new database password: 48 hex digits.
func RandomPassword() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// FreeLoopbackPort is a TCP port free on 127.0.0.1 now.
func FreeLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// RunCommand runs a program with env added (a Runner), up to five minutes (docker run may pull the image).
func RunCommand(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = time.Second
	return cmd.CombinedOutput()
}
