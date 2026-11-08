package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// A scheduled task's "Push when done" (#2396, docs/assistant/assistant.md "Push when done"): each run is a turn in a conversation, and the
// hub pushes every finished turn (hub/machine.go notifyFinished). Right for a morning briefing, wrong for a task that watches
// something ("push me when there is new mail"): every run pushed "task name · no new mail". A task's push is then
//
//   - pushEvery (""): every run's answer is pushed, as before;
//   - pushAsked: only what the run pushes itself (notify_user); a run with nothing new is not pushed at all.
//
// Such a task always runs in its own conversation (ownConversationIfQuiet: a follow-up would run in the conversation it
// came from, often the main one), and that conversation's finished turns are not pushed when they end normally: the
// hub's finished push (hub quietTaskConversation, by the task list the snapshot reports) and the channels' mirror
// (quietTaskTurn) leave them out. Errors, cut-offs and a stuck turn are told as ever, and so is what the run sends with
// notify_user. A turn the user starts in that conversation is not pushed either: the price of judging by the
// conversation, not by the turn — which, across queued messages, background turns and compactions, the node can not
// tell for sure (three rounds of review on #2396).
const (
	pushEvery = ""
	pushAsked = assistantwire.PushAsked
)

// pushesOnlyAsked: the task's runs push only what they ask for (notify_user).
func (t Task) pushesOnlyAsked() bool { return t.Push == pushAsked }

// checkPush says whether p is a push the app may save.
func checkPush(p string) error {
	switch p {
	case pushEvery, pushAsked:
		return nil
	}
	return fmt.Errorf("跑完推送 %q 不认识（\"\" 每次 / \"asked\" 只在它自己提醒时）", p)
}

// pushArg is a scheduling tool's push argument: "every" (or empty) or "asked".
func pushArg(a string) (string, error) {
	switch a {
	case "", "every":
		return pushEvery, nil
	case pushAsked:
		return pushAsked, nil
	}
	return "", fmt.Errorf("push %q: use \"every\" (each run's answer is pushed) or \"asked\" (only what the run pushes with notify_user)", a)
}

// pushText is how list_followups and the tools' answers say a task's push.
func pushText(t Task) string {
	if t.pushesOnlyAsked() {
		return "pushes only what a run sends with notify_user (nothing new: no push); it runs in its own conversation"
	}
	return "each run's answer is pushed to the user"
}

// pushIn reads push off a request body: has says it was there. null or anything but a string is refused (400), not
// taken for "every".
func pushIn(raw map[string]json.RawMessage) (push string, has bool, err error) {
	v, ok := raw["push"]
	if !ok {
		return "", false, nil
	}
	if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		return "", true, errors.New(`跑完推送不能是 null（"" 每次 / "asked" 只在它自己提醒时）`)
	}
	if err := json.Unmarshal(v, &push); err != nil {
		return "", true, fmt.Errorf("跑完推送要是字符串：%w", err)
	}
	return push, true, checkPush(push)
}

// ownConversationIfQuiet: a task that pushes only what its runs send runs in its own conversation, never in the one it
// came from (the main one's turns would go unpushed with it).
func ownConversationIfQuiet(t *Task) {
	if t.pushesOnlyAsked() {
		t.SessionID = ""
	}
}

// quietConversation: session is the own conversation of a task that pushes only what its runs send. Another task
// does not run into it (runTask, toolScheduleFollowup): its turns would go unpushed there.
func (s *Service) quietConversation(ctx context.Context, session string) bool {
	if session == "" {
		return false
	}
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return false // nothing known
	}
	defer sc.mu.Unlock()
	for _, t := range sc.tasks {
		if t.pushesOnlyAsked() && t.RunSessionID == session {
			return true
		}
	}
	return false
}

// quietTaskTurn: session is a quiet task's own conversation whose turn may go unpushed now
// (assistantwire.QuietTaskFinish): the channels' mirror leaves it out, as the hub's finished push does.
func (s *Service) quietTaskTurn(ctx context.Context, machine, session string) bool {
	if s.supervising(session) { // a supervision message started its turn (supervise_digest.go): as the hub's finished push leaves it out
		sess, ok := s.session(ctx, machine, session)
		return ok && assistantwire.SupervisedFinish(sess, true)
	}
	if !s.quietConversation(ctx, session) {
		return false
	}
	main, known := s.knownMain(ctx)
	if !known {
		return false // which conversation is the main one is not known: mirrored as ever
	}
	sess, ok := s.session(ctx, machine, session)
	return ok && assistantwire.QuietTaskFinish(sess, main)
}

// knownMain is the main conversation's id, and whether the settings could be read to say: a read that failed is not
// "no main conversation" (savedSettings' zero value is that).
func (s *Service) knownMain(ctx context.Context) (string, bool) {
	v, ok := s.savedSettings(ctx)
	if !ok {
		return "", false
	}
	return s.effective(ctx, v).Main, true
}

// mayBeMain: session is the main conversation, or the settings could not be read to tell. A quiet task does not go on
// in such a conversation: the main one is never quiet.
func (s *Service) mayBeMain(ctx context.Context, session string) bool {
	main, known := s.knownMain(ctx)
	return !known || session == main
}
