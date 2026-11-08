package kb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrReadOnly = errors.New("the knowledge base is read-only")

// OpenReadOnly reads an archive in place. A missing archive is empty; existing
// files must be readable. Opening never creates directories or repairs files.
func OpenReadOnly(dir string) (*Store, error) {
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return &Store{Dir: dir, readOnly: true}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("knowledge archive is not a directory")
	}
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("knowledge archive contains a symbolic link")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("knowledge archive contains a non-regular file")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if entry.Name() == ingestJournal {
			return errors.New("knowledge archive requires writable ingestion recovery")
		}
		return validateArchiveMetadata(dir, path)
	})
	if err != nil {
		return nil, err
	}
	return &Store{Dir: dir, readOnly: true}, nil
}

func (s *Store) ReadOnly() bool { return s.readOnly }

func validateArchiveMetadata(dir, path string) error {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return err
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	var entity any
	if len(parts) == 3 && parts[2] == "meta.json" {
		switch parts[0] {
		case KindChat:
			entity = &Chat{}
		case KindPerson:
			entity = &Person{}
		case KindProject:
			entity = &Project{}
		case KindRecording:
			entity = &Recording{}
		}
	} else if len(parts) == 2 && strings.HasSuffix(parts[1], ".json") && !strings.HasPrefix(parts[1], ".") {
		switch parts[0] {
		case KindDoc, KindMeeting, KindEvent:
			entity = &Item{}
		}
	}
	if entity == nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return errors.New("invalid knowledge archive metadata")
	}
	return json.Unmarshal(raw, entity)
}

// A read-only archive keeps each chat's recorded time zone instead of rebucketing
// its day files into the current machine's zone.
func (s *Store) chatReadZoneLocked(id string) *time.Location {
	if s.readOnly {
		raw, err := os.ReadFile(s.path(KindChat, id, "messages", zoneFile))
		if err == nil {
			if loc, err := loadZone(strings.TrimSpace(string(raw))); err == nil {
				return loc
			}
		}
	}
	return s.zoneLocked()
}
