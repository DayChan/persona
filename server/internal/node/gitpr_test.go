package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// fakePRGh points ghLookPath at a script that answers as gh would: pr view (a PR, or none for branch "lonely"), the
// review threads, create and merge, each call's arguments written down. auth: "out" answers every call as gh does
// when not signed in.
func fakePRGh(t *testing.T, auth string, views ...string) (calls func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	view := `{"number":42,"url":"https://github.com/acme/web/pull/42","title":"Fix login","state":"OPEN","isDraft":false,
"baseRefName":"main","headRefName":"fix/login","headRefOid":"0123456789abcdef0123456789abcdef01234567","mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED","reviewDecision":"CHANGES_REQUESTED",
"autoMergeRequest":null,"statusCheckRollup":[
 {"name":"test","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://ci.example.com/1"},
 {"name":"lint","status":"COMPLETED","conclusion":"SUCCESS"},
 {"context":"deploy/preview","state":"PENDING","targetUrl":"https://ci.example.com/2"},
 {"name":"rerun","status":"IN_PROGRESS","conclusion":"SUCCESS"}]}`
	if len(views) > 0 {
		view = views[0]
	}
	threads := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
 {"isResolved":false,"isOutdated":false,"path":"src/login.ts","line":12,"originalLine":10,
  "comments":{"nodes":[{"author":{"login":"reviewer"},"body":"Handle the empty password.","url":"https://github.com/acme/web/pull/42#r1","createdAt":"2026-11-01T10:00:00Z"}]}},
 {"isResolved":true,"isOutdated":false,"path":"src/old.ts","line":3,"originalLine":3,"comments":{"nodes":[]}}]}}}}}`
	script := `#!/bin/sh
printf '%s\n' "$*" >> '` + log + `'
if [ '` + auth + `' = out ]; then echo 'To get started with GitHub CLI, please run:  gh auth login' >&2; exit 4; fi
case "$1 $2" in
"pr view")
  if [ "$3" = lonely ]; then echo 'no pull requests found for branch "lonely"' >&2; exit 1; fi
  cat <<'EOF'
` + view + `
EOF
  ;;
"api graphql")
  cat <<'EOF'
` + threads + `
EOF
  ;;
