package storage

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

// NewRequestID encodes its immutable expiry window in the identity itself. A
// caller must retry an uncertain mutation with this exact ID and payload, not a
// new timestamp/ID. After expiry the request is refused even if its receipt was
// pruned, rather than silently executed again.
func NewRequestID(now time.Time) string { return fmt.Sprintf("%d:%s", now.UnixMilli(), rand.Text()) }

func requestTime(id string) (time.Time, error) {
	stamp, nonce, ok := strings.Cut(id, ":")
	if !ok || len(id) > 80 || len(nonce) < 20 || len(nonce) > 40 || stamp == "" {
		return time.Time{}, ErrInvalid
	}
	for _, c := range stamp {
		if c < '0' || c > '9' {
			return time.Time{}, ErrInvalid
		}
	}
	for _, c := range nonce {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return time.Time{}, ErrInvalid
		}
	}
	ms, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || ms <= 0 || strconv.FormatInt(ms, 10) != stamp {
		return time.Time{}, ErrInvalid
	}
	return time.UnixMilli(ms), nil
}

func decode(params json.RawMessage, out any) error {
	if len(params) == 0 || len(params) > RequestMost || params[0] != '{' {
		return ErrInvalid
	}
	reader := json.NewDecoder(bytes.NewReader(params))
	reader.DisallowUnknownFields()
	if err := reader.Decode(out); err != nil {
		return ErrInvalid
	}
	var extra any
	if err := reader.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func allowed(grant *extension.StoragePermission, key string) error {
	if !grant.Allows(key) {
		return ErrDenied
	}
	return nil
}

func copyState(state *State) (State, error) {
	raw, err := json.Marshal(state)
	if err != nil || len(raw) > DocumentMost {
		return State{}, ErrCorrupt
	}
	var copy State
	if json.Unmarshal(raw, &copy) != nil {
		return State{}, ErrCorrupt
	}
	return copy, nil
}

func mutation(state *State, method, id string, params any, now time.Time, apply func(*State) (any, error)) (any, bool, error) {
	created, err := requestTime(id)
	if err != nil {
		return nil, false, err
	}
	if created.After(now.Add(ClockSkew)) {
		return nil, false, ErrInvalid
	}
	if created.Before(now.Add(-ReceiptLifetime)) {
		return nil, false, ErrExpired
	}
	canonical, err := json.Marshal(params)
	if err != nil {
		return nil, false, ErrInvalid
	}
	hash := sha256.Sum256(append([]byte(method+"\x00"), canonical...))
	fingerprint := hex.EncodeToString(hash[:])
	if receipt, exists := state.Receipts[id]; exists {
		if receipt.Fingerprint != fingerprint {
			return nil, false, ErrConflict
		}
		var result any
		if json.Unmarshal(receipt.Result, &result) != nil {
			return nil, false, ErrCorrupt
		}
		return result, false, nil
	}
	copy, err := copyState(state)
	if err != nil {
		return nil, false, err
	}
	for old := range copy.Receipts {
		created, err := requestTime(old)
		if err != nil {
			return nil, false, ErrCorrupt
		}
		if created.Before(now.Add(-ReceiptLifetime)) {
			delete(copy.Receipts, old)
		}
	}
	if len(copy.Receipts) >= ReceiptsMost {
		return nil, false, ErrQuota
	}
	result, err := apply(&copy)
	if err != nil {
		return nil, false, err
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > RequestMost {
		return nil, false, ErrQuota
	}
	copy.Receipts[id] = Receipt{Fingerprint: fingerprint, Result: raw}
	if err := copy.Validate(); err != nil {
		return nil, false, err
	}
	*state = copy
	return result, true, nil
}

type Operation struct {
	Kind      string  `json:"kind"`
	Key       string  `json:"key"`
	Data      string  `json:"data,omitempty"`
	Revision  *string `json:"revision,omitempty"`
	Dedup     string  `json:"dedup,omitempty"`
	NotBefore int64   `json:"not_before,omitempty"`
	JobID     string  `json:"job_id,omitempty"`
	Lease     string  `json:"lease,omitempty"`
}

type Transaction struct {
	RequestID  string      `json:"request_id"`
	Operations []Operation `json:"operations"`
}

type KeyParams struct {
	Key string `json:"key"`
}

type ListParams struct {
	Prefix string `json:"prefix,omitempty"`
	After  string `json:"after,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type Item struct {
	Key      string `json:"key"`
	Revision string `json:"revision,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
	JobID    string `json:"job_id,omitempty"`
}

// Execute works only on a detached manager-owned snapshot. The backend serializes
// it with the installation row and persists the changed state atomically. A
// failed validation/CAS/quota operation leaves even this snapshot unchanged.
func Execute(state *State, scope Scope, method string, params json.RawMessage, now time.Time) (any, bool, error) {
	if scope.Granted == nil || scope.Plugin == "" || scope.Run == "" || scope.InstalledAt.IsZero() {
		return nil, false, ErrDenied
	}
	if err := state.Validate(); err != nil {
		return nil, false, err
	}
	switch method {
	case "storage.request":
		var in struct{}
		if err := decode(params, &in); err != nil {
			return nil, false, err
		}
		return map[string]string{"request_id": NewRequestID(now)}, false, nil
	case "storage.get":
		var in KeyParams
		if err := decode(params, &in); err != nil {
			return nil, false, err
		}
		if err := allowed(scope.Granted, in.Key); err != nil {
			return nil, false, err
		}
		value, exists := state.Entries[in.Key]
		if !exists {
			return nil, false, ErrNotFound
		}
		return value, false, nil
	case "storage.list":
		var in ListParams
		if err := decode(params, &in); err != nil || len(in.Prefix) > extension.StorageKeyMost || len(in.After) > extension.StorageKeyMost || in.Limit < 0 || in.Limit > 64 {
			return nil, false, ErrInvalid
		}
		if in.Limit == 0 {
			in.Limit = 16
		}
		keys := make([]string, 0, len(state.Entries))
		for key := range state.Entries {
			if key > in.After && strings.HasPrefix(key, in.Prefix) && scope.Granted.Allows(key) {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
		items := make([]Item, 0, min(in.Limit, len(keys)))
		for _, key := range keys[:min(in.Limit, len(keys))] {
			items = append(items, Item{Key: key, Revision: state.Entries[key].Revision})
		}
		return map[string]any{"items": items, "more": len(keys) > in.Limit}, false, nil
	case "storage.transaction":
		var in Transaction
		if err := decode(params, &in); err != nil || len(in.Operations) == 0 || len(in.Operations) > OperationsMost {
			return nil, false, ErrInvalid
		}
		for _, op := range in.Operations {
			if err := allowed(scope.Granted, op.Key); err != nil {
				return nil, false, err
			}
			// Current authority is checked before the receipt lookup, including
			// flags that can be withdrawn while a mutation receipt remains live.
			if (op.Kind == "enqueue" || op.Kind == "ack") && !scope.Granted.Queues {
				return nil, false, ErrDenied
			}
		}
		return mutation(state, method, in.RequestID, in, now, func(copy *State) (any, error) {
			items := make([]Item, 0, len(in.Operations))
			for _, op := range in.Operations {
				if op.Kind == "enqueue" || op.Kind == "ack" {
					if !scope.Granted.Queues || op.Revision != nil {
						return nil, ErrDenied
					}
					var id string
					if op.Kind == "enqueue" {
						if op.JobID != "" || op.Lease != "" {
							return nil, ErrInvalid
						}
						var err error
						id, err = enqueue(copy, Enqueue{Key: op.Key, Dedup: op.Dedup, Data: op.Data, NotBefore: op.NotBefore})
						if err != nil {
							return nil, err
						}
					} else {
						if op.Data != "" || op.Dedup != "" || op.NotBefore != 0 {
							return nil, ErrInvalid
						}
						job := copy.Queues[op.Key][op.JobID]
						if job == nil {
							return nil, ErrNotFound
						}
						if job.State != "leased" || job.Run != scope.Run || op.Lease == "" || job.Lease != op.Lease || job.LeaseUntil <= now.UnixMilli() {
							return nil, ErrStale
						}
						job.State = "completed"
						id = job.ID
					}
					items = append(items, Item{Key: op.Key, JobID: id})
					continue
				}
				if op.Dedup != "" || op.JobID != "" || op.Lease != "" || op.NotBefore != 0 {
					return nil, ErrInvalid
				}
				old, exists := copy.Entries[op.Key]
				if op.Revision != nil && ((!exists && *op.Revision != "") || (exists && old.Revision != *op.Revision)) {
					return nil, ErrConflict
				}
				switch op.Kind {
				case "check":
					if op.Revision == nil || op.Data != "" {
						return nil, ErrInvalid
					}
					items = append(items, Item{Key: op.Key, Revision: old.Revision})
				case "put":
					data, err := decodeData(op.Data, ValueMost)
					if err != nil {
						return nil, err
					}
					value := Entry{Revision: rand.Text(), Data: base64.StdEncoding.EncodeToString(data)}
					copy.Entries[op.Key] = value
					items = append(items, Item{Key: op.Key, Revision: value.Revision})
				case "delete":
					if op.Data != "" {
						return nil, ErrInvalid
					}
					delete(copy.Entries, op.Key)
					items = append(items, Item{Key: op.Key, Deleted: true})
				default:
					return nil, ErrInvalid
				}
			}
			return map[string]any{"items": items}, nil
		})
	default:
		if strings.HasPrefix(method, "storage.queue.") {
			return queueCall(state, scope, method, params, now)
		}
		if strings.HasPrefix(method, "storage.blob.") {
			return blobCall(state, scope, method, params, now)
		}
		return nil, false, ErrInvalid
	}
}
