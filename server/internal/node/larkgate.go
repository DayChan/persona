package node

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant's skills call HTTP APIs as the user through commands (`<command> METHOD PATH [BODY]`, the
// command gates the hub hands over: plugins' assistant.gate, docs/extensions/extensions.md "Assistant") and through lark-cli. Every Bash
// call of the assistant passes a PreToolUse hook that reads the command (docs/assistant/assistant.md):
//
//   - only gated calls (and a few output filters): each call goes by its gate's rules (the first match: allow, confirm
//     on the phone first with method, path and body on the card, or deny; none matching is confirmed); lark-cli's
//     by its own risk level (larkcligate.go).
//   - anything naming a gate's secret, a process's environ, curl / wget at a gate's API, or curl flags that would
//     print or redirect the credentials: denied.
//   - anything else: no decision, the usual permission flow (the phone asks about the whole command).

// larkCall is one gated command's or lark-cli's invocation read off a Bash command.
type larkCall struct {
	Gate   int    // the command gate (index in the config's Gates); -1 for lark-cli
	Method string // HTTP method; "CLI" for a lark-cli command (larkcligate.go)
	Path   string // without /agent/v1 and the query string; a lark-cli command's words joined by "/"
	Query  string
	Body   string // JSON text, a file's content (@file, cut), or "upload file X" (in Chinese)

	CLI  bool     // lark-cli
	Risk string   // lark-cli's own risk level for a "CLI" call: read | write | high-risk-write ("" unknown)
	Args []string // lark-cli's arguments, for the phone card
}

// larkVerdict: what the gate makes of a whole Bash command.
type larkVerdict struct {
	calls    []larkCall
	deny     string // why the command is refused
	other    bool   // something besides gated calls and filters: the usual permission flow decides
	unsure   bool   // command substitution / unknown variables: fine for writes (the user sees them), not for reads
	unparsed bool   // the command could not be split into its parts at all
	cliWrite bool   // a lark-cli call that is not a known read, even in a command the gate cannot follow
}

var (
	larkFilters = map[string]bool{"jq": true, "head": true, "tail": true, "grep": true, "egrep": true, "wc": true, "sort": true, "uniq": true, "cut": true, "tr": true, "column": true,
		// and commands that only print
		"echo": true, "printf": true, "true": true, "date": true, "which": true, "type": true, "cd": true}
	// curl flags a gated command passes on that cannot leak the token or reach another host, with whether they take a value.
	gateCurlFlags = map[string]bool{"--url-query": true, "-G": false, "--get": false, "--data-urlencode": true, "-d": true, "--data": true,
		"--data-raw": true, "-o": true, "--output": true, "-i": false, "--include": false, "-m": true, "--max-time": true, "-s": false,
		"-S": false, "-f": false, "--compressed": false, "-H": true, "--header": true, "--upload": true, "-X": true, "--request": true}
)

