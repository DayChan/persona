package storage

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"slices"
	"time"
)

type Enqueue struct {
	RequestID string `json:"request_id"`
	Key       string `json:"key"`
	Dedup     string `json:"dedup"`
	Data      string `json:"data"`
	NotBefore int64  `json:"not_before,omitempty"`
}

type Claim struct {
	RequestID    string `json:"request_id"`
	Key          string `json:"key"`
	LeaseSeconds int    `json:"lease_seconds"`
}

type QueueList struct {
	Key   string `json:"key"`
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type Retry struct {
	RequestID string `json:"request_id"`
	Key       string `json:"key"`
	ID        string `json:"id"`
	Lease     string `json:"lease"`
	NotBefore int64  `json:"not_before"`
}

type Settle struct {
	RequestID string `json:"request_id"`
	Key       string `json:"key"`
	ID        string `json:"id"`
	Lease     string `json:"lease,omitempty"`
}

type JobView struct {
	ID         string `json:"id"`
	Dedup      string `json:"dedup"`
	Data       string `json:"data"`
	State      string `json:"state"`
	NotBefore  int64  `json:"not_before"`
	Attempts   int    `json:"attempts"`
	Lease      string `json:"lease,omitempty"`
	LeaseUntil int64  `json:"lease_until,omitempty"`
}

func jobView(job *Job, now time.Time) JobView {
	state := job.State
	if state == "leased" && job.LeaseUntil <= now.UnixMilli() {
		state = "unknown"
	}
	return JobView{ID: job.ID, Dedup: job.Dedup, Data: job.Data, State: state, NotBefore: job.NotBefore, Attempts: job.Attempts, Lease: job.Lease, LeaseUntil: job.LeaseUntil}
}

func enqueue(state *State, in Enqueue) (string, error) {
	if in.Dedup == "" || len(in.Dedup) > 128 || in.NotBefore < 0 {
		return "", ErrInvalid
	}
	data, err := decodeData(in.Data, ValueMost)
	if err != nil {
		return "", err
	}
	in.Data = base64.StdEncoding.EncodeToString(data)
	queue := state.Queues[in.Key]
	for _, job := range queue {
		if job.Dedup == in.Dedup {
			if job.Data != in.Data || job.NotBefore != in.NotBefore {
				return "", ErrConflict
			}
			return job.ID, nil
		}
	}
	if queue == nil {
		queue = map[string]*Job{}
		state.Queues[in.Key] = queue
	}
	job := &Job{ID: rand.Text(), Dedup: in.Dedup, Data: in.Data, State: "pending", NotBefore: in.NotBefore}
	queue[job.ID] = job
	return job.ID, nil
}

func queueCall(state *State, scope Scope, method string, params json.RawMessage, now time.Time) (any, bool, error) {
	if !scope.Granted.Queues {
		return nil, false, ErrDenied
	}
	switch method {
	case "storage.queue.list":
		var in QueueList
		if err := decode(params, &in); err != nil || len(in.After) > 80 || in.Limit < 0 || in.Limit > 32 {
			return nil, false, ErrInvalid
		}
		if in.Limit == 0 {
			in.Limit = 16
		}
		if err := allowed(scope.Granted, in.Key); err != nil {
			return nil, false, err
		}
		queue := state.Queues[in.Key]
		ids := make([]string, 0, len(queue))
		for id := range queue {
			if id > in.After {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		items := make([]JobView, 0, min(in.Limit, len(ids)))
		// A bounded, cursor-addressable page keeps every job accessible without
		// encoding every maximum-sized payload into one source frame.
		for _, id := range ids[:min(in.Limit, len(ids))] {
			items = append(items, jobView(queue[id], now))
		}
		return map[string]any{"items": items, "more": len(ids) > in.Limit}, false, nil
	case "storage.queue.enqueue":
		var in Enqueue
		if err := decode(params, &in); err != nil || in.Dedup == "" || len(in.Dedup) > 128 || in.NotBefore < 0 {
			return nil, false, ErrInvalid
		}
		if err := allowed(scope.Granted, in.Key); err != nil {
			return nil, false, err
		}
		data, err := decodeData(in.Data, ValueMost)
		if err != nil {
			return nil, false, err
		}
		in.Data = base64.StdEncoding.EncodeToString(data)
		return mutation(state, method, in.RequestID, in, now, func(copy *State) (any, error) {
			id, err := enqueue(copy, in)
			if err != nil {
				return nil, err
			}
			return map[string]string{"id": id}, nil
		})
	case "storage.queue.claim":
		var in Claim
		if err := decode(params, &in); err != nil || in.LeaseSeconds <= 0 || time.Duration(in.LeaseSeconds) > LeaseMost/time.Second {
			return nil, false, ErrInvalid
		}
		if err := allowed(scope.Granted, in.Key); err != nil {
			return nil, false, err
		}
		value, changed, err := mutation(state, method, in.RequestID, in, now, func(copy *State) (any, error) {
			var next *Job
			for _, job := range copy.Queues[in.Key] {
				if job.State == "leased" && job.LeaseUntil <= now.UnixMilli() {
					job.State = "unknown"
				}
				if job.State == "pending" && job.NotBefore <= now.UnixMilli() && (next == nil || job.NotBefore < next.NotBefore || job.NotBefore == next.NotBefore && job.ID < next.ID) {
					next = job
				}
			}
			if next == nil {
				return map[string]any{}, nil
			}
			next.State, next.Lease, next.Run = "leased", rand.Text(), scope.Run
			next.LeaseUntil = now.Add(time.Duration(in.LeaseSeconds) * time.Second).UnixMilli()
			next.Attempts++
			return map[string]any{"job": jobView(next, now)}, nil
		})
		if err != nil || changed {
			return value, changed, err
		}
		// A replay receipt records the original claim, not a renewable lease.
		// Never hand a lost/expired/replaced worker its old "leased" result.
		var replay struct {
			Job *JobView `json:"job"`
		}
		raw, err := json.Marshal(value)
		if err != nil || json.Unmarshal(raw, &replay) != nil {
			return nil, false, ErrCorrupt
		}
		if replay.Job != nil {
			job := state.Queues[in.Key][replay.Job.ID]
			if job == nil || job.State != "leased" || job.Run != scope.Run || job.Lease != replay.Job.Lease || job.LeaseUntil <= now.UnixMilli() {
				return nil, false, ErrStale
			}
		}
		return value, false, nil
	case "storage.queue.retry":
		var in Retry
		if err := decode(params, &in); err != nil || in.ID == "" || len(in.ID) > 80 || in.NotBefore < 0 {
			return nil, false, ErrInvalid
		}
		if err := allowed(scope.Granted, in.Key); err != nil {
			return nil, false, err
		}
		return mutation(state, method, in.RequestID, in, now, func(copy *State) (any, error) {
			// Time-relative admission applies only to a new mutation. An exact
			// lost-response replay must still recover after this deadline passes.
			if in.NotBefore < now.UnixMilli() || in.NotBefore > now.Add(24*time.Hour).UnixMilli() {
				return nil, ErrInvalid
			}
			job := copy.Queues[in.Key][in.ID]
			if job == nil {
				return nil, ErrNotFound
			}
			if job.State != "leased" || job.Run != scope.Run || in.Lease == "" || job.Lease != in.Lease || job.LeaseUntil <= now.UnixMilli() {
				return nil, ErrStale
			}
			// An explicit current-worker retry preserves the original job identity.
			// It grants no external effect and is never automatic for an unknown
			// lease or a lost worker; integrations must preserve their write gate.
			job.State, job.NotBefore, job.Lease, job.Run, job.LeaseUntil = "pending", in.NotBefore, "", "", 0
			return map[string]string{"id": job.ID, "state": job.State}, nil
		})
	case "storage.queue.ack", "storage.queue.forget":
		var in Settle
		if err := decode(params, &in); err != nil || in.ID == "" || len(in.ID) > 80 {
			return nil, false, ErrInvalid
		}
		if err := allowed(scope.Granted, in.Key); err != nil {
			return nil, false, err
		}
		return mutation(state, method, in.RequestID, in, now, func(copy *State) (any, error) {
			job := copy.Queues[in.Key][in.ID]
			if job == nil {
				return nil, ErrNotFound
			}
			if method == "storage.queue.forget" {
				if job.State != "completed" || in.Lease != "" {
					return nil, ErrConflict
				}
				delete(copy.Queues[in.Key], in.ID)
				return map[string]bool{"deleted": true}, nil
			}
			if job.State != "leased" || job.Run != scope.Run || in.Lease == "" || job.Lease != in.Lease || job.LeaseUntil <= now.UnixMilli() {
				return nil, ErrStale
			}
			job.State = "completed"
			return map[string]string{"id": job.ID, "state": job.State}, nil
		})
	default:
		return nil, false, ErrInvalid
	}
}
