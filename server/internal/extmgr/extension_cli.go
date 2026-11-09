package extmgr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
)

// `personad extension …` on the hub's machine (docs/extensions/extensions.md "Command line"): what installs, upgrades and removes
// extensions. It talks to the running hub over loopback with the master token in hub.json, like `hub admin`.
// `personad ext …` is the same, and so is `personad plugin …` (its name before #1844).

const extensionUsage = `usage: personad extension <command> (also: personad ext, personad plugin)
  list                              the extensions and their state
  show <id>                         one extension: permissions, settings, secrets set or not
  install <source> [--yes]          install (source: a directory, dir:/p, file:/p.tar.gz | .zip,
                                    https://…/p.tar.gz#sha256=<hex>, remote:rpc=/p.sock,http=/q.sock);
                                    shows what it may do and asks first unless --yes
  upgrade <id> <source> [--yes]     a newer version of an installed extension
  rollback <id>                     back to the version before the last upgrade
  attach <id> remote:rpc=/p.sock,http=/q.sock
                                    reach an installed package's program, running on its own at the same version, on
                                    its sockets; the package, settings, secrets and data stay
  detach <id>                       run the attached package's program here again
  enable <id> | disable <id> [--cascade]
                                    --cascade: the extensions that depend on it are switched off too
  sandbox <id> on|off|default       run it in a sandbox or not (default: on unless from a trusted market)
  auth <id> [login|logout]          an extension's sign-in (manifest.auth): its state; login shows the link, code and QR
                                    code here and waits until it is done; logout signs it out
  config <id> key=value …           set settings (a value is JSON when it parses, text otherwise)
  secret <id> <name>                set a secret, read from stdin (never an argument)
  secret <id> <name> --delete
  uninstall <id> [--keep-data] [--cascade] [--members]
                                    --members: a pack's members go with it
  market list                       the extension markets this hub reads
  market add <spec> [--token]       a market: https://example.com/extensions/ (its persona-extensions.json),
                                    a .json URL, github:owner/repo[@ref] or
                                    git:<address>[#branch]; --token reads one from stdin, --token-from=<market id>
                                    uses that registered market's token (never shown)
  market set-spec <id> <spec>       the market moved (a renamed repository): same id, so what was installed from it
                                    stays its own
  market remove <id> | refresh <id>
  market token <id> [--clear]       set the market's token (read from stdin) and read it again; --clear removes it
  search [words]                    the markets' extensions
  install market:<market>/<id>      install (or upgrade) one of a market's extensions
  auto-upgrade on|off [<id>]        market extensions upgrade themselves (all of them, or one)
  upgrades                          newer versions: going in, waiting for your confirmation, skipped
  audit [<id>] [--limit=N]          who installed, switched, signed in, deleted what, and when, newest first
                                    (an extension, a connector, feishu for the Feishu bot; all of them without an id)`