// classifyLark reads a Bash command against the command gates (curl / wget aimed at one's API is refused); cwd
// resolves @file bodies, risk tells lark-cli's reads from its writes (nil without lark-cli: its commands are all
// confirmed).
func classifyLark(cmd string, gates []proto.CommandGate, cwd string, risk larkRisk) larkVerdict {
	var v larkVerdict
	for _, g := range gates {
		for _, name := range g.Secret {
			if name != "" && strings.Contains(cmd, name) {
				v.deny = "不要读取、打印或直接使用 " + name + "：" + g.Title + "一律用 " + g.Command + "，它自己带凭据。"
				return v
			}
		}
	}
	if strings.Contains(cmd, "/proc/") && strings.Contains(cmd, "environ") {
		v.deny = "不要读取进程的环境变量（/proc/*/environ）：里面有凭据。"
		return v
	}
	if strings.Contains(cmd, ".lark-cli/") || strings.HasSuffix(cmd, ".lark-cli") {
		v.deny = "不要读或改 lark-cli 的配置和凭据（~/.lark-cli）：飞书用 lark-cli 命令，它自己带登录。"
		return v
	}
	segs, ok := shellSegments(cmd)
	if !ok {
		v.other, v.unparsed = true, true
		return v
	}
	vars := map[string]string{}
	for _, s := range segs {
		if s.subst {
			v.unsure = true
		}
		words := s.words
		// Leading assignments: alone they set a variable for later words; before ol only the account / timeout.
		var env []string
		for len(words) > 0 && isAssignment(words[0]) {
			env = append(env, words[0])
			words = words[1:]
		}
		if len(words) == 0 {
			for _, a := range env {
				k, val, _ := strings.Cut(a, "=")
				vars[k] = val
			}
			continue
		}
		// `timeout 20 ol …` (the prompt asks for timeouts), `command -v ol`.
		if len(words) > 2 && words[0] == "timeout" && timeoutArg.MatchString(words[1]) {
			words = words[2:]
		}
		if len(words) > 1 && words[0] == "command" && (words[1] == "-v" || words[1] == "-V") {
			words = []string{"which"}
		}
		name := words[0]
		gate := -1
		if (name == "bash" || name == "sh") && len(words) > 1 {
			if i := gateOf(gates, words[1]); i >= 0 {
				gate, words = i, words[1:]
			}
		}
		if gate < 0 {
			gate = gateOf(gates, name)
		}
		switch {
		case isLarkCLI(name):
			if len(env) > 0 {
				v.other = true
			}
			c, deny, ok := parseLarkCLI(words[1:], risk)
			if deny != "" {
				v.deny = deny
				return v
			}
			if !ok {
				v.other, v.cliWrite = true, true
				continue
			}
			if s.redirect != "" && !safeRedirect(s.redirect) {
				v.other = true
			}
			if !c.cliRead() {
				v.cliWrite = true
			}
			v.calls = append(v.calls, c)
		case gate >= 0:
			g := gates[gate]
			for _, a := range env {
				if k, _, _ := strings.Cut(a, "="); !slices.Contains(g.AllowedEnv, k) {
					v.other = true
				}
			}
			c, deny, ok := parseGateCall(g, words[1:], vars, cwd)
			if deny != "" {
				v.deny = deny
				return v
			}
			if !ok {
				v.other = true
				continue
			}
			c.Gate = gate
			if s.redirect != "" && !safeRedirect(s.redirect) {
				v.other = true
			}
			v.calls = append(v.calls, c)
		case name == "curl" || name == "wget" || name == "http" || name == "xh":
			if g, ok := gateAimed(gates, s.text); ok {
				v.deny = "不要直接 curl " + g.Title + " 的接口：用 " + g.Command + "，写操作要先在手机上确认。"
				return v
			}
			v.other = true
		case larkFilters[name] && len(env) == 0 && (s.redirect == "" || safeRedirect(s.redirect)):
		default:
			v.other = true
			if larkCLIRe.MatchString(s.text) {
				v.cliWrite = true // lark-cli inside a loop, a subshell, xargs…: its risk is unknown
			}
		}
	}
	if len(v.calls) == 0 {
		v.other = true
	}
	return v
}

