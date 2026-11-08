package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/identity"
	"html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The service that keeps the hub running (docs/assistant/onboarding.md 4.1 step 3): on Linux the systemd user units of
// scripts/deploy/hub-systemd.sh (socket activation, docs/operations/hub-restart.md; keep the two in step), on a Mac a launchd
// agent.

func (e *setupEnv) service(ctx context.Context, cfg *setupConfig) error {
	if e.o.noService {
		e.report("service", true, "按 --no-service 没装常驻服务", "")
		return nil
	}
	if err := e.portsFree(ctx, cfg); err != nil {
		return err
	}
	bin, err := e.hubBinary()
	if err != nil {
		return e.fail("service", "放不进 "+filepath.Join(e.root, "bin", "personad")+"："+err.Error(), "检查这个目录的权限")
	}
	switch e.goos {
	case "linux":
		return e.systemdService(ctx, cfg.Config, bin)
	case "darwin":
		return e.launchdService(ctx, cfg.Config, bin)
	}
	return e.fail("service", e.goos+" 上还装不了常驻服务", "加 --no-service，自己让 personad hub run 一直运行")
}

// portsFree: the hub's ports are free, or held by this hub (its socket unit, or it answers with this token).
func (e *setupEnv) portsFree(ctx context.Context, cfg *setupConfig) error {
	addrs := []string{cfg.Listen}
	if cfg.Public.TLSListen != "" {
		addrs = append(addrs, cfg.Public.TLSListen)
	}
	var busy []string
	for _, a := range addrs {
		if p, err := strconv.Atoi(portOf(a)); err == nil && !e.portFree(p) {
			busy = append(busy, portOf(a))
		}
	}
	if len(busy) == 0 || e.hubAnswers(ctx, cfg.Config) || e.ownSocketActive(ctx) {
		return nil
	}
	return e.fail("service", "端口 "+strings.Join(busy, "、")+" 被别的程序占着，可能是另一个 Persona",
		"停掉占着它的程序，或者改 "+e.hubJSON()+" 的 listen / public.tls_listen，再重跑")
}

func (e *setupEnv) ownSocketActive(ctx context.Context) bool {
	if e.goos != "linux" {
		return false
	}
	_, err := e.run(ctx, nil, "systemctl", "--user", "is-active", "--quiet", e.compatibleHubName("personad.socket"))
	return err == nil
}

// hubBinary is <root>/bin/personad, where the units run it from and self-update replaces it; copied from this
// executable when it is not there yet (personad run from somewhere else), with personad next to it.
func (e *setupEnv) hubBinary() (string, error) {
	return e.compatibleHubBinary()
}

// binaryStamp is where setup notes which <root>/bin/personad the service was last (re)started with.
func binaryStamp(bin string) string {
	return filepath.Join(filepath.Dir(bin), ".personad.setup-sha256")
}

// binaryChanged says whether bin differs from the one the service last started with (install.sh replaced it, an
// upgrade): the running hub is then still the old one and has to be restarted. Without a note it cannot tell, so it
// says changed: one restart, which socket activation makes unnoticeable. sum goes to noteBinary once restarted.
func binaryChanged(bin string) (sum string, changed bool) {
	f, err := os.Open(bin)
	if err != nil {
		return "", true
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", true
	}
	sum = hex.EncodeToString(h.Sum(nil))
	old, err := os.ReadFile(binaryStamp(bin))
	return sum, err != nil || strings.TrimSpace(string(old)) != sum
}

func noteBinary(bin, sum string) {
	if sum != "" {
		_ = os.WriteFile(binaryStamp(bin), []byte(sum+"\n"), 0o644)
	}
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := to + ".setup-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, to)
}

// --- systemd ------------------------------------------------------------------------------------------------------

func systemdQuoteValue(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(v) + `"`
}

