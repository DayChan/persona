package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/link"
)

// On Linux persona-link runs as a systemd user service (docs/operations/link.md "Linux (headless)"): `install` writes the unit,
// enables and (re)starts it and waits for the link; `uninstall` takes it away. The service runs in the user's own
// systemd instance, so it needs no root, and runs with nobody logged in once linger is on.

const unitName = "persona-link.service"

// noUserInstance starts the reason when systemd is there but the user's own instance cannot be reached.
const noUserInstance = "连不上你的 systemd 用户实例"

// unitPath is where systemd looks for the user's own units.
func (e *env) unitPath() string {
	base := e.getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(e.home, ".config")
	}
	dir := filepath.Join(base, "systemd", "user")
	return existingPath(filepath.Join(dir, unitName), filepath.Join(dir, legacyLinkUnit))
}

// unitFile is the service. No network-online.target: a user instance has none to wait for, and persona-link retries
// by itself until the hub answers. Its status lines are for the menu bar app, so not for the journal; the log goes to
// its own file, which `persona-link logs` reads whether or not the journal keeps user logs.
func unitFile(bin, logPath string) string {
	return `# Written by persona-link install; persona-link uninstall removes it (docs/operations/link.md).
[Unit]
Description=Persona link: keeps this computer reachable from the Persona hub

[Service]
ExecStart=` + systemdQuote(bin) + ` run --log ` + systemdQuote(logPath) + `
` + stateEnvironment(filepath.Dir(logPath)) + `Restart=always
RestartSec=5
StandardOutput=null

[Install]
WantedBy=default.target
`
}

// systemdQuote makes s one word of a unit's command line: quoted, with the specifier and variable characters escaped.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(s) + `"`
}

