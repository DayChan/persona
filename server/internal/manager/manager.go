// Package manager is `personad manager …`: a manager made in the app (docs/assistant/managers.md, "A second manager") joins the hub
// with its one-time install code, runs, and is installed as a service of its own (systemd user unit, launchd agent,
// or a container). The manager itself is the assistant's program (assistant.RunManager) with a directory of its own.
package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/managerupdate"
	"github.com/nwverse/persona/server/internal/netaddr"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/update"
)

// Usage is the help of `personad manager`.
const Usage = `usage:
  personad manager update check|start|status|run|confirm|rollback --plan PATH
                       inspect or activate one explicit independent manager release plan
  personad manager update inventory --plan PATH --main ID --machine ID --machine-uid UID
                       read persisted main identity and two complete local node activity snapshots; writes nothing
  personad manager join <hub 地址> <安装码> [--dir 目录] [--database-url URL] [--fingerprint 指纹] [--install]
                       用 App 里「新建管理者」给的安装码换这个管理者的 token，写进目录里的 manager.json
                       （默认 ~/.persona/managers/<id>）；数据库是它自己的 Postgres：install 时在 Docker 里建
                       （容器 persona-mgr-<id>-pg），或者 --database-url 给一个
  personad manager join --builtin <hub address> [<code>] [--fingerprint <fingerprint>] [--install]
                       bind the built-in manager's own service to the hub with a code from personad hub manager-code
                       (given, or in PERSONA_MANAGER_CODE, which other users of the machine cannot read; the binding
                       goes into ~/.persona/managers/assistant/manager.json); --install: then install its service
  personad manager run [--dir 目录] [--join <hub 地址> <安装码>]
                       运行这个管理者（--join：目录里还没有 manager.json 时先换码，容器用）
  personad manager install [--dir 目录]     建好它自己的 Postgres（Docker），装成常驻服务（Linux：systemd 用户服务；Mac：launchd）
  personad manager uninstall [--dir 目录] [--purge]
                       停掉并去掉常驻服务；它的数据库容器停掉、数据卷留着（--purge：连容器和数据卷一起删掉）
  personad manager capabilities [--json]   只读列出这个程序支持的管理者能力
  personad manager status [--dir 目录]      这台电脑上的管理者和它们的服务
  personad manager install --builtin [--join <hub address>] | --check
                       install the built-in manager's own service (Linux: systemd user unit persona-manager-assistant;
                       Mac: launchd); --join: bind it first with the code in PERSONA_MANAGER_CODE, unless its binding
                       works (personad hub setup does this); --check: only say whether it can be installed here
  personad manager uninstall --builtin    stop and remove that service; its binding and data stay
  personad manager status --builtin       the built-in manager's binding and service
`

// errUsage: the arguments are wrong; the caller prints Usage.
var errUsage = errors.New("arguments")

// ErrUsage is errUsage for the command line (personad prints Usage and exits 2).
var ErrUsage = errUsage

// Main runs `personad manager <args>`.
func Main(ctx context.Context, build update.Build, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	e := newEnv(stdout)
	switch args[0] {
	case "update":
		return managerupdate.Main(ctx, build, args[1:], stdout)
	case "capabilities":
		return cmdCapabilities(stdout, args[1:])
	case "join":
		return cmdJoin(ctx, e, args[1:])
	case "run":
		return cmdRun(ctx, e, build, args[1:])
	case "install":
		if isBuiltin(args[1:]) {
			return cmdInstallBuiltin(ctx, e, args[1:])
		}
		return cmdInstall(ctx, e, args[1:])
	case "uninstall":
		if isBuiltin(args[1:]) {
			return cmdUninstallBuiltin(ctx, e, args[1:])
		}
		return cmdUninstall(ctx, e, args[1:])
	case "status":
		return cmdStatus(ctx, e, args[1:])
	case "-h", "--help", "help":
		fmt.Fprint(stdout, clitext.Named(Usage)) // as the program was run (persona-manager, personad)
		return nil
	}
	return errUsage
}

// --- join ---------------------------------------------------------------------------------------------------------

