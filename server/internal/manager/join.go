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
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/netaddr"
)

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