// ExtensionCommand runs `personad extension <args>`.
func ExtensionCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(clitext.Named(extensionUsage + extensionBuildUsage))
	}
	switch args[0] {
	case "new", "test", "pack":
		return extensionBuildCommand(ctx, args, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, clitext.Named(extensionUsage+extensionBuildUsage))
		return nil
	}
	// Before hub.json is read or the hub is called: a mistyped option must not run the command without it.
	if err := checkExtensionArgs(args); err != nil {
		return err
	}
	cfg, err := localHubConfig()
	if err != nil {
		return err
	}
	c := &extensionClient{cfg: cfg}
	flag := func(name string) bool {
		for _, a := range args[1:] {
			if a == name {
				return true
			}
		}
		return false
	}
	var positional []string // as many as extensionCommandArgs allows
	for _, a := range args[1:] {
		if !isExtensionOption(a) {
			positional = append(positional, a)
		}
	}
	switch args[0] {
	case "dev":
		return c.dev(ctx, dirArg(positional), stdout, stderr)
	case "list":
		var out struct {
			Extensions []ExtensionView `json:"extensions"`
			Legacy     []ExtensionView `json:"plugins"` // a hub before #1844
		}
		if err := c.do(ctx, "GET", "/api/v1/extensions", nil, &out); err != nil {
			return err
		}
		out.Extensions = append(out.Extensions, out.Legacy...)
		if len(out.Extensions) == 0 {
			fmt.Fprintln(stdout, "没有扩展")
		}
		for _, p := range out.Extensions {
			line := fmt.Sprintf("%-20s %-10s %-9s %-12s %s", p.ID, p.Version, p.Source, p.Status, p.Name)
			if p.Error != "" {
				line += "（" + p.Error + "）"
			}
			if len(p.NeedsSetup) > 0 {
				line += "；还缺：" + strings.Join(p.NeedsSetup, "、")
			}
			line += attachedLine(p.Attached, p.Error == "")
			fmt.Fprintln(stdout, strings.TrimRight(line, " "))
		}
		return nil
	case "show":
		var d ExtensionDetail
		if err := c.do(ctx, "GET", "/api/v1/extensions/"+url.PathEscape(positional[0])+"/-/", nil, &d); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(d, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return nil
	case "install", "upgrade":
		upgrade, source := "", ""
		if args[0] == "upgrade" {
			upgrade, source = positional[0], positional[1]
		} else {
			source = positional[0]
		}
		var pv extension.Preview
		if rest, ok := strings.CutPrefix(source, "market:"); ok {
			market, id, ok := strings.Cut(rest, "/")
			if !ok {
				return errors.New("写成 market:<市场 id>/<扩展 id>（市场 id 见 personad extension market list）")
			}
			if err := c.do(ctx, "POST", "/api/v1/extensions/-/catalog/preview", map[string]string{"market": market, "extension": id, "plugin": id}, &pv); err != nil {
				return err
			}
			if pv.Upgrade {
				upgrade = pv.Manifest.ID
				if pv.RenameFrom != "" {
					upgrade = pv.RenameFrom
				}
			}
		} else if err := c.do(ctx, "POST", "/api/v1/extensions/-/preview", map[string]string{"source": source, "upgrade": upgrade}, &pv); err != nil {
			return err
		}
		printPreview(stdout, &pv)
		printBundle(stdout, &pv)
		if !flag("--yes") && !confirm(stdin, stdout) {
			return errors.New("没有装")
		}
		var d ExtensionDetail
		body := map[string]any{"preview_id": pv.ID, "permissions_digest": pv.Digest, "bundle": bundleRefs(&pv)}
		path := "/api/v1/extensions/-/install"
		if upgrade != "" {
			path = "/api/v1/extensions/" + url.PathEscape(upgrade) + "/-/upgrade"
		}
		if err := c.do(ctx, "POST", path, body, &d); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %s：%s\n", d.ID, d.Version, d.Status)
		if len(d.NeedsSetup) > 0 {
			fmt.Fprintf(stdout, "还缺：%s（App 的 设置 › 扩展，或 personad extension config / secret）\n", strings.Join(d.NeedsSetup, "、"))
		}
		return nil
	case "market":
		switch positional[0] {
		case "list":
			var out struct {
				Markets []MarketView `json:"markets"`
			}
			if err := c.do(ctx, "GET", "/api/v1/extensions/-/markets", nil, &out); err != nil {
				return err
			}
			if len(out.Markets) == 0 {
				fmt.Fprintln(stdout, "没有登记扩展市场")
			}
			for _, m := range out.Markets {
				line := fmt.Sprintf("%-10s %-24s %3d 个扩展  %-12s %s", m.ID, m.Name, m.Extensions, signatureLabel(m.Signature), m.Spec)
				if m.Official {
					line += "  官方"
				}
				if m.Error != "" {
					line += "（" + m.Error + "）"
				}
				fmt.Fprintln(stdout, line)
			}
			return nil
		case "add":
			body := map[string]string{"spec": positional[1]}
			if flag("--token") {
				tok, err := readSecret(stdin, stdout)
				if err != nil {
					return err
				}
				body["token"] = tok
			}
			for _, a := range args[1:] {
				if from, ok := strings.CutPrefix(a, "--token-from="); ok {
					body["token_from"] = from
				}
			}
			var m MarketView
			if err := c.do(ctx, "POST", "/api/v1/extensions/-/markets", body, &m); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s：%s，%d 个扩展\n", m.ID, m.Name, m.Extensions)
			for _, w := range m.Warnings {
				fmt.Fprintln(stdout, "  警告："+w)
			}
			return nil
		case "set-spec":
			var m MarketView
			if err := c.do(ctx, "PUT", "/api/v1/extensions/-/markets/"+url.PathEscape(positional[1]), map[string]string{"spec": positional[2]}, &m); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s：%s，%d 个扩展（%s）\n", m.ID, m.Name, m.Extensions, m.Spec)
			return nil
		case "token":
			tok := ""
			if !flag("--clear") {
				var err error
				if tok, err = readSecret(stdin, stdout); err != nil {
					return err
				}
				if strings.TrimSpace(tok) == "" {
					return errors.New("token 是空的：要去掉 token 用 --clear")
				}
			}
			var m MarketView
			if err := c.do(ctx, "PUT", "/api/v1/extensions/-/markets/"+url.PathEscape(positional[1])+"/token", map[string]string{"token": tok}, &m); err != nil {
				return err
			}
			state := "已去掉 token"
			if m.HasToken {
				state = "已设 token"
			}
			line := fmt.Sprintf("%s：%s，%s，%d 个扩展", m.ID, m.Name, state, m.Extensions)
			if m.Error != "" {
				line += "（" + m.Error + "）"
			}
			fmt.Fprintln(stdout, line)
			return nil
		case "remove", "refresh":
			if positional[0] == "remove" {
				return c.do(ctx, "DELETE", "/api/v1/extensions/-/markets/"+url.PathEscape(positional[1]), nil, nil)
			}
			var m MarketView
			if err := c.do(ctx, "POST", "/api/v1/extensions/-/markets/"+url.PathEscape(positional[1])+"/refresh", nil, &m); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s：%s，%d 个扩展\n", m.ID, m.Name, m.Extensions)
			return nil
		}
		return errors.New(clitext.Named(extensionUsage))
	case "search":
		var out struct {
			Extensions []CatalogItem `json:"extensions"`
			Legacy     []CatalogItem `json:"plugins"` // a hub before #1844
		}
		if err := c.do(ctx, "GET", "/api/v1/extensions/-/catalog?q="+url.QueryEscape(strings.Join(positional, " ")), nil, &out); err != nil {
			return err
		}
		out.Extensions = append(out.Extensions, out.Legacy...)
		if len(out.Extensions) == 0 {
			fmt.Fprintln(stdout, "没有找到")
		}
		for _, p := range out.Extensions {
			state := ""
			switch {
			case p.Upgradable:
				state = "（已装 " + p.Installed + "，可升级）"
			case p.Installed != "":
				state = "（已装 " + p.Installed + "）"
			case p.Problem != "":
				state = "（" + p.Problem + "）"
			}
			fmt.Fprintf(stdout, "market:%s/%s  %s %s%s\n    %s\n", p.Market, p.ID, p.Name, p.Version, state, p.Description)
		}
		return nil
	case "auto-upgrade":
		if positional[0] != "on" && positional[0] != "off" {
			return errors.New(clitext.Named(extensionUsage))
		}
		on := positional[0] == "on"
		if len(positional) > 1 {
			var d ExtensionDetail
			if err := c.do(ctx, "PUT", "/api/v1/extensions/"+url.PathEscape(positional[1])+"/-/auto-upgrade", map[string]bool{"auto": on}, &d); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s：自动升级%s\n", d.ID, map[bool]string{true: "开", false: "关"}[on])
			return nil
		}
		var v upgradesView
		if err := c.do(ctx, "PUT", "/api/v1/extensions/-/upgrades", map[string]bool{"auto": on}, &v); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "扩展自动升级：%s\n", map[bool]string{true: "开", false: "关"}[v.Auto])
		if v.Auto && len(v.Off) > 0 {
			fmt.Fprintf(stdout, "单独关掉的：%s\n", strings.Join(v.Off, "、"))
		}
		return nil
	case "audit": // docs/extensions/extensions.md, after the install steps
		return auditCommand(ctx, c, args[1:], stdout)
	case "upgrades":
		var v upgradesView
		if err := c.do(ctx, "GET", "/api/v1/extensions/-/upgrades", nil, &v); err != nil {
			return err
		}
		printUpgrades(stdout, v)
		return nil
	case "rollback":
		var d ExtensionDetail
		if err := c.do(ctx, "POST", "/api/v1/extensions/"+url.PathEscape(positional[0])+"/-/rollback", map[string]string{}, &d); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s 回到了 %s\n", d.ID, d.Version)
		return nil
	case "attach", "detach":
		body := map[string]string{}
		if args[0] == "attach" {
			body["source"] = positional[1]
		}
		var d ExtensionDetail
		if err := c.do(ctx, "POST", "/api/v1/extensions/"+url.PathEscape(positional[0])+"/-/"+args[0], body, &d); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s：%s\n", d.ID, d.Status)
		return nil
	case "enable", "disable":
		var d ExtensionDetail
		body := map[string]bool{"enabled": args[0] == "enable", "cascade": flag("--cascade")}
		if err := c.do(ctx, "PUT", "/api/v1/extensions/"+url.PathEscape(positional[0])+"/-/enabled", body, &d); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s：%s\n", d.ID, d.Status)
		return nil
	case "auth": // docs/extensions/extensions.md "Sign-in (auth)"
		return extensionAuthCommand(ctx, c, positional, stdin, stdout)
	case "sandbox": // docs/extensions/extensions.md "Sandbox"
		var on *bool
		switch positional[1] {
		case "on", "off":
			v := positional[1] == "on"
			on = &v
		case "default":
		default:
			return errors.New("personad extension sandbox <id> on|off|default")
		}
		var d ExtensionDetail
		if err := c.do(ctx, "PUT", "/api/v1/extensions/"+url.PathEscape(positional[0])+"/-/sandbox", map[string]*bool{"enabled": on}, &d); err != nil {
			return err
		}
		switch sb := d.Sandbox; {
		case sb == nil:
			fmt.Fprintf(stdout, "%s：没有沙箱\n", d.ID)
		case sb.Enabled && sb.Active:
			fmt.Fprintf(stdout, "%s：在沙箱里运行，只能经 hub 的代理连 permissions.network 里的主机\n", d.ID)
		case sb.Enabled:
			fmt.Fprintf(stdout, "%s：%s；只把它指向 hub 的代理\n", d.ID, sb.Why)
		default:
			fmt.Fprintf(stdout, "%s：不在沙箱里运行\n", d.ID)
		}
		return nil
	case "config":
		id := positional[0]
		var d ExtensionDetail
		if err := c.do(ctx, "GET", "/api/v1/extensions/"+url.PathEscape(id)+"/-/", nil, &d); err != nil {
			return err
		}
		cfg := map[string]json.RawMessage{}
		_ = json.Unmarshal(d.Config, &cfg)
		for _, kv := range positional[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("%q is not key=value", kv)
			}
			if v == "" {
				delete(cfg, k)
				continue
			}
			if json.Valid([]byte(v)) {
				cfg[k] = json.RawMessage(v)
			} else {
				cfg[k], _ = json.Marshal(v)
			}
		}
		if err := c.do(ctx, "PUT", "/api/v1/extensions/"+url.PathEscape(id)+"/-/config", map[string]any{"config": cfg}, &d); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s：%s\n", d.ID, d.Status)
		return nil
	case "secret":
		path := "/api/v1/extensions/" + url.PathEscape(positional[0]) + "/-/secrets/" + url.PathEscape(positional[1])
		var d ExtensionDetail
		if flag("--delete") {
			if err := c.do(ctx, "DELETE", path, nil, &d); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s：删掉了 %s\n", d.ID, positional[1])
			return nil
		}
		value, err := readSecret(stdin, stdout)
		if err != nil {
			return err
		}
		if strings.TrimSpace(value) == "" { // the hub would refuse it with a size error
			return errors.New("the secret is empty: to remove it, use --delete")
		}
		if err := c.do(ctx, "PUT", path, map[string]string{"value": value}, &d); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s：%s\n", d.ID, d.Status)
		return nil
	case "uninstall":
		q := url.Values{}
		for f, k := range map[string]string{"--keep-data": "keep_data", "--cascade": "cascade", "--members": "members"} {
			if flag(f) {
				q.Set(k, "1")
			}
		}
		path := "/api/v1/extensions/" + url.PathEscape(positional[0]) + "/-/"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		var out struct {
			Warnings []string `json:"warnings"`
		}
		if err := c.do(ctx, "DELETE", path, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "卸载了 %s\n", positional[0])
		for _, warning := range out.Warnings {
			fmt.Fprintln(stderr, "警告："+warning)
		}
		return nil
	}
	if done, err := extensionModelCommand(ctx, c, args[0], positional, stdout); done {
		return err
	}
	return unknownExtensionCommand(args[0])
}