// JoinOptions are `personad manager join`'s.
type JoinOptions struct {
	Hub, Code   string
	Dir         string // "" → assistant.DefaultManagerDir(id)
	DatabaseURL string // "" → its own Postgres in Docker, made by install (Docker must be there: checked first)
	Fingerprint string // the hub's TLS pin, for an https hub with its own certificate
	// CheckDocker says why Docker can't make its database (nil: it can); nil checks this machine.
	CheckDocker func(ctx context.Context) error
	// Warn gets what the user should know beyond the join itself (a database container left unused); nil: nobody.
	Warn       io.Writer
	HTTPClient *http.Client // tests
	// AllowPlaintext lets an http:// hub address on the public internet through (--allow-plaintext): the token would
	// cross it in the clear.
	AllowPlaintext bool
	// PublicHost says whether a hub address's host is on the public internet; nil: publicHost (tests stand in).
	PublicHost func(ctx context.Context, host string) bool
	// builtin: the join is the built-in manager's (JoinBuiltin): the hub trades its code only for this join, and
	// only this join's code.
	builtin bool
}

// joinReply is POST /api/v1/bus/join's answer (hub/managers.go).
type joinReply struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"`
	HubID string `json:"hub_id"`
}

// Join trades the install code for the manager's token and writes manager.json; it returns the directory. What it
// can check before the code is spent (that there will be a database: Docker to make it, or --database-url) it checks
// first: a code works once.
func Join(ctx context.Context, o JoinOptions) (string, *assistant.ManagerConfig, error) {
	hub, code, err := joinAddress(ctx, o)
	if err != nil {
		return "", nil, err
	}
	if o.DatabaseURL != "" {
		if _, err := db.WithDatabase(o.DatabaseURL, "x"); err != nil {
			return "", nil, ConfigError{fmt.Errorf("--database-url: %w", err)}
		}
		if hub := assistant.HubDatabaseURL(); hub != "" && assistant.SameDatabase(o.DatabaseURL, hub) {
			return "", nil, ConfigError{assistant.ErrHubDatabase}
		}
	} else if !hasDatabase(o.Dir) { // joined again into its directory (after a revoke): its database is there
		check := o.CheckDocker
		if check == nil {
			check = newEnv(io.Discard).dockerProblem
		}
		if err := check(ctx); err != nil {
			return "", nil, err // before the code is spent: it works once
		}
	}
	if o.Dir != "" { // a manager.json there that is a link to nothing would fail the write after the code is spent
		if _, err := managerconf.ReadFile(filepath.Join(o.Dir, assistant.ManagerFile)); err != nil {
			return "", nil, ConfigError{err}
		}
	}
	j, err := redeem(ctx, o, hub, code)
	if err != nil {
		return "", nil, err
	}
	c := assistant.ManagerConfig{ID: j.ID, Name: j.Name, Hub: hub, HubID: j.HubID, Token: j.Token,
		Fingerprint: o.Fingerprint, DatabaseURL: o.DatabaseURL}
	dir := o.Dir
	if dir == "" {
		dir = assistant.DefaultManagerDir(j.ID)
	}
	if old, err := assistant.ReadManagerConfig(dir); err == nil && old.ID != j.ID {
		// The directory is another manager's: keep both, this one in its own.
		dir = assistant.DefaultManagerDir(j.ID)
	} else if err == nil && c.DatabaseURL == "" {
		// Installed again in the same place: the same database.
		c.DatabaseURL, c.DatabaseContainer = old.DatabaseURL, old.DatabaseContainer
	} else if err == nil && old.DatabaseContainer != "" && o.Warn != nil {
		// Another database given now: the container install made is not used any more (nor removed).
		fmt.Fprintf(o.Warn, "它原来的数据库容器 %s（数据卷 %s）不再用了，留着没删：不要了就 docker rm -f %s && docker volume rm %s\n",
			old.DatabaseContainer, dbVolume(c.ID), old.DatabaseContainer, dbVolume(c.ID))
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}
	warn := o.Warn
	if warn == nil {
		warn = os.Stderr
	}
	if err := assistant.WriteManagerConfigTo(dir, c, warn); err != nil {
		return "", nil, fmt.Errorf("写不了 %s：%w（token 已经换出来了：在 App 里再拿一个安装码重来）", filepath.Join(dir, assistant.ManagerFile), err)
	}
	return dir, &c, nil
}

