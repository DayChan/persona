package hub

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
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/clitext"
)

// The mesh from the command line on the hub's machine (docs/server/mesh.md "Command line"): the same peers API as the admin page,
// under /api/v1/admin/cli/, over loopback with the master token (peerCLIRoutes).

// adminCallLimit bounds each of these commands' calls (adminCall): a hub that takes the connection but never answers (a
// stuck hub, or another program on hub.json's port) ends the command with an error instead of holding it forever. A
// variable so tests can shorten it.
var adminCallLimit = 45 * time.Second

// PairPeer prints a code another hub pairs with this one by (`personad hub pair --peer`): the code alone on stdout, what
// to do with it on stderr. ctx is the command's (Ctrl-C ends it).
func PairPeer(ctx context.Context) error {
	cfg, err := runningHubConfig()
	if err != nil {
		return err
	}
	var code peerCode
	if err := cfg.adminCall(ctx, http.MethodPost, "/api/v1/admin/cli/peers/code", nil, &code); err != nil {
		return err
	}
	fmt.Println(code.Code)
	fmt.Fprintf(os.Stderr, "\n这是这台 hub 的互联码，%d 分钟内有效，只能用一次。\n"+
		"在另一台 hub 上：管理界面「互联 › 输入互联码」里粘贴；或者在那台机器上运行 personad hub peer add '<互联码>'。\n",
		max(1, int(time.Until(code.ExpiresAt).Round(time.Minute).Minutes())))
	return nil
}

// PeerCommand is `personad hub peer add <code>|-`, `personad hub peer list`, `personad hub peer remove <hub_id>`,
// `personad hub peer center [<hub_id>] [--confirm]` (set as center; this hub when no id is given). ctx is the command's
// (Ctrl-C ends it); an argument mistake is a clitext.UsageError, found before hub.json is read. Help asked for prints
// the usage on out (exit 0).
func PeerCommand(ctx context.Context, args []string, stdin io.Reader, out io.Writer) error {
	if peerHelpAsked(args) {
		fmt.Fprintln(out, clitext.Named(peerUsage))
		return nil
	}
	if err := checkPeerArgs(args); err != nil {
		return err
	}
	cfg, err := runningHubConfig()
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		code := args[1]
		if code == "-" {
			if code, err = readPeerCode(ctx, stdin); err != nil {
				return err
			}
		}
		var p peerItem
		if err := cfg.adminCall(ctx, http.MethodPost, "/api/v1/admin/cli/peers", map[string]string{"code": code}, &p); err != nil {
			return err
		}
		fmt.Fprintf(out, "已和「%s」（%s）互联：%s\n", p.Name, p.HubID, peerStateText(p))
		return nil
	case "list":
		var res struct {
			Peers      []peerItem      `json:"peers"`
			Discovered []discoveredHub `json:"discovered"`
		}
		if err := cfg.adminCall(ctx, http.MethodGet, "/api/v1/admin/cli/peers", nil, &res); err != nil {
			return err
		}
		var mesh struct {
			Role   string `json:"role"`
			Term   int64  `json:"term"`
			Center struct {
				HubID  string `json:"hub_id"`
				Name   string `json:"name"`
				Online bool   `json:"online"`
			} `json:"center"`
		}
		if err := cfg.adminCall(ctx, http.MethodGet, "/api/v1/admin/cli/mesh", nil, &mesh); err != nil && ctx.Err() != nil {
			return err // stopped (Ctrl-C): not an older hub without the mesh route
		} else if err == nil {
			if mesh.Role == "center" {
				fmt.Fprintf(out, "这台是中心（任期 %d）。\n", mesh.Term)
			} else {
				state := "在线"
				if !mesh.Center.Online {
					state = "连不上"
				}
				fmt.Fprintf(out, "这台是成员，中心是「%s」（%s，%s，任期 %d）。\n", mesh.Center.Name, mesh.Center.HubID, state, mesh.Term)
			}
		}
		if len(res.Peers) == 0 {
			fmt.Fprintln(out, "还没有互联的 hub。")
		}
		for _, p := range res.Peers {
			role := map[string]string{"center": "中心", "member": "成员"}[p.Role]
			fmt.Fprintf(out, "%s  %s  %s  %s  %d 台电脑\n", p.HubID, p.Name, role, peerStateText(p), p.Machines)
		}
		for _, d := range res.Discovered {
			fmt.Fprintf(out, "局域网里发现：%s  %s  %s\n", d.HubID, d.Name, strings.Join(d.Addresses, " "))
		}
		return nil
	case "remove":
		if err := cfg.adminCall(ctx, http.MethodDelete, "/api/v1/admin/cli/peers/"+args[1], nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(out, "已断开并撤销。")
		return nil
	case "center":
		target, confirm := "", false
		for _, a := range args[1:] {
			if a == "--confirm" {
				confirm = true
			} else {
				target = a
			}
		}
		if target == "" {
			var health struct {
				HubID string `json:"hub_id"`
			}
			if err := cfg.adminCall(ctx, http.MethodGet, "/api/v1/health", nil, &health); err != nil {
				return err
			}
			target = health.HubID
		}
		var res struct {
			Term   int64 `json:"term"`
			Center struct {
				Name string `json:"name"`
			} `json:"center"`
		}
		err := cfg.adminCall(ctx, http.MethodPost, "/api/v1/admin/cli/peers/"+target+"/center", map[string]bool{"confirm": confirm}, &res)
		if err != nil {
			if strings.Contains(err.Error(), "连不上：确认") {
				return fmt.Errorf("%w（确认就加 --confirm）", err)
			}
			return err
		}
		fmt.Fprintf(out, "中心现在是「%s」（任期 %d）。\n", res.Center.Name, res.Term)
		return nil
	}
	return fmt.Errorf("unknown peer command %q", args[0]) // checkPeerArgs let through a command this switch lacks
}