// Environment= expands systemd specifiers, but never ExecStart's dollar variables.
func systemdEnvironmentQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`)
	return `"` + r.Replace(s) + `"`
}

func stateEnvironment(root string) string {
	return "Environment=" + systemdEnvironmentQuote("PERSONA_HOME="+root) + "\n"
}

func (e *env) systemctl(args ...string) (string, error) {
	return e.command("systemctl", append([]string{"--user"}, args...)...)
}

// systemdMissing says why there is no systemd user instance to run persona-link in; "" when there is one.
func (e *env) systemdMissing() string {
	if e.goos != "linux" {
		return "不是 Linux"
	}
	if _, err := e.lookPath("systemctl"); err != nil {
		return "没有 systemctl"
	}
	if !e.exists("/run/systemd/system") {
		return "系统不是由 systemd 启动的（容器里常见）"
	}
	if out, err := e.systemctl("show-environment"); err != nil {
		why := firstLine(out)
		if why == "" {
			why = err.Error()
		}
		return noUserInstance + "：" + why
	}
	return ""
}

// lingering: the user's systemd instance runs from boot, with nobody logged in (loginctl enable-linger).
func (e *env) lingering() bool {
	return e.user != "" && e.exists("/var/lib/systemd/linger/"+e.user)
}

func install(e *env, args []string) error {
	var pairLink, machine, listen, personad string
	var addrs []string
	nodeBinAny := false // --node-bin: either program, asked which (nodeInstall)
	pairing, passive, withNode, withTray := false, false, true, true
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--pair": // the link, or - or nothing for stdin
			pairing, pairLink = true, "-"
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				pairLink = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--pair="):
			pairing, pairLink = true, strings.TrimPrefix(a, "--pair=")
		case a == "--no-node":
			withNode = false
		case a == "--no-tray":
			withTray = false
		default:
			if v, next, ok := optionalValue(args, i, "--listen"); ok {
				passive, listen, i = true, v, next
				continue
			}
			var v string
			var next int
			var ok bool
			var err error
			for _, name := range []string{"--machine", "--addr", "--personad", "--node-bin"} {
				if v, next, ok, err = flagValue(args, i, name); ok || err != nil {
					switch {
					case err != nil:
					case name == "--machine":
						machine = v
					case name == "--addr":
						addrs = append(addrs, v)
					default:
						personad, nodeBinAny = v, name == "--node-bin"
					}
					break
				}
			}
			switch {
			case err != nil:
				return err
			case !ok:
				return errUsage
			}
			i = next
		}
	}
	if err := e.installationConflict(false); err != nil {
		return err
	}
	if withNode {
		if err := e.installationConflict(true); err != nil {
			return err
		}
	}
	if pairing && (passive || len(addrs) > 0) {
		return errors.New("--pair（我去连 hub）和 --listen（让 hub 来连）只能选一个")
	}
	switch e.goos {
	case "linux", "windows":
	case "darwin":
		return errors.New("Mac 上不用 install：装菜单栏 App「Persona Link」（scripts/app/mac-link.sh install），它带着 persona-link 和 persona-node、" +
			"管着它们，登录时启动在它的菜单里勾")
	default:
		return fmt.Errorf("%s 上 install 还装不了后台服务：先在前台运行 persona-link run，交给这台的进程管理", e.goos)
	}
	if e.goos == "windows" && nodeBinAny && personad != "" && e.programOf(personad) != programPersonad {
		return fmt.Errorf("%s is not personad: on Windows the node runs personad (docs/operations/windows.md); give personad with --personad", personad)
	}
	if pairing {
		c, err := pair(e, pairLink, machine)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "配对好了：这台是 hub 上的「%s」，凭据存在 %s。\n", c.Machine, e.configPath())
	}
	var invite *link.PairInvite
	if passive || len(addrs) > 0 {
		c, err := link.SetMode(e.configPath(), link.ModePassive, listen)
		if err != nil {
			return err
		}
		if c.Passive.Paired() && len(addrs) == 0 {
			fmt.Fprintf(e.stdout, "被动模式（让 hub 来连）：已经和 hub%s配对过，监听 %s。要换 hub：persona-link pair --listen。\n",
				passiveHubText(c.Passive), c.Passive.ListenAddr())
		} else if invite, err = link.PairListen(e.configPath(), "", addrs, e.now()); errors.Is(err, link.ErrNoAddress) {
			// The service still goes in; the link waits for an address the hub can use.
			fmt.Fprintf(e.stdout, "%v；装好以后：persona-link pair --listen --addr <hub 连这台用的地址>\n", err)
		} else if err != nil {
			return err
		}
	}
	if e.goos == "windows" {
		return installTask(e, withNode, withTray, personad, invite) // winservice.go
	}
	bin, copied, err := e.installBinary()
	if err != nil {
		return fmt.Errorf("放不进 %s：%w", e.binPath(), err)
	}
	if copied {
		fmt.Fprintf(e.stdout, "装好了 %s。\n", bin)
	}
	if why := e.systemdMissing(); why != "" {
		noSystemd(e, why, bin)
		if withNode {
			fmt.Fprintf(e.stdout, "常驻 node 同理：nohup %s node --attach >> %s 2>&1 &\n", shellQuote(e.nodeBin()), shellQuote(e.nodeLog()))
			// nodeupdate.Options: what restarts the node on exit has to say so, or the hub cannot update it.
			fmt.Fprintln(e.stdout, "  交给退出后会重启它的进程管理（容器的入口脚本、supervisord、runit、s6）时写成 node --attach --supervised，hub 才能远程更新它；nohup、cron 不会重启它，别加。")
		}
		printInvite(e, invite)
		return exitCode(1)
	}
	if withNode {
		if err := lockedNodeCommand(e, true, func() error { return nodeInstall(e, personad, nodeBinAny) }); err != nil {
			fmt.Fprintf(e.stdout, "常驻 node 没装好：%v\n", err)
		}
	}
	var oldPID int
	if st, err := link.ReadStatus(e.configPath()); err == nil && link.Running(e.configPath()) {
		oldPID = st.PID
	}
	unit := e.unitPath()
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unit, []byte(unitFile(bin, e.logPath())), 0o644); err != nil {
		return err
	}
	started := e.now()
	for _, step := range [][]string{{"daemon-reload"}, {"enable", e.linkUnitName()}, {"restart", e.linkUnitName()}} {
		if out, err := e.systemctl(step...); err != nil {
			return fmt.Errorf("systemctl --user %s 失败：%s", strings.Join(step, " "), orErr(out, err))
		}
	}
	fmt.Fprintf(e.stdout, "装好了 systemd 用户服务 %s（%s），已启动，以后跟着你的 systemd 用户实例启动。\n", e.linkUnitName(), unit)
	if e.uid == 0 {
		fmt.Fprintln(e.stdout, "注意：用 root 装的，它以 root 的身份跑、用 /root/.persona；常驻 node 也要在 root 下跑。")
	}
	if !e.lingering() {
		fmt.Fprintf(e.stdout, "注意：没开 linger，退出登录后服务就停、开机也不会自己起来。让它一直跑：sudo loginctl enable-linger %s\n", e.userOr("$USER"))
	}
	return waitStarted(e, started, oldPID, invite)
}

// waitStarted is install's end once the service is (re)started: wait for the link (passive mode: for it to listen,
// then print the pairing link for the hub), or say how to pair when it is not paired yet.
func waitStarted(e *env, started time.Time, oldPID int, invite *link.PairInvite) error {
	if c, _ := link.Read(e.configPath()); c.CurrentMode() == link.ModePassive {
		err := waitConnected(e, started, oldPID)
		printInvite(e, invite)
		fmt.Fprintln(e.stdout, firewallHint(e.goos, c.Passive.ListenAddr()))
		return err
	}
	if _, err := link.Load(e.configPath()); err != nil {
		fmt.Fprintln(e.stdout, "还没配对：persona-link pair '<personad hub pair --machine <这台的 id> 给的链接>'（我去连 hub），"+
			"或者 persona-link pair --listen（让 hub 来连）；服务几秒内就会用上，不用重启。")
		return nil
	}
	return waitConnected(e, started, oldPID)
}

// printInvite shows the pairing link of passive mode, for the hub's admin page.
func printInvite(e *env, inv *link.PairInvite) {
	if inv == nil {
		return
	}
	fmt.Fprintf(e.stdout, "把这条链接粘贴到 hub 管理界面「电脑 › 添加电脑 › hub 来连」（%s内有效，只能用一次；地址 %s，hub 连不到时在那里改）：\n%s\n",
		duration(link.PairCodeLife), strings.Join(inv.Addrs, "、"), inv.Link)
	if inv.LegacyLink != "" {
		fmt.Fprintln(e.stdout, inv.LegacyLink)
	}
}

func (e *env) userOr(fallback string) string {
	if e.user != "" {
		return e.user
	}
	return fallback
}

func orErr(out string, err error) string {
	if s := firstLine(out); s != "" {
		return s
	}
	return err.Error()
}

// noSystemd tells what to do instead where there is no systemd user instance: a container, some NAS systems.
func noSystemd(e *env, why, bin string) {
	runLine := shellQuote(bin) + " run --log " + shellQuote(e.logPath())
	fmt.Fprintf(e.stdout, `没装成后台服务：这台没有可用的 systemd 用户服务（%s）。换一种办法让 persona-link 一直跑：
  · 现在就在后台跑起来：
      nohup %s >/dev/null 2>&1 &
  · 开机自动跑（cron）：
      (crontab -l 2>/dev/null; echo '@reboot %s >/dev/null 2>&1') | crontab -
  · 群晖等 NAS：任务计划里加一个「开机」触发的用户脚本，内容是上面 nohup 那一行（用这个用户跑，不要 root）。
  · 容器：让 %s 当主进程，重启策略 unless-stopped，把 ~/.persona 挂成卷（凭据在里面）；常驻 node 要在同一个容器里。
  · supervisord、runit、s6、OpenRC 这类进程管理：让它前台跑上面那条命令，退出了就重启。
