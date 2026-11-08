package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant archiving coding agents' sessions when the user asks (agent_archive, agent_unarchive): the same
// manager-owned, reversible archive the apps use (setAgentArchive, agent_archives.go), and with close the apps'
// "Archive and close" (archiveAndCloseAgentSession, agent_archive_close.go). Nothing is ever deleted.
//
// Archiving and restoring change nothing on the computer and are undone with one call, so they are not confirmed
// (as conversation_archive is not). Closing the agent on its computer is confirmed on the phone every time (a
// permission card, as persona_settings asks), and a busy agent is refused before the card is shown; the manager checks
// again when it closes. The answer says what happened in the words the assistant repeats to the user: archived or
// not, what the agent's own tool did (native archive), and whether the agent was closed, or why it is still open.

func (s *Service) toolAgentArchive(ctx context.Context, machine, self string, args json.RawMessage, archived bool) (string, error) {
	var a struct {
		Machine   string `json:"machine"`
		SessionID string `json:"session_id"`
		Close     bool   `json:"close"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	am, c, err := s.findAgent(ctx, a.Machine, a.SessionID)
	if err != nil {
		return "", err
	}
	if c.Assistant {
		return "", fmt.Errorf("that is a Persona conversation, not a coding agent: use conversation_archive / conversation_unarchive")
	}
	who := fmt.Sprintf("%s on %s (session %s)", agentLabel(c), am.Name, c.SessionID)
	switch {
	case !archived:
		if !c.Archived {
			return fmt.Sprintf("%s is not archived: it is in the Agents list already. Nothing changed.", who), nil
		}
		if _, err := s.setAgentArchive(ctx, am.ID, c.SessionID, false); err != nil {
			return "", archiveToolError(who, err, false)
		}
		s.logf("agent_unarchive %s on %s (from %s)", short(c.SessionID), am.ID, short(self))
		return fmt.Sprintf("Restored %s to the Agents list. Nothing was reopened on the computer; a message to it (agent_send) resumes it.%s",
			who, nativeArchiveWords(s.nativeArchiveAwaited(ctx, am.ID, c.SessionID))), nil
	case a.Close:
		return s.archiveAndCloseTool(ctx, machine, self, am, c, who, args)
	case c.Archived:
		return fmt.Sprintf("%s was archived already. Nothing changed.%s", who, nativeArchiveWords(s.nativeArchiveView(am.ID, c.SessionID))), nil
	}
	if _, err := s.setAgentArchive(ctx, am.ID, c.SessionID, true); err != nil {
		return "", archiveToolError(who, err, false)
	}
	s.logf("agent_archive %s on %s (from %s)", short(c.SessionID), am.ID, short(self))
	still := ""
	if c.Status != proto.StatusOffline {
		still = " Its agent was not closed: it is still open on the computer (agent_archive with close closes it, once the user confirms)."
	}
	return fmt.Sprintf("Archived %s: it left the Agents list and is under Archived in the app; nothing was deleted, and agent_unarchive "+
		"brings it back. New activity in it brings it back by itself.%s%s", who, still,
		nativeArchiveWords(s.nativeArchiveAwaited(ctx, am.ID, c.SessionID))), nil
}

// archiveAndCloseTool is agent_archive with close: refused when busy, confirmed on the phone, then archived and closed.
func (s *Service) archiveAndCloseTool(ctx context.Context, machine, self string, am Device, c proto.Session, who string, args json.RawMessage) (string, error) {
	if agentCloseBusy(c) {
		return "", archiveToolError(who, archiveRefusal(0, "agent_busy", errors.New("busy")), true)
	}
	l := s.lang(ctx)
	now := time.Now().UTC()
	ix := proto.Interaction{ID: "ix_" + randomHex(6), MachineID: machine, SessionID: self, Kind: proto.IxPermission, ToolName: "agent_archive",
		Summary: i18n.T(l, "assistant-tools.archive_close_card", i18n.Args{"agent": agentLabel(c), "machine": am.Name}), ToolInput: args,
		SessionTitle: "Persona", CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	if conv, ok := s.session(ctx, machine, self); ok {
		ix.Cwd = conv.Cwd
	}
	r, err := s.ask(ctx, ix)
	if errors.Is(err, errUserSpoke) {
		return "The user wrote in the conversation instead of confirming: nothing changed (not archived, not closed).", nil
	}
	if err != nil {
		return "", err
	}
	if r.Allow == nil || !*r.Allow {
		return "The user declined on the phone (or did not answer): nothing changed (not archived, not closed).", nil
	}
	res, err := s.archiveAndCloseAgentSession(ctx, am.ID, c.SessionID)
	if err != nil {
		return "", archiveToolError(who, err, true)
	}
	s.logf("agent_archive close %s on %s (from %s): %s", short(c.SessionID), am.ID, short(self), res.Outcome)
	native := nativeArchiveWords(s.nativeArchiveAwaited(ctx, am.ID, c.SessionID))
	switch res.Outcome {
	case archiveCloseClosed:
		return fmt.Sprintf("Archived %s and closed its agent on the computer (close: closed): its tab and process are gone, its transcript "+
			"stays, and a message to it resumes it. agent_unarchive brings it back to the list.%s", who, native), nil
	case archiveCloseNotRunning:
		return fmt.Sprintf("Archived %s. It was not running on the computer, so there was nothing to close (close: not_running).%s", who, native), nil
	}
	why := i18n.T(l, res.ReasonKey)
	if res.Detail != "" {
		why += " (" + clip(oneLine(res.Detail), 200) + ")"
	}
	return fmt.Sprintf("Archived %s, but its agent is still open on the computer (close: %s): %s. Tell the user so: they can close it on "+
		"the computer, or restore it with agent_unarchive.%s", who, res.Outcome, why, native), nil
}

// archiveToolError is a refused or failed archive in the assistant's words: nothing changed, and why.
func archiveToolError(who string, err error, closing bool) error {
	var refusal *agentArchiveError
	if !errors.As(err, &refusal) {
		return fmt.Errorf("the archive of %s could not be saved, nothing changed: %w", who, err)
	}
	if refusal.code != "agent_busy" {
		return refusal.err
	}
	if closing {
		return fmt.Errorf("refused: %s is busy (working, waiting for an answer, background work, a /loop, queued messages or a server it "+
			"runs), and closing it would cut that off. Nothing changed. Wait until it is done, or archive it without close", who)
	}
	return fmt.Errorf("refused: %s is busy (working, waiting for an answer or running background work). Nothing changed; it can be "+
		"archived once it is idle", who)
}

// nativeArchiveWords say what the agent's own tool did with Persona's archive or restore ("" when it was not asked).
func nativeArchiveWords(n *proto.NativeArchive) string {
	if n == nil {
		return ""
	}
	what := "archive"
	if !n.Archived {
		what = "restore"
	}
	switch n.State {
	case nativeArchiveDone:
		return fmt.Sprintf(" Its own tool has the %s too (native: done).", what)
	case nativeArchiveUnsupported:
		return " Its own tool has no archive Persona can drive, so the archive is Persona's alone (native: unsupported)."
	}
	out := fmt.Sprintf(" Its own tool has not followed the %s yet; Persona tries again by itself (native: pending", what)
	if e := strings.TrimSpace(n.Error); e != "" {
		out += ", " + clip(oneLine(e), 200)
	}
	return out + ")."
}
