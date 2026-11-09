// Package imessage reads the Messages app's data on a Mac (docs/plans/plan-connectors.md "5. iMessage connector"):
// ~/Library/Messages/chat.db and the address book for names, read-only, through macOS's own /usr/bin/sqlite3
// (personad is built without cgo). It runs in the node of the Mac the hub was told to read (imessage.machine): the
// files are protected by macOS (Full Disk Access), and nothing here writes to them, marks anything read or sends.
//
// Conversations are what the user sees in Messages: a person's chats over iMessage, SMS and RCS are one, a group is
// its own. Their ids are hashes (imessage-<12 hex>): no phone number in an id, a URL or a log line.
package imessage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// ID is the connector's id: the key in hub.json, the RPCs' "id".
const ID = "imessage"

// Reader reads one home's Messages data.
type Reader struct {
	Home string
	// SQLite is the sqlite3 to run; macOS's own when empty.
	SQLite string
	// Via is how this node runs, for the permission guide: "launchd" (a resident node: Full Disk Access goes to
	// personad) or "ssh" (it goes to sshd-keygen-wrapper, through the "Remote Login" switch).
	Via string
	// Executable is this personad's path, for the launchd guide.
	Executable string

	mu       sync.Mutex
	contacts *contacts
	loaded   time.Time
}

// Status is whether this Mac's messages can be read, and if not, how to fix it.
type Status struct {
	State string   `json:"status"` // ok | not_configured | no_permission | error
	Error string   `json:"error,omitempty"`
	Guide []string `json:"guide,omitempty"` // steps on this Mac, when State is no_permission
	Via   string   `json:"via,omitempty"`
}

// Person is someone in a conversation: their number or address, and their name in this Mac's contacts.
type Person struct {
	Handle string `json:"handle"`
	Name   string `json:"name,omitempty"`
}

// Chat is a conversation as the list shows it.
type Chat struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Group    bool      `json:"group,omitempty"`
	Members  []Person  `json:"members"` // the others; for a one-to-one chat, the one
	Services []string  `json:"services"`
	Last     time.Time `json:"last"`
	Preview  string    `json:"preview,omitempty"`
	FromMe   bool      `json:"from_me,omitempty"` // the last message is the user's
	Unread   int       `json:"unread,omitempty"`
}

// Message is one message, or a line about the group (Kind event).
type Message struct {
	ID          string       `json:"id"`
	At          time.Time    `json:"at"`
	Kind        string       `json:"kind"` // message | event
	FromMe      bool         `json:"from_me,omitempty"`
	Sender      *Person      `json:"sender,omitempty"` // who wrote it, when not the user
	Text        string       `json:"text,omitempty"`
	Service     string       `json:"service,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ReplyTo     string       `json:"reply_to,omitempty"` // the start of the message it answers
	Edited      bool         `json:"edited,omitempty"`
	Unsent      bool         `json:"unsent,omitempty"`
	Unread      bool         `json:"unread,omitempty"`
	// Undecoded: the text is archived in a form this reader does not understand; the app says so.
	Undecoded bool `json:"undecoded,omitempty"`
}

// Attachment is described, not copied: its file stays on the Mac.
type Attachment struct {
	Kind string `json:"kind"` // image | video | audio | file
	Name string `json:"name,omitempty"`
	Mime string `json:"mime,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// ChatsPage is a page of conversations, newest first; Before asks for the next one.
type ChatsPage struct {
	Chats  []Chat `json:"chats"`
	More   bool   `json:"more,omitempty"`
	Before string `json:"before,omitempty"`
}

// MessagesPage is a page of one conversation's messages, oldest first; Before asks for the older ones.
type MessagesPage struct {
	Chat     Chat      `json:"chat"`
	Messages []Message `json:"messages"`
	More     bool      `json:"more,omitempty"`
	Before   string    `json:"before,omitempty"`
}

var (
	// ErrNoPermission: macOS does not let this process read the Messages data (Full Disk Access).
	ErrNoPermission = errors.New("no permission to read the Messages data")
	// ErrNoChat: no conversation has that id.
	ErrNoChat = errors.New("no such conversation")
)

func (r *Reader) chatDB() string { return filepath.Join(r.Home, "Library", "Messages", "chat.db") }

func (r *Reader) sqlite() string {
	if r.SQLite != "" {
		return r.SQLite
	}
	return "/usr/bin/sqlite3"
}

// Status checks that chat.db is there and readable.
func (r *Reader) Status(ctx context.Context) Status {
	st := Status{State: connector.StateOK, Via: r.Via}
	if _, err := os.Stat(r.chatDB()); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return r.denied(st)
		}
		st.State, st.Error = connector.StateNotConfigured, "这台电脑上没有「信息」的数据"
		return st
	}
	var rows []struct{ N int }
	if err := r.query(ctx, r.chatDB(), "SELECT count(*) AS n FROM chat", &rows); err != nil {
		if errors.Is(err, ErrNoPermission) {
			return r.denied(st)
		}
		st.State, st.Error = connector.StateError, "读不了「信息」的数据："+err.Error()
	}
	return st
}