// joinAddress checks a join's hub address and code before anything is asked of the hub: an http(s) address, a code,
// and no plaintext http over the public internet unless allowed (the token would cross it in the clear).
func joinAddress(ctx context.Context, o JoinOptions) (hub, code string, err error) {
	hub = strings.TrimRight(strings.TrimSpace(o.Hub), "/")
	if !strings.HasPrefix(hub, "http://") && !strings.HasPrefix(hub, "https://") {
		return "", "", fmt.Errorf("hub 地址 %q 要写成 http(s)://主机:端口", o.Hub)
	}
	code = strings.TrimSpace(o.Code)
	if code == "" {
		return "", "", errors.New("没有安装码：在 App 的「设置 › 管理者 › 新建」里拿一个")
	}
	if u, err := url.Parse(hub); err == nil && u.Scheme == "http" && !o.AllowPlaintext {
		public := o.PublicHost
		if public == nil {
			public = publicHost
		}
		if public(ctx, u.Hostname()) {
			return "", "", ConfigError{fmt.Errorf("hub 地址 %s 是公网上的明文 http：换来的 token 会明文经过公网。用 hub 的 https 地址（TLS 端口），确实要明文就加 --allow-plaintext", hub)}
		}
	}
	return hub, code, nil
}

// redeem trades code at hub (POST /api/v1/bus/join) for its manager's id and token, once.
func redeem(ctx context.Context, o JoinOptions, hub, code string) (joinReply, error) {
	var j joinReply
	client := o.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
		if o.Fingerprint != "" {
			client = link.Pinned(o.Fingerprint)
			client.Timeout = 30 * time.Second
		}
	}
	fields := map[string]any{"code": code}
	if o.builtin { // only then: a hub that does not know the field may refuse it
		fields["builtin"] = true
	}
	body, _ := json.Marshal(fields)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hub+bus.JoinPath, bytes.NewReader(body))
	if err != nil {
		return j, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return j, fmt.Errorf("连不上 hub %s：%w", hub, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		// Trying again with the same code cannot help: a configuration error (manager join exits 2; manager run
		// --join waits for manager.json instead, as a container would be restarted on any exit).
		return j, ConfigError{errors.New("hub 不认这个安装码：它不对、过期了（10 分钟）或者已经用过；在 App 里给这个管理者重新拿一个")}
	case http.StatusForbidden:
		var e struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Code == "builtin_local_only" {
			return j, ConfigError{errors.New("the built-in manager's code is traded only from the hub's machine or its local network")}
		}
		if e.Code == "tls_required" {
			// The hub refused a plaintext join from the public internet, and voided the code that crossed it: the
			// same command can never work.
			return j, ConfigError{fmt.Errorf("hub 不接受经公网明文换码，这个安装码也作废了：在 App 里重新拿一个，用 hub 的 https 地址（TLS 端口）")}
		}
		// Some other refusal (a proxy's, say): nothing says the same command can't work later.
		if e.Error != "" {
			return j, fmt.Errorf("hub 拒绝了换码（HTTP 403）：%s", firstLine(e.Error))
		}
		return j, fmt.Errorf("hub 拒绝了换码（HTTP 403）：%s", firstLine(string(raw)))
	case http.StatusConflict: // a code of the other kind of join, or the built-in manager not a service here: not spent
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return j, ConfigError{fmt.Errorf("the hub did not trade the code, which is still good (HTTP 409): %s", firstLine(e.Error))}
	case http.StatusTooManyRequests:
		return j, errors.New("试错太多次，hub 暂时不接受这台电脑换码：过几分钟再试")
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return j, fmt.Errorf("hub %s 还不支持管理者（HTTP %d）：先升级 hub", hub, resp.StatusCode)
	default:
		return j, fmt.Errorf("hub 没换成（HTTP %d）：%s", resp.StatusCode, firstLine(string(raw)))
	}
	if err := json.Unmarshal(raw, &j); err != nil || j.ID == "" || j.Token == "" {
		return j, errors.New("hub 的回答不对：没有 id 或 token")
	}
	return j, nil
}

// JoinBuiltin trades the built-in manager's one-time code (`persona-hub manager-code` on the hub's machine) for its
// token, and keeps the binding (the hub's address, the token, the fingerprint) in its manager.json in the data
// directory home, its other keys as they were (docs/assistant/managers.md "The built-in manager as its own service").
// The join says it is the built-in manager's: the hub refuses another manager's code for it, unspent.
func JoinBuiltin(ctx context.Context, o JoinOptions, home string) (managerconf.Binding, error) {
	var b managerconf.Binding
	hub, code, err := joinAddress(ctx, o)
	if err != nil {
		return b, err
	}
	file := managerconf.DefaultFile(home)
	// What would fail the write after the code is spent fails first: a link to nothing, a file that does not read.
	raw, err := managerconf.ReadFile(file)
	if err != nil {
		return b, ConfigError{err}
	}
	if raw != nil {
		if _, err := managerconf.ParseObject(raw); err != nil {
			return b, ConfigError{fmt.Errorf("%s: %w", file, err)}
		}
	}
	o.builtin = true // the hub trades only the built-in manager's code for this join
	j, err := redeem(ctx, o, hub, code)
	if err != nil {
		return b, err
	}
	if j.ID != assistantwire.AssistantID { // a hub that did not tell the kinds apart (an older one)
		return b, ConfigError{fmt.Errorf("that code was manager %s's (it is spent now: get it a new one), not the built-in "+
			"manager's: persona-hub manager-code on the hub's machine prints one", j.ID)}
	}
	b = managerconf.Binding{Hub: hub, HubID: j.HubID, Token: j.Token, Fingerprint: o.Fingerprint}
	if err := managerconf.WriteBinding(home, b); err != nil {
		return b, fmt.Errorf("could not write %s: %w (the token was traded: get a new code with persona-hub manager-code)", file, err)
	}
	return b, nil
}

