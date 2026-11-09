package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

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

var httpMethod = lazyre.New(`^(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)$`)

var (
	// timeoutArg is the duration of a leading `timeout 20 …` (also read by readsOnlyMemory).
	timeoutArg = lazyre.New(`^[0-9.]+[smh]?$`)
	// shellVarRef is a $NAME or ${NAME} reference parseGateCall expands.
	shellVarRef = lazyre.New(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
	// commandNameRe is what safeCommandName lets onto PATH.
	commandNameRe = lazyre.New(`^[a-z][a-z0-9_-]{0,31}$`)
)

func larkHost(base string) string {
	h := base
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	h, _, _ = strings.Cut(h, "/")
	return h
}
