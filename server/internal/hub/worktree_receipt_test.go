package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
)

const worktreeReceiptID = "00000000-0000-4000-8000-000000001399"
const worktreeReceiptPath = "/api/v1/machines/machine/worktree/create"
const worktreeReceiptBody = `{"workspace_id":"repo::/code","kind":"orca","parent_workspace_id":"other::/collection","auto_name":true}`

func worktreeReceiptRequest(path, body, id, device, role string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if id != "" {
		r.Header.Set("X-Persona-Op-Id", id)
	}
	return r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{ID: device, Role: role}))
}

func worktreeReceiptRoutes(h *Hub) http.Handler {
	mux := http.NewServeMux()
	h.paneRoutes(mux)
	return mux
}

// The host has created a randomly named checkout, but the HTTP response is lost. Retrying the operation replays
// its result, even on another hub instance without a node; it must not create a second checkout.
func TestWorktreeCreateReceiptSurvivesLostResponse(t *testing.T) {
	store := &clientOpsStore{ops: map[string]db.ClientOp{}}
	h := newTestHub(t)
	h.store = store
	var creates atomic.Int32
	boardNode(t, h, "machine", func(_ context.Context, method string, raw json.RawMessage) (any, error) {
		if method != "worktree.create" {
			t.Errorf("unexpected node method %s", method)
		}
		creates.Add(1)
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if p["auto_name"] != true || p["workspace_id"] != "repo::/code" || p["parent_workspace_id"] != "other::/collection" {
			t.Errorf("source, parent or naming mode changed: %s", raw)
		}
		return map[string]any{"workspace_id": "repo::/worktrees/manta", "branch": "dev/manta", "path": "/worktrees/manta"}, nil
	})
	lost := &lostWorktreeResponse{header: make(http.Header)}
	worktreeReceiptRoutes(h).ServeHTTP(lost, worktreeReceiptRequest(worktreeReceiptPath, worktreeReceiptBody, worktreeReceiptID, "device", roleFull))
	if creates.Load() != 1 || lost.status != http.StatusOK || !lost.wrote {
		t.Fatalf("lost response: status=%d writes=%v creates=%d", lost.status, lost.wrote, creates.Load())
	}
	// Nothing is registered here: successful replay must use the saved receipt, not the original node.
	restarted := newTestHub(t)
	restarted.store = store
	replay := httptest.NewRecorder()
	worktreeReceiptRoutes(restarted).ServeHTTP(replay, worktreeReceiptRequest(worktreeReceiptPath,
		` { "auto_name": true, "parent_workspace_id":"other::/collection", "kind":"orca", "workspace_id":"repo::/code" } `,
		worktreeReceiptID, "device", roleFull))
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"branch":"dev/manta"`) || creates.Load() != 1 {
		t.Fatalf("retry: %d %s, creates=%d", replay.Code, replay.Body, creates.Load())
	}
	op, err := store.ClientOp(context.Background(), worktreeReceiptID)
	if err != nil || op == nil || op.Kind != "worktree_create" || op.State != "done" || !strings.Contains(string(op.Target), worktreeReceiptPath) {
		t.Fatalf("receipt: %+v, %v", op, err)
	}
	for _, tc := range []struct{ path, body, device string }{
		{worktreeReceiptPath, strings.Replace(worktreeReceiptBody, "/collection", "/other", 1), "device"},
		{strings.Replace(worktreeReceiptPath, "/machine/", "/other/", 1), worktreeReceiptBody, "device"},
		{strings.Replace(worktreeReceiptPath, "/create", "/rename", 1), worktreeReceiptBody, "device"},
		{worktreeReceiptPath, worktreeReceiptBody, "another-device"},
	} {
		w := httptest.NewRecorder()
		// The create handler is used for the changed path too, so digest binding is tested before dispatch.
		restarted.withClientOp("worktree_create", restarted.nodeAction(nodeActions["worktree/create"]))(w,
			worktreeReceiptRequest(tc.path, tc.body, worktreeReceiptID, tc.device, roleFull))
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "op_id_reused") || creates.Load() != 1 {
			t.Fatalf("changed request reused receipt: %d %s, creates=%d", w.Code, w.Body, creates.Load())
		}
	}
	// Clients without operation IDs retain the original route and need no receipt storage.
	h.store = nil
	legacy := httptest.NewRecorder()
	worktreeReceiptRoutes(h).ServeHTTP(legacy, worktreeReceiptRequest(worktreeReceiptPath, worktreeReceiptBody, "", "device", roleFull))
	if legacy.Code != http.StatusOK || creates.Load() != 2 {
		t.Fatalf("legacy create: %d %s, creates=%d", legacy.Code, legacy.Body, creates.Load())
	}
}

type lostWorktreeResponse struct {
	header http.Header
	status int
	wrote  bool
}

func (w *lostWorktreeResponse) Header() http.Header       { return w.header }
func (w *lostWorktreeResponse) WriteHeader(status int)    { w.status = status }
func (w *lostWorktreeResponse) Write([]byte) (int, error) { w.wrote = true; return 0, io.ErrClosedPipe }

// A node may time out after making the checkout. Keep its diagnostic response and never dispatch the same
// operation again, including after restart recovery has only an unknown state and no saved response.
func TestWorktreeCreateReceiptKeepsUnknownOutcome(t *testing.T) {
	store := &clientOpsStore{ops: map[string]db.ClientOp{}}
	h := newTestHub(t)
	h.store = store
	var creates atomic.Int32
	boardNode(t, h, "machine", func(context.Context, string, json.RawMessage) (any, error) {
		creates.Add(1)
		return nil, context.DeadlineExceeded
	})
	post := func(h *Hub) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		worktreeReceiptRoutes(h).ServeHTTP(w, worktreeReceiptRequest(worktreeReceiptPath, worktreeReceiptBody, worktreeReceiptID, "device", roleFull))
		return w
	}
	first := post(h)
	if first.Code != http.StatusBadGateway || !strings.Contains(first.Body.String(), `"code":"timeout"`) || creates.Load() != 1 {
		t.Fatalf("timeout response: %d %s, creates=%d", first.Code, first.Body, creates.Load())
	}
	op, _ := store.ClientOp(context.Background(), worktreeReceiptID)
	if op == nil || op.State != "unknown" {
		t.Fatalf("ambiguous outcome lost: %+v", op)
	}
	restarted := newTestHub(t)
	restarted.store = store
	replay := post(restarted)
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() || creates.Load() != 1 {
		t.Fatalf("timeout replay: %d %s, creates=%d", replay.Code, replay.Body, creates.Load())
	}
	store.mu.Lock()
	recovered := store.ops[worktreeReceiptID]
	recovered.Result = nil // RecoverClientOps cannot reconstruct a response lost during restart.
	store.ops[worktreeReceiptID] = recovered
	store.mu.Unlock()
	unknown := post(restarted)
	if unknown.Code != http.StatusAccepted || !strings.Contains(unknown.Body.String(), `"state":"unknown"`) || creates.Load() != 1 {
		t.Fatalf("recovered outcome was retried: %d %s, creates=%d", unknown.Code, unknown.Body, creates.Load())
	}
}

func TestWorktreeCreateReceiptRequiresFullCredentials(t *testing.T) {
	for _, role := range []string{roleAnswer, roleExtension} {
		t.Run(role, func(t *testing.T) {
			h := newTestHub(t)
			h.store = &clientOpsStore{ops: map[string]db.ClientOp{}}
			var creates atomic.Int32
			boardNode(t, h, "machine", func(context.Context, string, json.RawMessage) (any, error) {
				creates.Add(1)
				return map[string]any{}, nil
			})
			w := httptest.NewRecorder()
			worktreeReceiptRoutes(h).ServeHTTP(w, worktreeReceiptRequest(worktreeReceiptPath, worktreeReceiptBody, worktreeReceiptID, "device", role))
			if w.Code != http.StatusForbidden || creates.Load() != 0 || allowed(principal{ID: "device", Role: role}, http.MethodPost, worktreeReceiptPath) {
				t.Fatalf("restricted create: %d %s, creates=%d", w.Code, w.Body, creates.Load())
			}
		})
	}
}
