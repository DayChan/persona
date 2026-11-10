package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeFolder(t *testing.T) {
	long := strings.Repeat("x", 50)
	for in, want := range map[string]string{
		"":                           "",
		"  ":                         "",
		"work/alerts/engine":         "work/alerts/engine",
		" /work//alerts/ ":           "work/alerts",
		"work /  build   bot ":       "work/build bot",
		"../etc/./passwd":            "etc/passwd",
		`a\b`:                        "a/b",
		"a\tb/c\nd":                  "a b/c d",
		"Projects/Foo":               "Projects/Foo",
		"a/b/c/d/e/f/g/h":            "a/b/c/d/e/f",
		long:                         strings.Repeat("x", folderMaxSegment),
		"工作/告警":                      "工作/告警",
		"/":                          "",
		strings.Repeat(long+"/", 10): strings.TrimSuffix(strings.Repeat(strings.Repeat("x", 40)+"/", 4), "/"),
	} {
		if got := normalizeFolder(in); got != want {
			t.Errorf("normalizeFolder(%q) = %q, want %q", in, got, want)
		}
		if got := normalizeFolder(in); len(got) > folderMaxBytes {
			t.Errorf("normalizeFolder(%q) is %d bytes", in, len(got))
		}
	}
}

// folderHub is an inbox with folders on and n open items f1…fn, newest first.
func folderHub(t *testing.T, n int) *inboxHub {
	t.Helper()
	h := newInboxHub(t)
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.inbox.folderCfg = InboxFolderConfig{Enabled: true}
	at := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("f%d", i)
		h.inbox.items[id] = &InboxItem{ID: id, Kind: KindMail, Title: "Build failed #" + id, Sender: "ci@example.com",
			Preview: "The build failed", Last: "1", Context: "x", UpdatedAt: at.Add(-time.Duration(i) * time.Minute)}
	}
	return h
}

func TestFileFoldersInBatches(t *testing.T) {
	ctx := context.Background()
	h := folderHub(t, 25)
	answer := map[string]string{}
	for i := 1; i <= 25; i++ {
		answer[fmt.Sprintf("f%d", i)] = " work / alerts /ci "
	}
	answer["f2"] = "../" // nothing usable: asked, left unfiled
	answer["zz"] = "a/b" // not asked: ignored
	delete(answer, "f3") // not answered: asked, left unfiled
	raw, _ := json.Marshal(map[string]any{"folders": answer})
	model, calls := captureModel(t, string(raw))
	h.in.model = model
	// one item the user filed by hand: never asked about, shown as an example
	h.inbox.items["mine"] = &InboxItem{ID: "mine", Kind: KindDM, Title: "Release plan", Sender: "Example Person", Last: "1",
		Folder: "projects/example/releases", FolderBy: FolderByUser, UpdatedAt: time.Now()}

	h.fileFolders(ctx)
	got := calls()
	if len(got) != 2 {
		t.Fatalf("%d model calls, want 2 (20 + 5)", len(got))
	}
	if !strings.Contains(got[0].User, "projects/example/releases（1）") || !strings.Contains(got[0].User, "Release plan（Example Person） → projects/example/releases") {
		t.Errorf("the folders in use and the user's own moves are not shown:\n%s", got[0].User)
	}
	if strings.Contains(got[0].User, "id：mine") || strings.Contains(got[1].User, "id：mine") {
		t.Error("an item the user filed was sent to the model")
	}
	if n := strings.Count(got[0].User, "\nid："); n != 20 {
		t.Errorf("first batch has %d items", n)
	}
	for id, it := range h.inbox.items {
		switch {
		case id == "mine":
			if it.Folder != "projects/example/releases" || it.FolderBy != FolderByUser {
				t.Errorf("the user's folder changed: %q by %q", it.Folder, it.FolderBy)
			}
		case id == "f2" || id == "f3":
			if it.Folder != "" || it.FolderBy != "" || it.FolderOf != "1" {
				t.Errorf("%s: %q by %q of %q, want unfiled and asked", id, it.Folder, it.FolderBy, it.FolderOf)
			}
		default:
			if it.Folder != "work/alerts/ci" || it.FolderBy != FolderByModel {
				t.Errorf("%s: %q by %q", id, it.Folder, it.FolderBy)
			}
			if !h.st.saved(id) {
				t.Errorf("%s was not saved", id)
			}
		}
	}
	// asked about what they hold: the next pass asks nothing
	h.fileFolders(ctx)
	if n := len(calls()); n != 2 {
		t.Errorf("a second pass asked again: %d calls", n)
	}
	// new messages in an unfiled item: it is asked again; a filed one keeps its folder
	h.inbox.items["f3"].Last = "2"
	h.inbox.items["f4"].Last = "2"
	h.fileFolders(ctx)
	last := calls()[len(calls())-1]
	if len(calls()) != 3 || !strings.Contains(last.User, "id：f3") || strings.Contains(last.User, "id：f4") {
		t.Errorf("after new messages: %d calls, last:\n%s", len(calls()), last.User)
	}
}

