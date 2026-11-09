package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// fakeFiles is what the nodes answer for attachments (#2437): files on the assistant's computer ("server") read with
// file.chunk, uploads on any other written with file.upload.*.
type fakeFiles struct {
	mu        sync.Mutex
	on        map[string][]byte // path on "server" → contents
	uploads   []map[string]any  // file.upload.begin params, with "machine"
	chunks    int               // file.chunk reads that carried data
	uploadErr error             // file.upload.begin's answer when set (a node too old for uploads)
}

func newFakeFiles(f *fakeHub, on map[string][]byte) *fakeFiles {
	ff := &fakeFiles{on: on}
	f.answerNode("file.chunk", func(machine string, raw json.RawMessage) (any, error) {
		var req proto.FileChunkRequest
		_ = json.Unmarshal(raw, &req)
		ff.mu.Lock()
		defer ff.mu.Unlock()
		data, ok := ff.on[req.Path]
		if machine != "server" || !ok {
			return nil, errors.New("no such file")
		}
		out := proto.FileChunk{Path: req.Path, Name: req.Path[strings.LastIndex(req.Path, "/")+1:], Size: int64(len(data)), Revision: "r1"}
		if strings.HasSuffix(req.Path, ".png") {
			out.Mime = "image/png"
		}
		if req.Length > 0 {
			ff.chunks++
			out.Data = data[req.Offset:min(int64(len(data)), req.Offset+int64(req.Length))]
		}
		return out, nil
	})
	f.answerNode("file.upload.begin", func(machine string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		p["machine"] = machine
		ff.mu.Lock()
		defer ff.mu.Unlock()
		if ff.uploadErr != nil {
			return nil, ff.uploadErr
		}
		ff.uploads = append(ff.uploads, p)
		return map[string]any{"upload_id": "u" + p["name"].(string)}, nil
	})
	f.answerNode("file.upload.chunk", func(string, json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerNode("file.upload.end", func(machine string, raw json.RawMessage) (any, error) {
		var p struct {
			UploadID string `json:"upload_id"`
		}
		_ = json.Unmarshal(raw, &p)
		return proto.UploadedFile{Path: "/home/me/.persona/uploads/terminal/2026-11-09/ab12-" + strings.TrimPrefix(p.UploadID, "u")}, nil
	})
	return ff
}

// The tools take only absolute paths, at most attachmentsMost, repeats dropped.
func TestCleanAttachments(t *testing.T) {
	got, err := cleanAttachments([]string{" /a.png ", "/a.png", "", "~/b.txt"})
	if err != nil || len(got) != 2 || got[0] != "/a.png" || got[1] != "~/b.txt" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := cleanAttachments([]string{"notes.txt"}); err == nil {
		t.Fatal("a relative path should be refused")
	}
	many := make([]string, attachmentsMost+1)
	for i := range many {
		many[i] = "/f" + string(rune('a'+i))
	}
	if _, err := cleanAttachments(many); err == nil {
		t.Fatal("too many should be refused")
	}
}

// Only where attachments live: this conversation's pictures (Claude Code's images/ of it) and paths the user's
// messages carry. Another conversation's pictures or any other file on the computer is refused.
func TestCheckAttachmentsAcceptsOnlyWhatTheUserGave(t *testing.T) {
	s, f := newTaskService(t)
	ctx := context.Background()
	f.sessions["server"] = nil
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "看下这个日志 /tmp/build-20261109-120000.log"})
	ok, err := s.checkAttachments(ctx, "server", "conv1", []string{"/tmp/claude-1000/-home-me/conv1/images/1.png", "/tmp/build-20261109-120000.log"})
	if err != nil || len(ok) != 2 {
		t.Fatalf("%q %v", ok, err)
	}
	for _, bad := range []string{"/tmp/claude-1000/-home-me/conv2/images/1.png", "/home/me/.ssh/id_ed25519"} {
		if _, err := s.checkAttachments(ctx, "server", "conv1", []string{bad}); err == nil || !strings.Contains(err.Error(), "not one the user gave you") {
			t.Fatalf("%s: %v", bad, err)
		}
	}
}