func (r *Reader) denied(st Status) Status {
	st.State = connector.StateNoPermission
	st.Error = "macOS 不让 Persona 读这台 Mac 上的「信息」：要给它「完全磁盘访问权限」"
	if r.Via == "launchd" {
		exe := r.Executable
		if exe == "" {
			exe = "personad"
		}
		st.Guide = []string{
			"在这台 Mac 上打开「系统设置 › 隐私与安全性 › 完全磁盘访问权限」",
			"点「+」，按 ⇧⌘G 输入 " + exe + "，选中它，打开它的开关",
			"回到 Persona 刷新。personad 更新后如果又读不了，把它的开关关了再打开",
		}
	} else {
		st.Guide = []string{
			"在这台 Mac 上打开「系统设置 › 通用 › 共享」",
			"点「远程登录」右边的 ⓘ，打开「允许远程用户完全访问磁盘」",
			"回到 Persona 刷新。注意：这样经 ssh 登录这台 Mac 的任何会话都能读整个磁盘",
		}
	}
	return st
}

// query runs one SELECT read-only and decodes its rows (sqlite3's -json) into out, a pointer to a slice of structs.
// Every value spliced into SQL is an integer or a checked identifier: nothing from a request reaches it as text.
func (r *Reader) query(ctx context.Context, db, sql string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.sqlite(), "-readonly", "-bail", "-json", "-cmd", ".timeout 3000", db)
	cmd.Stdin = strings.NewReader(sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		msg := strings.ToLower(stderr.String())
		switch {
		case strings.Contains(msg, "authorization denied"), strings.Contains(msg, "not permitted"),
			strings.Contains(msg, "unable to open database"):
			return ErrNoPermission
		case ctx.Err() != nil:
			return fmt.Errorf("sqlite3 took too long")
		}
		// The error names the database's path at most, never a row.
		return fmt.Errorf("sqlite3: %s", firstLine(stderr.String(), err))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil // no rows: sqlite3 prints nothing
	}
	return json.Unmarshal(raw, out)
}

func firstLine(s string, err error) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if s == "" {
		s = err.Error()
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// appleEpoch is 2001-01-01 UTC, where Messages counts its dates from: nanoseconds since macOS 10.13, seconds before.
const appleEpoch = 978307200

func appleTime(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	if v > 1e11 || v < -1e11 {
		return time.Unix(appleEpoch+v/1e9, v%1e9).UTC()
	}
	return time.Unix(appleEpoch+v, 0).UTC()
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func ints(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

// names are this Mac's contacts, read again every few minutes.
func (r *Reader) names(ctx context.Context) *contacts {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.contacts != nil && time.Since(r.loaded) < 5*time.Minute {
		return r.contacts
	}
	c, err := r.loadContacts(ctx)
	if err != nil {
		c = &contacts{} // no names: numbers are shown instead
	}
	r.contacts, r.loaded = c, time.Now()
	return c
}
