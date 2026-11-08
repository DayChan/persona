package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateSubagentsNeverExpireUnfinishedChildrenByMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-child.meta.json")
	if err := os.WriteFile(path, []byte(`{"finished":false,"requestShape":"background","parentAgentId":"parent"}`), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	rows, err := observeUpdateSubagents(context.Background(), dir)
	if err != nil || len(rows) != 1 || !rows[0].Running || !rows[0].Background || rows[0].ParentID != "parent" {
		t.Fatalf("old unfinished child was hidden: %+v, %v", rows, err)
	}
}

func TestUpdateSubagentsRefuseMissingUnknownAndMalformedCompletion(t *testing.T) {
	for _, contents := range []string{`{}`, `{"finished":null}`, `{"finished":"false"}`, `{`} {
		t.Run(contents, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "agent-child.meta.json"), []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := observeUpdateSubagents(context.Background(), dir); err == nil {
				t.Fatal("unknown completion became idle")
			}
		})
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent-child.jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := observeUpdateSubagents(context.Background(), dir); err == nil {
		t.Fatal("transcript without metadata became a known terminal child")
	}
}

func TestUpdateSubagentsPreserveExplicitCompletionAndCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent-child.meta.json"), []byte(`{"finished":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := observeUpdateSubagents(context.Background(), dir)
	if err != nil || len(rows) != 1 || rows[0].Running {
		t.Fatalf("explicit terminal evidence lost: %+v %v", rows, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observeUpdateSubagents(ctx, dir); err == nil {
		t.Fatal("canceled child collection succeeded")
	}
}

func TestUpdateSubagentsIncludesNestedWorkflowAndRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "workflows", "run")
	if err := os.MkdirAll(run, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "agent-child.meta.json"), []byte(`{"finished":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := observeUpdateSubagentTree(context.Background(), dir)
	if err != nil || len(rows) != 1 || !rows[0].Running {
		t.Fatalf("nested workflow omitted: %+v %v", rows, err)
	}
	if err := os.Symlink(run, filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := observeUpdateSubagentTree(context.Background(), dir); err == nil {
		t.Fatal("symlinked child source accepted")
	}
}
