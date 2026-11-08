package sessionlist

import (
	"errors"
	"reflect"
	"testing"
)

// Revision and Query are separate reads: concurrent writers may evict the
// baseline between them. Only that typed expiry calls for a full refresh.
func queryDeltaOrRefresh(i *Index, baseline string) (Page, error) {
	page, err := i.Query(Options{Since: baseline})
	var expired *Error
	if errors.As(err, &expired) && expired.Code == "revision_expired" {
		return i.Query(Options{})
	}
	return page, err
}

func TestConcurrentRevisionExpiryAndRecovery(t *testing.T) {
	var i Index
	i.Replace("writer", []Item{testItem("a", 0), testItem("b", 1)})
	baseline := i.Revision()
	// The writer completes only after the reader has obtained its baseline.
	// No sleeps or scheduler assumptions are needed to exercise the lock gap.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := 1; n <= maxJournalRevisions+1; n++ {
			i.Replace("writer", []Item{testItem("a", n), testItem("c", n+1)})
		}
	}()
	<-done
	_, err := i.Query(Options{Since: baseline})
	requireCode(t, err, "revision_expired")
	if len(i.journal) != maxJournalRevisions || i.floor != 2 {
		t.Fatalf("journal=%d floor=%d", len(i.journal), i.floor)
	}
	full, err := queryDeltaOrRefresh(&i, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sessionIDs(full), []string{"c", "a"}) || full.Total != 2 ||
		len(full.Remove) != 0 || full.HasMore || full.NextCursor != "" || full.Revision != i.Revision() {
		t.Fatalf("full recovery = %+v", full)
	}
	if got := full.Sessions[1].(testPayload); !reflect.DeepEqual(got, testPayload{ID: "a", Title: "Full a"}) {
		t.Fatalf("recovered payload = %+v", got)
	}
	// The recovered highwater supports the next real delta, including removal.
	i.Replace("writer", []Item{testItem("a", maxJournalRevisions+2), testItem("d", maxJournalRevisions+3)})
	delta, err := queryDeltaOrRefresh(&i, full.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sessionIDs(delta), []string{"d", "a"}) ||
		!reflect.DeepEqual(delta.Remove, []string{"c"}) || delta.Total != 2 ||
		delta.HasMore || delta.Revision == full.Revision || delta.Revision != i.Revision() {
		t.Fatalf("delta after recovery = %+v", delta)
	}
}

func TestRevisionRecoveryPreservesOtherErrors(t *testing.T) {
	var i Index
	_, err := queryDeltaOrRefresh(&i, "malformed")
	requireCode(t, err, "invalid_query")
	i.Revision()
	_, err = queryDeltaOrRefresh(&i, "v1."+i.epoch+".1")
	requireCode(t, err, "invalid_query") // valid token, but ahead of this index
}
