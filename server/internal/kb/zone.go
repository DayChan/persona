package kb

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The archive's times are the user's wall clock: a chat's day files (messages/2006-01-02.md) split at the user's
// midnight and their lines say "- 15:04"; a project's timeline says "- 2006-01-02 15:04". Only the wall clock is
// written, so every file also records its zone, and when the user's zone changes (the hub ran in UTC and wrote UTC
// days before 2026-11-01) the files are moved into the new zone before anything reads or writes them in it:
//   - a chat: messages/.zone names the zone (no file: written before zones were recorded, in the hub's own zone,
//     time.Local). Moving re-buckets every line into the new zone's days, written to messages.rezone and swapped in.
//   - a timeline: its header names the zone (the Chinese "Timeline" heading, then " · Asia/Shanghai"; the heading alone: time.Local).
// Attachments keep the directory of the day they were saved under (lines link them by path).

const zoneFile = ".zone"

// Zone is the zone the archive is written in (Rezone); time.Local until set.
func (s *Store) Zone() *time.Location {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.zoneLocked()
}

func (s *Store) zoneLocked() *time.Location {
	if s.zone == nil {
		return time.Local
	}
	return s.zone
}

// Rezone makes `to` the archive's zone and moves every chat's days and every project's timeline written in another
// zone into it. Returns how many chats and timelines it moved.
func (s *Store) Rezone(to *time.Location) (int, error) {
	if s.readOnly {
		return 0, ErrReadOnly
	}
	s.lockArchive()
	defer s.unlockArchive()
	if err := s.recoverIngestsLocked(); err != nil {
		return 0, err
	}
	s.zone = to
	if s.settled == to.String() {
		return 0, nil // every file is in it; what is written from now on is too
	}
	moved := 0
	var errs []string
	for _, id := range s.IDs(KindChat) {
		did, err := s.settleChatLocked(id)
		if err != nil {
			errs = append(errs, id+": "+err.Error())
		}
		if did {
			moved++
		}
	}
	for _, slug := range s.IDs(KindProject) {
		did, err := s.settleLogLocked(slug)
		if err != nil {
			errs = append(errs, slug+": "+err.Error())
		}
		if did {
			moved++
		}
	}
	if len(errs) > 0 {
		return moved, fmt.Errorf("rezone: %s", strings.Join(errs, "; "))
	}
	s.settled = to.String()
	return moved, nil
}

func loadZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return time.Local, nil
	}
	return time.LoadLocation(name)
}