func unknownExtensionCommand(name string) error {
	return fmt.Errorf("unknown extension command %q\n%s\n%s", name, clitext.Named(extensionUsage), extensionModelUsage)
}

// extensionArgs is what a hub command takes (extensionUsage, docs/extensions/extensions.md "Command line").
type extensionArgs struct {
	min, max  int      // positional arguments (a market command's include its own word); max -1: any number
	options   []string // exact words; one ending in "=" takes a value after it (--token-from=<id>)
	exclusive bool     // at most one of the options
	usage     string   // printed with a mistake; extensionUsage when empty
}

// extensionCommandArgs are the hub commands and their arguments; `audit` parses its own, and new / test / pack need
// no hub.
var extensionCommandArgs = map[string]extensionArgs{
	"list":            {},
	"upgrades":        {},
	"show":            {min: 1, max: 1},
	"rollback":        {min: 1, max: 1},
	"attach":          {min: 2, max: 2},
	"detach":          {min: 1, max: 1},
	"install":         {min: 1, max: 1, options: []string{"--yes"}},
	"upgrade":         {min: 2, max: 2, options: []string{"--yes"}},
	"enable":          {min: 1, max: 1, options: []string{"--cascade"}},
	"disable":         {min: 1, max: 1, options: []string{"--cascade"}},
	"sandbox":         {min: 2, max: 2},
	"auth":            {min: 1, max: 2},
	"config":          {min: 2, max: -1},
	"secret":          {min: 2, max: 2, options: []string{"--delete"}},
	"uninstall":       {min: 1, max: 1, options: []string{"--keep-data", "--cascade", "--members"}},
	"search":          {max: -1},
	"auto-upgrade":    {min: 1, max: 2},
	"dev":             {max: 1},
	"contributions":   {min: 1, max: 1, usage: extensionModelUsage},
	"contribution":    {min: 3, max: 3, usage: extensionModelUsage},
	"market list":     {min: 1, max: 1},
	"market add":      {min: 2, max: 2, options: []string{"--token", "--token-from="}, exclusive: true},
	"market set-spec": {min: 3, max: 3},
	"market remove":   {min: 2, max: 2},
	"market refresh":  {min: 2, max: 2},
	"market token":    {min: 2, max: 2, options: []string{"--clear"}},
}