// hubServiceUnit is personad.service, as hub-systemd.sh writes it.
func hubServiceUnit(root string, socket bool) string {
	lines := []string{"[Unit]", "Description=Persona hub (personad)", "After=network-online.target"}
	if socket {
		lines = append(lines, "Wants=personad.socket", "After=personad.socket")
	}
	lines = append(lines, "", "[Service]",
		"ExecStart="+strings.ReplaceAll(systemdQuoteValue(root+"/bin/personad"), "$", "$$")+" hub",
		"Restart=always", "RestartSec=3", "TimeoutStopSec=20",
		"Environment="+systemdQuoteValue("PERSONA_HOME="+root),
		"Environment=PATH=%h/.local/bin:%h/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "",
		"[Install]", "WantedBy=default.target", "")
	return strings.Join(lines, "\n")
}

// hubSocketUnit is personad.socket, as hub-systemd.sh writes it: the plaintext and TLS addresses. FreeBind lets an
// address-bound listener (a new install's "<LAN address>:18662") come up while that address is not there yet or any
// more (DHCP), instead of failing the socket and with it the plaintext port.
func hubSocketUnit(listens []string) string {
	lines := []string{"[Unit]", "Description=Persona hub's listening sockets (docs/operations/hub-restart.md)", "", "[Socket]"}
	bound := false
	for _, a := range listens {
		if strings.HasPrefix(a, ":") {
			a = a[1:]
		} else {
			bound = true
		}
		lines = append(lines, "ListenStream="+a)
	}
	lines = append(lines, "Backlog=4096", "NoDelay=true")
	if bound {
		lines = append(lines, "FreeBind=true")
	}
	lines = append(lines, "Service=personad.service", "", "[Install]", "WantedBy=sockets.target", "")
	return strings.Join(lines, "\n")
}

func (e *setupEnv) unitDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(e.home, ".config")
	}
	return filepath.Join(base, "systemd", "user")
}

func (e *setupEnv) systemctl(ctx context.Context, args ...string) error {
	for i, name := range args {
		args[i] = e.compatibleHubName(name)
	}
	out, err := e.run(ctx, nil, "systemctl", append([]string{"--user"}, args...)...)
	if err != nil {
		if line := setupFirstLine(string(out)); line != "" {
			return errors.New(line)
		}
	}
	return err
}