跑起来以后：persona-link status 看状态，persona-link logs -f 看日志。
`, why, runLine, runLine, runLine)
	if strings.HasPrefix(why, noUserInstance) {
		fmt.Fprintf(e.stdout, "如果这台其实有 systemd：用 ssh 直接登录这个用户再装（su / sudo -u 进来的会话找不到用户实例），"+
			"或者先 sudo loginctl enable-linger %s 再试。\n", e.userOr("$USER"))
	}
}

// shellQuote quotes a path for sh when it needs it.
func shellQuote(s string) string {
	if strings.ContainsAny(s, " \t'\"$`\\!*?[]{}()<>|&;#~") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

// waitConnected waits up to e.wait for the service just (re)started to report the link up and the node checked, and
// says how it went. The old process's last words (oldPID, "stopped") do not count.
func waitConnected(e *env, since time.Time, oldPID int) error {
	c, _ := link.Read(e.configPath())
	passive := c.CurrentMode() == link.ModePassive
	if passive {
		fmt.Fprintln(e.stdout, "等它开始监听…")
	} else {
		fmt.Fprintln(e.stdout, "等它连上 hub…")
	}
	deadline := since.Add(e.wait)
	var st *link.SavedStatus
	for {
		if s, err := link.ReadStatus(e.configPath()); err == nil && !s.Updated.Before(since) && s.PID != oldPID && s.State != "stopped" &&
			(s.Mode == link.ModePassive) == passive {
			st = s
		}
		if st != nil && passive && st.State != "" {
			break // listening, waiting for a pairing, a stream already open, or unable to listen: each says enough
		}
		if st != nil && (st.State == "refused" || st.State == "unpaired" || (st.State == "connected" && st.Node != "unknown")) {
			break
		}
		if !e.now().Before(deadline) {
			break
		}
		e.sleep(200 * time.Millisecond)
	}
	switch {
	case st == nil:
		fmt.Fprintf(e.stdout, "%s 内服务没报告状态：persona-link status、persona-link logs 看看。\n", e.wait)
		return exitCode(1)
	case passive && st.State == "waiting":
		fmt.Fprintf(e.stdout, "还没开始监听：%s\n服务会一直重试；persona-link status 看进展。\n", st.Error)
		return exitCode(1)
	case passive:
		line := "在监听 " + st.Listen
		switch st.State {
		case "connected":
			line += "，hub 已经连进来了（来自 " + st.Address + "）。"
		case "listening":
			line += "，等 hub" + hubName(st.HubName) + "连进来。"
		default:
			line += "，等 hub 来配对。"
		}
		fmt.Fprintln(e.stdout, line)
		fmt.Fprintln(e.stdout, "看状态：persona-link status；看日志：persona-link logs -f")
		return nil
	case st.State == "connected":
		line := "连上了：" + hubText(st.HubName, st.MeshHubs, "", true) + "，" + addressText(st.Kind, st.Address)
		switch st.Node {
		case "online":
			line += "；本机 node 在线。"
		case "offline":
			line += "；但 hub 连不上本机的 node：" + st.NodeError + nodeHint(st.NodeError)
		default:
			line += "；hub 正在连本机的 node。"
		}
		fmt.Fprintln(e.stdout, line)
		fmt.Fprintln(e.stdout, "看状态：persona-link status；看日志：persona-link logs -f")
		return nil
	case st.State == "unpaired":
		fmt.Fprintln(e.stdout, "还没配对：persona-link pair '<链接>'。")
		return nil
	default:
		fmt.Fprintf(e.stdout, "还没连上：%s\n服务会一直重试；persona-link status 看进展，persona-link logs -f 看日志。\n", stateText(&st.Status, e.now()))
		for _, part := range errorParts(st.Error) {
			fmt.Fprintln(e.stdout, "  · "+part)
		}
		return exitCode(1)
	}
}

