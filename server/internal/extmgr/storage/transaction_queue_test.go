package storage

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestAtomicQueueAndProgressTransactionRollsBackTogether(t *testing.T) {
	state, scope, now := NewState(), testScope(), time.Now()
	in := Transaction{RequestID: NewRequestID(now), Operations: []Operation{
		{Kind: "put", Key: "state/progress", Data: encoded("queued")},
		{Kind: "enqueue", Key: "queue/jobs", Dedup: "fixture", Data: encoded("work")},
	}}
	if _, changed, err := call(t, &state, scope, "storage.transaction", in, now); err != nil || !changed {
		t.Fatal(err)
	}
	if len(state.Queues["queue/jobs"]) != 1 || state.Entries["state/progress"].Data != encoded("queued") {
		t.Fatal("progress and queue were not committed together")
	}
	before := jsonValue(t, state)
	bad := Transaction{RequestID: NewRequestID(now), Operations: []Operation{
		{Kind: "put", Key: "state/progress", Data: encoded("must rollback")},
		{Kind: "enqueue", Key: "queue/jobs", Dedup: "fixture", Data: encoded("changed work")},
	}}
	if _, _, err := call(t, &state, scope, "storage.transaction", bad, now); !errors.Is(err, ErrConflict) || before != jsonValue(t, state) {
		t.Fatal("queue conflict partially committed progress", err)
	}
	var id string
	for key := range state.Queues["queue/jobs"] {
		id = key
	}
	if _, _, err := call(t, &state, scope, "storage.queue.claim", Claim{RequestID: NewRequestID(now), Key: "queue/jobs", LeaseSeconds: 30}, now); err != nil {
		t.Fatal(err)
	}
	lease := state.Queues["queue/jobs"][id].Lease
	ack := Transaction{RequestID: NewRequestID(now), Operations: []Operation{
		{Kind: "put", Key: "state/progress", Data: encoded("completed")},
		{Kind: "ack", Key: "queue/jobs", JobID: id, Lease: lease},
	}}
	first, _, err := call(t, &state, scope, "storage.transaction", ack, now)
	if err != nil {
		t.Fatal(err)
	}
	if state.Entries["state/progress"].Data != encoded("completed") || state.Queues["queue/jobs"][id].State != "completed" {
		t.Fatal("completion and queue receipt not atomic")
	}
	replayed, changed, err := call(t, &state, scope, "storage.transaction", ack, now)
	if err != nil || changed || jsonValue(t, first) != jsonValue(t, replayed) {
		t.Fatal("completion replay changed result", err)
	}
}

func TestDeletedBlobMutationReplayAndCurrentGrantAreBothEnforced(t *testing.T) {
	state, scope, now := NewState(), testScope(), time.Now()
	value, _, err := call(t, &state, scope, "storage.blob.create", CreateBlob{RequestID: NewRequestID(now), Key: "media/fixture"}, now)
	if err != nil {
		t.Fatal(err)
	}
	handle := value.(map[string]string)["handle"]
	in := MutateBlob{RequestID: NewRequestID(now), Key: "media/fixture", Handle: handle}
	first, _, err := call(t, &state, scope, "storage.blob.delete", in, now)
	if err != nil {
		t.Fatal(err)
	}
	value, changed, err := call(t, &state, scope, "storage.blob.delete", in, now)
	if err != nil || changed || jsonValue(t, first) != jsonValue(t, value) {
		t.Fatal("lost delete response could not replay", err)
	}
	scope.Granted.Keys = []string{"state/only"}
	scope.Granted.Patterns = nil
	if _, _, err := call(t, &state, scope, "storage.blob.delete", in, now); !errors.Is(err, ErrDenied) {
		t.Fatal("receipt bypassed grant withdrawal", err)
	}
}