func (e *setupEnv) systemdService(ctx context.Context, cfg *Config, bin string) error {
	units, unitErr := e.compatibleHubUnits()
	if unitErr != nil {
		return unitErr
	}
	if !e.userSystemd(ctx) {
		return e.fail("service", "这台电脑没有 systemd 用户服务（systemctl --user 用不了）",
			"加 --no-service 重跑，自己让 "+bin+" hub 一直运行（例如 tmux、nohup）")
	}
	dir := e.unitDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return e.fail("service", "建不了 "+dir+"："+err.Error(), "检查这个目录的权限")
	}
	socketChoice, explicitSocket := identity.LookupEnv("PERSONA_HUB_SOCKET")
	useSocket := socketChoice != "0"
	if units.legacy && !explicitSocket {
		_, err := os.Stat(filepath.Join(dir, units.socket))
		useSocket = err == nil
	}
	listens := []string{cfg.Listen}
	if cfg.Public.TLSListen != "" {
		listens = append(listens, cfg.Public.TLSListen)
	}
	servicePath, socketPath := filepath.Join(dir, units.service+".service"), filepath.Join(dir, units.socket)
	serviceBefore, _ := os.ReadFile(servicePath)
	socketBefore, _ := os.ReadFile(socketPath)
	service := compatibleUnitText(hubServiceUnit(e.root, useSocket), units)
	if units.legacy && len(serviceBefore) > 0 {
		service = string(serviceBefore)
	}
	socket := ""
	if useSocket {
		socket = compatibleUnitText(hubSocketUnit(listens), units)
		if units.legacy && sameSocketAddresses(string(socketBefore), listens) {
			socket = string(socketBefore)
		}
	}
	if err := os.WriteFile(servicePath, []byte(service), 0o644); err != nil {
		return e.fail("service", "写不了 "+servicePath+"："+err.Error(), "检查这个目录的权限")
	}
	if useSocket {
		if err := os.WriteFile(socketPath, []byte(socket), 0o644); err != nil {
			return e.fail("service", "写不了 "+socketPath+"："+err.Error(), "检查这个目录的权限")
		}
	} else {
		_ = os.Remove(socketPath)
	}
	changed := string(serviceBefore) != service
	sum, binChanged := binaryChanged(bin)
	socketChanged := string(socketBefore) != socket
	steps := [][]string{{"daemon-reload"}, {"enable", "personad"}}
	serviceActive := e.systemctl(ctx, "is-active", "--quiet", "personad") == nil
	socketActive := e.systemctl(ctx, "is-active", "--quiet", e.compatibleHubName("personad.socket")) == nil
	what := "装好了常驻服务（systemd 用户服务 personad，端口归 personad.socket）并启动"
	switch {
	case useSocket && (!socketActive || socketChanged):
		// First time, or other addresses: a running hub holds the port itself; one stop to hand it to the socket.
		// Both in one stop: the service stopped alone while the socket listens is started again by the next
		// connection, and the socket then cannot start while it runs (hub-systemd.sh, the same steps).
		steps = append(steps, []string{"stop", "personad.socket", "personad"}, []string{"enable", "personad.socket"},
			[]string{"start", "personad.socket"}, []string{"start", "personad"})
	case !useSocket && (socketActive || len(socketBefore) > 0):
		steps = append(steps, []string{"stop", "personad"}, []string{"disable", "--now", "personad.socket"}, []string{"start", "personad"})
	case changed || !serviceActive:
		steps = append(steps, []string{"restart", "personad"})
	case e.restartHub:
		steps = append(steps, []string{"restart", "personad"})
		what = "常驻服务已经装好；" + e.restartReason(binChanged) + "，重启了 hub"
	case binChanged:
		// The units are as they were, but the binary is new (an upgrade): the hub running is the old one. The port
		// stays with personad.socket, so the restart is not noticed (docs/operations/hub-restart.md).
		steps = append(steps, []string{"restart", "personad"})
		what = "常驻服务已经装好；hub 换了新版本，重启了它"
	default:
		what = "常驻服务已经装好、在运行，没动它"
	}
	if !useSocket {
		what = strings.Replace(what, "，端口归 personad.socket", "", 1)
	}
	for _, s := range steps {
		if err := e.systemctl(ctx, s...); err != nil {
			return e.fail("service", "systemctl --user "+strings.Join(s, " ")+" 失败："+err.Error(), "运行 journalctl --user -u personad -n 50 看原因")
		}
	}
	noteBinary(bin, sum)
	e.hubRestart = func(ctx context.Context) error { return e.systemctl(ctx, "restart", "personad") }
	detail, next := what, e.linger(ctx)
	if next != "" {
		detail += "；没打开 linger：你退出登录后 hub 会停"
	}
	e.report("service", true, detail, next)
	return nil
}

// linger keeps the user's services running after they log out (and starts them at boot): systemd user services stop
// with the last session otherwise. It may take sudo; that is asked once, saying why. Returns what is left to do.
func (e *setupEnv) linger(ctx context.Context) string {
	manual := "运行 sudo loginctl enable-linger " + e.user + "，让 hub 在你退出登录、电脑重启后也接着运行"
	if _, err := e.lookPath("loginctl"); err != nil || e.user == "" {
		return ""
	}
	if out, err := e.run(ctx, nil, "loginctl", "show-user", e.user, "--property=Linger", "--value"); err == nil && strings.TrimSpace(string(out)) == "yes" {
		return ""
	}
	if _, err := e.run(ctx, nil, "loginctl", "enable-linger", e.user); err == nil {
		return ""
	}
	yes := e.o.yes
	if !yes {
		var asked bool
		yes, asked = e.ask("要让 hub 在你退出登录、电脑重启后也接着运行，需要用 sudo 打开 linger（loginctl enable-linger）。现在打开吗？")
		if !asked {
			return manual
		}
	}
	if !yes {
		return manual
	}
	if err := e.runTTY(ctx, "sudo", "loginctl", "enable-linger", e.user); err != nil {
		return manual
	}
	return ""
}

// --- launchd ------------------------------------------------------------------------------------------------------

