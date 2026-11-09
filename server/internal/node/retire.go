package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Retiring a finished task's agent (hub tasks_close.go, docs/assistant/todos.md "Closing the agent of a finished task"): its pane closes and
// the agent process with it, while everything needed to bring it back stays — the session id, its transcript, and in
// the node's state the launcher, directory and model it ran with. The next message to it resumes it (send: claude
// --resume, codex resume, traex resume) in a new tab of the same engine. Only an agent that can come back that way is
// closed: one this engine can open a tab for again (on Orca, Claude sessions only: orcaContinues), stopped, not an assistant conversation, not one another tool owns.

const notRetirable = "not_retirable"

// retire closes session id's pane when the agent in it is stopped and can be resumed later; closed false (and no
// error) when it was not running.
func (n *Node) retire(ctx context.Context, id string) (closed bool, err error) {
	if !validID(id) {
		return false, fmt.Errorf("invalid session id %q", id)
	}
	no := func(why string) (bool, error) { return false, refusal(notRetirable, why, nil) }
	_, terminalOnly := n.terminalOnly(id)
	switch h, _, known := n.runtimes.belongs(id); {
	case n.isAssistantSession(id) || n.runtimeAssistant(id):
		return no("assistant conversations are not closed")
	case n.externalCodex(id) || h != nil || known || terminalOnly:
		return no("this session belongs to another tool")
	}
	defer n.typing.lock(id)() // not while a message is on its way in (a send may be resuming it right now)
	if r := n.starts.get(id); r != nil && r.ended.IsZero() {
		return no("still starting")
	}
	a, ok := n.agent(id)
	if !ok {
		return false, nil
	}
	if _, runtime := engineRuntimes[n.defaultKind()]; runtime {
		return no("this engine continues sessions itself")
	}
	if _, err := hostCap[host.Launcher](n); err != nil {
		return no("this engine cannot open it again")
	}
	if h := n.defaultHost(); h != nil && h.Kind() == host.Orca && !n.orcaContinues(id) {
		return no("Orca continues only Claude sessions") // send would refuse it as not running (notRunningRefusal)
	}
	if a.AgentStatus == proto.StatusWorking || a.AgentStatus == proto.StatusBlocked {
		return no("the agent is " + a.AgentStatus)
	}
	st, mine := n.state.get(id)
	switch a.Agent {
	case "", "claude":
		if n.transcriptPath(id) == "" {
			return no("no transcript to resume from")
		}
	case "codex", traexAgent:
		if !mine || st.Launcher == "" || st.Cwd == "" {
			return no("not started by Persona: its launcher is not known")
		}
		if a.Agent == "codex" {
			if _, ok := n.codexLauncher(ctx, st.Launcher); !ok {
				return no("its launcher does not run Codex any more")
			}
		}
	default:
		return no("cannot resume a " + a.Agent)
	}
	if err := n.close(ctx, id); err != nil {
		return false, err
	}
	return true, nil
}

// resumeTabName names the tab a session is resumed in: its title (Claude's AI title, or the one it was given), so
// the tab reads as it did before it was closed rather than agent-<n>.
func (n *Node) resumeTabName(id string) string {
	p := n.transcriptPath(id)
	if p == "" {
		return ""
	}
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return n.meta(p, fi).title
}

// resumeOther resumes a Codex or a compatible CLI that is not running, with text as its next message: in a new tab of the
// engine, in the directory and with the launcher (and model) it was started with, the message going in as the
// resume's prompt (nothing is typed). One Persona did not start (its launcher unknown) is refused as before.
func (n *Node) resumeOther(ctx context.Context, id, text string, images []proto.Image) error {
	st, mine := n.state.get(id)
	if _, runtime := engineRuntimes[n.defaultKind()]; runtime || !mine || st.Launcher == "" || st.Cwd == "" {
		return fmt.Errorf("这个 Agent 已经退出：Codex / TRAEX 的会话要在电脑上重新打开")
	}
	return n.resumeOtherWith(ctx, id, st, text, images)
}

