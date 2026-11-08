package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/mail"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestMailAttachmentConstantsMatchMail(t *testing.T) {
	if toolMailSaveAttachment != mail.ToolSaveAttachment || proto.MaxMailAttachment != mail.MaxSavedAttachment {
		t.Fatalf("node has %q / %d, mail %q / %d", toolMailSaveAttachment, proto.MaxMailAttachment, mail.ToolSaveAttachment, mail.MaxSavedAttachment)
	}
}

func fetchReturning(name string, data []byte, calls *int) func(context.Context, mailAttachmentArgs) (proto.MailAttachment, error) {
	return func(_ context.Context, a mailAttachmentArgs) (proto.MailAttachment, error) {
		*calls++
		if a.AccountID != "acc" || a.UID != 7 || a.AttachmentID != "2-abc" {
			return proto.MailAttachment{}, errors.New("wrong arguments")
		}
		return proto.MailAttachment{Filename: name, Mime: "application/pdf", Size: int64(len(data)), Data: data}, nil
	}
}

func saveArgs(dir string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"account_id": "acc", "uid": 7, "attachment_id": "2-abc", "dir": dir})
	return b
}

func resultOf(res proto.ToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// Saved under the root's subfolder with mode 0600; saving again never overwrites: the next one is numbered.
func TestSaveMailAttachment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "attachments", "mail")
	calls := 0
	fetch := fetchReturning("statement 2026-10.pdf", []byte("%PDF"), &calls)

	res := saveMailAttachment(context.Background(), root, saveArgs("bills/2026-10"), fetch)
	want := filepath.Join(root, "bills", "2026-10", "statement 2026-10.pdf")
	if res.IsError || !strings.Contains(resultOf(res), want) {
		t.Fatalf("save: %+v (want %s)", res, want)
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != "%PDF" {
		t.Fatalf("saved file: %q %v", b, err)
	}
	if st, _ := os.Stat(want); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}

	for i, name := range []string{"statement 2026-10 (2).pdf", "statement 2026-10 (3).pdf"} {
		res = saveMailAttachment(context.Background(), root, saveArgs("bills/2026-10/"), fetch)
		path := filepath.Join(root, "bills", "2026-10", name)
		if res.IsError || !strings.Contains(resultOf(res), path) {
			t.Fatalf("save %d: %+v", i+2, res)
		}
	}
	if b, _ := os.ReadFile(want); string(b) != "%PDF" {
		t.Fatalf("the first file changed: %q", b)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "bills", "2026-10")); len(entries) != 3 {
		t.Fatalf("files: %v", entries)
	}

	// A file someone else put there under the name is kept as it is.
	other := filepath.Join(root, "kept.pdf")
	if err := os.WriteFile(other, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = saveMailAttachment(context.Background(), root, saveArgs(""), fetchReturning("kept.pdf", []byte("%PDF"), &calls))
	if res.IsError || !strings.Contains(resultOf(res), filepath.Join(root, "kept (2).pdf")) {
		t.Fatalf("no dir: %+v", res)
	}
	if b, _ := os.ReadFile(other); string(b) != "mine" {
		t.Fatalf("overwrote an existing file: %q", b)
	}
}

func TestSaveMailAttachmentRejectsDirsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	calls := 0
	fetch := fetchReturning("a.pdf", []byte("x"), &calls)
	for _, dir := range []string{"/etc", "~/Library", "~", "../x", "..", "a/../../x", "a/..", "a/./b", ".ssh", "a/.hidden", `a\b`, `..\x`,
		"a//b", "a/ b", "a\x00b", "a\nb", "c:/x", strings.Repeat("a/", 300)} {
		if res := saveMailAttachment(context.Background(), root, saveArgs(dir), fetch); !res.IsError {
			t.Errorf("dir %q accepted: %+v", dir, res)
		}
	}
	if calls != 0 {
		t.Fatalf("fetched %d times for bad dirs (should refuse before asking the manager)", calls)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("bad dirs left something: %v", entries)
	}
}

