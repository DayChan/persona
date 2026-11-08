package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// mail_save_attachment is answered here, on the machine the agent runs on: the manager fetches the attachment (the
// mail.attachment request, at most proto.MaxMailAttachment, only for a caller the mail connector offers the tool to)
// and this node writes it under its own data directory, <data dir>/attachments/mail/<dir>/<file>
// (docs/extensions/mail.md "Saving attachments"). The agent picks only a relative subfolder and the file name comes
// from the mail's sender, so both are cleaned: nothing lands outside that folder, nothing is overwritten.

// toolMailSaveAttachment is mail.ToolSaveAttachment (a test keeps them equal; the node does not take in the mail
// package for it).
const toolMailSaveAttachment = "mail_save_attachment"

// mailAttachmentRoot is where saved attachments go (swapped out by tests).
var mailAttachmentRoot = func() string { return filepath.Join(identity.HomeDir(), "attachments", "mail") }

type mailAttachmentArgs struct {
	AccountID    string `json:"account_id"`
	UID          uint32 `json:"uid"`
	Mailbox      string `json:"mailbox"`
	AttachmentID string `json:"attachment_id"`
	Dir          string `json:"dir"`
}

// saveMailAttachment is the tool for this node's caller: session is its conversation, assistant and guest what the
// caller is, passed on so the manager can check them as it does an mcp.call.
func (n *Node) saveMailAttachment(ctx context.Context, args json.RawMessage, session string, assistant, guest bool) proto.ToolResult {
	return saveMailAttachment(ctx, mailAttachmentRoot(), args, func(ctx context.Context, a mailAttachmentArgs) (proto.MailAttachment, error) {
		var out proto.MailAttachment
		if n.peer == nil {
			return out, errors.New("Persona hub is not connected")
		}
		err := n.peer.Call(ctx, proto.MethodMailAttachment, proto.MailAttachmentRequest{AccountID: a.AccountID, UID: a.UID,
			Mailbox: a.Mailbox, AttachmentID: a.AttachmentID, SessionID: session, Assistant: assistant, Guest: guest}, &out)
		return out, err
	})
}

func saveMailAttachment(ctx context.Context, root string, args json.RawMessage,
	fetch func(context.Context, mailAttachmentArgs) (proto.MailAttachment, error)) proto.ToolResult {
	var a mailAttachmentArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return proto.TextResult("invalid arguments: "+err.Error(), true)
	}
	if a.AccountID == "" || a.UID == 0 || a.AttachmentID == "" {
		return proto.TextResult("account_id, uid and attachment_id are required (attachment ids are listed by mail_read)", true)
	}
	sub, err := cleanSubdir(a.Dir)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	got, err := fetch(ctx, a)
	if err != nil {
		return proto.TextResult("could not fetch the attachment: "+err.Error(), true)
	}
	if got.Size > proto.MaxMailAttachment || int64(len(got.Data)) > proto.MaxMailAttachment {
		return proto.TextResult(fmt.Sprintf("the attachment is larger than %d MB and was not saved", proto.MaxMailAttachment>>20), true)
	}
	if int64(len(got.Data)) != got.Size {
		return proto.TextResult("could not fetch the attachment: it arrived incomplete", true)
	}
	path, err := writeAttachment(root, sub, cleanFilename(got.Filename), got.Data)
	if err != nil {
		return proto.TextResult("could not save the attachment: "+err.Error(), true)
	}
	mime := got.Mime
	if mime == "" {
		mime = "unknown type"
	}
	return proto.TextResult(fmt.Sprintf("Saved %q (%s, %d bytes) to %s", got.Filename, mime, got.Size, path), false)
}

// cleanSubdir is dir as a relative path of plain folder names, or why it is not one.
func cleanSubdir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", nil
	}
	bad := func() (string, error) {
		return "", fmt.Errorf("dir must be a relative subfolder such as bills/2026-10 (no absolute path, '~', '..', hidden folders or backslashes): %q", dir)
	}
	if filepath.IsAbs(dir) || strings.HasPrefix(dir, "~") || strings.ContainsRune(dir, '\\') || len(dir) > 512 {
		return bad()
	}
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	for _, p := range parts {
		if p == "" || strings.HasPrefix(p, ".") || !utf8.ValidString(p) || strings.ContainsFunc(p, unicode.IsControl) ||
			strings.ContainsRune(p, ':') || strings.TrimSpace(p) != p {
			return bad()
		}
	}
	return filepath.Join(parts...), nil
}

// maxFilenameBytes keeps a name (with " (99)" added) under the 255 bytes file systems allow.
const maxFilenameBytes = 200

// cleanFilename is the sender's file name as one plain name in a folder: no path, no control characters, not hidden.
func cleanFilename(name string) string {
	name = strings.ToValidUTF8(name, "")
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ':' {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimLeft(strings.TrimSpace(name), ".")
	if strings.TrimSpace(name) == "" {
		name = "attachment"
	}
	if len(name) > maxFilenameBytes {
		ext := filepath.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		stem := name[:len(name)-len(ext)]
		cut := maxFilenameBytes - len(ext)
		for cut > 0 && !utf8.RuneStart(stem[cut]) {
			cut--
		}
		name = stem[:cut] + ext
	}
	return name
}

// writeAttachment writes data to root/sub/name, or "name (2).ext"… when that exists, and returns the path.
func writeAttachment(root, sub, name string, data []byte) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	// Folder by folder, so that a symlink someone made inside the root is seen before anything is created through it.
	dir := realRoot
	for _, part := range strings.Split(sub, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next := filepath.Join(dir, part)
		if err := os.Mkdir(next, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(next)
		if err != nil {
			return "", err
		}
		if !insideRoot(realRoot, resolved) {
			return "", fmt.Errorf("%s is outside %s", filepath.Join(root, sub), root)
		}
		if st, err := os.Stat(resolved); err != nil || !st.IsDir() {
			return "", fmt.Errorf("%s is not a folder", filepath.Join(root, sub))
		}
		dir = resolved
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i <= 99; i++ {
		candidate := name
		if i > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		path := filepath.Join(dir, candidate)
		// O_EXCL: never overwrites, and does not follow a symlink already at path.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			os.Remove(path)
			return "", err
		}
		if err := f.Close(); err != nil {
			os.Remove(path)
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("%s and 98 numbered copies already exist in %s", name, dir)
}

// insideRoot says whether path is root or inside it (both real paths).
func insideRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
