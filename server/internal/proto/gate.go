package proto

// LarkRule is one rule of the assistant's Feishu write gate (node/larkgate.go): a Lark gateway agent API call matching
// Method ("*" for any), Path (segments; "*" one segment, "**" the rest) and, when set, the top-level fields of its
// JSON body, gets Action: "allow" (run it), "confirm" (the user approves it on the phone first) or "deny".
// hub.json's lark_gate rules come first, then the built-in ones; the first match decides.
type LarkRule struct {
	Method string         `json:"method"`
	Path   string         `json:"path"`
	Body   map[string]any `json:"body,omitempty"`
	Action string         `json:"action"`
}

// AssistantLark is what the assistant's machine needs to gate its skills' Feishu commands: the write gate's extra
// rules, lark-cli and the plugins' command gates. Sent by the hub to the assistant's node only; never written to disk
// or logs. (It no longer carries the Lark gateway's base and token: that is an extension's now, which supplies its own gate
// and credentials through Gates; an older hub's "account", "base" and "token" are unknown keys and ignored.)
type AssistantLark struct {
	Rules  []LarkRule `json:"rules,omitempty"`
	CLI    *LarkCLI   `json:"cli,omitempty"`    // lark-cli is configured (hub.json lark.cli, docs/plans/plan-lark-cli.md)
	Prefer string     `json:"prefer,omitempty"` // "cli", or the serving extension's own source: the primary one when both are there
	// Gates are the command gates of the assistant's skills (plugins' assistant.gate, docs/extensions/extensions.md "Assistant"):
	// a command calling an HTTP API as the user, its credentials and the rules its calls go by.
	Gates []CommandGate `json:"gates,omitempty"`
	// MCPServers are the extensions' MCP servers for the assistant (mcp_servers, docs/extensions/extensions.md "MCP servers").
	MCPServers []AssistantMCPServer `json:"mcp_servers,omitempty"`
}

// AssistantMCPServer is one extension's MCP server for the Claude assistant: a command line on its machine, its tools
// as mcp__<ID>__<tool> (node/assistant_mcp.go). Allow run without asking, Deny are never callable, the rest go by the
// conversation's permission mode.
type AssistantMCPServer struct {
	Plugin   string   `json:"plugin,omitempty"`
	ID       string   `json:"id"`
	Command  string   `json:"command"`
	Env      string   `json:"env,omitempty"`
	Args     []string `json:"args,omitempty"`
	Requires []string `json:"requires,omitempty"`
	Allow    []string `json:"allow,omitempty"`
	Deny     []string `json:"deny,omitempty"`
}

// CommandGate is one skill command that calls an HTTP API as the user (`<command> METHOD PATH [BODY] [curl flags]`),
// as the assistant's machine gates it (node/larkgate.go): the command on the assistant's PATH runs the skill's
// script with Env; every Bash call of it is read, and its calls go by Rules (the first match; none matching is
// confirmed on the phone). With Auth the credentials stay in the node: BaseEnv points at its proxy, which adds them
// and holds every request to Rules (node/gateproxy.go, #1317).
type CommandGate struct {
	Plugin  string `json:"plugin,omitempty"`
	Command string `json:"command"` // the name on the assistant's PATH
	Script  string `json:"script"`  // the script it runs, relative to the skills directory ("<skill>/scripts/x.sh")
	Title   string `json:"title"`   // what the phone card says it acts on ("on <Title>…", in Chinese)
	// Env is put into the assistant's process; BaseEnv names the one holding the API's base URL (curl / wget at its
	// host is refused); Secret are names the assistant may never mention (the token); AllowedEnv may be set before
	// the command (VAR=x cmd …).
	Env        map[string]string `json:"env,omitempty"`
	BaseEnv    string            `json:"base_env,omitempty"`
	Secret     []string          `json:"secret,omitempty"`
	AllowedEnv []string          `json:"allowed_env,omitempty"`
	Headers    []string          `json:"headers,omitempty"`     // -H headers the command may be given (lowercase)
	PathPrefix string            `json:"path_prefix,omitempty"` // dropped from a call's path ("/agent/v1")
	Rules      []LarkRule        `json:"rules,omitempty"`
	// Actions name what a call does on the phone card: the first whose pattern (a regexp over "METHOD /path")
	// matches.
	Actions []GateAction `json:"actions,omitempty"`
	// Tool is the command as a tool for an assistant on a runtime without Bash ({args: [words after the command]}).
	Tool *GateTool `json:"tool,omitempty"`
	// Auth is how the node's proxy puts the credentials on a request; nil: Env goes into the assistant's process
	// whole, Secret included (a gate whose script needs its secret itself, or a hub from before #1317).
	Auth *GateAuth `json:"auth,omitempty"`
}

// GateAuth is a command gate's credentials as its API takes them: the header Header set to Value, each ${NAME} in it
// replaced by that one of the gate's Env.
type GateAuth struct {
	Header string `json:"header"`
	Value  string `json:"value"`
}

// GateAction names a kind of call for the phone card.
type GateAction struct {
	Pattern string `json:"pattern"`
	What    string `json:"what"`
}

// GateTool is a command gate's tool for runtimes.
type GateTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// LarkCLI is how the assistant's machine runs lark-cli.
type LarkCLI struct {
	Bin     string `json:"bin,omitempty"`
	Profile string `json:"profile,omitempty"`
}