func TestGrantFlagsAndAggregateBytesDoNotAdmitPrivilegeOrTruncation(t *testing.T) {
	state, scope, now := NewState(), testScope(), time.Now()
	scope.Granted.Queues = false
	queue := Transaction{RequestID: NewRequestID(now), Operations: []Operation{{Kind: "enqueue", Key: "queue/jobs", Dedup: "one", Data: encoded("payload")}}}
	if _, _, err := call(t, &state, scope, "storage.transaction", queue, now); !errors.Is(err, ErrDenied) {
		t.Fatal("transaction bypassed separate queue flag", err)
	}
	scope.Granted.Blobs = false
	if _, _, err := call(t, &state, scope, "storage.blob.create", CreateBlob{RequestID: NewRequestID(now), Key: "media/one"}, now); !errors.Is(err, ErrDenied) {
		t.Fatal("declared key bypassed separate blob flag", err)
	}
	// Build a valid logical-key aggregate directly; the last value crosses the
	// decoded-byte cap without allocating or increasing an RPC frame.
	state = NewState()
	payload := encoded(string(make([]byte, ValueMost)))
	for i := 0; i < 128; i++ {
		state.Entries["state/"+time.Unix(int64(i), 0).UTC().Format("150405")] = Entry{Revision: "fixture", Data: payload}
	}
	if err := state.Validate(); !errors.Is(err, ErrQuota) {
		t.Fatal("aggregate value quota was not enforced", err)
	}
	invalid := json.RawMessage(`{"key":"state/one"}{"key":"state/two"}`)
	state = NewState()
	if _, _, err := Execute(&state, scope, "storage.get", invalid, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("concatenated request accepted", err)
	}
}

func TestTransactionReceiptCannotBypassWithdrawnQueueFlag(t *testing.T) {
	state, scope, now := NewState(), testScope(), time.Now()
	in := Transaction{RequestID: NewRequestID(now), Operations: []Operation{{Kind: "enqueue", Key: "queue/jobs", Dedup: "fixture", Data: encoded("work")}}}
	if _, _, err := call(t, &state, scope, "storage.transaction", in, now); err != nil {
		t.Fatal(err)
	}
	before := jsonValue(t, state)
	scope.Granted.Queues = false
	if _, changed, err := call(t, &state, scope, "storage.transaction", in, now); !errors.Is(err, ErrDenied) || changed || jsonValue(t, state) != before {
		t.Fatal("live receipt bypassed withdrawn queue authority", err)
	}
}

func TestClaimReceiptDoesNotReviveExpiredOrReplacedLease(t *testing.T) {
	for _, scenario := range []string{"expired", "replaced", "retry"} {
		t.Run(scenario, func(t *testing.T) {
			state, scope, now := NewState(), testScope(), time.Now()
			if _, _, err := call(t, &state, scope, "storage.queue.enqueue", Enqueue{RequestID: NewRequestID(now), Key: "queue/jobs", Dedup: "fixture", Data: encoded("work")}, now); err != nil {
				t.Fatal(err)
			}
			claim := Claim{RequestID: NewRequestID(now), Key: "queue/jobs", LeaseSeconds: 30}
			first, _, err := call(t, &state, scope, "storage.queue.claim", claim, now)
			if err != nil {
				t.Fatal(err)
			}
			if replay, changed, err := call(t, &state, scope, "storage.queue.claim", claim, now.Add(time.Second)); err != nil || changed || jsonValue(t, first) != jsonValue(t, replay) {
				t.Fatal("active claim could not recover its lost reply", err)
			}
			later := now.Add(2 * time.Second)
			switch scenario {
			case "expired":
				later = now.Add(31 * time.Second)
			case "replaced":
				scope.Run = "next-runtime"
				state.ReplaceRun(scope.Run)
			case "retry":
				for _, job := range state.Queues[claim.Key] {
					if _, _, err := call(t, &state, scope, "storage.queue.retry", Retry{RequestID: NewRequestID(now), Key: claim.Key, ID: job.ID, Lease: job.Lease, NotBefore: later.UnixMilli()}, now); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := jsonValue(t, state)
			if _, changed, err := call(t, &state, scope, "storage.queue.claim", claim, later); !errors.Is(err, ErrStale) || changed || before != jsonValue(t, state) {
				t.Fatal("old claim receipt revived a lease", err)
			}
		})
	}
}

func TestCanonicalDataAndBoundedQueueNames(t *testing.T) {
	for _, value := range []string{"Zg==\n", "Zh==", "Zg==\r\n", "Zg"} {
		if _, err := decodeData(value, ValueMost); !errors.Is(err, ErrInvalid) {
			t.Fatalf("noncanonical base64 admitted: %q: %v", value, err)
		}
	}
	if data, err := decodeData("Zg==", ValueMost); err != nil || string(data) != "f" {
		t.Fatal("canonical base64 refused", err)
	}
	state := NewState()
	for i := 0; i <= EntriesMost; i++ {
		state.Queues["queue/"+time.Unix(int64(i), 0).UTC().Format("150405")] = map[string]*Job{}
	}
	if err := state.Validate(); !errors.Is(err, ErrQuota) {
		t.Fatal("empty queue names escaped the count quota", err)
	}
}