func uninstall(e *env, args []string) error {
	purge := false
	for _, a := range args {
		if a != "--purge" {
			return errUsage
		}
		purge = true
	}
	unit := e.unitPath()
	switch {
	case e.goos == "windows":
		uninstallTask(e) // winservice.go
	case e.goos != "linux":
	case fileExists(unit):
		if why := e.systemdMissing(); why == "" {
			if out, err := e.systemctl("disable", "--now", e.linkUnitName()); err != nil {
				fmt.Fprintf(e.stdout, "停服务时出错：%s\n", orErr(out, err))
			}
		} else {
			fmt.Fprintf(e.stdout, "停不了服务（%s），只删了它的文件。\n", why)
		}
		if err := os.Remove(unit); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_, _ = e.systemctl("daemon-reload")
		fmt.Fprintf(e.stdout, "停掉并删掉了 systemd 用户服务 %s。\n", e.linkUnitName())
	default:
		fmt.Fprintf(e.stdout, "没装 systemd 用户服务（%s 不存在）。\n", unit)
	}
	if !purge {
		fmt.Fprintf(e.stdout, "凭据还在 %s。这台不再连 hub 了：persona-link uninstall --purge，再在 App 的「设置 › 设备」里撤销它。\n", e.configPath())
		return nil
	}
	if link.Running(e.configPath()) {
		return errors.New("还有一个 persona-link 在运行（不是这个服务起的：菜单栏 App，或者你自己跑的）：先停掉它，再 --purge")
	}
	cfg := e.configPath()
	var gone []string
	files := []string{cfg, cfg + ".lock", cfg + ".run", cfg + ".tmp", link.StatusPath(cfg), link.StatusPath(cfg) + ".tmp",
		e.logPath(), e.logPath() + ".1", e.binPath()}
	if e.goos == "windows" { // the supervisor's (winservice.go); a running persona-link.exe cannot remove itself
		files = append(files, e.serviceLog(), e.serviceLog()+".1", filepath.Join(e.personaDir(), "link-service.run"))
	}
	for _, p := range files {
		if err := os.Remove(p); err == nil {
			gone = append(gone, p)
		}
	}
	if dir := link.TLSDir(cfg); fileExists(dir) && os.RemoveAll(dir) == nil {
		gone = append(gone, dir)
	}
	if len(gone) == 0 {
		fmt.Fprintln(e.stdout, "没有要删的文件。")
	} else {
		fmt.Fprintf(e.stdout, "删掉了：%s\n", strings.Join(gone, "、"))
	}
	nodeLine := "常驻 node 的服务留着：persona-link node uninstall。"
	if e.goos == "windows" {
		nodeLine = "常驻 node 随计划任务一起停了，" + e.nodeBin() + " 留着。"
	}
	fmt.Fprintln(e.stdout, "hub 那边这台的凭据在 App 的「设置 › 设备」里撤销（被动模式的在 hub 管理界面的「电脑」里删掉这台）。"+nodeLine)
	return nil
}

// hubName is " name " in full-width corner brackets for a sentence, or nothing.
func hubName(name string) string {
	if name == "" {
		return ""
	}
	return "「" + name + "」"
}