// hasDatabase: dir has a manager.json that names its database.
func hasDatabase(dir string) bool {
	if dir == "" {
		return false
	}
	c, err := assistant.ReadManagerConfig(dir)
	return err == nil && c.DatabaseURL != ""
}

// databaseEnv is how the container `commands.docker` starts hands the manager its database (the address and its
// password): in the environment, never on a command line (`ps`, `docker inspect`'s Cmd). Not PERSONA_DATABASE_URL:
// that is the hub's (HubDatabaseURL), which a manager refuses.
const databaseEnv = "PERSONA_MANAGER_DATABASE_URL"

func cmdJoin(ctx context.Context, e *env, args []string) error {
	fs := flag.NewFlagSet("manager join", flag.ContinueOnError)
	fs.SetOutput(e.stdout)
	builtin := fs.Bool("builtin", false, "the built-in manager's code (persona-hub manager-code): bind its own service to the hub")
	dir := fs.String("dir", "", "管理者的目录（默认 ~/.persona/managers/<id>）")
	dbURL := fs.String("database-url", "", "它的 Postgres 库（默认 install 时在 Docker 里给它建一个）")
	fp := fs.String("fingerprint", "", "hub 的 TLS 指纹（https 地址用 hub 自己的证书时）")
	plain := fs.Bool("allow-plaintext", false, "允许公网上的明文 http hub 地址（token 会明文经过公网）")
	install := fs.Bool("install", false, "换好以后接着装成常驻服务（manager install）")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if *builtin && len(pos) == 1 { // the code in the environment: no other user of the machine sees it
		pos = append(pos, strings.TrimSpace(e.getenv(managerconf.BuiltinCodeEnv)))
		_ = os.Unsetenv(managerconf.BuiltinCodeEnv)
	}
	if len(pos) != 2 {
		return errUsage
	}
	if *builtin {
		if *dir != "" || *dbURL != "" {
			return ConfigError{errors.New("--builtin takes the hub's address, the code, and --fingerprint, --allow-plaintext or --install only")}
		}
		j := builtinJoin{hub: pos[0], code: pos[1], fingerprint: *fp, plaintext: *plain, always: true}
		if *install { // what can stop the install is checked before the code is spent
			return e.installBuiltin(ctx, dataDir(), j)
		}
		b, err := JoinBuiltin(ctx, JoinOptions{Hub: j.hub, Code: j.code, Fingerprint: j.fingerprint, AllowPlaintext: j.plaintext}, dataDir())
		if err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "Bound the built-in manager to the hub at %s (in %s).\n", b.Hub, managerconf.DefaultFile(dataDir()))
		fmt.Fprintf(e.stdout, "Next: install its service with %s manager install --builtin (or run %s assistant with "+
			"PERSONA_ASSISTANT_DEPLOY=service yourself).\n", e.self(), e.self())
		return nil
	}
	d, c, err := Join(ctx, JoinOptions{Hub: pos[0], Code: pos[1], Dir: *dir, DatabaseURL: *dbURL, Fingerprint: *fp,
		CheckDocker: e.dockerProblem, Warn: e.stdout, AllowPlaintext: *plain})
	if err != nil {
		return err
	}
	where := "它自己的 Postgres，install 时在 Docker 里建（容器 " + dbContainer(c.ID) + "）"
	if c.DatabaseContainer == "" && c.DatabaseURL != "" {
		where = db.DatabaseName(c.DatabaseURL) + "（--database-url 给的，没有就建）"
	}
	fmt.Fprintf(e.stdout, "加入了 hub：这台是管理者「%s」（%s）。\n  目录：%s\n  数据库：%s\n", c.Name, c.ID, d, where)
	if *install {
		return cmdInstall(ctx, e, []string{"--dir", d})
	}
	fmt.Fprintf(e.stdout, "下一步：%s manager install --dir %s（装成常驻服务），或者在前台 %s manager run --dir %s\n",
		e.self(), shellQuote(d), e.self(), shellQuote(d))
	return nil
}