func (e *setupEnv) plistPath() string {
	return filepath.Join(e.home, "Library", "LaunchAgents", hubLaunchdLabel+".plist")
}

// hubPlist is the hub's launchd agent: it runs <root>/bin/personad hub, which hub-systemd.sh and self-update find.
func hubPlist(home, root, bin string) string {
	x := html.EscapeString
	log := filepath.Join(root, "hub.log")
	path := strings.Join([]string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"), filepath.Join(home, ".orbstack", "bin"),
		"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":")
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + hubLaunchdLabel + `</string>
  <key>ProgramArguments</key><array><string>` + x(bin) + `</string><string>hub</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>PERSONA_HOME</key><string>` + x(root) + `</string>
    <key>PATH</key><string>` + x(path) + `</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>3</integer>
  <key>StandardOutPath</key><string>` + x(log) + `</string>
  <key>StandardErrorPath</key><string>` + x(log) + `</string>
</dict></plist>
`
}

// otherHubAgent is the label of a launchd agent someone set up before that already runs this hub ("" when none):
// left as it is, like hub-systemd.sh does. Matched the way that script matches: ProgramArguments (or Program) is
// "<personad> hub", the binary resolved through symlinks and ~, so an agent naming ~/.persona/bin/personad is found
// when ~/.persona and ~/.persona are the same directory.
func (e *setupEnv) otherHubAgent(bin string) string {
	want := resolvedPath(bin)
	files, _ := filepath.Glob(filepath.Join(e.home, "Library", "LaunchAgents", "*.plist"))
	sort.Strings(files)
	for _, f := range files {
		if filepath.Base(f) == hubLaunchdLabel+".plist" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		label, args, ok := launchdJob(b)
		if !ok || len(args) < 2 || args[1] != "hub" {
			continue
		}
		prog := args[0]
		if prog == "~" || strings.HasPrefix(prog, "~/") {
			prog = filepath.Join(e.home, strings.TrimPrefix(prog, "~"))
		}
		if resolvedPath(prog) != want {
			continue
		}
		if label == "" {
			label = strings.TrimSuffix(filepath.Base(f), ".plist")
		}
		return label
	}
	return ""
}

// resolvedPath is p with symlinks resolved, or just cleaned when it does not exist.
func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

type plistNode struct {
	XMLName xml.Name
	Text    string      `xml:",chardata"`
	Nodes   []plistNode `xml:",any"`
}

// launchdJob reads an XML property list's Label and its ProgramArguments (or Program when there are none).
func launchdJob(b []byte) (label string, args []string, ok bool) {
	var root plistNode
	if err := xml.Unmarshal(b, &root); err != nil || root.XMLName.Local != "plist" || len(root.Nodes) == 0 || root.Nodes[0].XMLName.Local != "dict" {
		return "", nil, false
	}
	var program string
	kv := root.Nodes[0].Nodes
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i].XMLName.Local != "key" {
			return "", nil, false
		}
		v := kv[i+1]
		switch strings.TrimSpace(kv[i].Text) {
		case "Label":
			label = strings.TrimSpace(v.Text)
		case "Program":
			program = strings.TrimSpace(v.Text)
		case "ProgramArguments":
			for _, a := range v.Nodes {
				args = append(args, a.Text)
			}
		}
	}
	if len(args) == 0 && program != "" {
		args = []string{program}
	}
	return label, args, true
}

func (e *setupEnv) agentPlist(label string) string {
	return filepath.Join(e.home, "Library", "LaunchAgents", label+".plist")
}

// restartReason says why setup restarts a running hub: a new binary, what config changed, or both.
func (e *setupEnv) restartReason(binChanged bool) string {
	why := e.restartWhy
	if binChanged {
		why = append([]string{"hub 换了新版本"}, why...)
	}
	return strings.Join(why, "、")
}

// relaunch is a launchd agent's restart, for undoing a switch to the built-in manager's own service.
func (e *setupEnv) relaunch(target string) func(context.Context) error {
	return func(ctx context.Context) error {
		if out, err := e.run(ctx, nil, "launchctl", "kickstart", "-k", target); err != nil {
			return fmt.Errorf("%v: %s", err, setupFirstLine(string(out)))
		}
		return nil
	}
}