// readPeerCode reads `peer add -`'s code, a line on stdin; Ctrl-C (ctx) ends the wait.
func readPeerCode(ctx context.Context, stdin io.Reader) (string, error) {
	type read struct {
		line string
		err  error
	}
	got := make(chan read, 1)
	go func() { // left blocked on stdin when ctx ends first: the command is over then
		line, err := bufio.NewReader(stdin).ReadString('\n')
		got <- read{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-got:
		if r.err != nil && r.line == "" {
			return "", fmt.Errorf("读不到互联码：%w", r.err)
		}
		return strings.TrimSpace(r.line), nil
	}
}

// peerUsage is `hub peer`'s usage line.
const peerUsage = "usage: personad hub peer add <code>|- | list | remove <hub_id> | center [<hub_id>] [--confirm]"

// peerHelpAsked: help as the command, or -h / --help anywhere (no code or hub id starts with "-": codes are
// persona://peer links, ids UUIDs).
func peerHelpAsked(args []string) bool {
	if len(args) > 0 && args[0] == "help" {
		return true
	}
	return slices.ContainsFunc(args, clitext.IsHelpArg)
}

// checkPeerArgs refuses what PeerCommand would otherwise drop or misread: an extra word (`list x`), a second id
// (`center a b`), a flag taken for an id or a code (`center --bogus`, `remove -x`).
func checkPeerArgs(args []string) error {
	if len(args) == 0 {
		return clitext.UsageError(peerUsage)
	}
	rest := args[1:]
	switch args[0] {
	case "add":
		if len(rest) != 1 || (rest[0] != "-" && strings.HasPrefix(rest[0], "-")) {
			return clitext.UsageError("usage: personad hub peer add <code> (- reads the code from stdin)")
		}
	case "list":
		if len(rest) != 0 {
			return clitext.UsageError("usage: personad hub peer list (takes no arguments)")
		}
	case "remove":
		if len(rest) != 1 || strings.HasPrefix(rest[0], "-") {
			return clitext.UsageError("usage: personad hub peer remove <hub_id>")
		}
	case "center":
		ids := 0
		for _, a := range rest {
			switch {
			case a == "--confirm":
			case strings.HasPrefix(a, "-"):
				return clitext.UsageError(fmt.Sprintf("peer center: unknown flag %q; usage: personad hub peer center [<hub_id>] [--confirm]", a))
			default:
				ids++
			}
		}
		if ids > 1 {
			return clitext.UsageError("usage: personad hub peer center [<hub_id>] [--confirm] (at most one hub id)")
		}
	default:
		return clitext.UsageError(fmt.Sprintf("unknown peer command %q; %s", args[0], peerUsage))
	}
	return nil
}

func peerStateText(p peerItem) string {
	s := map[string]string{"connected": "已连接", "connecting": "连接中", "offline": "连不上", "refused": "对方不认这台的凭据"}[p.State]
	if s == "" {
		s = p.State
	}
	if p.RTTMS != nil {
		s += fmt.Sprintf("（%d ms）", *p.RTTMS)
	}
	if p.Error != "" {
		s += "：" + p.Error
	}
	return s
}

// adminCall calls this machine's hub over loopback with the master token, for at most adminCallLimit.
func (c *Config) adminCall(ctx context.Context, method, path string, body, out any) error {
	_, port, err := net.SplitHostPort(c.Listen)
	if n, perr := strconv.Atoi(port); err != nil || perr != nil || n <= 0 || n > 65535 {
		return errors.New("hub.json 的 listen 没有有效端口")
	}
	callCtx, cancel := context.WithTimeout(ctx, adminCallLimit)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(callCtx, method, "http://"+net.JoinHostPort("127.0.0.1", port)+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json") // the admin API takes JSON writes only
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // stopped (Ctrl-C)
		}
		if callCtx.Err() != nil {
			return fmt.Errorf("the hub on 127.0.0.1:%s did not answer within %s: it may be stuck, or another program is listening on hub.json's port", port, adminCallLimit)
		}
		return errors.New("无法连接本机 hub：hub 可能没在运行，或没监听回环地址")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		why := apierr.Describe(b) // with the cause the hub moved to detail (#5728)
		if why == "" {
			why = resp.Status
		}
		return fmt.Errorf("hub 回了 %d：%s", resp.StatusCode, why)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}