// publicHost: host (a hub address's) is on the public internet: an address of a public range, or a name that
// resolves to one. Loopback, private, link-local, shared (CGNAT) and ULA addresses, localhost, .local names and
// single-label names are not; a name that does not resolve is not either (the dial fails anyway). A NAT64 address
// is what the IPv4 address in it is.
func publicHost(ctx context.Context, host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return publicAddr(ip)
	}
	if !strings.Contains(host, ".") {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if publicAddr(ip) {
			return true
		}
	}
	return false
}

// NAT64 prefixes: RFC 6052's well-known 64:ff9b::/96, whose last 32 bits are the IPv4 address, and RFC 8215's
// local-use 64:ff9b:1::/48, which a network may use whole or as any longer prefix inside it.
var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")
	nat64LocalUse  = netip.MustParsePrefix("64:ff9b:1::/48")
)

// publicAddr: ip is on the public internet. A NAT64 address is what the IPv4 address in it is. In the local-use
// prefix that is read where RFC 6052 (section 2.2) puts it for the /48 itself (bytes 6, 7, 9 and 10; the u octet and
// the suffix zero) or for 64:ff9b:1::/96 (the last 32 bits); an address of a longer prefix inside it, whose length
// it doesn't say, is taken for public.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	b := ip.As16()
	switch {
	case nat64WellKnown.Contains(ip):
		return publicClass(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	case nat64LocalUse.Contains(ip):
		if b[6]|b[7]|b[8]|b[9]|b[10]|b[11] == 0 { // 64:ff9b:1::/96
			return publicClass(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		}
		if b[8]|b[11]|b[12]|b[13]|b[14]|b[15] == 0 { // the /48
			return publicClass(netip.AddrFrom4([4]byte{b[6], b[7], b[9], b[10]}))
		}
		return true
	}
	return publicClass(ip)
}

func publicClass(ip netip.Addr) bool {
	switch netaddr.Classify(ip) {
	case netaddr.PublicIPv4, netaddr.GlobalIPv6, netaddr.TransitionIPv6:
		return true
	}
	return false
}

// --- run ----------------------------------------------------------------------------------------------------------

// restart starts the program again once a new token is there (reexec; tests: nil, run again in this process).
var restart = reexec

// revokedPoll is how often a manager whose token was refused looks for a new one in manager.json.
var revokedPoll = 10 * time.Second

// revokedRetry is how long a manager whose token was refused waits, with no new one, before it tries the same token
// again: the hub said so explicitly (CloseRevoked, 401 token_revoked), but a hub restored from a backup may know it
// again, and trying once in ten minutes costs nothing.
var revokedRetry = 10 * time.Minute

func cmdRun(ctx context.Context, e *env, build update.Build, args []string) error {
	fs := flag.NewFlagSet("manager run", flag.ContinueOnError)
	fs.SetOutput(e.stdout)
	dir := fs.String("dir", "", "管理者的目录（这台只有一个管理者时可以不写）")
	join := fs.Bool("join", false, "目录里还没有 manager.json 时，先用后面的 <hub 地址> <安装码> 换码")
	dbURL := fs.String("database-url", "", "和 --join 一起：它自己的 Postgres 库（容器里经环境变量 "+databaseEnv+" 给，不放命令行）")
	fp := fs.String("fingerprint", "", "和 --join 一起：hub 的 TLS 指纹")
	plain := fs.Bool("allow-plaintext", false, "和 --join 一起：允许公网上的明文 http hub 地址")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if *join != (len(pos) == 2) || (!*join && len(pos) != 0) {
		return errUsage
	}
	d := *dir
	if *join && (d == "" || !fileExists(filepath.Join(d, assistant.ManagerFile))) {
		dbAt := *dbURL
		if dbAt == "" {
			dbAt = e.getenv(databaseEnv)
		}
		joined, _, err := Join(ctx, JoinOptions{Hub: pos[0], Code: pos[1], Dir: d, DatabaseURL: dbAt, Fingerprint: *fp,
			CheckDocker: e.dockerProblem, Warn: e.stdout, AllowPlaintext: *plain})
		var ce ConfigError
		switch {
		case err == nil:
			d = joined
			fmt.Fprintf(e.stdout, "加入了 hub，目录 %s\n", d)
		case errors.As(err, &ce) && d == "":
			// No directory to wait on: watching for "the one manager of this computer" would take up another
			// manager's directory when there is exactly one. A container always names its --dir; anyone else gets the
			// configuration error (exit 2).
			return err
		case errors.As(err, &ce):
			// The same command can't do better next time (a used or expired code, a refused plaintext join, no
			// database it can have), and a container's --restart unless-stopped starts it again on any exit, exit 2 too: wait,
			// not dialing, until a join run by hand into the directory writes manager.json, as after a revoke.
			fmt.Fprintf(e.stdout, "manager: 没加入 hub，同样的命令再试也不会成：%v。在等：在 App 里拿一个新安装码，"+
				"再运行一次新的安装命令（没加入过的容器会用新码重建），或者运行 personad manager join <hub 地址> <安装码> --dir %s\n", err, shellQuote(d))
			if err = waitJoined(ctx, d); err != nil {
				return err
			}
			fmt.Fprintf(e.stdout, "manager: 有了 manager.json，目录 %s\n", d)
		default:
			return err
		}
	}
	if d == "" {
		if d, err = onlyManager(); err != nil {
			return err
		}
	}
	return e.runManager(ctx, build, d)
}

// Run runs the manager in dir until ctx ends. When the hub refuses its token it says so and waits — not dialing —
// until manager.json has another one (`personad manager join` with a new code, into the same directory), so a
// service manager that restarts it does not turn into a loop of refused connections.
func Run(ctx context.Context, build update.Build, dir string, out io.Writer) error {
	return newEnv(out).runManager(ctx, build, dir)
}

func (e *env) runManager(ctx context.Context, build update.Build, dir string) error {
	osproc.SealInherited() // what a parent left inheritable reaches none of what the manager starts (osproc)
	out := e.stdout
	env := os.Environ() // as it started: RunManager points PERSONA_HOME at dir
	c, err := assistant.ReadManagerConfig(dir)
	if err != nil {
		return ConfigError{err} // restarting cannot fix it (the service's unit does not, exit 2)
	}
	// Its own Postgres comes up with it (started again after a reboot; made here when install did not).
	if _, err := e.ownDatabase(ctx, dir, c); err != nil {
		return err
	}
	for {
		before, _ := assistant.ReadManagerConfig(dir)
		err := assistant.RunManager(ctx, build, dir)
		if !errors.Is(err, bus.ErrRevoked) {
			return err
		}
		id := ""
		if before != nil {
			id = before.ID
		}
		fmt.Fprintf(out, "manager: [%s] hub 不再认这个管理者的 token（在 App 里删掉了，或者在别处重新装了）：先不连 hub，每 %s 再试一次。"+
			"要接着用这个目录：在 App 里给它拿一个新安装码，运行 personad manager join <hub 地址> <安装码> --dir %s\n", id, revokedRetry, shellQuote(dir))
		if err := awaitRestart(ctx, dir, before, out); err != nil {
			return err
		}
		if restart != nil {
			if err := restart(env); err != nil {
				fmt.Fprintf(out, "manager: [%s] could not start again in place (%v): running it again here\n", id, err)
			}
		}
	}
}

// RunBuiltin is the built-in manager as a service of its own (`persona-manager assistant` with
// PERSONA_ASSISTANT_DEPLOY=service, docs/assistant/managers.md "The built-in manager as its own service"), on the
// binding in its manager.json. Not bound is a configuration error (exit 2: its unit does not restart it). Once the hub
// refuses its token (bound again, or a hub that starts the built-in manager itself again: assistant.mode child, a
// version from before the split) nothing of it runs until manager.json has another token or the hub takes this one
// again (awaitRestart), so it never runs next to the one that has the token; then it starts again in place. It also
// starts again in place on a new file where it runs from, one that runs, once it is idle (binaryWatch): nothing else
// restarts its service after an update.
func RunBuiltin(ctx context.Context, build update.Build, out io.Writer) error {
	env := os.Environ()
	home := dataDir()
	// One at a time: a second one on the same binding would take turns on the bus with the first, both running the
	// computers and the extensions.
	if err := os.MkdirAll(managerconf.DefaultDir(home), 0o700); err != nil {
		return err
	}
	if err := lockFile(filepath.Join(managerconf.DefaultDir(home), builtinLock)); err != nil {
		if errors.Is(err, errLocked) {
			return ConfigError{errors.New("another persona-manager assistant runs on this data directory as a service already")}
		}
		return err
	}
	watch := newBinaryWatch(out)
	for {
		before, err := managerconf.ReadBinding(home)
		if err != nil { // not bound, or a manager.json that does not read: restarting cannot fix it (exit 2)
			return ConfigError{err}
		}
		// A new binary (an update): once it runs and nothing is going on, it stops and starts again on that one, as
		// the hub's child would with the hub (watchBinary).
		run, stop := context.WithCancel(ctx)
		var idle atomic.Pointer[assistant.Idle]
		var renew atomic.Bool
		watched := make(chan struct{})
		if watch != nil {
			go func() {
				defer close(watched)
				if watch.next(run, func() assistant.Idle {
					if p := idle.Load(); p != nil {
						return *p
					}
					return nil
				}) {
					renew.Store(true)
					stop()
				}
			}()
		} else {
			close(watched)
		}
		err = runAssistant(run, build, nil, assistant.MainHooks{Ready: func(p assistant.Idle) { idle.Store(&p) }})
		stop()
		<-watched
		if renew.Load() && ctx.Err() == nil {
			watch.restartOnNew(ctx, env) // returns when it did not: the manager runs again on this code
			continue
		}
		if errors.Is(err, managerconf.ErrNotBound) {
			return ConfigError{err}
		}
		if !errors.Is(err, bus.ErrRevoked) {
			return err
		}
		fmt.Fprintf(out, "assistant: the hub no longer takes the built-in manager's token (bound again, or the hub "+
			"starts it itself again): not connecting, trying the same token every %s. To bind it again: persona-hub "+
			"manager-code on the hub's machine, then persona-manager join --builtin <hub address> <code>\n", revokedRetry)
		c := &assistant.ManagerConfig{ID: assistantwire.AssistantID, Hub: before.Hub, Token: before.Token, Fingerprint: before.Fingerprint}
		if err := awaitRestartWith(ctx, c, builtinToken(home), out); err != nil {
			return err
		}
		if restart != nil {
			// The file there may be a new one by now: only one that runs (as for a new binary, binaryWatch).
			if watch != nil {
				if err := checkBinary(ctx, watch.path); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					fmt.Fprintf(out, "assistant: %v: running it again here, on the code it has\n", err)
					if fi, err := os.Stat(watch.path); err == nil {
						watch.seen = fi
					}
					continue
				}
			}
			if err := restart(env); err != nil {
				fmt.Fprintf(out, "assistant: could not start again in place (%v): running it again here\n", err)
			}
		}
	}
}