// A symlinked folder inside the root that points elsewhere is refused, as the folder or on the way to it, and
// nothing is written or created outside; a symlink at the file's name is never followed.
func TestSaveMailAttachmentRefusesSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fetch := fetchReturning("a.pdf", []byte("x"), &calls)
	for _, dir := range []string{"link", "link/deeper/still"} {
		if res := saveMailAttachment(context.Background(), root, saveArgs(dir), fetch); !res.IsError || !strings.Contains(resultOf(res), "outside") {
			t.Fatalf("symlink escape %q: %+v", dir, res)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote outside the root: %v", entries)
	}

	// A link to a folder inside the root is fine.
	if err := os.Mkdir(filepath.Join(root, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if res := saveMailAttachment(context.Background(), root, saveArgs("alias"), fetch); res.IsError {
		t.Fatalf("link inside the root: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "real", "a.pdf")); err != nil {
		t.Fatal(err)
	}

	// A symlink already at the file's name (pointing out) is not written through: the save takes the next name.
	target := filepath.Join(outside, "target")
	if err := os.Symlink(target, filepath.Join(root, "real", "b.pdf")); err != nil {
		t.Fatal(err)
	}
	res := saveMailAttachment(context.Background(), root, saveArgs("real"), fetchReturning("b.pdf", []byte("x"), &calls))
	if res.IsError || !strings.Contains(resultOf(res), "b (2).pdf") {
		t.Fatalf("symlink at the name: %+v", res)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("wrote through the symlink: %v", err)
	}
}

func TestSaveMailAttachmentCleansSenderFilename(t *testing.T) {
	for name, want := range map[string]string{
		"../../.zshrc":                    "zshrc",
		`..\..\evil.exe`:                  "evil.exe",
		"/etc/passwd":                     "passwd",
		"...":                             "attachment",
		"":                                "attachment",
		"  ":                              "attachment",
		"a\nb:c.pdf":                      "a_b_c.pdf",
		"x\x00y\x7f.csv":                  "x_y_.csv",
		"bad\xffutf8.pdf":                 "badutf8.pdf",
		"账单.xlsx":                         "账单.xlsx",
		strings.Repeat("长", 120) + ".csv": strings.Repeat("长", 65) + ".csv", // 200 bytes at most, whole runes
	} {
		if got := cleanFilename(name); got != want {
			t.Errorf("cleanFilename(%q) = %q, want %q", name, got, want)
		}
	}

	root := t.TempDir()
	calls := 0
	res := saveMailAttachment(context.Background(), root, saveArgs("x"), fetchReturning("../../../escape.sh", []byte("x"), &calls))
	if res.IsError || !strings.Contains(resultOf(res), filepath.Join(root, "x", "escape.sh")) {
		t.Fatalf("traversal in the file name: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.sh")); !os.IsNotExist(err) {
		t.Fatalf("escaped the root: %v", err)
	}
}

// Over 25 MB is refused whatever the manager hands over, and nothing is written.
func TestSaveMailAttachmentRefusesOverTheLimit(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, proto.MaxMailAttachment+1)
	for _, got := range []proto.MailAttachment{
		{Filename: "big.zip", Size: int64(len(big)), Data: big},
		{Filename: "big.zip", Size: int64(len(big))},
	} {
		fetch := func(context.Context, mailAttachmentArgs) (proto.MailAttachment, error) { return got, nil }
		if res := saveMailAttachment(context.Background(), root, saveArgs(""), fetch); !res.IsError || !strings.Contains(resultOf(res), "25 MB") {
			t.Fatalf("over the limit: %+v", res)
		}
	}
	// Exactly the limit is fine.
	exact := big[:proto.MaxMailAttachment]
	fetch := func(context.Context, mailAttachmentArgs) (proto.MailAttachment, error) {
		return proto.MailAttachment{Filename: "edge.zip", Size: int64(len(exact)), Data: exact}, nil
	}
	if res := saveMailAttachment(context.Background(), root, saveArgs(""), fetch); res.IsError {
		t.Fatalf("at the limit: %+v", res)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 || entries[0].Name() != "edge.zip" {
		t.Fatalf("files: %v", entries)
	}
}

func TestSaveMailAttachmentErrors(t *testing.T) {
	root := t.TempDir()
	for _, args := range []string{`{`, `{"uid":7,"attachment_id":"2-abc"}`, `{"account_id":"acc","attachment_id":"2-abc"}`, `{"account_id":"acc","uid":7}`} {
		calls := 0
		if res := saveMailAttachment(context.Background(), root, json.RawMessage(args), fetchReturning("a", nil, &calls)); !res.IsError || calls != 0 {
			t.Errorf("args %s: %+v", args, res)
		}
	}
	// The manager refuses (not offered to this caller, too large at the source…): said, nothing written.
	refused := func(context.Context, mailAttachmentArgs) (proto.MailAttachment, error) {
		return proto.MailAttachment{}, errors.New("mail is not available")
	}
	if res := saveMailAttachment(context.Background(), root, saveArgs(""), refused); !res.IsError || !strings.Contains(resultOf(res), "not available") {
		t.Fatalf("refused: %+v", res)
	}
	short := func(context.Context, mailAttachmentArgs) (proto.MailAttachment, error) {
		return proto.MailAttachment{Filename: "a.pdf", Size: 10, Data: []byte("abc")}, nil
	}
	if res := saveMailAttachment(context.Background(), root, saveArgs(""), short); !res.IsError {
		t.Fatalf("incomplete data saved: %+v", res)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("left files behind: %v", entries)
	}
}
