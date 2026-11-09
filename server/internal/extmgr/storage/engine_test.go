package storage

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

func testScope() Scope {
	at := time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC)
	return Scope{Plugin: "fixture-storage", PublicID: "fixture-storage", InstalledAt: at, UpdatedAt: at, Run: "fixture-runtime", Granted: &extension.StoragePermission{Patterns: []string{"state/*", "queue/*", "media/*"}, Queues: true, Blobs: true}}
}
func encoded(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }
func call(t *testing.T, state *State, scope Scope, method string, value any, now time.Time) (any, bool, error) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return Execute(state, scope, method, raw, now)
}
func jsonValue(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var canonical any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTransactionCASAtomicFailureAndReplay(t *testing.T) {
	state, scope, now := NewState(), testScope(), time.Now()
	missing := ""
	in := Transaction{RequestID: NewRequestID(now), Operations: []Operation{{Kind: "put", Key: "state/a", Data: encoded("original"), Revision: &missing}}}
	first, changed, err := call(t, &state, scope, "storage.transaction", in, now)
	if err != nil || !changed {
		t.Fatal(first, changed, err)
	}
	revision := state.Entries["state/a"].Revision
	replayed, changed, err := call(t, &state, scope, "storage.transaction", in, now)
	if err != nil || changed || jsonValue(t, first) != jsonValue(t, replayed) {
		t.Fatal("replay changed the committed result", err)
	}
	in.Operations[0].Data = encoded("different")
	if _, _, err := call(t, &state, scope, "storage.transaction", in, now); !errors.Is(err, ErrConflict) {
		t.Fatal("same request accepted a new payload", err)
	}
	before := jsonValue(t, state)
	badRevision := "another-revision"
	bad := Transaction{RequestID: NewRequestID(now), Operations: []Operation{{Kind: "put", Key: "state/b", Data: encoded("must rollback")}, {Kind: "check", Key: "state/a", Revision: &badRevision}}}
	if _, _, err := call(t, &state, scope, "storage.transaction", bad, now); !errors.Is(err, ErrConflict) || jsonValue(t, state) != before {
		t.Fatal("failed transaction partially changed state", err)
	}
	good := Transaction{RequestID: NewRequestID(now), Operations: []Operation{{Kind: "put", Key: "state/a", Data: encoded("next"), Revision: &revision}, {Kind: "put", Key: "state/b", Data: encoded("also committed")}}}
	if _, changed, err := call(t, &state, scope, "storage.transaction", good, now); err != nil || !changed || state.Entries["state/a"].Data != encoded("next") || state.Entries["state/b"].Data != encoded("also committed") {
		t.Fatal("valid CAS transaction failed", err)
	}
}

func TestDefaultDenyUnknownFieldsAndQuotaLeaveStateUnchanged(t *testing.T) {
	state, scope, now := NewState(), testScope(), time.Now()
	before := jsonValue(t, state)
	cases := []struct {
		scope  Scope
		params string
		want   error
	}{
		{Scope{}, `{}`, ErrDenied},
		{scope, `{"request_id":"invalid","operations":[]}`, ErrInvalid},
		{scope, `{"request_id":"invalid","operations":[],"installation":"another"}`, ErrInvalid},
		{scope, `{"request_id":"invalid","operations":[{"kind":"put","key":"private/x","data":""}]}`, ErrDenied},
		{scope, `{"request_id":"invalid","operations":[{"kind":"put","key":"state/../private","data":""}]}`, ErrDenied},
	}
	for _, row := range cases {
		if _, _, err := Execute(&state, row.scope, "storage.transaction", json.RawMessage(row.params), now); !errors.Is(err, row.want) || jsonValue(t, state) != before {
			t.Fatal("invalid request changed state", err)
		}
	}
	tooLarge := Transaction{RequestID: NewRequestID(now), Operations: []Operation{{Kind: "put", Key: "state/a", Data: encoded(strings.Repeat("x", ValueMost+1))}}}
	if _, _, err := call(t, &state, scope, "storage.transaction", tooLarge, now); !errors.Is(err, ErrQuota) || jsonValue(t, state) != before {
		t.Fatal("oversized value changed state", err)
	}
	if _, _, err := Execute(&state, scope, "storage.transaction", json.RawMessage(strings.Repeat("x", RequestMost+1)), now); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized request admitted", err)
	}
}

