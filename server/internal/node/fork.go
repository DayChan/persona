package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Forking a conversation ("Branch to New Chat", #2137): a new Claude Code session that knows the conversation up to one of its
// replies, the original untouched. The transcript is copied under a new id up to the end of the turn that reply is in
// (so no tool call is cut from its result) and the copy is an offline session like any other: the next message to it
// resumes it (claude --resume <new id>) in a new tab, or in the assistant's process for an assistant conversation.
// `claude --resume --fork-session` copies the whole conversation only, and --resume-session-at, which would cut it,
// is not honoured by an interactive claude (tried 2026-11-05), so the node makes the copy itself.

// forkable: the session is a Claude Code conversation whose transcript the node can copy. Codex, a compatible CLI, runtimes and
// other tools' sources have their own ids and files; the client hides "Branch to New Chat" for them.
func (n *Node) forkable(s proto.Session) bool {
	if _, codex := n.codexSource(s.SessionID); codex || s.SourceKind != "" || (s.Agent != "" && s.Agent != "claude") || !validID(s.SessionID) || n.runtimeAssistant(s.SessionID) || s.Starting {
		return false
	}
	return n.claudeTranscript(s.SessionID, false) != ""
}

// claudeTranscript is session id's own Claude transcript (<id>.jsonl), or "". look: find it when it is not known yet;
// the session list does not (it has looked for every row already, and a glob per row on every rebuild is costly).
func (n *Node) claudeTranscript(id string, look bool) string {
	var p string
	if look {
		p = n.transcriptPath(id)
	} else {
		n.mu.Lock()
		p = n.paths[id]
		n.mu.Unlock()
	}
	if p == "" || filepath.Base(p) != id+".jsonl" {
		return ""
	}
	return p
}

// fork copies session id's transcript up to the turn of item at (a transcript item id, "<uuid>#<n>"; empty: all of
// it) into a new session and returns the new session's id.
func (n *Node) fork(ctx context.Context, id, at string) (string, error) {
	if !validID(id) {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	a, live := n.agent(id)
	src := n.claudeTranscript(id, true)
	if src == "" || !n.forkable(proto.Session{SessionID: id, Agent: a.Agent}) {
		return "", refusal("fork_unsupported", "这个会话不能分支：只支持 Claude Code 的会话", nil)
	}
	uuid, _, _ := strings.Cut(at, "#")
	newID := newUUID()
	dst := filepath.Join(filepath.Dir(src), newID+".jsonl")
	var title, first string
	if st, err := os.Stat(src); err == nil {
		m := n.meta(src, st)
		title, first = m.title, m.first
	}
	whole, err := copyTranscriptUntil(src, dst, id, newID, uuid, forkTitle(title, first))
	if err != nil {
		return "", err
	}
	if whole && (live && a.AgentStatus == proto.StatusWorking || n.assistantBusy(id)) {
		// The turn is still being written: the copy could end on a tool call without its result.
		_ = os.Remove(dst)
		return "", refusal("fork_turn_running", "这一轮还没做完，等它停下再分支", nil)
	}
	if st, ok := n.state.get(id); ok { // resumed with what the original ran on: launcher, directory, model
		// A guest conversation's branch is one too (assistant_guest.go).
		n.state.put(newID, sessionState{Launcher: st.Launcher, Cwd: st.Cwd, Model: st.Model, ModelFingerprint: st.ModelFingerprint, ModelEffort: st.ModelEffort, Guest: st.Guest})
	}
	n.setPath(newID, dst)
	n.mu.Lock()
	if n.forked == nil {
		n.forked = map[string]bool{}
	}
	n.forked[newID] = true // listed however short it is (scanOffline skips files under 2 KB)
	n.mu.Unlock()
	offlineCache.Lock()
	offlineCache.at = time.Time{} // listed at once, not with the next scan
	offlineCache.Unlock()
	n.notifySessions()
	return newID, nil
}

// forkTitle is the copy's name: the original's (else its first prompt, cut to 40 characters) with the Chinese "Branch · " prefix in front,
// once.
func forkTitle(title, first string) string {
	title = strings.TrimSpace(title)
	if title == "" || title == "Claude Code" {
		title = snippet(first)
		if r := []rune(title); len(r) > 40 {
			title = string(r[:40]) + "…"
		}
	}
	if title == "" {
		return ""
	}
	if strings.HasPrefix(title, "分支 · ") {
		return title
	}
	return "分支 · " + title
}

// copyTranscriptUntil writes dst (a new file) with src's complete lines, oldID's sessionId replaced by newID, up to the
// end of the turn the line with uuid is in: the line before the next prompt the user typed. An empty uuid copies every
// line. A title adds a custom-title line, the name /rename would give it. whole: the copy ran to the end of src (the
// turn may still be going on). Lines are copied as they are, only the id swapped, so nothing else in them changes.
func copyTranscriptUntil(src, dst, oldID, newID, uuid, title string) (whole bool, err error) {
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	ok := false
	defer func() {
		if !ok {
			out.Close()
			_ = os.Remove(dst)
		}
	}()
	w := bufio.NewWriterSize(out, 1<<16)
	r := bufio.NewReaderSize(in, 1<<16)
	found := uuid == ""
	lines := 0
	oldKey, newKey := []byte(`"sessionId":"`+oldID+`"`), []byte(`"sessionId":"`+newID+`"`)
	whole = true
	for {
		line, err := r.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		if !bytes.HasSuffix(line, []byte("\n")) { // the end of the file, or a line still being written
			break
		}
		var e map[string]json.RawMessage
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if found && uuid != "" && typedPrompt(e) {
			whole = false
			break // the next turn starts here
		}
		if !found && string(e["uuid"]) == `"`+uuid+`"` {
			found = true
		}
		line = bytes.ReplaceAll(line, oldKey, newKey)
		if _, err := w.Write(line); err != nil {
			return false, err
		}
		lines++
	}
	if !found {
		return false, refusal("fork_item_missing", "找不到要分支的那条消息", nil)
	}
	if lines == 0 {
		return false, errors.New("the conversation is empty")
	}
	if title != "" {
		b, _ := json.Marshal(map[string]string{"type": "custom-title", "customTitle": title, "sessionId": newID})
		if _, err := w.Write(append(b, '\n')); err != nil {
			return false, err
		}
	}
	if err := w.Flush(); err != nil {
		return false, err
	}
	if err := out.Close(); err != nil {
		return false, err
	}
	ok = true
	return whole, nil
}

// assistantBusy: id is an assistant conversation answering right now.
func (n *Node) assistantBusy(id string) bool {
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	if p == nil || !p.alive() {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.busy
}

// typedPrompt: the transcript line is a message the user sent (a turn's start), not a tool's result, a hook's note or
// a subagent's line.
func typedPrompt(e map[string]json.RawMessage) bool {
	if string(e["type"]) != `"user"` || string(e["isMeta"]) == "true" || string(e["isSidechain"]) == "true" {
		return false
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(e["message"], &m) != nil {
		return false
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return true
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			return true
		}
	}
	return false
}
