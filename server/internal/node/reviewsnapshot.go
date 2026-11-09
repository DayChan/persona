package node

// A reviewer's own copy of an agent's change (#2173, docs/assistant/unified-entry.md "Independent review"):
// worktree.snapshot checks out the HEAD of the repository a folder is in, detached, under the directory the computer
// puts this repository's checkouts in, and puts the folder's uncommitted change there too (tracked edits as a binary
// diff, untracked files copied). The reviewer runs in that copy, so whatever it edits, formats or builds lands there
// and never in the agent's tree; worktree.remove (forced) takes the copy away when the review ends. Nothing is
// written in the agent's folder: it is only read.

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/proto"
)

// snapshotPrefix starts the folder name of every review copy.
const snapshotPrefix = "review-"

// Untracked files copied into a review copy: at most this many, this many bytes in all; the rest are left out and
// counted (Skipped), so a folder full of unignored build output does not stall the review.
const (
	snapshotMaxFiles = 2000
	snapshotMaxBytes = 64 << 20
)

// worktreeSnapshot makes the review copy of the repository cwd is in (see above).
func (n *Node) worktreeSnapshot(ctx context.Context, kind, cwd, nameHint string) (proto.WorktreeSnapshot, error) {
	var res proto.WorktreeSnapshot
	dir := proto.ExpandHome(strings.TrimSpace(cwd), n.home)
	if dir == "" || !filepath.IsAbs(dir) {
		return res, refusal("not_a_repository", "the folder is not in a Git repository", nil)
	}
	repo, err := repoAt(ctx, dir)
	if err != nil {
		return res, refusal("not_a_repository", "the folder is not in a Git repository", nil)
	}
	head, err := git(ctx, repo.root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return res, refusal("no_commit", "the repository has no commit yet", nil)
	}
	h, _ := n.hostOfKind(kind)
	base := n.worktreesDir(h, repo.name)
	slug := herdr.WorktreeSlug(nameHint)
	if len(slug) > maxSlug {
		slug = strings.TrimRight(slug[:maxSlug], "-")
	}
	name := snapshotPrefix + strings.Trim(cmp.Or(slug, "change"), "-")
	dest := ""
	for i := 1; i <= 50 && dest == ""; i++ {
		p := filepath.Join(base, name)
		if i > 1 {
			p += "-" + strconv.Itoa(i)
		}
		if !pathTaken(p) {
			dest = p
		}
	}
	if dest == "" {
		return res, refusal("worktree_failed", "no free folder for the review copy", nil)
	}
	if dest, err = n.checkoutPath(repo.root, dest); err != nil {
		return res, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return res, err
	}
	head = strings.TrimSpace(head)
	if _, err := gitTimed(ctx, repo.root, 2*time.Minute, "worktree", "add", "--detach", "--", dest, head); err != nil {
		return res, refusal("worktree_failed", gitFailure(err), nil)
	}
	res = proto.WorktreeSnapshot{Path: dest, Root: repo.root, Head: head}
	fail := func(err error) (proto.WorktreeSnapshot, error) {
		_, _ = gitTimed(context.WithoutCancel(ctx), repo.root, time.Minute, "worktree", "remove", "--force", "--", dest)
		return proto.WorktreeSnapshot{}, refusal("worktree_failed", err.Error(), nil)
	}
	if err := applyTrackedChange(ctx, repo.root, dest); err != nil {
		return fail(err)
	}
	if res.Skipped, err = copyUntracked(ctx, repo.root, dest); err != nil {
		return fail(err)
	}
	return res, nil
}

// applyTrackedChange puts the tracked files' uncommitted change of root (staged and not, against HEAD) into dest.
func applyTrackedChange(ctx context.Context, root, dest string) error {
	diff, err := gitTimed(ctx, root, 2*time.Minute, "diff", "--binary", "--no-color", "--no-ext-diff", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := gitCmd(cctx, dest, "apply", "--binary", "--whitespace=nowarn")
	cmd.Stdin = strings.NewReader(diff)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git apply: %s", cmp.Or(strings.TrimSpace(errb.String()), err.Error()))
	}
	return nil
}

// copyUntracked copies root's untracked, not ignored files into dest (a symlink as a symlink), within the limits
// above; how many were left out.
func copyUntracked(ctx context.Context, root, dest string) (int, error) {
	out, err := gitTimed(ctx, root, time.Minute, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return 0, err
	}
	skipped, files := 0, 0
	var total int64
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" {
			continue
		}
		src, dst := filepath.Join(root, rel), filepath.Join(dest, rel)
		if !within(filepath.Clean(dst), filepath.Clean(dest)) {
			skipped++
			continue
		}
		fi, err := os.Lstat(src)
		if err != nil {
			skipped++ // gone meanwhile
			continue
		}
		if files >= snapshotMaxFiles || fi.Mode().IsRegular() && total+fi.Size() > snapshotMaxBytes {
			skipped++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return skipped, err
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(src)
			if err != nil {
				skipped++
				continue
			}
			if err := os.Symlink(target, dst); err != nil {
				return skipped, err
			}
		case fi.Mode().IsRegular():
			if err := copySnapshotFile(src, dst, fi.Mode().Perm()); err != nil {
				return skipped, err
			}
			total += fi.Size()
		default:
			skipped++
			continue
		}
		files++
	}
	return skipped, nil
}

func copySnapshotFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
