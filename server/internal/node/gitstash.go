package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

var stashOIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func staleStash() error {
	return &rpc.Error{Code: gitCodeStashStale, Message: "The selected stash changed. Refresh the list and select it again."}
}

func unsupportedStash() error {
	return &rpc.Error{Code: gitCodeStashUnsupported, Message: "Safe stash removal requires Git's files reference backend, regular stash storage and no reference-transaction hook."}
}

// Git's files backend serializes reflog writes/deletes with refs/stash.lock, including
// terminal Git and other worktrees. There is no Git CLI compare-and-delete for a reflog
// entry. Follow files_reflog_expire's ref-lock, log-lock, rewrite, log-then-ref publication
// order, checking the selected OID only after locking. A process mutex alone is insufficient.
// Reftable and symbolic stash refs are refused before applying content. As with Git's
// own reflog rewrite, a disk/publication failure is reported, never retried as another pop.
func mutateStash(ctx context.Context, root, op string, index int, oid string) error {
	backend, err := git(ctx, root, "rev-parse", "--show-ref-format")
	if err != nil {
		return unsupportedStash()
	}
	if stashRefBackend(backend) != "files" {
		return unsupportedStash()
	}
	common, err := git(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return gitRefusal("stash", "", err.Error(), err, nil)
	}
	// Direct files publication cannot run reference-transaction hooks. Refuse repositories
	// that install one, rather than bypass their reference policy (including the final delete).
	hook, hookErr := git(ctx, root, "rev-parse", "--git-path", "hooks/reference-transaction")
	if hookErr != nil {
		return unsupportedStash()
	}
	hook = strings.TrimSpace(hook)
	if !filepath.IsAbs(hook) {
		hook = filepath.Join(root, hook)
	}
	if fi, err := os.Stat(hook); err == nil && fi.Mode().IsRegular() && (fi.Mode().Perm()&0111 != 0 || runtime.GOOS == "windows") {
		return unsupportedStash()
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return unsupportedStash()
	}
	common = strings.TrimSpace(common)
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return unsupportedStash()
	}
	// Resolve the common directory, but refuse redirected components within its storage.
	for _, dir := range []string{"refs", "logs", "logs/refs"} {
		fi, err := os.Lstat(filepath.Join(common, filepath.FromSlash(dir)))
		if errors.Is(err, os.ErrNotExist) {
			return staleStash()
		}
		if err != nil || !fi.IsDir() {
			return unsupportedStash()
		}
	}
	refPath := filepath.Join(common, "refs", "stash")
	logPath := filepath.Join(common, "logs", "refs", "stash")
	packedPath := filepath.Join(common, "packed-refs")
	shared, err := stashSharedPermissions(ctx, root)
	if err != nil {
		return err
	}
	ref, err := lockStashFile(refPath, shared)
	if err != nil {
		return err
	}
	defer ref.release()
	// pack-refs and ref deletions also own packed-refs.lock. Holding it keeps a packed
	// copy from reappearing when removing the final entry. Never remove another writer's lock.
	packed, err := lockStashFile(packedPath, shared)
	if err != nil {
		return err
	}
	defer packed.release()
	log, err := lockStashFile(logPath, shared)
	if err != nil {
		return err
	}
	defer log.release()
	refData, err := readStashFile(refPath)
	if err != nil {
		return err
	}
	packedData, err := readStashFile(packedPath)
	if err != nil {
		return err
	}
	logData, err := readStashFile(logPath)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	lines := strings.Split(strings.TrimSuffix(string(logData), "\n"), "\n")
	if len(logData) == 0 || index >= len(lines) {
		return staleStash()
	}
	// Raw reflogs are oldest first. Validate the complete structure before touching it;
	// retain each entry's original identity, timestamp and message bytes.
	width := strings.IndexByte(lines[0], ' ')
	if width != 40 && width != 64 {
		return unsupportedStash()
	}
	for _, line := range lines {
		if len(line) <= 2*width+2 || line[width] != ' ' || line[2*width+1] != ' ' ||
			!stashOIDPattern.MatchString(line[:width]) || !stashOIDPattern.MatchString(line[width+1:2*width+1]) ||
			!strings.Contains(line[2*width+2:], "\t") {
			return unsupportedStash()
		}
	}
	selected := len(lines) - 1 - index
	if lines[selected][width+1:2*width+1] != oid {
		return staleStash()
	}
	top := lines[len(lines)-1][width+1 : 2*width+1]
	refOID := strings.TrimSpace(string(refData))
	packedLines := strings.Split(string(packedData), "\n")
	var keptPacked []string
	packedOID := ""
	for i, line := range packedLines {
		if strings.HasSuffix(line, " refs/stash") {
			if packedOID != "" || len(line) != width+len(" refs/stash") || !stashOIDPattern.MatchString(line[:width]) {
				return unsupportedStash()
			}
			packedOID = line[:width]
			// Stash objects are commits, so a peeled tag line here is unsupported.
		} else {
			if i > 0 && strings.HasSuffix(packedLines[i-1], " refs/stash") && strings.HasPrefix(line, "^") {
				return unsupportedStash()
			}
			keptPacked = append(keptPacked, line)
		}
	}
	if refOID == "" {
		refOID = packedOID
	}
	if refOID != top {
		return unsupportedStash()
	}
	resolved, err := git(ctx, root, "rev-parse", "--verify", "refs/stash")
	if err != nil || strings.TrimSpace(resolved) != top {
		return unsupportedStash()
	}
	remaining := append(append([]string{}, lines[:selected]...), lines[selected+1:]...)
	previous := strings.Repeat("0", width)
	for i, line := range remaining {
		remaining[i] = previous + line[width:]
		previous = line[width+1 : 2*width+1]
	}
	newLog := ""
	if len(remaining) > 0 {
		newLog = strings.Join(remaining, "\n") + "\n"
	}
	if err := log.stage(newLog); err != nil {
		return err
	}
	if len(remaining) > 0 {
		if err := ref.stage(previous + "\n"); err != nil {
			return err
		}
	} else if packedOID != "" {
		if err := packed.stage(strings.Join(keptPacked, "\n")); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if op == "pop" {
		out, errOut, err := gitExec(ctx, root, time.Minute, "stash", "apply", oid)
		if err != nil {
			return gitRefusal("stash", out, errOut, err, nil)
		}
	}
	// Once apply succeeds, finish the bounded publication even if the caller disconnects.
	// Failure after apply explicitly says content was applied: retrying pop is unsafe.
	publish := func() error {
		if len(remaining) == 0 && packedOID != "" {
			if err := packed.commit(); err != nil {
				return err
			}
		}
		if err := log.commit(); err != nil {
			return err
		}
		if len(remaining) > 0 {
			return ref.commit()
		}
		if err := os.Remove(refPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := publish(); err != nil {
		prefix := "Stash removal failed; refresh before retrying."
		if op == "pop" {
			prefix = "Stash content was applied, but removal failed; refresh and inspect before retrying."
		}
		return &rpc.Error{Code: gitCodeFailed, Message: prefix + " " + err.Error()}
	}
	return nil
}

// stashRefBackend reads `git rev-parse --show-ref-format`. Git before 2.45 does not know the
// option and echoes it back; those versions have only the files backend and refuse to open a
// repository whose extensions.refStorage names another one, so the echo means files.
func stashRefBackend(out string) string {
	out = strings.TrimSpace(out)
	if out == "--show-ref-format" {
		return "files"
	}
	return out
}

// stashFileLock owns only a lock created by O_EXCL. Git sees the same path; cleanup
// never removes a pre-existing lock, even after a refusal, canceled request or conflict.
type stashFileLock struct {
	target string
	file   *os.File
	owned  bool
}

func lockStashFile(target string, shared int) (*stashFileLock, error) {
	f, err := os.OpenFile(target+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if err != nil {
		code := gitCodeFailed
		if errors.Is(err, os.ErrExist) {
			code = gitCodeBusy
		}
		return nil, &rpc.Error{Code: code, Message: "Cannot lock stash storage: " + err.Error()}
	}
	lock := &stashFileLock{target: target, file: f, owned: true}
	// Rename preserves the lock inode's permissions, not the replaced file's. Restore
	// existing storage modes exactly; newly materialized loose refs follow Git sharing.
	fi, err := os.Lstat(target)
	var mode os.FileMode
	if err == nil {
		if !fi.Mode().IsRegular() {
			lock.release()
			return nil, unsupportedStash()
		}
		mode = fi.Mode().Perm()
	} else if errors.Is(err, os.ErrNotExist) {
		fi, err = f.Stat()
		if err == nil {
			mode = fi.Mode().Perm()
			tweak := shared
			if tweak < 0 {
				tweak = -tweak
			}
			if mode&0200 == 0 {
				tweak &^= 0222
			}
			if shared < 0 {
				mode = os.FileMode(tweak)
			} else {
				mode |= os.FileMode(tweak)
			}
		}
	}
	if err == nil {
		err = f.Chmod(mode)
	}
	if err != nil {
		lock.release()
		return nil, err
	}
	return lock, nil
}

func readStashFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, unsupportedStash()
	}
	return os.ReadFile(path)
}

func (l *stashFileLock) stage(data string) error {
	if _, err := l.file.WriteString(data); err != nil {
		return err
	}
	if err := l.file.Sync(); err != nil {
		return err
	}
	return l.file.Close()
}

func (l *stashFileLock) commit() error {
	if err := os.Rename(l.target+".lock", l.target); err != nil {
		return fmt.Errorf("publish stash storage: %w", err)
	}
	l.owned = false
	return nil
}

func (l *stashFileLock) release() {
	_ = l.file.Close()
	if l.owned {
		_ = os.Remove(l.target + ".lock")
	}
}

// Match Git's core.sharedRepository permission values. Read only this key; bare
// boolean keys and explicit empty values differ, so retain --null's key/value boundary.
func stashSharedPermissions(ctx context.Context, root string) (int, error) {
	out, errOut, err := gitExec(ctx, root, 15*time.Second, "config", "--null", "--get-regexp", `^core\.sharedrepository$`)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && out == "" {
			return 0, nil
		}
		return 0, gitRefusal("config", out, errOut, err, nil)
	}
	entries := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	_, value, hasValue := strings.Cut(entries[len(entries)-1], "\n")
	if !hasValue {
		return 0660, nil
	}
	switch value {
	case "umask":
		return 0, nil
	case "group":
		return 0660, nil
	case "all", "world", "everybody":
		return 0664, nil
	}
	switch strings.ToLower(value) {
	case "true", "yes", "on":
		return 0660, nil
	case "false", "no", "off", "":
		return 0, nil
	}
	mode, parseErr := strconv.ParseUint(value, 8, 32)
	if parseErr != nil {
		return 0, unsupportedStash()
	}
	switch mode {
	case 0:
		return 0, nil
	case 1:
		return 0660, nil
	case 2:
		return 0664, nil
	}
	if mode&0600 != 0600 {
		return 0, unsupportedStash()
	}
	return -int(mode & 0666), nil
}