func TestFileFoldersOffAsksNothing(t *testing.T) {
	h := folderHub(t, 3)
	h.inbox.folderCfg.Enabled = false
	model, calls := captureModel(t, `{"folders":{}}`)
	h.in.model = model
	h.fileFolders(context.Background())
	h.fileFoldersSoon(context.Background())
	if n := len(calls()); n != 0 {
		t.Fatalf("%d model calls with folders off", n)
	}
}

func TestFileFoldersModelFails(t *testing.T) {
	h := folderHub(t, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadRequest) }))
	t.Cleanup(srv.Close)
	h.in.model = modelAt(t, srv.URL)
	h.fileFolders(context.Background())
	for id, it := range h.inbox.items {
		if it.Folder != "" || it.FolderOf != "" {
			t.Errorf("%s: %q of %q after a failed call: the next pass must ask again", id, it.Folder, it.FolderOf)
		}
	}
}

// The assistant's inbox_folders and inbox_move (#3099): the user's move, checked before anything moves.
func TestInboxFolderTools(t *testing.T) {
	ctx := context.Background()
	h := folderHub(t, 3)
	h.inbox.folderCfg.Enabled = false
	run := func(name, args string) (string, error) { return h.runTool(ctx, "", "", name, json.RawMessage(args)) }

	if _, err := run("inbox_move", `{"items":["f1","nope"],"folder":"work"}`); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := run("inbox_move", `{"items":["f1"],"folder":"/../"}`); err == nil {
		t.Fatal("a path of nothing was taken")
	}
	if _, err := run("inbox_move", `{"items":["f1"]}`); err == nil {
		t.Fatal("no folder was taken")
	}
	if it := h.inbox.items["f1"]; it.Folder != "" || it.FolderBy != "" {
		t.Fatalf("a refused move moved f1: %q by %q", it.Folder, it.FolderBy)
	}

	out, err := run("inbox_move", `{"items":["f1","f2","f1"],"folder":" work//alerts "}`)
	if err != nil || !strings.Contains(out, "Moved 2 items into work/alerts") {
		t.Fatalf("move: %q %v", out, err)
	}
	for _, id := range []string{"f1", "f2"} {
		if it := h.inbox.items[id]; it.Folder != "work/alerts" || it.FolderBy != FolderByUser {
			t.Errorf("%s: %q by %q", id, it.Folder, it.FolderBy)
		}
	}
	if _, err := run("inbox_move", `{"items":["f2"],"folder":"work/alerts/ci"}`); err != nil {
		t.Fatal(err)
	}

	out, err = run("inbox_folders", `{}`)
	if err != nil || !strings.Contains(out, "Automatic filing is off") || !strings.Contains(out, "- work/alerts (1, 1 open)") ||
		!strings.Contains(out, "- work/alerts/ci (1, 1 open)") || strings.Contains(out, "id=") {
		t.Fatalf("folders: %q %v", out, err)
	}
	out, _ = run("inbox_folders", `{"folder":"work/alerts"}`)
	if !strings.Contains(out, "2 items in work/alerts and below") || !strings.Contains(out, "id=f1 ") ||
		!strings.Contains(out, "id=f2 ") || !strings.Contains(out, "folder work/alerts/ci") || strings.Contains(out, "id=f3") {
		t.Errorf("items in a folder: %q", out)
	}
	out, _ = run("inbox_folders", `{"unfiled":true}`)
	if !strings.Contains(out, "1 item in no folder") || !strings.Contains(out, "id=f3 ") || strings.Contains(out, "id=f1") {
		t.Errorf("unfiled items: %q", out)
	}

	// "" takes them out of every folder, and the model leaves them alone
	if _, err := run("inbox_move", `{"items":["f1"],"folder":""}`); err != nil {
		t.Fatal(err)
	}
	if it := h.inbox.items["f1"]; it.Folder != "" || it.FolderBy != FolderByUser || it.unfiled() {
		t.Errorf("out of every folder: %q by %q", it.Folder, it.FolderBy)
	}

	// a failed write is said, and the item stays where it was
	h.st.writeFails(true)
	if _, err := run("inbox_move", `{"items":["f2"],"folder":"elsewhere"}`); err == nil || !strings.Contains(err.Error(), "not moved: f2") {
		t.Errorf("failed write: %v", err)
	}
	h.st.writeFails(false)
	if it := h.inbox.items["f2"]; it.Folder != "work/alerts/ci" {
		t.Errorf("after a failed write: %q", it.Folder)
	}
}