// On the assistant's own computer the paths go as they are, nothing read or copied; the picture ends the message in
// the <attached_images> block a picture sent straight to an agent ends with.
func TestAttachmentsOnTheSameComputerAreNotCopied(t *testing.T) {
	s, f := newTaskService(t)
	f.sessions["server"] = nil
	ff := newFakeFiles(f, nil)
	h := s.handAttachments(context.Background(), "server", "server", []string{"/tmp/notes-20261109-120000.txt", "/x/conv1/images/1.png"}, nil)
	if ff.chunks != 0 || len(ff.uploads) != 0 || h.Copied || len(h.Missing) != 0 {
		t.Fatalf("copied on the same computer: %+v %+v", h, ff.uploads)
	}
	got := withAttachments("看图修 bug", h)
	want := "看图修 bug\n\n用户给的文件：\n- /tmp/notes-20261109-120000.txt\n<attached_images>\n/x/conv1/images/1.png\npath: /x/conv1/images/1.png\n</attached_images>"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// On another computer each file is read from the assistant's and uploaded into the agent's uploads/terminal; what
// cannot come (gone, too big, a node too old) is named instead, and the hand-off still goes.
func TestAttachmentsAreCopiedToAnotherComputer(t *testing.T) {
	s, f := newTaskService(t)
	f.sessions["server"] = nil
	big := make([]byte, attachmentMostBytes+1)
	ff := newFakeFiles(f, map[string][]byte{"/x/conv1/images/1.png": []byte("png-bytes"), "/tmp/n-20261109-120000.txt": []byte("SECRET"), "/tmp/big.bin": big})
	h := s.handAttachments(context.Background(), "server", "box", []string{"/x/conv1/images/1.png", "/tmp/n-20261109-120000.txt", "/tmp/gone.txt", "/tmp/big.bin"}, nil)
	if !h.Copied || len(h.Pictures) != 1 || len(h.Files) != 1 || len(h.Missing) != 2 {
		t.Fatalf("%+v", h)
	}
	if len(ff.uploads) != 2 || ff.uploads[0]["machine"] != "box" || ff.uploads[0]["dest"] != "terminal" {
		t.Fatalf("uploads %+v", ff.uploads)
	}
	text := h.text()
	if !strings.Contains(text, "（已从助手那台电脑复制到这台）") || !strings.Contains(text, "/home/me/.persona/uploads/terminal/2026-11-09/ab12-1.png") ||
		!strings.Contains(text, "/tmp/gone.txt（") || !strings.Contains(text, "over 50 MB") || !strings.HasSuffix(text, "</attached_images>") {
		t.Fatalf("text:\n%s", text)
	}
	if said := h.said(); !strings.Contains(said, "2 handed (copied") || !strings.Contains(said, "not handed: /tmp/gone.txt") {
		t.Fatalf("said %q", said)
	}

	ff.uploadErr = errors.New("unknown method file.upload.begin")
	h = s.handAttachments(context.Background(), "server", "box", []string{"/tmp/n-20261109-120000.txt"}, nil)
	if len(h.Files) != 0 || len(h.Missing) != 1 || !strings.Contains(h.Missing[0], "unknown method") {
		t.Fatalf("an old node: %+v", h)
	}
}

// A task's attachments are checked when the plan is made (AttachFrom is the conversation's computer, whatever the
// call says) and copied to its computer when it starts, ending its first message.
func TestTaskPlanHandsAttachmentsToItsAgent(t *testing.T) {
	s, f := newTaskService(t)
	ctx := context.Background()
	f.sessions["server"] = nil
	ff := newFakeFiles(f, map[string][]byte{"/x/conv1/images/1.png": []byte("png-bytes")})
	bad, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"title": "看图", "prompt": "看看这张图", "machine": "box", "cwd": "/code/app",
		"readonly": true, "why": "-", "attachments": []string{"/home/me/.ssh/id_ed25519"}}}})
	if _, err := s.toolTaskPlanFrom(ctx, "server", "conv1", bad); err == nil || !strings.Contains(err.Error(), "task 1: attachment") {
		t.Fatalf("a disallowed attachment: %v", err)
	}
	good, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"title": "看图", "prompt": "看看这张图", "machine": "box", "cwd": "/code/app",
		"readonly": true, "why": "-", "attach_from": "box", "attachments": []string{"/x/conv1/images/1.png"}}}})
	if _, err := s.toolTaskPlanFrom(ctx, "server", "conv1", good); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the agent to start", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.starts) == 1 })
	f.mu.Lock()
	prompt, _ := f.starts[0]["prompt"].(string)
	f.mu.Unlock()
	if !strings.HasPrefix(prompt, "看看这张图") || !strings.HasSuffix(prompt, "<attached_images>\n/home/me/.persona/uploads/terminal/2026-11-09/ab12-1.png\npath: /home/me/.persona/uploads/terminal/2026-11-09/ab12-1.png\n</attached_images>") {
		t.Fatalf("prompt:\n%s", prompt)
	}
	if len(ff.uploads) != 1 || ff.uploads[0]["machine"] != "box" {
		t.Fatalf("uploads %+v", ff.uploads)
	}
}

