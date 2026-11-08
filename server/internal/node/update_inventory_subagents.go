package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// observeUpdateSubagents deliberately has no display age cutoff. Neither an old
// mtime nor an unchanged transcript proves a child has stopped. The surrounding
// producer must independently cover live processes and runtime-owned children.
func observeUpdateSubagents(ctx context.Context, dir string) ([]proto.Subagent, error) {
	out := []proto.Subagent{}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, fmt.Errorf("subagent directory identity unavailable")
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read complete subagent directory: %w", err)
	}
	metas, transcripts := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "agent-") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil, fmt.Errorf("ambiguous subagent file identity")
		}
		id := strings.TrimPrefix(name, "agent-")
		switch {
		case strings.HasSuffix(id, ".meta.json"):
			id = strings.TrimSuffix(id, ".meta.json")
			metas[id] = true
		case strings.HasSuffix(id, ".jsonl"):
			transcripts[strings.TrimSuffix(id, ".jsonl")] = true
			continue
		default:
			continue
		}
		st, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() || st.Size() > 64<<10 {
			return nil, fmt.Errorf("invalid subagent metadata file")
		}
		file, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		opened, statErr := file.Stat()
		if statErr != nil || !os.SameFile(st, opened) {
			return nil, errors.Join(fmt.Errorf("subagent file identity changed"), statErr, file.Close())
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		if err := errors.Join(readErr, file.Close()); err != nil {
			return nil, err
		}
		if len(data) > 64<<10 {
			return nil, fmt.Errorf("subagent metadata exceeds limit")
		}
		var meta struct {
			Finished     *bool  `json:"finished"`
			Parent       string `json:"parentAgentId"`
			RequestShape string `json:"requestShape"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("invalid subagent metadata: %w", err)
		}
		if id == "" || meta.Finished == nil {
			return nil, fmt.Errorf("subagent completion evidence unavailable")
		}
		out = append(out, proto.Subagent{ID: id, ParentID: meta.Parent,
			Running: !*meta.Finished, Background: meta.RequestShape == "background"})
	}
	for id := range transcripts {
		if !metas[id] {
			return nil, fmt.Errorf("subagent transcript lacks completion metadata")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Workflow runs have the same explicit completion metadata in nested
// directories. No workflow or nested child may disappear behind a display cap.
func observeUpdateSubagentTree(ctx context.Context, dir string) ([]proto.Subagent, error) {
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return []proto.Subagent{}, nil
	} else if err != nil {
		return nil, err
	}
	rows := []proto.Subagent{}
	count := 0
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 10000 {
			return fmt.Errorf("subagent inventory entry budget exceeded")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("subagent tree contains ambiguous symlink")
		}
		if entry.IsDir() {
			children, err := observeUpdateSubagents(ctx, path)
			if err != nil {
				return err
			}
			rows = append(rows, children...)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("subagent tree changed or is unavailable")
	}
	return rows, err
}
