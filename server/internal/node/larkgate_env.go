package node

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
)

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

var envName = lazyre.New(`^[A-Z_][A-Z0-9_]*$`)

// safeCommandName: a plain file name for a command on PATH.
func safeCommandName(s string) bool {
	return commandNameRe.MatchString(s) && s != "lark-cli"
}