func TestInboxFolderRoutes(t *testing.T) {
	h := folderHub(t, 2)
	h.inbox.folderCfg.Enabled = false
	r := h.routes()

	code, out := inboxCall(t, r, "PUT", "/api/v1/inbox/f1/folder", `{"folder":" work//alerts "}`)
	if code != 200 || out["folder"] != "work/alerts" || out["folder_by"] != FolderByUser || out["folder_of"] != nil {
		t.Fatalf("move: %d %v", code, out)
	}
	if code, _ := inboxCall(t, r, "PUT", "/api/v1/inbox/f2/folder", `{"folder":"work/alerts/ci"}`); code != 200 {
		t.Fatal(code)
	}
	code, out = inboxCall(t, r, "GET", "/api/v1/inbox/folders", "")
	folders, _ := out["folders"].([]any)
	if code != 200 || out["enabled"] != false || len(folders) != 2 {
		t.Fatalf("folders: %d %v", code, out)
	}
	if f := folders[0].(map[string]any); f["path"] != "work/alerts" || f["count"] != 1.0 || f["open"] != 1.0 {
		t.Errorf("folder: %v", f)
	}
	code, out = inboxCall(t, r, "GET", "/api/v1/inbox", "")
	if code != 200 || !strings.Contains(fmt.Sprint(out["items"]), "folder:work/alerts") {
		t.Errorf("the list does not carry the folder: %v", out["items"])
	}

	// no folder, by the user: the model leaves it alone
	code, out = inboxCall(t, r, "PUT", "/api/v1/inbox/f1/folder", `{"folder":""}`)
	if code != 200 || out["folder"] != nil || out["folder_by"] != FolderByUser {
		t.Fatalf("no folder: %d %v", code, out)
	}
	if h.inbox.items["f1"].unfiled() {
		t.Error("an item the user took out of every folder would be filed again")
	}
	// back to the model
	code, out = inboxCall(t, r, "PUT", "/api/v1/inbox/f1/folder", `{"folder":null}`)
	if code != 200 || out["folder_by"] != nil || !h.inbox.items["f1"].unfiled() {
		t.Fatalf("back to the model: %d %v", code, out)
	}
	if code, _ := inboxCall(t, r, "PUT", "/api/v1/inbox/f1/folder", `{"folder":"/../"}`); code != 400 {
		t.Errorf("a path of nothing: %d", code)
	}
	if code, _ := inboxCall(t, r, "PUT", "/api/v1/inbox/nope/folder", `{"folder":"a"}`); code != 404 {
		t.Errorf("unknown item: %d", code)
	}
	// a failed write puts the item back
	h.st.writeFails(true)
	if code, _ := inboxCall(t, r, "PUT", "/api/v1/inbox/f2/folder", `{"folder":"elsewhere"}`); code != 500 {
		t.Errorf("failed write: %d", code)
	}
	h.st.writeFails(false)
	if it := h.inbox.items["f2"]; it.Folder != "work/alerts/ci" || it.FolderBy != FolderByUser {
		t.Errorf("after a failed write: %q by %q", it.Folder, it.FolderBy)
	}

	// settings: kept and read back
	code, out = inboxCall(t, r, "PUT", "/api/v1/inbox/folders", `{"rules":"  alerts go to work/alerts/<service>  "}`)
	if code != 200 || out["enabled"] != false || out["rules"] != "alerts go to work/alerts/<service>" {
		t.Fatalf("rules: %d %v", code, out)
	}
	model, calls := captureModel(t, `{"folders":{"f1":"work/builds"}}`)
	h.in.model = model
	if code, out = inboxCall(t, r, "PUT", "/api/v1/inbox/folders", `{"enabled":true}`); code != 200 || out["enabled"] != true || out["rules"] == "" {
		t.Fatalf("enable: %d %v", code, out)
	}
	h.inbox.foldering.Lock() // turning folders on started a pass: wait for it
	h.inbox.foldering.Unlock()
	if c := onlyCall(t, calls()); !strings.Contains(c.System, "alerts go to work/alerts/<service>") {
		t.Errorf("the folder rules are not in the prompt:\n%s", c.System)
	}
	if it := h.inbox.items["f1"]; it.Folder != "work/builds" || it.FolderBy != FolderByModel {
		t.Errorf("after the pass: %q by %q", it.Folder, it.FolderBy)
	}
	h.inbox.folderCfg = InboxFolderConfig{}
	if !h.loadInboxFolders(context.Background()) || !h.inbox.folderCfg.Enabled || h.inbox.folderCfg.Rules == "" {
		t.Errorf("settings not kept: %+v", h.inbox.folderCfg)
	}
}
