// Package storage implements the manager-owned logical data contract. The guest
// receives declared keys and opaque handles, never files, SQL or new network rights.
package storage

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

const (
	Version         = 1
	ValueMost       = 64 << 10
	ChunkMost       = 64 << 10
	DataMost        = 8 << 20
	DocumentMost    = 12 << 20 // encoded data and bounded metadata, never an RPC frame
	EntriesMost     = 256
	JobsMost        = 128
	BlobsMost       = 32
	ReceiptsMost    = 256
	OperationsMost  = 32
	RequestMost     = 256 << 10
	ReceiptLifetime = 5 * time.Minute
	ClockSkew       = 5 * time.Second
	LeaseMost       = 60 * time.Second
)

var (
	ErrDenied      = extension.ErrStorageDenied
	ErrInvalid     = errors.New("invalid storage request")
	ErrConflict    = errors.New("storage revision or request conflict")
	ErrNotFound    = errors.New("storage item not found")
	ErrQuota       = extension.ErrStorageQuota
	ErrExpired     = errors.New("storage request expired")
	ErrStale       = extension.ErrStorageScope
	ErrCorrupt     = errors.New("stored extension data is invalid")
	ErrUnknown     = errors.New("storage commit outcome is unknown")
	ErrUnavailable = errors.New("storage is unavailable")
)

// Scope is derived by the trusted manager, never accepted from source params.
// InstalledAt is the actual persisted installation epoch; Run is an opaque
// host-created identity for one process attempt, including initialization.
type Scope = extension.StorageScope

type Entry struct {
	Revision string `json:"revision"`
	Data     string `json:"data"`
}

type Job struct {
	ID         string `json:"id"`
	Dedup      string `json:"dedup"`
	Data       string `json:"data"`
	State      string `json:"state"`
	NotBefore  int64  `json:"not_before"`
	Attempts   int    `json:"attempts"`
	Lease      string `json:"lease,omitempty"`
	LeaseUntil int64  `json:"lease_until,omitempty"`
	Run        string `json:"run,omitempty"`
}

type Blob struct {
	Key      string   `json:"key"`
	Chunks   []string `json:"chunks"`
	Bytes    int      `json:"bytes"`
	Complete bool     `json:"complete"`
}

type Receipt struct {
	Fingerprint string          `json:"fingerprint"`
	Result      json.RawMessage `json:"result"`
}

type State struct {
	Version  int                        `json:"version"`
	Entries  map[string]Entry           `json:"entries"`
	Queues   map[string]map[string]*Job `json:"queues"`
	Blobs    map[string]*Blob           `json:"blobs"`
	Receipts map[string]Receipt         `json:"receipts"`
}

func NewState() State {
	return State{Version: Version, Entries: map[string]Entry{}, Queues: map[string]map[string]*Job{}, Blobs: map[string]*Blob{}, Receipts: map[string]Receipt{}}
}

func decodeData(value string, most int) ([]byte, error) {
	if len(value) > base64.StdEncoding.EncodedLen(most) {
		return nil, ErrQuota
	}
	data, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || base64.StdEncoding.EncodeToString(data) != value {
		return nil, ErrInvalid
	}
	if len(data) > most {
		return nil, ErrQuota
	}
	return data, nil
}

// Validate also runs on persisted input. Corrupt or unsupported state must never
// be silently replaced by an empty map, including during restore or a failed load.
func (s *State) Validate() error {
	if s.Version != Version || s.Entries == nil || s.Queues == nil || s.Blobs == nil || s.Receipts == nil {
		return ErrCorrupt
	}
	if len(s.Entries) > EntriesMost || len(s.Queues) > EntriesMost || len(s.Blobs) > BlobsMost || len(s.Receipts) > ReceiptsMost {
		return ErrQuota
	}
	total, jobs := 0, 0
	for key, value := range s.Entries {
		if !extension.ValidStorageKey(key) || value.Revision == "" {
			return ErrCorrupt
		}
		data, err := decodeData(value.Data, ValueMost)
		if err != nil {
			return err
		}
		total += len(data) + len(key) + len(value.Revision)
	}
	for key, queue := range s.Queues {
		if !extension.ValidStorageKey(key) || queue == nil {
			return ErrCorrupt
		}
		total += len(key)
		for id, job := range queue {
			jobs++
			if job == nil || job.ID != id || id == "" || len(job.Dedup) > extension.StorageKeyMost || job.Attempts < 0 {
				return ErrCorrupt
			}
			switch job.State {
			case "pending", "leased", "unknown", "completed":
			default:
				return ErrCorrupt
			}
			data, err := decodeData(job.Data, ValueMost)
			if err != nil {
				return err
			}
			total += len(data) + len(id) + len(job.Dedup) + len(job.Lease) + len(job.Run)
		}
	}
	if jobs > JobsMost {
		return ErrQuota
	}
	for handle, blob := range s.Blobs {
		if handle == "" || blob == nil || !extension.ValidStorageKey(blob.Key) || blob.Bytes < 0 || blob.Bytes > DataMost || len(blob.Chunks) > DataMost/ChunkMost+1 {
			return ErrCorrupt
		}
		bytes := 0
		for _, chunk := range blob.Chunks {
			data, err := decodeData(chunk, ChunkMost)
			if err != nil {
				return err
			}
			bytes += len(data)
		}
		if bytes != blob.Bytes {
			return ErrCorrupt
		}
		total += bytes + len(handle) + len(blob.Key)
	}
	for id, receipt := range s.Receipts {
		if _, err := requestTime(id); err != nil || receipt.Fingerprint == "" || !json.Valid(receipt.Result) {
			return ErrCorrupt
		}
		total += len(id) + len(receipt.Fingerprint) + len(receipt.Result)
	}
	if total > DataMost {
		return ErrQuota
	}
	encoded, err := json.Marshal(s)
	if err != nil || len(encoded) > DocumentMost {
		return ErrQuota
	}
	return nil
}

// ReplaceRun fences old leases without claiming that an external effect can be
// safely repeated. Unclaimed pending work remains recoverable; a lost worker's
// leased work becomes explicitly unknown, never automatically a new send.
func (s *State) ReplaceRun(run string) {
	for _, queue := range s.Queues {
		for _, job := range queue {
			if job.State == "leased" && job.Run != run {
				job.State = "unknown"
			}
		}
	}
}