// agent_send copies a picture and a file to the agent's computer and ends the text with them, the picture in the
// <attached_images> block a picture sent straight to the agent ends with.
func TestAgentSendHandsAttachments(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "/tmp/n-20261109-120000.txt 也一起给它"})
	newFakeFiles(f, map[string][]byte{"/x/conv1/images/1.png": []byte("png-bytes"), "/tmp/n-20261109-120000.txt": []byte("SECRET")})
	f.setSessions("box", proto.Session{SessionID: "s1abcdef", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle})
	args := json.RawMessage(`{"machine":"box","session_id":"s1abcdef","text":"看这张图和这个文件","attachments":["/x/conv1/images/1.png","/tmp/n-20261109-120000.txt"]}`)
	res, err := s.runTool(ctx, "server", "conv1", "agent_send", args)
	if err != nil {
		t.Fatal(err)
	}
	texts := sentToAgent(f)
	text := texts[len(texts)-1]
	if !strings.HasPrefix(text, "看这张图和这个文件\n\n用户给的文件（已从助手那台电脑复制到这台）：\n- /home/me/.persona/uploads/terminal/2026-11-09/ab12-n-20261109-120000.txt") ||
		!strings.HasSuffix(text, "<attached_images>\n/home/me/.persona/uploads/terminal/2026-11-09/ab12-1.png\npath: /home/me/.persona/uploads/terminal/2026-11-09/ab12-1.png\n</attached_images>") ||
		!strings.Contains(res, "Attachments: 2 handed") {
		t.Fatalf("sent %q res %q", text, res)
	}
	if _, err := s.runTool(ctx, "server", "conv1", "agent_send", json.RawMessage(`{"machine":"box","session_id":"s1abcdef","text":"x","attachments":["/etc/passwd"]}`)); err == nil ||
		!strings.Contains(err.Error(), "nothing was sent") {
		t.Fatalf("a disallowed attachment: %v", err)
	}
}

// A model does not always use the parameter: a path of the user's attachment the text names (a file the app uploaded,
// a picture of this conversation) is copied all the same and the text names the copy. Any other path stays as written:
// a file of a repository the user named is the agent's to open where it is, not a copy.
func TestAgentSendCopiesTheAttachmentsItsTextNames(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "/tmp/n-20261109-120000.txt 交给它，顺便看下 /code/app/main.go"})
	ff := newFakeFiles(f, map[string][]byte{"/tmp/n-20261109-120000.txt": []byte("SECRET"), "/code/app/main.go": []byte("package main")})
	f.setSessions("box", proto.Session{SessionID: "s1abcdef", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle})
	args := json.RawMessage(`{"machine":"box","session_id":"s1abcdef","text":"读一下 /tmp/n-20261109-120000.txt 里的暗号，再看 /code/app/main.go。"}`)
	if _, err := s.runTool(ctx, "server", "conv1", "agent_send", args); err != nil {
		t.Fatal(err)
	}
	texts := sentToAgent(f)
	text := texts[len(texts)-1]
	copied := "/home/me/.persona/uploads/terminal/2026-11-09/ab12-n-20261109-120000.txt"
	if !strings.HasPrefix(text, "读一下 "+copied+" 里的暗号，再看 /code/app/main.go。") || strings.Contains(text, "/tmp/n-20261109-120000.txt") || len(ff.uploads) != 1 {
		t.Fatalf("sent %q uploads %+v", text, ff.uploads)
	}
}

// The paths a text names that are the user's attachments: this conversation's pictures and the files the app uploaded
// that a message of theirs carries; not another conversation's, not one they never sent, not a repository's file.
func TestMentionedAttachments(t *testing.T) {
	s, f := newTaskService(t)
	f.sessions["server"] = nil
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "/tmp/a-20261109-120000.txt 还有 /code/app/main.go"})
	text := "看图 /x/conv1/images/1.png，读 /tmp/a-20261109-120000.txt。别看 /x/conv2/images/1.png 和 /tmp/b-20261109-120000.txt，改 /code/app/main.go"
	got := s.mentionedAttachments(context.Background(), "server", "conv1", text, []string{"/x/other.png"})
	if strings.Join(got, ",") != "/x/conv1/images/1.png,/tmp/a-20261109-120000.txt" {
		t.Fatalf("%q", got)
	}
}
