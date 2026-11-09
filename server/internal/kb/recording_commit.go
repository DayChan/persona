package kb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const recordingJournal = ".recording-undo.json"

var recordingFiles = [...]string{"meta.json", "segments.json", "transcript.md"}

// A durable undo snapshot precedes any replacement. Removing it commits all three
// files. One writable Store owns the directory; recFilesMu protects its readers.
// A pending journal is also a read overlay, so a failed rollback cannot expose
// rejected text. Writable open restores it before making the Store available.
type recordingUndo struct {
	Version int               `json:"version"`
	ID      string            `json:"id"`
	Files   map[string][]byte `json:"files"` // nil means the file did not exist
}

func (s *Store) recordingSnapshot(id string) (map[string][]byte, error) {
	if s.recordingReadWait != nil {
		s.recordingReadWait()
	}
	s.recFilesMu.RLock()
	defer s.recFilesMu.RUnlock()
	undo, err := s.readRecordingUndo(id)
	if err != nil {
		return nil, err
	}
	if undo != nil {
		return undo.Files, nil
	}
	return s.recordingDiskSnapshot(id)
}

// Single-file readers avoid loading an entire transcript just to list metadata.
func (s *Store) recordingRead(id, name string) ([]byte, error) {
	if s.recordingReadWait != nil {
		s.recordingReadWait()
	}
	s.recFilesMu.RLock()
	defer s.recFilesMu.RUnlock()
	undo, err := s.readRecordingUndo(id)
	if err != nil {
		return nil, err
	}
	if undo != nil {
		return undo.Files[name], nil
	}
	raw, err := os.ReadFile(s.path(KindRecording, id, name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return raw, err
}

func (s *Store) readEntityJSON(kind, id string, out any) (bool, error) {
	if kind != KindRecording {
		return readJSON(s.entityPath(kind, id), out)
	}
	raw, err := s.recordingRead(id, "meta.json")
	if err != nil || raw == nil {
		return false, err
	}
	return true, json.Unmarshal(raw, out)
}

// Freeze search metadata and rendered text together. A response must not pair
// hits from one accepted revision with a name or revision from another.
func (s *Store) recordingSearchView() map[string]map[string][]byte {
	if s.recordingReadWait != nil {
		s.recordingReadWait()
	}
	s.recFilesMu.RLock()
	defer s.recFilesMu.RUnlock()
	view := map[string]map[string][]byte{}
	for _, id := range s.IDs(KindRecording) {
		undo, err := s.readRecordingUndo(id)
		if err != nil {
			continue
		}
		if undo != nil {
			view[id] = undo.Files
			continue
		}
		files := map[string][]byte{}
		for _, name := range []string{"meta.json", "transcript.md"} {
			raw, err := os.ReadFile(s.path(KindRecording, id, name))
			if err == nil {
				files[name] = raw
			}
		}
		view[id] = files
	}
	return view
}

func (s *Store) recordingDiskSnapshot(id string) (map[string][]byte, error) {
	files := make(map[string][]byte, len(recordingFiles))
	for _, name := range recordingFiles {
		raw, err := os.ReadFile(s.path(KindRecording, id, name))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		files[name] = raw
	}
	return files, nil
}

func (s *Store) readRecordingUndo(id string) (*recordingUndo, error) {
	var undo recordingUndo
	ok, err := readJSON(s.path(KindRecording, id, recordingJournal), &undo)
	if err != nil || !ok {
		return nil, err
	}
	if undo.Version != 1 || undo.ID != id || len(undo.Files) != len(recordingFiles) {
		return nil, fmt.Errorf("invalid recording recovery for %s", id)
	}
	for _, name := range recordingFiles {
		if _, ok := undo.Files[name]; !ok {
			return nil, fmt.Errorf("incomplete recording recovery for %s", id)
		}
	}
	return &undo, nil
}

func syncRecordingDir(dir string) {
	// Directory syncing is best effort, as in managerconf.WriteFileAtomic (Windows
	// and some filesystems do not support it). File contents are synced before rename.
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
}

// Keep the existing temporary-file name as well as the public file layout.
func writeRecordingFile(path string, data []byte) error {
	f, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	syncRecordingDir(filepath.Dir(path))
	return nil
}

// Requires recFilesMu. Recovery deliberately bypasses candidate fault injection
// and uses unique temporary files, so a leftover upload temp cannot prevent undo.
func (s *Store) recoverRecordingLocked(id string) error {
	undo, err := s.readRecordingUndo(id)
	if err != nil || undo == nil {
		return err
	}
	for _, name := range recordingFiles {
		path := s.path(KindRecording, id, name)
		if raw := undo.Files[name]; raw != nil {
			if err := archiveWrite(path, raw); err != nil {
				return err
			}
		} else if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Remove(s.path(KindRecording, id, recordingJournal)); err != nil {
		return err
	}
	syncRecordingDir(s.path(KindRecording, id))
	return nil
}

func (s *Store) recoverRecordings() error {
	s.recFilesMu.Lock()
	defer s.recFilesMu.Unlock()
	for _, id := range s.IDs(KindRecording) {
		if err := s.recoverRecordingLocked(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) commitRecording(r *Recording, segs []RecordingSegment) (err error) {
	s.recFilesMu.Lock()
	s.metaMu.Lock()
	defer func() { s.metaMu.Unlock(); s.recFilesMu.Unlock() }()
	if err := s.recoverRecordingLocked(r.ID); err != nil {
		return err
	}
	before, err := s.recordingDiskSnapshot(r.ID)
	if err != nil {
		return err
	}
	current := map[string]json.RawMessage{}
	if before["meta.json"] != nil {
		if err := json.Unmarshal(before["meta.json"], &current); err != nil {
			return err
		}
	}
	incoming := protectedEntity(r, &r.Tagging, current, false)
	meta, err := json.MarshalIndent(incoming, "", "  ")
	if err != nil {
		return err
	}
	segments, err := json.MarshalIndent(nonNilSegments(segs), "", "  ")
	if err != nil {
		return err
	}
	transcript := []byte(s.renderTranscript(r, segs))
	undo, err := json.Marshal(recordingUndo{Version: 1, ID: r.ID, Files: before})
	if err != nil {
		return err
	}
	write := s.recordingWrite
	if write == nil {
		write = writeRecordingFile
	}
	// Journal writes use the same seam, with the production durable implementation.
	journal := s.path(KindRecording, r.ID, recordingJournal)
	if s.recordingWrite == nil {
		err = archiveWrite(journal, undo)
	} else {
		err = write(journal, undo)
	}
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.recoverRecordingLocked(r.ID))
		}
	}()
	for _, file := range []struct {
		name string
		raw  []byte
	}{
		{"segments.json", append(segments, '\n')}, {"transcript.md", transcript}, {"meta.json", append(meta, '\n')},
	} {
		if err = write(s.path(KindRecording, r.ID, file.name), file.raw); err != nil {
			return err
		}
	}
	remove := s.recordingRemove
	if remove == nil {
		remove = os.Remove
	}
	if err = remove(journal); err != nil {
		return err
	}
	syncRecordingDir(s.path(KindRecording, r.ID))
	*r = Recording{}
	_ = json.Unmarshal(meta, r)
	r.persisted = true
	return nil
}