// resumeOtherWith is resumeOther with the launcher, directory and model to resume with (st): the session's own, or
// what a Codex thread started outside Persona is resumed with (codex_resume.go).
func (n *Node) resumeOtherWith(ctx context.Context, id string, st sessionState, text string, images []proto.Image) error {
	if r := n.notRunningRefusal(ctx, id); r != nil {
		return r
	}
	launch, err := hostCap[host.Launcher](n)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(st.Cwd); err != nil || !fi.IsDir() {
		return fmt.Errorf("directory %s does not exist", st.Cwd)
	}
	codex := n.codexPath(id) != ""
	var argv []string
	if codex {
		var ok bool
		argv, ok = n.codexLauncher(ctx, st.Launcher)
		if !ok {
			return fmt.Errorf("启动器「%s」不再运行 Codex，没法接着这个会话", st.Launcher)
		}
		if argv, err = n.codexReadonlyArgs(st.Launcher, argv, st.AppMode); err != nil {
			return err // Refuse conflicting permissions before creating a workspace, tab or attachment.
		}
		if st.Model != "" {
			argv = append(withoutModelArgs(argv), "--model", st.Model)
		}
	}
	paths, err := n.saveImages(images)
	if err != nil {
		return err
	}
	if len(paths) > 0 {
		text = strings.TrimSpace(text) + "\n\n<attached_images>\n" + strings.Join(paths, "\n") + "\n</attached_images>"
	}
	ws, err := n.workspaceFor(ctx, launch, st.Cwd)
	if err != nil {
		return err
	}
	tab := n.resumeTabName(id)
	if !codex { // a compatible CLI: `<launcher> resume <id> <message>`
		_, err = n.startOtherAgent(ctx, launch, st.Cwd, st.Launcher, traexAgent, text, id, ws, tab)
		return uncertain(err)
	}
	return uncertain(n.resumeCodex(ctx, launch, id, st, argv, text, ws, tab))
}

// resumeCodex runs `<launcher words> resume <id> -- <message>` (through codex-start, like a new Codex) in a new tab
// and waits until the engine shows that Codex in it.
func (n *Node) resumeCodex(ctx context.Context, launch host.Launcher, id string, st sessionState, argv []string, prompt, ws, tabName string) error {
	if codexOnRemote(argv) {
		argv = append(argv, "-C", st.Cwd)
	}
	argv = append(argv, "resume", id)
	dir, err := os.MkdirTemp(n.dir, "codex-start-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "start.json")
	b, _ := json.Marshal(codexStartFile{Argv: argv, Prompt: prompt, Agent: herdrAgentFor(launch, "codex")})
	if err := os.WriteFile(file, b, 0o600); err != nil {
		return err
	}
	created, err := n.newTab(ctx, launch, st.Cwd, ws, tabName, id)
	if err != nil {
		return err
	}
	pane := created.Pane.String()
	time.Sleep(300 * time.Millisecond) // the fresh shell loads ~/.zshrc; typed input is buffered either way
	shells := paneShells(ctx, created)
	done, err := n.runAgentCommand(ctx, launch, created, ws, "", n.agentCommand(shellQuote(n.bin)+" codex-start "+shellQuote(file), shellQuote, shells...), shells...) // typedstart.go
	if err != nil {
		return err // the tab is closed: no pane to bind
	}
	defer done()
	n.bindPane(pane, id) // the pane runs this thread (kept in its state): no guessing another rollout for it
	wait, cancel := context.WithTimeout(ctx, codexStartWait)
	defer cancel()
	for {
		if err := n.refreshHosts(wait); err == nil {
			if a, ok := n.agent(id); ok && a.PaneID == pane {
				return nil
			}
		}
		select {
		case <-wait.Done():
			screen, _ := n.readPane(context.WithoutCancel(ctx), pane, "recent-unwrapped", 30)
			return fmt.Errorf("Codex 没有在新标签页里接上会话 %s\n%s", id, lastLines(screen, 12))
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// newTab opens a tab in workspace ws for cwd, named tabName (or agent-<n>); with no workspace, a new one named after
// the directory, whose first tab it is. The pane is marked as Persona's (personaTabToken, the session id when it is
// already known): once its agent exits, the shell left behind is cleaned up (shelltabs.go).
func (n *Node) newTab(ctx context.Context, launch host.Launcher, cwd, ws, tabName, id string) (*host.Created, error) {
	if c := adoptedPane(ctx); c != nil { // "Run Agent Here": the pane the user picked, not a new tab (panes_actions.go)
		return c, nil
	}
	var created *host.Created
	var err error
	if ws == "" {
		if created, err = launch.CreateSpace(ctx, cwd, filepath.Base(cwd)); err == nil && created.SlotID != "" {
			var taken map[string]bool
			if _, ok := launch.(*herdrCoreHost); ok {
				taken = n.tabLabels(ctx, launch, created.SpaceID) // CreateSpace can reuse an app space by its folder.
			}
			_ = renameSlot(ctx, launch, created.SlotID, tabLabel(tabName, taken))
		}
	} else {
		created, err = launch.CreateSlot(ctx, ws, cwd, tabLabel(tabName, n.tabLabels(ctx, launch, ws)))
	}
	if err != nil {
		return nil, fmt.Errorf("创建终端标签页失败：%w", err)
	}
	markPersonaTab(ctx, launch, created.Pane, id)
	return created, nil
}