// runAssistant is the built-in manager itself (tests: a stand-in).
var runAssistant = assistant.MainWith

// builtinLock is the file in the built-in manager's directory its own service holds a lock on while it runs.
const builtinLock = ".service.lock"

// errLocked: another process holds the lock (lockFile).
var errLocked = errors.New("locked by another process")

// held keeps the locked files open for the life of the process.
var held []*os.File

// builtinToken reads the token in the built-in manager's binding ("" when it does not read).
func builtinToken(home string) func() string {
	return func() string {
		b, err := managerconf.ReadBinding(home)
		if err != nil {
			return ""
		}
		return b.Token
	}
}

// awaitRestart waits, after the hub refused the token of the manager in dir (before is its manager.json then), until
// it should start again: a new token in manager.json, or the hub no longer refusing the token. Nothing of the manager
// runs meanwhile (no bots, connectors, extensions, inbox): the sources it had may be another manager's now. Every
// revokedRetry the token alone is tried with a bare dial (probeToken), never the whole manager.
func awaitRestart(ctx context.Context, dir string, before *assistant.ManagerConfig, out io.Writer) error {
	return awaitRestartWith(ctx, before, func() string {
		c, err := assistant.ReadManagerConfig(dir)
		if err != nil {
			return ""
		}
		return c.Token
	}, out)
}