// isExtensionOption is a word that is not a positional argument.
func isExtensionOption(a string) bool { return len(a) > 1 && strings.HasPrefix(a, "-") }

// checkExtensionArgs refuses a hub command line extensionCommandArgs does not allow: an unknown command, an option
// the command does not take, a missing or an extra argument.
func checkExtensionArgs(args []string) error {
	if args[0] == "audit" {
		return nil
	}
	var positional, options []string
	for _, a := range args[1:] {
		if isExtensionOption(a) {
			options = append(options, a)
		} else {
			positional = append(positional, a)
		}
	}
	name := args[0]
	if name == "market" {
		if len(positional) == 0 {
			return errors.New(clitext.Named(extensionUsage))
		}
		name += " " + positional[0]
	}
	spec, ok := extensionCommandArgs[name]
	if !ok {
		return unknownExtensionCommand(name)
	}
	usage := clitext.Named(extensionUsage)
	if spec.usage != "" {
		usage = spec.usage
	}
	mistake := func(format string, a ...any) error {
		return fmt.Errorf("%s: %s\n%s", clitext.Named("personad extension "+name), fmt.Sprintf(format, a...), usage)
	}
	for _, o := range options {
		known := func(want string) bool { return o == want || strings.HasSuffix(want, "=") && strings.HasPrefix(o, want) }
		switch {
		case strings.HasSuffix(o, "=") && slices.Contains(spec.options, o): // --token-from= with nothing after it
			return mistake("%s needs a value", o)
		case !slices.ContainsFunc(spec.options, known):
			return mistake("unknown option %q", o)
		case spec.exclusive && o != options[0]:
			return mistake("%q and %q do not go together", options[0], o)
		}
	}
	if len(positional) < spec.min {
		return errors.New(usage)
	}
	if spec.max >= 0 && len(positional) > spec.max {
		return mistake("unexpected argument %q", positional[spec.max])
	}
	return nil
}

