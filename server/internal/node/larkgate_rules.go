package node

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
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
	re   *lazyre.Regexp
	what string
}{
	// lark-cli commands (larkcligate.go): "CLI /<domain>/<command>"
	{lazyre.New(`^CLI /im/\+messages-send`), "发消息"},
	{lazyre.New(`^CLI /im/\+messages-reply`), "回复消息"},
	{lazyre.New(`^CLI /im/reactions/`), "表情回复"},
	{lazyre.New(`^CLI /im/messages/delete`), "撤回消息"},
	{lazyre.New(`^CLI /im/(messages/(forward|merge_forward)|threads/forward)`), "转发消息"},
	{lazyre.New(`^CLI /im/messages/urgent`), "加急"},
	{lazyre.New(`^CLI /im/`), "改群 / 会话"},
	{lazyre.New(`^CLI /(markdown|slides|whiteboard|mindnotes)`), "写文档"},
	{lazyre.New(`^CLI /task`), "改待办"},
	{lazyre.New(`^CLI /approval`), "处理审批"},
	{lazyre.New(`^CLI /calendar`), "建 / 改日程"},
	{lazyre.New(`^CLI /(docs|sheets|wiki|drive|base)`), "写文档"},
	{lazyre.New(`^CLI /mail`), "发 / 改邮件"},
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
