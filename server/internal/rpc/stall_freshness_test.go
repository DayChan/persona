package rpc

import (
	"testing"
	"time"
)

func TestProgressObservationFreshnessBounds(t *testing.T) {
	now := time.Now()
	fresh := progressObservation{value: 1, started: now, finished: now}
	if !fresh.freshAt(now.Add(controlStallPoll / 2)) {
		t.Fatal("FAIL prompt sampling was treated as unavailable")
	}
	if fresh.freshAt(now.Add(2 * controlStallPoll)) {
		t.Fatal("FAIL lock admission did not age the measurement")
	}
	slow := progressObservation{value: 1, started: now.Add(-2 * controlStallPoll), finished: now}
	if slow.freshAt(now) {
		t.Fatal("FAIL callback latency was ignored")
	}
	unknown := progressObservation{value: progressUnavailable, started: now, finished: now}
	if unknown.freshAt(now) {
		t.Fatal("FAIL unavailable query was treated as reliable")
	}
}