func isAssignment(w string) bool {
	k, _, ok := strings.Cut(w, "=")
	if !ok || k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c == '_' || 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || i > 0 && '0' <= c && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// safeRedirect: output into /tmp, or stderr folded or dropped.
func safeRedirect(r string) bool {
	for _, t := range strings.Fields(r) {
		t = strings.TrimLeft(t, "0123456789&>")
		if t == "" || t == "/dev/null" || strings.HasPrefix(t, "/tmp/") {
			continue
		}
		return false
	}
	return true
}

// gateOf is the gate whose command or script name is, -1 for none.
func gateOf(gates []proto.CommandGate, name string) int {
	for i, g := range gates {
		if g.Command != "" && name == g.Command || g.Script != "" && (name == g.Script || strings.HasSuffix(name, "/"+g.Script)) {
			return i
		}
	}
	return -1
}

// gateAimed: text reaches a gate's API (its base's host, or its path prefix).
func gateAimed(gates []proto.CommandGate, text string) (proto.CommandGate, bool) {
	for _, g := range gates {
		if h := larkHost(g.Env[g.BaseEnv]); h != "" && strings.Contains(text, h) || g.PathPrefix != "" && strings.Contains(text, g.PathPrefix) {
			return g, true
		}
	}
	return proto.CommandGate{}, false
}

// parseGateCall reads `METHOD PATH [BODY] [curl flags]` after a gated command.
func parseGateCall(g proto.CommandGate, args []string, vars map[string]string, cwd string) (larkCall, string, bool) {
	expand := func(s string) (string, bool) {
		if !strings.Contains(s, "$") {
			return s, true
		}
		out := shellVarRef.ReplaceAllStringFunc(s, func(m string) string {
			k := strings.Trim(m, "${}")
			if val, ok := vars[k]; ok {
				return val
			}
			return m
		})
		return out, !strings.Contains(out, "$")
	}
	if len(args) < 2 || !httpMethod.MatchString(strings.ToUpper(args[0])) && !strings.Contains(args[0], "$") {
		return larkCall{}, "", false // a subcommand of its own (ol events …): not a call the rules know
	}
	method, ok1 := expand(strings.ToUpper(args[0]))
	path, ok2 := expand(args[1])
	if !ok1 || !ok2 {
		return larkCall{}, "", false
	}
	c := larkCall{Method: method}
	if g.PathPrefix != "" {
		path = strings.TrimPrefix(path, g.PathPrefix)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	c.Path, c.Query, _ = strings.Cut(path, "?")
	rest := args[2:]
	if len(rest) > 0 && (strings.HasPrefix(rest[0], "{") || strings.HasPrefix(rest[0], "[")) {
		c.Body, rest = rest[0], rest[1:]
	} else if len(rest) > 0 && strings.HasPrefix(rest[0], "@") {
		c.Body, rest = readBodyFile(rest[0][1:], cwd), rest[1:]
	}
	for i := 0; i < len(rest); i++ {
		f := rest[i]
		switch f {
		case "-v", "--verbose", "--trace", "--trace-ascii", "--trace-time", "-K", "--config", "--libcurl", "-w", "--write-out", "-D", "--dump-header":
			return larkCall{}, "不要给 " + g.Command + " 加会打印请求头（含凭据）或改写请求的 curl 参数（" + f + "）。", false
		}
		name, val, inline := strings.Cut(f, "=")
		if strings.HasPrefix(f, "-X") && len(f) > 2 { // -XPOST
			name, val, inline = "-X", f[2:], true
		}
		takes, known := gateCurlFlags[name]
		if !known {
			return larkCall{}, "", false // a positional word would be another URL curl sends the token to
		}
		if takes && !inline {
			if i+1 >= len(rest) {
				return larkCall{}, "", false
			}
			i++
			val = rest[i]
		}
		switch name {
		case "-X", "--request":
			c.Method = strings.ToUpper(val) // curl takes the last -X
		case "-H", "--header":
			if h, _, _ := strings.Cut(val, ":"); !slices.Contains(g.Headers, strings.ToLower(strings.TrimSpace(h))) {
				return larkCall{}, "", false
			}
		case "--upload":
			c.Body = "上传文件 " + val
		case "-d", "--data", "--data-raw", "--data-urlencode":
			if c.Body == "" {
				c.Body = val
			}
		}
	}
	return c, "", true
}

func readBodyFile(p, cwd string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "（读不到 " + p + "）"
	}
	if len(b) > 4000 {
		b = append(b[:4000], "…"...)
	}
	return string(b)
}

var httpMethod = regexp.MustCompile(`^(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)$`)

var (
	// timeoutArg is the duration of a leading `timeout 20 …` (also read by readsOnlyMemory).
	timeoutArg = regexp.MustCompile(`^[0-9.]+[smh]?$`)
	// shellVarRef is a $NAME or ${NAME} reference parseGateCall expands.
	shellVarRef = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
	// commandNameRe is what safeCommandName lets onto PATH.
	commandNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

// larkDecision is the gate's call on one call: its command gate's rules (the user's first, the hub put them there),
// or for a lark-cli command the configured rules and then lark-cli's own risk level (a rule for one:
// `{"method":"CLI","path":"im/+messages-send","action":"allow"}`).
func larkDecision(cfg proto.AssistantLark, c larkCall) string {
	if c.CLI && c.Method == "CLI" {
		if action, ok := matchLark(cfg.Rules, c); ok {
			return action
		}
		if c.Risk == "read" {
			return "allow"
		}
		return "confirm"
	}
	if c.CLI { // lark-cli api METHOD PATH: the configured rules, then reads go
		if action, ok := matchLark(cfg.Rules, c); ok {
			return action
		}
		if c.Method == "GET" || c.Method == "HEAD" {
			return "allow"
		}
		return "confirm"
	}
	if c.Gate < 0 || c.Gate >= len(cfg.Gates) {
		return "confirm"
	}
	return decideLark(cfg.Gates[c.Gate].Rules, c)
}

// larkRead: a call that only reads (lark-cli's own read risk, a GET / HEAD).
func larkRead(c larkCall) bool {
	if c.CLI && c.Method == "CLI" {
		return c.Risk == "read"
	}
	return c.Method == "GET" || c.Method == "HEAD"
}

// decideLark: the action for a call, from the rules in order (the first match); none matching is confirmed.

func decideLark(rules []proto.LarkRule, c larkCall) string {
	if action, ok := matchLark(rules, c); ok {
		return action
	}
	return "confirm"
}

// matchLark is the action of the first rule that matches the call.
func matchLark(rules []proto.LarkRule, c larkCall) (string, bool) {
	r, ok := firstLark(rules, c)
	return r.Action, ok
}

// firstLark is the first rule that matches the call.
func firstLark(rules []proto.LarkRule, c larkCall) (proto.LarkRule, bool) {
	for _, r := range rules {
		if (r.Method == "*" || strings.EqualFold(r.Method, c.Method)) && pathMatches(r.Path, c.Path) && bodyMatches(r.Body, c.Body) {
			return r, true
		}
	}
	return proto.LarkRule{}, false
}

func pathMatches(pattern, path string) bool {
	p := strings.Split(strings.Trim(pattern, "/"), "/")
	s := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range p {
		if seg == "**" {
			return true
		}
		if i >= len(s) || (seg != "*" && seg != s[i]) {
			return false
		}
	}
	return len(p) == len(s)
}

func bodyMatches(want map[string]any, body string) bool {
	if len(want) == 0 {
		return true
	}
	var got map[string]any
	if json.Unmarshal([]byte(body), &got) != nil {
		return false
	}
	for k, v := range want {
		if fmt.Sprint(got[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

// larkPattern: the call's route with its ids blanked, what "Always allow for this session" remembers.
func larkPattern(c larkCall) string {
	segs := strings.Split(strings.Trim(c.Path, "/"), "/")
	for i, s := range segs {
		if strings.ContainsAny(s, "0123456789") || len(s) > 24 {
			segs[i] = "*"
		}
	}
	return c.Method + " /" + strings.Join(segs, "/")
}

var larkActions = []struct {
	re   *regexp.Regexp
	what string
}{
	// lark-cli commands (larkcligate.go): "CLI /<domain>/<command>"
	{regexp.MustCompile(`^CLI /im/\+messages-send`), "发消息"},
	{regexp.MustCompile(`^CLI /im/\+messages-reply`), "回复消息"},
	{regexp.MustCompile(`^CLI /im/reactions/`), "表情回复"},
	{regexp.MustCompile(`^CLI /im/messages/delete`), "撤回消息"},
	{regexp.MustCompile(`^CLI /im/(messages/(forward|merge_forward)|threads/forward)`), "转发消息"},
	{regexp.MustCompile(`^CLI /im/messages/urgent`), "加急"},
	{regexp.MustCompile(`^CLI /im/`), "改群 / 会话"},
	{regexp.MustCompile(`^CLI /(markdown|slides|whiteboard|mindnotes)`), "写文档"},
	{regexp.MustCompile(`^CLI /task`), "改待办"},
	{regexp.MustCompile(`^CLI /approval`), "处理审批"},
	{regexp.MustCompile(`^CLI /calendar`), "建 / 改日程"},
	{regexp.MustCompile(`^CLI /(docs|sheets|wiki|drive|base)`), "写文档"},
	{regexp.MustCompile(`^CLI /mail`), "发 / 改邮件"},
}

// larkSummary is the phone card's text: what each write does, then its method, path and body.
func larkSummary(gates []proto.CommandGate, calls []larkCall) string {
	var b strings.Builder
	for i, c := range calls {
		if i > 0 {
			b.WriteString("\n\n")
		}
		what := "写操作"
		for _, a := range larkActions {
			if a.re.MatchString(c.Method + " " + c.Path) {
				what = a.what
				break
			}
		}
		if c.Gate >= 0 && c.Gate < len(gates) {
			for _, a := range gates[c.Gate].Actions {
				if re, err := regexp.Compile(a.Pattern); err == nil && re.MatchString(c.Method+" "+c.Path) {
					what = a.What
					break
				}
			}
		}
		if c.CLI {
			b.WriteString(larkCLIActor(c.Args) + "在飞书上" + what + larkRiskNote(c) + "\n" + prettyBody("lark-cli "+shellJoin(c.Args)))
			continue
		}
		b.WriteString("以你的身份在" + callTitle(gates, c) + "上" + what + "\n" + c.Method + " " + c.Path)
		if c.Query != "" {
			b.WriteString("?" + c.Query)
		}
		if body := prettyBody(c.Body); body != "" {
			b.WriteString("\n" + body)
		}
	}
	return b.String()
}

func prettyBody(body string) string {
	var v any
	if json.Unmarshal([]byte(body), &v) == nil {
		if b, err := json.MarshalIndent(v, "", "  "); err == nil {
			body = string(b)
		}
	}
	if r := []rune(body); len(r) > 2000 {
		body = string(r[:2000]) + "…"
	}
	return body
}

// larkGate decides an assistant Bash call (see the top of the file); nil leaves it to the usual permission flow.
func (n *Node) larkGate(ctx context.Context, in hookInput, ix proto.Interaction) (any, error) {
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(in.ToolInput, &a) != nil || a.Command == "" {
		return nil, nil
	}
	decision, reason, err := n.larkCheck(ctx, in.SessionID, in.Cwd, a.Command, ix)
	if err != nil || decision == "" {
		return nil, err
	}
	return preToolDecision(decision, reason), nil
}

// larkCheck is the gate itself, for any agent: "allow" / "deny" with the reason to give the model, or "" when the
// command is not the gate's to decide (the usual permission flow). Writes are confirmed on the phone here, so a
// "deny" may carry the user's own words. Claude reaches it through the Bash PreToolUse hook (larkGate), Codex through
// the app-server's command approvals (assistant_codex_approve.go).
func (n *Node) larkCheck(ctx context.Context, session, cwd, command string, ix proto.Interaction) (string, string, error) {
	cfg := n.assistantLark(ctx)
	if slices.ContainsFunc(cfg.Gates, func(g proto.CommandGate) bool { return g.Auth != nil }) {
		n.gates() // the proxy goes by this Node's config from now on (a resident node's next connection)
	}
	v := classifyLark(command, cfg.Gates, cwd, n.larkCLIRisk(cfg))
	if v.deny != "" {
		return "deny", v.deny, nil
	}
	if v.other {
		// A write through a gate whose credentials are at the proxy goes there only as a call read off the command and
		// let through here: in a command the gate cannot follow it would be refused after the phone said yes.
		for _, g := range cfg.Gates {
			if _, write := gateWrite([]proto.CommandGate{g}, command); write && g.Auth != nil {
				return "deny", "写操作要单独一条 `" + g.Command + " METHOD PATH [BODY]` 发：不放进循环、脚本或 $(…) 里，输出可以接 jq / head / grep，" +
					"或者用 -o 写到 /tmp。这样手机上能逐条确认，确认过的才发得出去。", nil
			}
		}
		// A command the gate cannot follow (loops, $(…), scripts) that still calls a gated command with a write: the
		// phone confirms it here, whole, whatever the permission mode (bypassPermissions would otherwise run it unasked).
		g, write := gateWrite(cfg.Gates, command)
		if write || v.cliWrite || v.unparsed && larkCLIRe.MatchString(command) {
			title := "飞书" // lark-cli's
			if write {
				title = g.Title
			}
			ix.Kind, ix.ToolName = proto.IxPermission, title
			ix.Summary = "以你的身份在" + title + "上写（命令较复杂，整条显示）\n" + prettyBody(command)
			decision, reason, _, err := n.confirmLark(ctx, session, ix, nil)
			return decision, reason, err
		}
		return "", "", nil
	}
	// A health conversation's writes are confirmed each time: no rule's allow, no "Always allow for this session" (docs/assistant/health.md "Outgoing").
	// Its space is the one its process was started in (assistantLarkEnv notes it, and the gate proxy goes by the same),
	// not the directory the hook says: that is where the model's last `cd` left it. A hook directory that is a health
	// space's counts too, so a mix-up can only ask more.
	health := n.gates().healthSession(session) || identity.SpaceOf(cwd) == identity.SpaceHealth
	// ask: the calls to confirm on the phone; covered: those "Always allow for this session" covers. Once let through
	// here, the gate proxy lets each go once (gateproxy.go).
	var ask, covered []larkCall
	for _, c := range v.calls {
		decision := larkDecision(cfg, c)
		if decision == "allow" && health && !larkRead(c) {
			decision = "confirm"
		}
		switch decision {
		case "deny":
			return "deny", "这个操作按配置不允许（" + c.Method + " " + c.Path + "）。", nil
		case "allow":
		default:
			if health || n.isAssistantSession(session) || !n.larkAlways(session, larkPattern(c)) {
				ask = append(ask, c)
			} else {
				covered = append(covered, c)
			}
		}
	}
	if len(ask) == 0 {
		n.grantGateCalls(cfg, session, covered, proto.LarkLetThrough{Kind: proto.LarkThroughAlways}) // "Always allow for this session" covers them
		if v.unsure {
			return "", "", nil // reads, but $(…) could run anything: the usual flow asks about the whole command
		}
		return "allow", "读操作", nil
	}
	ix.Kind, ix.ToolName, ix.Summary = proto.IxPermission, callTitle(cfg.Gates, ask[0]), larkSummary(cfg.Gates, ask)
	// The hub remembers these routes for assistant conversations and checks them again on every call.
	ix.AlwaysKeys = nil
	for _, c := range ask {
		if k := "lark:" + larkPattern(c); !health && !slices.Contains(ix.AlwaysKeys, k) {
			ix.AlwaysKeys = append(ix.AlwaysKeys, k)
		}
	}
	remember := ask
	if health {
		remember = nil // nothing for "Always allow for this session" to keep
	}
	decision, reason, card, err := n.confirmLark(ctx, session, ix, remember)
	if decision == "allow" {
		n.grantGateCalls(cfg, session, ask, card)
		n.grantGateCalls(cfg, session, covered, proto.LarkLetThrough{Kind: proto.LarkThroughAlways})
	}
	return decision, reason, err
}

// grantGateCalls lets calls larkCheck let through for session go once through the gate proxy: those of gates with
// Auth (lark-cli and the others carry their own credentials). through is how they were let through, which the proxy
// tells the manager once the API has taken one (the sent-as-user ledger).
func (n *Node) grantGateCalls(cfg proto.AssistantLark, session string, calls []larkCall, through proto.LarkLetThrough) {
	for _, c := range calls {
		if !c.CLI && c.Gate >= 0 && c.Gate < len(cfg.Gates) && cfg.Gates[c.Gate].Auth != nil {
			n.gates().grant(cfg.Gates[c.Gate].Command, session, c, through)
		}
	}
}

// gateWrite: a gated command (or its script) called with a method that is not a read, anywhere in a command.
func gateWrite(gates []proto.CommandGate, command string) (proto.CommandGate, bool) {
	for _, g := range gates {
		var names []string
		if g.Command != "" {
			names = append(names, regexp.QuoteMeta(g.Command))
		}
		if g.Script != "" {
			names = append(names, `\S*/`+regexp.QuoteMeta(path.Base(g.Script)))
		}
		if len(names) == 0 {
			continue
		}
		if gateWriteRe(strings.Join(names, "|")).MatchString(command) {
			return g, true
		}
	}
	return proto.CommandGate{}, false
}

// gateWriteRes caches gateWrite's regexp by its alternation of names: gates come from config, so there are few.
var gateWriteRes sync.Map // string -> *regexp.Regexp

// gateWriteRe matches one of names (an alternation of quoted names) called with a write method.
func gateWriteRe(names string) *regexp.Regexp {
	if re, ok := gateWriteRes.Load(names); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile("(?i)(^|[\\s;&|(`])(" + names + ")\\s+(POST|PUT|PATCH|DELETE|\"?\\$)")
	gateWriteRes.Store(names, re)
	return re
}

// callTitle is what the phone card names as the place a call acts on.
func callTitle(gates []proto.CommandGate, c larkCall) string {
	if c.Gate >= 0 && c.Gate < len(gates) && gates[c.Gate].Title != "" {
		return gates[c.Gate].Title
	}
	return "飞书" // lark-cli
}

// confirmLark asks the phone; "Always allow for this session" remembers the calls' routes (not for whole commands).
// When it allows, card is how: the card, with who answered it, or that "Always allow" covered it (the manager's
// answer says it kept one).
func (n *Node) confirmLark(ctx context.Context, session string, ix proto.Interaction, ask []larkCall) (decision, reason string, card proto.LarkLetThrough, err error) {
	if ix.ID == "" { // a gated tool's card has none yet: the ledger names the card by it
		ix.ID = "ix_" + randHex(6)
	}
	res, ok, err := n.ask(ctx, ix)
	if err != nil {
		return "", "", card, err
	}
	if !ok || res.Allow == nil || !*res.Allow {
		msg := res.Message
		if msg == "" {
			msg = "用户没有同意这个飞书写操作，没有执行。"
		}
		return "deny", msg, card, nil
	}
	// Assistant conversations consult the hub on every call, including after revocation.
	if res.Always && !n.isAssistantSession(session) {
		for _, c := range ask {
			n.rememberLarkAlways(session, larkPattern(c))
		}
	}
	card = proto.LarkLetThrough{Kind: proto.LarkThroughCard, InteractionID: ix.ID, AnsweredBy: res.AnsweredBy}
	if res.Remembered { // the manager answered from an "Always allow" it kept: no card was answered for this one
		card = proto.LarkLetThrough{Kind: proto.LarkThroughAlways}
	}
	return "allow", "用户在手机上同意了", card, nil
}

func preToolDecision(decision, reason string) any {
	return map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": decision, "permissionDecisionReason": reason,
	}}
}

func larkHost(base string) string {
	h := base
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	h, _, _ = strings.Cut(h, "/")
	return h
}

func (n *Node) larkAlways(session, pattern string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.larkAllowed[session+" "+pattern]
}

func (n *Node) rememberLarkAlways(session, pattern string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.larkAllowed == nil {
		n.larkAllowed = map[string]bool{}
	}
	n.larkAllowed[session+" "+pattern] = true
}

// assistantLark asks the hub for the assistant's Lark config (gate rules, lark-cli, plugins' command gates), at
// most once a minute (it may change); an empty config when the hub has none or is unreachable.
func (n *Node) assistantLark(ctx context.Context) proto.AssistantLark {
	n.mu.Lock()
	cached, at := n.larkCfg, n.larkCfgAt
	n.mu.Unlock()
	if time.Since(at) < time.Minute || n.peer == nil {
		return cached
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cfg proto.AssistantLark
	if err := n.peer.Call(ctx, "assistant.lark", map[string]any{}, &cfg); err != nil {
		n.logger.Printf("assistant: Feishu gate config from hub: %v", err) // never the config itself
		return cached
	}
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = cfg, time.Now()
	n.mu.Unlock()
	return cfg
}

// spaceMain is PERSONA_SPACE outside the health space: the assistant's main conversations and the ones beside them.
const spaceMain = "main"

// assistantSpace is the space of the conversation an assistant process runs in, from its trusted directory (as
// healthdeny.go and the gate decide it): identity.SpaceHealth or spaceMain.
func assistantSpace(cwd string) string {
	if cwd != "" && identity.SpaceOf(cwd) == identity.SpaceHealth {
		return identity.SpaceHealth
	}
	return spaceMain
}

// assistantLarkEnv puts the skills in the assistant's directory (the user's from ~/.persona/skills, linked, the
// built-in ones and the plugins', assistantskills) and gives its process what its gated commands need: each gate's
// command on PATH (a wrapper running the skill's script, when the skill is there) and its env. A gate with Auth keeps
// its credentials in the node: its BaseEnv is this process's own address at the gate proxy, and neither its Secret
// nor what Auth puts on the requests is given (gateproxy.go, #1317); release takes the addresses back once the
// process is gone. session is the conversation the process was started for: the proxy tells the manager of the writes
// that went out under it. A process started in the health space gets addresses that let a write through only once the
// phone confirmed it, whatever the rules allow (larkCheck holds the gated command to the same). Without a config from
// the hub the commands say their API is not reachable.
//
// The process also learns its conversation's space, PERSONA_SPACE (assistantSpace): every gated command reaches every
// space, and one meant for the health space alone checks this instead of its working directory, which a `cd` in front
// of the command changes without a confirmation (#6003).
func (n *Node) assistantLarkEnv(cwd, session string) (env []string, release func()) {
	env, release, _ = n.assistantLarkEnvNamed(cwd, session)
	return env, release
}

// assistantLarkEnvNamed is assistantLarkEnv for a process that may start before its conversation has an id (a new Codex
// conversation gets it from its thread): name tells the id once it is known, and the addresses opened so far stand for it
// (nameAddr).
func (n *Node) assistantLarkEnvNamed(cwd, session string) (env []string, release func(), name func(session string)) {
	var released []func()
	release = func() {
		for _, f := range released {
			f()
		}
	}
	skills := filepath.Join(cwd, ".claude", "skills")
	if err := assistantskills.Sync(skills, assistantskills.UserDir(), n.signedInConnectors(cwd), n.logger.Printf); err != nil {
		n.logger.Printf("assistant: skills: %v", err)
	}
	bin := n.assistantBin(cwd)
	_ = os.MkdirAll(bin, 0o755)
	space := assistantSpace(cwd)
	health := space == identity.SpaceHealth
	gp := n.gates()
	if health {
		gp.markHealth(session) // larkCheck decides the conversation's space by this, not by the directory a hook runs in
	}
	var bases []string // the addresses opened for this process
	env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "PERSONA_ASSISTANT_BIN=" + bin,
		"PERSONA_SPACE=" + space}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := n.assistantLark(ctx)
	wrote := map[string]bool{}
	for _, g := range cfg.Gates {
		if !safeCommandName(g.Command) || g.Script == "" {
			continue
		}
		script := filepath.Join(skills, filepath.FromSlash(g.Script))
		if !fileExists(script) || !strings.HasPrefix(filepath.Clean(script), filepath.Clean(skills)+string(os.PathSeparator)) {
			continue
		}
		wrapper := "#!/bin/sh\n" + gateWrapperMark + " (" + g.Title + ", node/larkgate.go)\nexec bash " + shellQuote(script) + " \"$@\"\n"
		_ = os.WriteFile(filepath.Join(bin, g.Command), []byte(wrapper), 0o755)
		wrote[g.Command] = true
		held := map[string]bool{} // credentials the proxy adds: not for the assistant's process
		base := ""
		if g.Auth != nil && g.BaseEnv != "" {
			for _, s := range g.Secret {
				held[s] = true
			}
			for _, m := range gateAuthRef.FindAllStringSubmatch(g.Auth.Value, -1) {
				held[m[1]] = true
			}
			b, done, err := gp.openIn(g.Command, session, health) // the proxy holds a health process's writes to the phone
			if err != nil {
				// Its credentials stay out all the same: without the base too, the command fails plainly.
				n.logger.Printf("assistant: %s: gate proxy: %v", g.Command, err)
				held[g.BaseEnv] = true
			} else {
				base, released, bases = b, append(released, done), append(bases, b)
			}
		}
		keys := make([]string, 0, len(g.Env))
		for k := range g.Env {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if held[k] || !envName.MatchString(k) || compat.ReservedEnv(k) || k == "PATH" {
				continue
			}
			v := g.Env[k]
			if k == g.BaseEnv && base != "" {
				v = base
			}
			env = append(env, k+"="+v)
		}
	}
	// A gate gone (the plugin switched off, the skill removed): its command goes too.
	if ents, err := os.ReadDir(bin); err == nil {
		for _, e := range ents {
			if wrote[e.Name()] {
				continue
			}
			p := filepath.Join(bin, e.Name())
			if b, err := os.ReadFile(p); err == nil && (strings.Contains(string(b), gateWrapperMark) || strings.Contains(string(b), legacyWrapperMark)) {
				_ = os.Remove(p)
			}
		}
	}
	n.writeLarkCLIWrapper(bin, cfg.CLI) // lark-cli, when hub.json has lark.cli (larkcligate.go)
	name = func(id string) {
		for _, b := range bases {
			gp.nameAddr(b, id)
		}
		if health {
			gp.markHealth(id)
		}
	}
	return env, release, name
}

const gateWrapperMark = "# a skill's gated command"

// legacyWrapperMark is in the wrapper an older node wrote for the one gated command it had, before gates were the
// extensions' ("# <helper> (<skill>), on the assistant's PATH (node/larkgate.go)."): it goes like any gone gate's.
const legacyWrapperMark = ", on the assistant's PATH (node/larkgate.go)."

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// safeCommandName: a plain file name for a command on PATH.
func safeCommandName(s string) bool {
	return commandNameRe.MatchString(s) && s != "lark-cli"
}
