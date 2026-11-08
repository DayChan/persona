package hub

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
)

// A card's answer (resolve) keeps no filled-in value in its operation receipt: the digest is stored, and the sha256 of
// a short password in it could be guessed offline (#1137). The password is a made-up value.

const receiptPassword = "hunter2-SECRET-xyz"

// resolveReceipt is a resolve handler behind receipts on store, and a poster of answers with op id id.
func resolveReceipt(store Store, id string) (post func(body string) *httptest.ResponseRecorder, calls *atomic.Int32) {
	calls = &atomic.Int32{}
	h := &Hub{store: store}
	handler := h.withClientOp("resolve", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	return func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/ix_card/resolve", strings.NewReader(body))
		r.Header.Set("X-Persona-Op-Id", id)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}, calls
}

// checkResolveReceipt answers a card with op id id on store: the answer runs once, the same answer with another
// password replays its receipt, and nothing stored carries the password. It returns the poster for more answers.
func checkResolveReceipt(t *testing.T, store Store, id string) func(body string) *httptest.ResponseRecorder {
	t.Helper()
	post, calls := resolveReceipt(store, id)
	if w := post(`{"allow":true,"values":{"password":"` + receiptPassword + `"}}`); w.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	op, err := store.ClientOp(context.Background(), id)
	if err != nil || op == nil {
		t.Fatalf("receipt: %v %v", op, err)
	}
	if stored, _ := json.Marshal(op); strings.Contains(string(stored), receiptPassword) {
		t.Fatalf("the receipt carries the password: %s", stored)
	}
	if w := post(`{"values":{"password":"another-SECRET-pw"},"allow":true}`); w.Code != http.StatusOK || w.Body.String() != `{"ok":true}` {
		t.Fatalf("the same answer with another password: %d %s", w.Code, w.Body)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("the answer ran %d times", n)
	}
	return post
}

// The same answer with another value is the same operation (its receipt replays), whatever case its field names are
// in; other fields or another choice are not; nothing stored carries the value.
func TestResolveReceiptLeavesFieldValuesOut(t *testing.T) {
	post := checkResolveReceipt(t, &clientOpsStore{ops: map[string]db.ClientOp{}}, "00000000-0000-4000-8000-0000000011aa")
	if w := post(`{"Allow":true,"Values":{"password":"third-SECRET-pw"}}`); w.Code != http.StatusOK || w.Body.String() != `{"ok":true}` {
		t.Fatalf("the same answer with Values: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{"allow":true,"values":{"code":"` + receiptPassword + `"}}`, `{"allow":false,"values":{"password":"` + receiptPassword + `"}}`} {
		if w := post(body); w.Code != http.StatusConflict {
			t.Fatalf("%s reused the receipt: %d %s", body, w.Code, w.Body)
		}
	}
}

// What a resolve's digest covers: the answer as proto.Resolution reads it (field names in any case), values by their
// key; one fixed thing for a body that is not one; any other kind of operation as it was sent.
func TestOpDigestParams(t *testing.T) {
	digest := func(kind, body string) string {
		b, _ := json.Marshal(opDigestParams(kind, []byte(body), normalizedOpParams([]byte(body))))
		return string(b)
	}
	lower := digest("resolve", `{"allow":true,"values":{"password":"`+receiptPassword+`"}}`)
	if upper := digest("resolve", `{"Allow":true,"Values":{"password":"another-SECRET-pw"}}`); upper != lower {
		t.Fatalf("Values: %s, values: %s", upper, lower)
	}
	if strings.Contains(lower, receiptPassword) || !strings.Contains(lower, `"values":{"password":"password"}`) {
		t.Fatalf("resolve digest covers %s", lower)
	}
	bad1, bad2 := digest("resolve", `{"values":{"password":"`+receiptPassword+`"`), digest("resolve", `not json `+receiptPassword)
	if bad1 != bad2 || strings.Contains(bad1, receiptPassword) {
		t.Fatalf("invalid bodies: %s / %s, want one fixed digest", bad1, bad2)
	}
	if got := digest("message", `{"text":"hi"}`); got != `{"text":"hi"}` {
		t.Fatalf("message params changed: %s", got)
	}
}

// The same on Postgres: the client_ops row has no trace of the value, and the receipt still replays.
func TestResolveReceiptInPostgres(t *testing.T) {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	checkResolveReceipt(t, testDB(t), fmt.Sprintf("00000000-0000-4000-8000-%x", b))
}