"pr create") echo https://github.com/acme/web/pull/43 ;;
"pr merge") ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac
`
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(gh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	old := ghLookPath
	ghLookPath = func() (string, error) { return gh, nil }
	t.Cleanup(func() { ghLookPath = old })
	return func() []string {
		b, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// The branch's pull request: its checks in one word and each, its open review threads only.
func TestGitPRView(t *testing.T) {
	f := newPullFixture(t)
	fakePRGh(t, "")
	runPullGit(t, f.local, "switch", "-q", "-c", "fix/login")
	out, err := f.node.gitPR(context.Background(), f.local)
	if err != nil {
		t.Fatal(err)
	}
	pr, ok := out["pr"].(GitPR)
	if !ok || pr.Number != 42 || pr.HeadOID != "0123456789abcdef0123456789abcdef01234567" || pr.Review != "CHANGES_REQUESTED" || pr.AutoMerge {
		t.Fatalf("pr %+v", out["pr"])
	}
	if pr.ChecksState != "failing" || !pr.ChecksComplete || len(pr.Checks) != 4 || pr.Checks[0].State != "fail" || pr.Checks[1].State != "pass" ||
		pr.Checks[2].Name != "deploy/preview" || pr.Checks[2].State != "pending" || pr.Checks[2].URL == "" || pr.Checks[3].State != "pending" {
		t.Fatalf("checks %s %+v", pr.ChecksState, pr.Checks)
	}
	if len(pr.Threads) != 1 || pr.Threads[0].Path != "src/login.ts" || pr.Threads[0].Line != 12 ||
		pr.Threads[0].Comments[0].Body != "Handle the empty password." {
		t.Fatalf("threads %+v", pr.Threads)
	}
	runPullGit(t, f.local, "switch", "-q", "-c", "lonely")
	if out, err = f.node.gitPR(context.Background(), f.local); err != nil || out["pr"] != nil || out["branch"] != "lonely" {
		t.Fatalf("no pr: %v %+v", err, out)
	}
}

// gh not signed in, or not there, is said as such.
func TestGitPRWithoutGh(t *testing.T) {
	f := newPullFixture(t)
	fakePRGh(t, "out")
	if _, err := f.node.gitPR(context.Background(), f.local); err == nil {
		t.Fatal("no error")
	} else if code, _ := gitCodeOf(t, err); code != proto.CodeNotSignedIn {
		t.Fatalf("code %s: %v", code, err)
	}
	ghLookPath = func() (string, error) { return "", os.ErrNotExist }
	if _, err := f.node.gitPR(context.Background(), f.local); err == nil {
		t.Fatal("no error")
	} else if code, _ := gitCodeOf(t, err); code != proto.CodeNoGh {
		t.Fatalf("code %s", code)
	}
}

// A pull request is made for a pushed branch only; merging passes the method, auto or turning auto off, and never
// deletes the branch.
func TestGitPRCreateAndMerge(t *testing.T) {
	f := newPullFixture(t)
	calls := fakePRGh(t, "")
	ctx := context.Background()
	runPullGit(t, f.local, "switch", "-q", "-c", "fix/login")
	f.commit(t, f.local, "app.txt", "fixed\n")
	if _, err := f.node.gitPRCreate(ctx, f.local, "Fix login", "Body", "main", false); err == nil {
		t.Fatal("made a pull request for an unpushed branch")
	} else if code, _ := gitCodeOf(t, err); code != gitCodeNotPushed {
		t.Fatalf("code %s", code)
	}
	runPullGit(t, f.local, "push", "-q", "-u", "origin", "fix/login")
	if _, err := f.node.gitPRCreate(ctx, f.local, "Fix login", "Body", "main", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.node.gitPRMerge(ctx, f.local, 42, "squash", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.node.gitPRMerge(ctx, f.local, 42, "", false, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.node.gitPRMerge(ctx, f.local, 42, "octopus", false, false); err == nil {
		t.Fatal("merged with an unknown method")
	}
	var made []string
	for _, c := range calls() {
		if strings.HasPrefix(c, "pr create") || strings.HasPrefix(c, "pr merge") {
			made = append(made, c)
		}
	}
	want := []string{"pr create --head fix/login --title Fix login --body Body --base main --draft", "pr merge 42 --squash --auto",
		"pr merge 42 --disable-auto"}
	if strings.Join(made, "|") != strings.Join(want, "|") {
		t.Fatalf("gh calls\n%s\nwant\n%s", strings.Join(made, "\n"), strings.Join(want, "\n"))
	}
}

func TestGitPRCheckCompleteness(t *testing.T) {
	for _, tc := range []struct {
		name     string
		count    int
		bad      map[string]any
		head     bool
		complete bool
	}{
		{"99 checks", 99, nil, true, true},
		{"100 checks", 100, nil, true, false},
		{"missing check status", 0, map[string]any{"name": "test", "conclusion": "SUCCESS"}, true, false},
		{"missing status context", 0, map[string]any{"state": "SUCCESS"}, true, false},
		{"missing context state", 0, map[string]any{"context": "ci"}, true, false},
		{"missing head", 0, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := make([]map[string]any, 0, tc.count+1)
			for i := 0; i < tc.count; i++ {
				checks = append(checks, map[string]any{"name": fmt.Sprintf("job-%d", i), "status": "COMPLETED", "conclusion": "SUCCESS"})
			}
			if tc.bad != nil {
				checks = append(checks, tc.bad)
			}
			head := ""
			if tc.head {
				head = "0123456789abcdef0123456789abcdef01234567"
			}
			raw, err := json.Marshal(map[string]any{"number": 42, "url": "https://github.com/example/repo/pull/42", "headRefOid": head, "statusCheckRollup": checks})
			if err != nil {
				t.Fatal(err)
			}
			f := newPullFixture(t)
			fakePRGh(t, "", string(raw))
			out, err := f.node.gitPR(context.Background(), f.local)
			if err != nil {
				t.Fatal(err)
			}
			pr, ok := out["pr"].(GitPR)
			if !ok || pr.ChecksComplete != tc.complete || pr.HeadOID != head {
				t.Fatalf("pr %+v", out["pr"])
			}
		})
	}
}