// kickstart restarts a loaded launchd agent (hub-systemd.sh's way on a Mac).
func (e *setupEnv) kickstart(ctx context.Context, target string) error {
	if out, err := e.run(ctx, nil, "launchctl", "kickstart", "-k", target); err != nil {
		return e.fail("service", "重启 launchd 服务 "+target+" 失败："+setupFirstLine(string(out)), "运行 launchctl print "+target+" 看原因")
	}
	return nil
}

func (e *setupEnv) launchdService(ctx context.Context, cfg *Config, bin string) error {
	domain := "gui/" + strconv.Itoa(e.uid)
	sum, binChanged := binaryChanged(bin)
	if label := e.otherHubAgent(bin); label != "" {
		detail := "用已有的 launchd 服务 " + label
		if _, err := e.run(ctx, nil, "launchctl", "print", domain+"/"+label); err != nil {
			if out, err := e.run(ctx, nil, "launchctl", "bootstrap", domain, e.agentPlist(label)); err != nil {
				return e.fail("service", "启动已有的 launchd 服务 "+label+" 失败："+setupFirstLine(string(out)), "运行 launchctl print "+domain+"/"+label+" 看原因")
			}
		} else if binChanged || e.restartHub {
			if err := e.kickstart(ctx, domain+"/"+label); err != nil {
				return err
			}
			detail += "；" + e.restartReason(binChanged) + "，重启了它"
		}
		e.hubRestart = e.relaunch(domain + "/" + label)
		noteBinary(bin, sum)
		e.report("service", true, detail, "")
		return nil
	}
	file := e.plistPath()
	want := hubPlist(e.home, e.root, bin)
	before, beforeErr := os.ReadFile(file)
	_, loadedErr := e.run(ctx, nil, "launchctl", "print", domain+"/"+hubLaunchdLabel)
	e.hubRestart = e.relaunch(domain + "/" + hubLaunchdLabel)
	if string(before) == want && loadedErr == nil {
		detail := "常驻服务已经装好、在运行，没动它（launchd " + hubLaunchdLabel + "）"
		if binChanged || e.restartHub {
			if err := e.kickstart(ctx, domain+"/"+hubLaunchdLabel); err != nil {
				return err
			}
			detail = "常驻服务已经装好；" + e.restartReason(binChanged) + "，重启了它（launchd " + hubLaunchdLabel + "）"
		}
		noteBinary(bin, sum)
		e.report("service", true, detail, "")
		return nil
	}
	if os.IsNotExist(beforeErr) && e.hubAnswers(ctx, cfg) {
		// A hub with this token runs already, started some other way (a terminal, an agent setup cannot tell runs
		// it): a second agent could not get the port and would restart forever. Leave it to the user.
		e.hubRestart = nil // not restarted here: it runs with the hub.json it read when it started
		e.report("service", true, "hub 已经在运行，但不是 launchd 服务起的，没装常驻服务",
			"停掉正在运行的 hub 后重跑 personad hub setup，装成登录后自动运行的服务")
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return e.fail("service", "建不了 "+filepath.Dir(file)+"："+err.Error(), "检查这个目录的权限")
	}
	if err := os.WriteFile(file, []byte(want), 0o644); err != nil {
		return e.fail("service", "写不了 "+file+"："+err.Error(), "检查这个目录的权限")
	}
	_, _ = e.run(ctx, nil, "launchctl", "bootout", domain+"/"+hubLaunchdLabel)
	if out, err := e.run(ctx, nil, "launchctl", "bootstrap", domain, file); err != nil {
		return e.fail("service", "launchctl bootstrap 失败："+setupFirstLine(string(out)), "运行 launchctl print "+domain+"/"+hubLaunchdLabel+" 看原因")
	}
	noteBinary(bin, sum)
	e.report("service", true, fmt.Sprintf("装好了常驻服务（launchd %s，登录后自动运行），日志在 %s", hubLaunchdLabel, filepath.Join(e.root, "hub.log")), "")
	return nil
}