func TestReceiptExhaustionExpiryAndUnknownRetryWindow(t *testing.T) {
	state, scope, now := NewState(), testScope(), time.Now()
	for i := 0; i < ReceiptsMost; i++ {
		state.Receipts[NewRequestID(now)] = Receipt{Fingerprint: "fixture", Result: json.RawMessage(`{}`)}
	}
	in := Transaction{RequestID: NewRequestID(now), Operations: []Operation{{Kind: "put", Key: "state/a", Data: encoded("x")}}}
	before := jsonValue(t, state)
	if _, _, err := call(t, &state, scope, "storage.transaction", in, now); !errors.Is(err, ErrQuota) || jsonValue(t, state) != before {
		t.Fatal("full live receipt ledger silently evicted a replay guard", err)
	}
	later := now.Add(ReceiptLifetime + time.Second)
	fresh := Transaction{RequestID: NewRequestID(later), Operations: in.Operations}
	if _, _, err := call(t, &state, scope, "storage.transaction", fresh, later); err != nil || len(state.Receipts) != 1 {
		t.Fatal("expired bounded receipt cleanup failed", err)
	}
	if _, _, err := call(t, &state, scope, "storage.transaction", in, later); !errors.Is(err, ErrExpired) {
		t.Fatal("expired unknown outcome silently retried as a new mutation", err)
	}
	future := Transaction{RequestID: NewRequestID(now.Add(time.Hour)), Operations: in.Operations}
	if _, _, err := call(t, &state, scope, "storage.transaction", future, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("forged future replay window accepted", err)
	}
}

func TestQueueDedupLeaseRetryAndGenerationLoss(t *testing.T) {
	state, scope, now := NewState(), testScope(), time.Now()
	in := Enqueue{RequestID: NewRequestID(now), Key: "queue/jobs", Dedup: "fixture-event", Data: encoded("payload")}
	value, _, err := call(t, &state, scope, "storage.queue.enqueue", in, now)
	if err != nil {
		t.Fatal(err)
	}
	id := value.(map[string]string)["id"]
	in.RequestID = NewRequestID(now)
	if _, _, err := call(t, &state, scope, "storage.queue.enqueue", in, now); err != nil || len(state.Queues[in.Key]) != 1 {
		t.Fatal("duplicate event created another job", err)
	}
	in.Data = encoded("changed")
	if _, _, err := call(t, &state, scope, "storage.queue.enqueue", in, now); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate key accepted a changed payload", err)
	}
	claim := Claim{RequestID: NewRequestID(now), Key: in.Key, LeaseSeconds: 30}
	if _, _, err := call(t, &state, scope, "storage.queue.claim", claim, now); err != nil {
		t.Fatal(err)
	}
	job := state.Queues[in.Key][id]
	if job.State != "leased" || job.Run != scope.Run || job.Lease == "" || job.Attempts != 1 {
		t.Fatal("claim lacks a bound lease")
	}
	oldLease := job.Lease
	retry := Retry{RequestID: NewRequestID(now), Key: in.Key, ID: id, Lease: oldLease, NotBefore: now.Add(time.Second).UnixMilli()}
	if _, _, err := call(t, &state, scope, "storage.queue.retry", retry, now); err != nil || state.Queues[in.Key][id].ID != id {
		t.Fatal("explicit retry changed original job identity", err)
	}
	beforeReplay := jsonValue(t, state)
	if _, changed, err := call(t, &state, scope, "storage.queue.retry", retry, now.Add(2*time.Second)); err != nil || changed || jsonValue(t, state) != beforeReplay {
		t.Fatal("committed retry could not recover after its due time", err)
	}
	late := retry
	late.RequestID = NewRequestID(now.Add(2 * time.Second))
	if _, _, err := call(t, &state, scope, "storage.queue.retry", late, now.Add(2*time.Second)); !errors.Is(err, ErrInvalid) {
		t.Fatal("new retry admitted a past deadline", err)
	}
	later := now.Add(2 * time.Second)
	claim.RequestID = NewRequestID(later)
	if _, _, err := call(t, &state, scope, "storage.queue.claim", claim, later); err != nil {
		t.Fatal(err)
	}
	lease := state.Queues[in.Key][id].Lease
	if lease == oldLease {
		t.Fatal("reused an expired worker capability")
	}
	state.ReplaceRun("next-runtime")
	if state.Queues[in.Key][id].State != "unknown" {
		t.Fatal("lost worker became a safe automatic retry")
	}
	scope.Run = "next-runtime"
	ack := Settle{RequestID: NewRequestID(later), Key: in.Key, ID: id, Lease: lease}
	if _, _, err := call(t, &state, scope, "storage.queue.ack", ack, later); !errors.Is(err, ErrStale) {
		t.Fatal("replacement worker accepted old lease", err)
	}
	claim.RequestID = NewRequestID(later)
	if _, _, err := call(t, &state, scope, "storage.queue.claim", claim, later); err != nil || state.Queues[in.Key][id].State != "unknown" {
		t.Fatal("unknown send was requeued automatically", err)
	}
}