// awaitRestartWith is awaitRestart with token reading the manager's token where it keeps it ("" when it does not
// read).
func awaitRestartWith(ctx context.Context, before *assistant.ManagerConfig, token func() string, out io.Writer) error {
	id := ""
	if before != nil {
		id = before.ID
	}
	wait := revokedRetry
	for {
		renewed, err := waitToken(ctx, token, before, wait)
		if err != nil {
			return err
		}
		if renewed {
			fmt.Fprintf(out, "manager: [%s] manager.json 里有了新 token，重新连 hub\n", id)
			return nil
		}
		// Only a dial the hub took (the upgrade went through: it accepted the token) brings it back. Refused again:
		// the next try in revokedRetry. Anything else (the hub unreachable, a proxy's answer, a 503) says nothing about
		// the token: tried again sooner, never taken for yes.
		err = probeToken(ctx, before)
		switch {
		case err == nil:
			fmt.Fprintf(out, "manager: [%s] hub 又认这个 token 了：重新连 hub\n", id)
			return nil
		case errors.Is(err, bus.ErrRevoked), errors.Is(err, bus.ErrRejected): // refused (a plain 401 from an older hub)
			wait = revokedRetry
		default:
			wait = min(revokedRetry, probeAgain)
		}
	}
}

// probeAgain is how soon the token is tried again after a probe that got no verdict (the hub unreachable).
var probeAgain = time.Minute