func printPreview(w io.Writer, pv *extension.Preview) {
	m := pv.Manifest
	if pv.RenameFrom != "" {
		fmt.Fprintln(w, i18n.T(i18n.Default, "extension-identity.confirmation", i18n.Args{"from": pv.RenameFrom, "to": m.ID}))
	}
	if pv.Upgrade {
		fmt.Fprintf(w, "升级 %s：%s → %s\n", m.ID, pv.From, m.Version)
	} else {
		fmt.Fprintf(w, "安装 %s %s（%s）\n", m.ID, m.Version, m.Name)
	}
	if m.Publisher != "" {
		fmt.Fprintf(w, "发布者：%s\n", m.Publisher)
	}
	if m.Description != "" {
		fmt.Fprintln(w, m.Description)
	}
	for point, ids := range m.Contributions() {
		fmt.Fprintf(w, "提供 %s：%s\n", point, strings.Join(ids, "、"))
	}
	fmt.Fprintln(w, "它可以：")
	if len(pv.Permissions) == 0 {
		fmt.Fprintln(w, "  （不运行程序，只有声明）")
	}
	for _, l := range pv.Permissions {
		fmt.Fprintf(w, "  · %s：%s\n", l.Title, l.Detail)
	}
	if pv.MorePermissions {
		fmt.Fprintln(w, "新版本要的权限比现在多。")
	}
	for _, warn := range pv.Warnings {
		fmt.Fprintf(w, "注意：%s\n", warn)
	}
}