// settleChatLocked moves a chat's day files into the archive's zone if they are in another one (or finishes a move
// that was cut short). New chats get the marker with their first day file.
func (s *Store) settleChatLocked(id string) (bool, error) {
	if s.readOnly {
		return false, nil
	}
	if err := s.pendingIngestLocked(id); err != nil {
		return false, err
	}
	dir := s.path(KindChat, id, "messages")
	tmp, old := dir+".rezone", dir+".old"
	// A move cut short: before the swap, the old days are still the chat's; after it, only the leftovers go.
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if _, err := os.Stat(old); err == nil {
			if err := os.Rename(old, dir); err != nil {
				return false, err
			}
		}
	}
	_ = os.RemoveAll(tmp)
	_ = os.RemoveAll(old)

	to := s.zoneLocked()
	raw, err := os.ReadFile(filepath.Join(dir, zoneFile))
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	name := strings.TrimSpace(string(raw))
	if name == to.String() || (name == "" && to == time.Local) {
		return false, nil
	}
	days := s.Days(id)
	if len(days) == 0 {
		if to == time.Local {
			return false, nil
		}
		return false, writeFile(filepath.Join(dir, zoneFile), []byte(to.String()+"\n"))
	}
	from, err := loadZone(name)
	if err != nil {
		return false, fmt.Errorf("%s names %q: %w", zoneFile, name, err)
	}

	type entry struct {
		at   time.Time
		text string
	}
	byDay := map[string][]entry{}
	chatName := ""
	if c, ok := s.Chat(id); ok {
		chatName = c.Name
	}
	for _, d := range days {
		raw, err := os.ReadFile(filepath.Join(dir, d+".md"))
		if err != nil {
			return false, err
		}
		var cur *entry
		flush := func() {
			if cur != nil {
				day := cur.at.In(to).Format("2006-01-02")
				byDay[day] = append(byDay[day], *cur)
				cur = nil
			}
		}
		for _, l := range strings.Split(string(raw), "\n") {
			switch {
			case strings.HasPrefix(l, "# "):
				if chatName == "" {
					chatName = strings.TrimSuffix(l[2:], " · "+d)
				}
			case strings.HasPrefix(l, "- "):
				flush()
				at, err := time.ParseInLocation("2006-01-02 15:04", d+" "+safeSlice(l, 2, 7), from)
				if err != nil {
					// Not a message line as Line.Format writes them: kept on its day, as it is.
					at, _ = time.ParseInLocation("2006-01-02", d, to)
					cur = &entry{at: at, text: l}
					continue
				}
				cur = &entry{at: at, text: "- " + at.In(to).Format("15:04") + l[7:]}
			case strings.HasPrefix(l, "  ") && cur != nil:
				cur.text += "\n" + l
			default:
				flush()
			}
		}
		flush()
	}

	for d, es := range byDay {
		sort.SliceStable(es, func(i, j int) bool { return es[i].at.Before(es[j].at) })
		var b strings.Builder
		fmt.Fprintf(&b, "# %s · %s\n\n", chatName, d)
		for _, e := range es {
			b.WriteString(e.text + "\n")
		}
		if err := writeFile(filepath.Join(tmp, d+".md"), []byte(b.String())); err != nil {
			return false, err
		}
	}
	if err := writeFile(filepath.Join(tmp, zoneFile), []byte(to.String()+"\n")); err != nil {
		return false, err
	}
	if err := os.Rename(dir, old); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.Rename(old, dir)
		return false, err
	}
	_ = os.RemoveAll(old)
	return true, nil
}

func safeSlice(s string, i, j int) string {
	if len(s) < j {
		return ""
	}
	return s[i:j]
}

const logTitle = "# 时间线"

func logHeader(zone *time.Location) string {
	if zone == time.Local {
		return logTitle + "\n\n"
	}
	return logTitle + " · " + zone.String() + "\n\n"
}

// settleLogLocked moves a project's timeline into the archive's zone if its header names another one. It runs before
// every append and read, so it reads only the header; the whole timeline only when it moves it.
func (s *Store) settleLogLocked(slug string) (bool, error) {
	if s.readOnly {
		return false, nil
	}
	p := s.path(KindProject, slug, "log.md")
	head, err := firstLine(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	to := s.zoneLocked()
	name := ""
	if z, ok := strings.CutPrefix(head, logTitle+" · "); ok {
		name = strings.TrimSpace(z)
	}
	if name == to.String() || (name == "" && to == time.Local) {
		return false, nil
	}
	from, err := loadZone(name)
	if err != nil {
		return false, fmt.Errorf("log.md names %q: %w", name, err)
	}
	raw, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(l, "- ") {
			continue
		}
		if at, err := time.ParseInLocation("2006-01-02 15:04", safeSlice(l, 2, 18), from); err == nil {
			l = "- " + at.In(to).Format("2006-01-02 15:04") + l[18:]
		}
		lines = append(lines, l)
	}
	return true, s.writeLogLocked(slug, lines)
}

// firstLine is the first line of the file at p without its "\n" (the whole file when it has none), read without the rest.
func firstLine(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 256).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSuffix(line, "\n"), nil
}

// writeLogLocked replaces a project's timeline with lines (each "- 2006-01-02 15:04 · …", in the archive's zone).
func (s *Store) writeLogLocked(slug string, lines []string) error {
	if s.readOnly {
		return ErrReadOnly
	}
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	return writeFile(s.path(KindProject, slug, "log.md"), []byte(logHeader(s.zoneLocked())+body))
}

// WriteLog replaces a project's timeline (merging two projects): lines in the archive's zone.
func (s *Store) WriteLog(slug string, lines []string) (err error) {
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		if err == nil {
			s.notify(KindProject, slug, "log")
		}
	}()
	if _, err := s.settleLogLocked(slug); err != nil {
		return err
	}
	return s.writeLogLocked(slug, lines)
}