// probeToken dials the hub's bus with c's token and hangs up at once: nil when the hub took it (the upgrade went
// through), bus.ErrRevoked while it still refuses it, another error when it said nothing about it. Nothing is said on
// the connection.
var probeToken = func(ctx context.Context, c *assistant.ManagerConfig) error {
	if c == nil {
		return nil
	}
	var client *http.Client
	if c.Fingerprint != "" {
		client = link.Pinned(c.Fingerprint)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ws, err := bus.Dial(ctx, c.Hub, c.Token, client)
	if err != nil {
		return err
	}
	_ = ws.Close(websocket.StatusNormalClosure, "probe")
	return nil
}

// waitNewToken returns once manager.json in dir has a token other than before's (renewed), after most (not renewed:
// try the same token again), or with ctx's error.
func waitNewToken(ctx context.Context, dir string, before *assistant.ManagerConfig, most time.Duration) (renewed bool, err error) {
	return waitToken(ctx, func() string {
		c, err := assistant.ReadManagerConfig(dir)
		if err != nil {
			return ""
		}
		return c.Token
	}, before, most)
}

// waitToken is waitNewToken with token reading the token ("" when it does not read: not renewed).
func waitToken(ctx context.Context, token func() string, before *assistant.ManagerConfig, most time.Duration) (renewed bool, err error) {
	t := time.NewTicker(revokedPoll)
	defer t.Stop()
	deadline := time.NewTimer(most)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return false, nil
		case <-t.C:
		}
		if tok := token(); tok != "" && (before == nil || tok != before.Token) {
			return true, nil
		}
	}
}

// waitJoined returns once dir has a manager.json, or with ctx's error.
func waitJoined(ctx context.Context, dir string) error {
	for {
		renewed, err := waitNewToken(ctx, dir, nil, time.Hour)
		if err != nil {
			return err
		}
		if renewed {
			return nil
		}
	}
}

// managersRoot is where the managers of this machine are by default (~/.persona/managers).
func managersRoot() string { return filepath.Dir(assistant.DefaultManagerDir("x")) }

// localManagers are the directories under managersRoot with a manager.json, but the built-in manager's (the
// assistant the hub runs, managerconf.DefaultDir: its manager.json holds only its configuration).
func localManagers() []string {
	entries, _ := os.ReadDir(managersRoot())
	var out []string
	for _, ent := range entries {
		d := filepath.Join(managersRoot(), ent.Name())
		if ent.Name() == assistantwire.AssistantID {
			continue
		}
		if ent.IsDir() && fileExists(filepath.Join(d, assistant.ManagerFile)) {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// onlyManager is the one manager of this machine, for a command without --dir.
func onlyManager() (string, error) {
	list := localManagers()
	switch len(list) {
	case 1:
		return list[0], nil
	case 0:
		return "", fmt.Errorf("%s 下没有管理者：先 personad manager join，或者用 --dir 指定目录", managersRoot())
	}
	return "", fmt.Errorf("这台有 %d 个管理者（%s）：用 --dir 指定是哪个", len(list), strings.Join(list, "、"))
}

// --- helpers ------------------------------------------------------------------------------------------------------

// parseInterleaved parses flags that may come before, between or after the positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, errHelp
			}
			return nil, errUsage
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// ConfigError is a mistake in the manager's configuration (no manager.json, no database to use): personad exits 2,
// which its systemd service does not restart on (RestartPreventExitStatus=2; a container's restart policy does, so
// manager run --join waits instead).
type ConfigError struct{ Err error }

func (e ConfigError) Error() string { return e.Err.Error() }
func (e ConfigError) Unwrap() error { return e.Err }

// errHelp: -h was asked for; the flags' help is printed already.
var errHelp = errors.New("help")

// ErrHelp is errHelp for the command line (exit 0).
var ErrHelp = errHelp

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// shellQuote makes s one word for sh.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+=:@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// dataDir is this machine's data directory (~/.persona), for the binary the service runs.
func dataDir() string { return identity.HomeDir() }