func TestEveryQueuedJobIsCursorReachableWithinFrameBudget(t *testing.T) {
	state, scope, now := NewState(), testScope(), time.Now()
	state.Queues["queue/jobs"] = map[string]*Job{}
	for i := 0; i < JobsMost; i++ {
		id := fmt.Sprintf("fixture-%03d", i)
		state.Queues["queue/jobs"][id] = &Job{ID: id, Dedup: id, Data: encoded("fixture"), State: "pending"}
	}
	seen, after := []string{}, ""
	for {
		value, _, err := call(t, &state, scope, "storage.queue.list", QueueList{Key: "queue/jobs", After: after, Limit: 16}, now)
		if err != nil {
			t.Fatal(err)
		}
		items := value.(map[string]any)["items"].([]JobView)
		for _, item := range items {
			seen = append(seen, item.ID)
			after = item.ID
		}
		if !value.(map[string]any)["more"].(bool) {
			break
		}
	}
	if len(seen) != JobsMost || seen[0] != "fixture-000" || seen[len(seen)-1] != "fixture-127" {
		t.Fatal("bounded page lost queued work", seen)
	}
	in := Enqueue{RequestID: NewRequestID(now), Key: "queue/jobs", Dedup: "one-more", Data: encoded("fixture")}
	before := jsonValue(t, state)
	if _, _, err := call(t, &state, scope, "storage.queue.enqueue", in, now); !errors.Is(err, ErrQuota) || jsonValue(t, state) != before {
		t.Fatal("full queue silently dropped or evicted work", err)
	}
}

func TestOpaqueBlobHandlesChunkingSealAndGrantWithdrawal(t *testing.T) {
	state, scope, now := NewState(), testScope(), time.Now()
	create := CreateBlob{RequestID: NewRequestID(now), Key: "media/image"}
	value, _, err := call(t, &state, scope, "storage.blob.create", create, now)
	if err != nil {
		t.Fatal(err)
	}
	handle := value.(map[string]string)["handle"]
	if strings.ContainsAny(handle, "/\\:") {
		t.Fatal("blob handle is a path or URL")
	}
	for _, row := range []struct {
		offset int
		data   string
	}{{0, "first"}, {5, "second"}} {
		in := WriteBlob{RequestID: NewRequestID(now), Key: "media/image", Handle: handle, Offset: row.offset, Data: encoded(row.data)}
		if _, _, err := call(t, &state, scope, "storage.blob.write", in, now); err != nil {
			t.Fatal(err)
		}
		before := state.Blobs[handle].Bytes
		if _, changed, err := call(t, &state, scope, "storage.blob.write", in, now); err != nil || changed || state.Blobs[handle].Bytes != before {
			t.Fatal("replayed chunk was appended twice", err)
		}
	}
	if _, _, err := call(t, &state, scope, "storage.blob.read", ReadBlob{Key: "media/image", Handle: handle, Limit: 20}, now); !errors.Is(err, ErrConflict) {
		t.Fatal("unsealed blob was exposed", err)
	}
	if _, _, err := call(t, &state, scope, "storage.blob.finish", MutateBlob{RequestID: NewRequestID(now), Key: "media/image", Handle: handle}, now); err != nil {
		t.Fatal(err)
	}
	value, _, err = call(t, &state, scope, "storage.blob.read", ReadBlob{Key: "media/image", Handle: handle, Offset: 3, Limit: 5}, now)
	if err != nil || value.(map[string]any)["data"] != encoded("stsec") {
		t.Fatal("read across chunk boundary failed", value, err)
	}
	withdrawn := scope
	withdrawn.Granted = &extension.StoragePermission{Patterns: []string{"state/*"}, Blobs: true}
	if _, _, err := call(t, &state, withdrawn, "storage.blob.read", ReadBlob{Key: "media/image", Handle: handle, Limit: 20}, now); !errors.Is(err, ErrDenied) {
		t.Fatal("handle bypassed current key grant", err)
	}
	other := NewState()
	if _, _, err := call(t, &other, scope, "storage.blob.read", ReadBlob{Key: "media/image", Handle: handle, Limit: 20}, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign namespace handle returned data", err)
	}
	before := jsonValue(t, state)
	if _, _, err := call(t, &state, scope, "storage.blob.write", WriteBlob{RequestID: NewRequestID(now), Key: "media/image", Handle: handle, Offset: 11, Data: encoded("late")}, now); !errors.Is(err, ErrConflict) || jsonValue(t, state) != before {
		t.Fatal("sealed blob mutated", err)
	}
}

func TestPersistedCorruptionIsNotReplacedWithDefaults(t *testing.T) {
	state, scope, now := NewState(), testScope(), time.Now()
	state.Version = Version + 1
	before := reflect.ValueOf(state.Version).Int()
	if _, _, err := call(t, &state, scope, "storage.request", struct{}{}, now); !errors.Is(err, ErrCorrupt) || int64(state.Version) != before {
		t.Fatal("unknown format silently reset", err)
	}
	state = NewState()
	state.Blobs["fixture"] = &Blob{Key: "media/image", Chunks: []string{encoded("payload")}, Bytes: 1}
	if _, _, err := call(t, &state, scope, "storage.request", struct{}{}, now); !errors.Is(err, ErrCorrupt) {
		t.Fatal("corrupt blob length accepted", err)
	}
}