func confirm(stdin io.Reader, stdout io.Writer) bool {
	fmt.Fprint(stdout, "继续吗？[y/N] ")
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// attachedLine is what `extension list` adds for an attached extension: online or offline and since when, why it is
// offline when the line does not say it yet, and the version the program says it is when that was not approved.
func attachedLine(a *ExtensionAttached, withError bool) string {
	if a == nil {
		return ""
	}
	line := "；独立运行，离线"
	if a.Online {
		line = "；独立运行，在线"
	}
	if !a.Since.IsZero() {
		line += "（自 " + a.Since.Format("01-02 15:04:05") + "）"
	}
	if withError && a.Error != "" {
		line += "：" + a.Error
	}
	if a.Version != "" {
		line += "；程序自报版本 " + a.Version
	}
	return line
}

// readSecret reads a secret from the terminal without echo, or the first line of a pipe.
func readSecret(stdin io.Reader, stdout io.Writer) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(stdout, "值（不显示）：")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stdout)
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// localHubConfig is hub.json on this machine (the token, the port).
func localHubConfig() (*Config, error) {
	dir := identity.HomeDir()
	cfg := &Config{Listen: ":18660", dir: dir}
	b, err := os.ReadFile(dir + "/hub.json")
	if err != nil {
		return nil, fmt.Errorf("这台机器上没有 hub 的配置（%s/hub.json）：扩展只能在 hub 机器上管理", dir)
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	if v := identity.Getenv("PERSONA_LISTEN"); v != "" {
		cfg.Listen = v
	}
	return cfg, nil
}

type extensionClient struct{ cfg *Config }

// do calls the hub on this machine over loopback with the master token.
func (c *extensionClient) do(ctx context.Context, method, path string, body, out any) error {
	err := c.call(ctx, method, path, body, out)
	if legacy, ok := strings.CutPrefix(path, "/api/v1/extensions"); ok && errors.Is(err, errNoRoute) {
		// A hub before #1844 (not restarted since this binary came): the old paths.
		err = c.call(ctx, method, "/api/v1/plugins"+legacy, body, out)
	}
	if errors.Is(err, errNoRoute) {
		return errors.New("hub 没有这个接口：hub 比这个命令旧，先重启 hub")
	}
	return err
}

// errNoRoute is a 404 that is not the hub's JSON answer: the hub has no such route.
var errNoRoute = errors.New("no such route")

func (c *extensionClient) call(ctx context.Context, method, path string, body, out any) error {
	_, port, err := net.SplitHostPort(c.cfg.Listen)
	if n, perr := strconv.Atoi(port); err != nil || perr != nil || n <= 0 || n > 65535 {
		return errors.New("hub.json listen has no valid port")
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+net.JoinHostPort("127.0.0.1", port)+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	// CLI output stays in the default language even when an app changes the user's language.
	req.Header.Set("Accept-Language", string(i18n.Default))
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 10 * time.Minute, Transport: transport} // a download may take a while
	res, err := client.Do(req)
	if err != nil {
		var ne *net.OpError
		if errors.As(err, &ne) {
			return errors.New("hub 没在运行：先启动 hub")
		}
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode == http.StatusNotFound && !json.Valid(data) {
		return errNoRoute
	}
	if res.StatusCode != http.StatusOK {
		why := apierr.Describe(data) // with the cause the hub moved to detail (#5728)
		if why == "" {
			why = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("%s（HTTP %d）", why, res.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// printUpgrades lists the newer versions by what happens to them.
func printUpgrades(w io.Writer, v upgradesView) {
	fmt.Fprintf(w, "扩展自动升级：%s\n", map[bool]string{true: "开", false: "关"}[v.Auto])
	groups := []struct {
		title  string
		states []string
	}{
		{"可升级", []string{upgradeAvailable, upgradeWaitingIdle, upgradeOff, upgradeFailed, upgradeIncompatible}},
		{"等你确认", []string{upgradeNeedsConsent}},
		{"已跳过", []string{upgradeSkipped}},
	}
	what := map[string]string{upgradeAvailable: "下次检查时装", upgradeWaitingIdle: "等空闲", upgradeOff: "自动升级关着",
		upgradeFailed: "没装上", upgradeIncompatible: "装不了", upgradeSkipped: "回滚过，不再自动装"}
	any := false
	for _, g := range groups {
		var lines []string
		for _, up := range v.Upgrades {
			if !slices.Contains(g.states, up.State) {
				continue
			}
			line := fmt.Sprintf("  %-20s %s → %s", up.ID, up.From, up.To)
			switch {
			case up.State == upgradeNeedsConsent:
				line += "  " + morePermissionsText(i18n.Default, up.AddedPermissions) + "（App 的 设置 › 扩展，或 personad extension install market:" + up.Market + "/" + up.ID + "）"
			case up.Detail != "":
				line += "  " + what[up.State] + "：" + up.Detail
			default:
				line += "  " + what[up.State]
			}
			lines = append(lines, line)
		}
		if len(lines) > 0 {
			any = true
			fmt.Fprintln(w, g.title+"：")
			for _, l := range lines {
				fmt.Fprintln(w, l)
			}
		}
	}
	if !any {
		fmt.Fprintln(w, "从扩展市场装的扩展都是最新的")
	}
}

// signatureLabel is a market's signature in a line of the command line.
func signatureLabel(s MarketSignatureView) string {
	switch s.State {
	case extension.SigSigned:
		return "已签名 " + s.KeyID
	case extension.SigInvalid:
		return "签名不对"
	case extension.SigKeyChanged:
		return "换了 key " + s.KeyID
	}
	return "未签名"
}
