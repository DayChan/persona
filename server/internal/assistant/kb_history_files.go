package assistant

import (
	"archive/tar"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// The history import's files, all under the knowledge base's history/ directory (search, lists and cleanup never look
// there; docs/assistant/knowledge.md "History import"):
//
//	history/plan-<source>.json     the chats a source lists and the approval (kb_history_plan; kb_history_plan.go)
//	history/state/<chat id>.json   how far each chat got (kb.Store.HistoryState)
//	history/anchors-<source>.json  the owner's messages a searchable source found, by year (kb_history_anchor.go)
//	history/status.json            each source's state, last call, slowdown, pause and stop, for kb_history_status
//	history/run.lock               one import at a time: a file lock and a lease with a heartbeat
//	history/STOP                   there: nothing is read until a person resumes (kb_history_resume, or deleting it)
//	history/log-YYYY-MM-DD.jsonl   one line per call: time, method, a keyed hash of the chat, duration, result
//	history/log.key                the key of those hashes (0600)
//
// And before every run, a backup of the data directory beside it (<parent>/kb-backups/), at most three kept.

const (
	historyDirName  = "history"
	historyLockName = "run.lock"
	historyStopName = "STOP"
	// historyLeaseStale is how old a lease's heartbeat may be before another process takes the import over: a page
	// takes seconds, a rest is waited in steps that renew it, so ten minutes without one means its process is gone.
	historyLeaseStale = 10 * time.Minute
	// historyBackupsKept is how many backups of the data directory are kept.
	historyBackupsKept = 3
)

func historyDir(data string) string { return filepath.Join(data, historyDirName) }

// writeJSONFile writes v as JSON to path atomically (a temporary file renamed over it), 0600, its directory 0700.
func writeJSONFile(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(raw, '\n'))
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// readJSONFile reads path into v; false when there is no such file.
func readJSONFile(path string, v any) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return true, nil
}

// ---- the lock ----

// historyLease is what run.lock says while an import holds it. No host name: the process and a random instance id.
type historyLease struct {
	PID       int       `json:"pid"`
	Instance  string    `json:"instance"`
	Source    string    `json:"source,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Heartbeat time.Time `json:"heartbeat"`
}

var (
	errHistoryLocked = errors.New("another process holds the history import's lock")
	errHistoryLeased = errors.New("another process's lease on the history import is still fresh")
)

// historyLock is run.lock held: the file lock (flock) and the lease written in it.
type historyLock struct {
	f     *os.File
	lease historyLease
}

// lockHistory takes run.lock for instance: not while another process has the file locked, nor while another
// instance's lease is fresh (a file system without working locks); a stale one is taken over, and tookOver says so
// (with the lease it replaced) for the log.
func lockHistory(data, instance string, now time.Time) (l *historyLock, tookOver *historyLease, err error) {
	path := filepath.Join(historyDir(data), historyLockName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, err
	}
	ok, err := tryLockFile(f)
	if err != nil || !ok {
		f.Close()
		if err == nil {
			err = errHistoryLocked
		}
		return nil, nil, err
	}
	var old historyLease
	if raw, _ := io.ReadAll(f); len(strings.TrimSpace(string(raw))) > 0 && json.Unmarshal(raw, &old) == nil &&
		old.Instance != "" && old.Instance != instance {
		if now.Sub(old.Heartbeat) < historyLeaseStale {
			unlockFile(f)
			f.Close()
			return nil, nil, errHistoryLeased
		}
		tookOver = &old
	}
	l = &historyLock{f: f, lease: historyLease{PID: os.Getpid(), Instance: instance, StartedAt: now.UTC(), Heartbeat: now.UTC()}}
	if err := l.write(); err != nil {
		l.release()
		return nil, nil, err
	}
	return l, tookOver, nil
}

func (l *historyLock) write() error {
	raw, _ := json.Marshal(l.lease)
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	if _, err := l.f.WriteAt(append(raw, '\n'), 0); err != nil {
		return err
	}
	return l.f.Sync()
}

// beat renews the heartbeat (and says which source runs).
func (l *historyLock) beat(now time.Time, source string) error {
	l.lease.Heartbeat, l.lease.Source = now.UTC(), source
	return l.write()
}

// release empties the lease and lets the lock go.
func (l *historyLock) release() {
	_ = l.f.Truncate(0)
	unlockFile(l.f)
	l.f.Close()
}

// readHistoryLease is the lease in run.lock, for the status (false: none).
func readHistoryLease(data string) (historyLease, bool) {
	var l historyLease
	raw, err := os.ReadFile(filepath.Join(historyDir(data), historyLockName))
	if err != nil || json.Unmarshal(raw, &l) != nil || l.Instance == "" {
		return historyLease{}, false
	}
	return l, true
}

// ---- STOP ----

// historyStop is what STOP says: when, why and by whom. A STOP a person made by hand (empty, or a few words) stops as
// well (reason stop_file, with the words as its text).
type historyStop struct {
	At time.Time `json:"at,omitzero"`
	historyReason
	By     string `json:"by,omitempty"` // "tool" (kb_history_stop), "driver" (the import stopped itself)
	Source string `json:"source,omitempty"`
}

func historyStopPath(data string) string { return filepath.Join(historyDir(data), historyStopName) }

// readHistoryStop: STOP is there (and what it says, as far as it reads).
func readHistoryStop(data string) (historyStop, bool) {
	raw, err := os.ReadFile(historyStopPath(data))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return historyStop{}, false
		}
		return historyStop{historyReason: reason("stop_file")}, true // there but unreadable: stopped all the same
	}
	var st historyStop
	if json.Unmarshal(raw, &st) != nil {
		st = historyStop{historyReason: historyReason{Code: "stop_file", Text: clipRunes(strings.TrimSpace(string(raw)), 200)}}
	}
	if st.Code == "" {
		st.Code = "stop_file"
	}
	return st, true
}

func writeHistoryStop(data string, st historyStop) error {
	return writeJSONFile(historyStopPath(data), st)
}

func removeHistoryStop(data string) error {
	if err := os.Remove(historyStopPath(data)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ---- the log ----

// historyLogLine is one line of log-YYYY-MM-DD.jsonl: a call (Method) or an event (Event). Never a message's text,
// a chat's name or its id: Chat is a keyed hash of the id.
type historyLogLine struct {
	At     time.Time `json:"at"`
	Source string    `json:"source,omitempty"`
	Method string    `json:"method,omitempty"`
	Chat   string    `json:"chat,omitempty"`
	MS     int64     `json:"ms,omitempty"`
	Result string    `json:"result,omitempty"` // ok, paced, unreadable, or the error's code
	N      int       `json:"n,omitempty"`      // messages (or anchors) it answered
	Pace   string    `json:"pace,omitempty"`   // retry_after=<ms>, stop
	Event  string    `json:"event,omitempty"`  // start, backup, takeover, slow, rest, pause, stop, done, end, resume
	// The event's reason: its code and arguments, never its text (historyReason.logged).
	historyReason
}

// appendHistoryLog appends l to the log of its day (in zone).
func appendHistoryLog(data string, zone *time.Location, l historyLogLine) error {
	if zone == nil {
		zone = time.UTC
	}
	l.At = l.At.UTC()
	l.historyReason = l.historyReason.logged()
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	dir := historyDir(data)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "log-"+l.At.In(zone).Format("2006-01-02")+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(raw, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// historyLogKey is the key the log hashes chat ids with, made once (32 random bytes, 0600).
func historyLogKey(data string) ([]byte, error) {
	path := filepath.Join(historyDir(data), "log.key")
	if raw, err := os.ReadFile(path); err == nil && len(raw) >= 32 {
		return raw, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, key); err != nil {
		return nil, err
	}
	return key, nil
}

// historyHash is the log's name for a chat: the first 12 hex digits of HMAC-SHA256(key, source/chat).
func historyHash(key []byte, source, chat string) string {
	if chat == "" {
		return ""
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(source + "/" + chat))
	return hex.EncodeToString(m.Sum(nil))[:12]
}

// ---- backups ----

// historyBackupDir is where the data directory's backups go: kb-backups beside it.
func historyBackupDir(data string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(data)), "kb-backups")
}

var historyBackupName = lazyre.New(`^kb-(\d{14})(?:-(\d+))?\.tar\.gz$`)

// backupKB archives the data directory into kb-backups/kb-YYYYMMDDHHMMSS.tar.gz (UTC; written to a temporary name
// and renamed, 0600, the directory 0700), leaving out every attachments/ directory (never rewritten by the import, and
// it can be tens of gigabytes), the lock files and anything that is not a regular file or a directory. Then only the
// newest historyBackupsKept are kept.
func backupKB(data string, now time.Time) (string, error) {
	data = filepath.Clean(data)
	dir := historyBackupDir(data)
	// A data directory that is a link is walked where it leads (WalkDir would not follow it, and archive nothing).
	root, err := filepath.EvalSymlinks(data)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0o700)
	stamp := now.UTC().Format("20060102150405")
	name := "kb-" + stamp + ".tar.gz"
	for i := 2; ; i++ { // two runs in one second: kb-<stamp>-2.tar.gz
		if _, err := os.Lstat(filepath.Join(dir, name)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("kb-%s-%d.tar.gz", stamp, i)
	}
	f, err := os.CreateTemp(dir, ".kb-*.tar.gz.tmp")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	fail := func(err error) (string, error) {
		f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	base := filepath.Base(data)
	files := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) { // went while the walk was on
				return nil
			}
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)
		if d.IsDir() && d.Name() == "attachments" {
			return filepath.SkipDir
		}
		if slash == ".lock" || slash == historyDirName+"/"+historyLockName {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil // links, sockets: not followed, not kept
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = base + "/" + slash
		if info.IsDir() {
			hdr.Name += "/"
			return tw.WriteHeader(hdr)
		}
		src, err := os.Open(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		defer src.Close()
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		// A file that grows while it is read is cut at the size its header says.
		_, err = io.Copy(tw, io.LimitReader(src, hdr.Size))
		files++
		return err
	})
	if err == nil && files == 0 { // the import's own plan is there at least: an empty backup is a backup of nothing
		err = errors.New("the backup found no file to keep")
	}
	if err != nil {
		return fail(err)
	}
	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := gz.Close(); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	_, err = pruneKBBackups(dir, historyBackupsKept)
	return path, err
}

// pruneKBBackups deletes all but the newest keep backups in dir, by the time in their names: only regular files named
// kb-YYYYMMDDHHMMSS[-n].tar.gz, never anything else. It returns the names it deleted.
func pruneKBBackups(dir string, keep int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type backup struct {
		name  string
		stamp string
		n     int
	}
	var list []backup
	for _, e := range entries {
		m := historyBackupName.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		if _, err := time.Parse("20060102150405", m[1]); err != nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		list = append(list, backup{e.Name(), m[1], n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].stamp != list[j].stamp {
			return list[i].stamp > list[j].stamp
		}
		return list[i].n > list[j].n
	})
	var deleted []string
	for _, b := range list[min(keep, len(list)):] {
		if err := os.Remove(filepath.Join(dir, b.name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return deleted, err
		}
		deleted = append(deleted, b.name)
	}
	return deleted, nil
}

// historyStatePathFor is a chat's state file, for tests that look at it.
func historyStatePathFor(data, chatID string) string {
	return filepath.Join(historyDir(data), "state", kb.SafeID(chatID)+".json")
}
