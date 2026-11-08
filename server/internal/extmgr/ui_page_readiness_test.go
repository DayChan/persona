package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extwire"
)

func TestPageCanceledCallbackRecoveryAndAmbiguousReplay(t *testing.T) {
	for _, cause := range []string{"suspend", "context-cancel", "context-timeout", "host-timeout"} {
		for _, mode := range []string{"never", "late", "cooperative"} {
			t.Run(cause+"/"+mode, func(t *testing.T) {
				var h *Manager
				var process *host.Proc
				var output *pageTestHub
				if cause == "suspend" {
					h, process, output = pageProcessManager(t)
				} else {
					h, process, output = pageTrustedAdapterManager(t, cause == "host-timeout")
				}
				close(h.started)
				pageFixture(t, h, "callback", map[string]any{"mode": mode})
				tree := pageOpenTest(t, h, pageWho)
				run := process.Generation()
				original := pageEvent(t, tree, "no-ui", "uncertain-write")
				type reply struct {
					frame json.RawMessage
					err   error
				}
				pending := make(chan reply, 1)
				ctx, cancel := context.WithCancel(t.Context())
				if cause == "context-timeout" {
					cancel()
					ctx, cancel = context.WithTimeout(t.Context(), 150*time.Millisecond)
				}
				defer cancel()
				go func() {
					raw, _ := json.Marshal(wire.UIStreamClientEnvelope{Type: "extension_ui_stream", Request: &original})
					frame, err := h.uiStream(ctx, extwire.UIStreamCall{Who: pageWho, Envelope: raw})
					pending <- reply{frame, err}
				}()
				pageAwait(t, func() bool { return pageStats(t, process)["callbackActive"] == 1 })
				beforeCancel := pageStats(t, process)
				if cause == "suspend" {
					suspend := wire.UIStreamRequest{Request: "suspend", Protocol: 1, RequestID: "hide", Session: tree.Session(), Epoch: tree.Epoch()}
					if _, err := pageCall(t, h, pageWho, suspend); err != nil {
						t.Fatal(err)
					}
				} else if cause == "context-cancel" {
					cancel()
				}
				var first reply
				select {
				case first = <-pending:
				case <-time.After(3 * time.Second):
					t.Fatal("canceled callback RPC did not finish")
				}
				if first.err != nil || !bytes.Contains(first.frame, []byte(`"status":"ambiguous"`)) || bytes.Contains(first.frame, []byte(`"result"`)) {
					t.Fatalf("unknown commit lost receipt: %s %v", first.frame, first.err)
				}
				if pageTree(h, tree.Session()).Version() != tree.Version()+1 {
					t.Fatal("ambiguous write did not consume one wire revision")
				}
				pageAwait(t, func() bool { return pageStats(t, process)["callbackCanceled"] == 1 })
				afterCancel := pageStats(t, process)
				pageAwait(t, func() bool { return pageStats(t, process)["serviceTicks"] > afterCancel["serviceTicks"]+3 })
				stable := pageStats(t, process)
				h.uiPages.mu.Lock()
				retained := h.uiPages.sessions[tree.Session()] != nil
				h.uiPages.mu.Unlock()
				if !retained || process.Generation() != run || stable["uiTicks"] != afterCancel["uiTicks"] || stable["disposes"] != 0 || stable["mounts"] != beforeCancel["mounts"] {
					t.Fatalf("cancellation destroyed process/page or retained intervals: before=%v after=%v", afterCancel, stable)
				}
				t.Logf("PASS cancellation cause=%s active=%d canceled=%d uiTicks=%d serviceTicks=%d disposes=%d", cause, stable["callbackActive"], stable["callbackCanceled"], stable["uiTicks"], stable["serviceTicks"], stable["disposes"])
				// Manager receipts remain replayable even when the renderer's callback never settles.
				replay, err := pageCall(t, h, pageWho, original)
				if err != nil || !bytes.Equal(first.frame, replay) || pageStats(t, process)["writes"] != 1 {
					t.Fatalf("ambiguous replay repeated work: %s %v", replay, err)
				}
				resume := wire.UIStreamRequest{Request: "resume", Protocol: 1, RequestID: "recover-callback", Session: tree.Session(), Epoch: tree.Epoch(), Version: pageTree(h, tree.Session()).Version()}
				if mode == "cooperative" {
					pageAwait(t, func() bool { return pageStats(t, process)["callbackActive"] == 0 })
					if _, err := pageCall(t, h, pageWho, resume); err != nil {
						t.Fatal(err)
					}
					pageFixture(t, h, "callback", map[string]any{"mode": ""})
					fresh := pageEvent(t, pageTree(h, tree.Session()), "no-ui", "new-write")
					accepted, err := pageCall(t, h, pageWho, fresh)
					if err != nil || !bytes.Contains(accepted, []byte(`"status":"accepted"`)) {
						t.Fatalf("cooperative recovery failed: %s %v", accepted, err)
					}
					if _, err := pageCall(t, h, pageWho, fresh); err != nil || pageStats(t, process)["writes"] != 2 {
						t.Fatal("new action replay repeated callback")
					}
					return
				}
				// Exercise the actual correlated transport, not an error-text classifier.
				envelope, _ := json.Marshal(wire.UIStreamClientEnvelope{Type: "extension_ui_stream", Request: &resume})
				params, _ := json.Marshal(extwire.UIStreamCall{Who: pageWho, Envelope: envelope})
				if _, handled, err := h.Request(t.Context(), extwire.MethodUIStream, params); !handled || err != nil {
					t.Fatalf("uncorrelated recovery: %v", err)
				}
				pageAwait(t, func() bool {
					for _, packet := range output.all() {
						var frame wire.UIStreamServerEnvelope
						_ = json.Unmarshal(packet.Envelope, &frame)
						if frame.RequestID == resume.RequestID {
							return frame.Kind == "error" && frame.Error != nil && frame.Error.Code == "expired"
						}
					}
					return false
				})
				h.uiPages.mu.Lock()
				left := len(h.uiPages.sessions)
				h.uiPages.mu.Unlock()
				if left != 0 {
					t.Fatal("retired callback session retained")
				}
				if _, err := pageCall(t, h, pageWho, original); pageFailureCode(err) != "expired" {
					t.Fatal("old write regained execution authority")
				}
				pageFixture(t, h, "callback", map[string]any{"mode": ""})
				replacement := pageOpenTest(t, h, pageWho)
				if replacement.Session() == tree.Session() || replacement.Epoch() == tree.Epoch() {
					t.Fatal("new allocation reused retired identity")
				}
				if mode == "late" {
					pageFixture(t, h, "releaseCallback", nil)
					pageAwait(t, func() bool { return pageStats(t, process)["callbackCompleted"] == 1 })
					pageFixture(t, h, "stats", nil) // Ordered source round trip after callback completion.
					if bytes.Contains(pageTree(h, replacement.Session()).ViewJSON(), []byte(`"text":"99"`)) {
						t.Fatal("late callback revived retired state")
					}
				}
				fresh := pageEvent(t, replacement, "no-ui", "replacement-write")
				accepted, err := pageCall(t, h, pageWho, fresh)
				if err != nil || !bytes.Contains(accepted, []byte(`"status":"accepted"`)) || pageStats(t, process)["writes"] != 2 {
					t.Fatalf("fresh allocation unusable: %s %v", accepted, err)
				}
				t.Log("PASS unknown commit fenced; correlated expired recovery; no automatic write replay")
			})
		}
	}
}

func loadedHTTP(t *testing.T, result extension.ActionResult, expected string) {
	t.Helper()
	if result.View == nil || len(result.View.Sections) != 1 || len(result.View.Sections[0].Items) == 0 || result.View.Sections[0].Items[0].Text != expected {
		raw, _ := json.Marshal(result)
		t.Fatalf("HTTP readiness: want %q, got %s", expected, raw)
	}
}

// Actual manager plus the official source Program over an injected Node host.
// Synthetic delayed data is not installed-Deno, egress or production acceptance.
func TestPageHTTPFiniteColdCachedResumeAndAction(t *testing.T) {
	for _, version := range []int{1, 3, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			h, process, _ := pageProcessManager(t)
			pageFixture(t, h, "loading", map[string]any{"enabled": true, "delay": 200})
			var in uiRequest
			in.Context.UIVersion = version
			var firstState string
			for turn := 0; turn < 3; turn++ {
				result, err := h.uiCall(t.Context(), "example-page", "home", in)
				if err != nil {
					t.Fatal(err)
				}
				loadedHTTP(t, result, "loaded")
				in.State = result.View.State
				if turn == 0 {
					firstState = in.State
				} else if in.State != firstState {
					t.Fatal("cached resume advanced the wire version or replaced the owner session")
				}
			}
			stats := pageStats(t, process)
			if stats["loadReads"] != 1 || stats["loadCanceled"] != 0 || stats["loadActive"] != 0 || stats["snapshotTransport"] != 1 || stats["mountUIVersion"] != version {
				t.Fatalf("cold/cached HTTP stats: %v", stats)
			}
			result, err := h.uiCall(t.Context(), "example-page", "home", in)
			if err != nil {
				t.Fatal(err)
			}
			in.Action = result.View.Sections[0].Items[1].Action.ID
			in.nativeResult = new(json.RawMessage)
			result, err = h.uiCall(t.Context(), "example-page", "home", in)
			if err != nil {
				t.Fatal(err)
			}
			loadedHTTP(t, result, "loaded")
			if !bytes.Contains(*in.nativeResult, []byte(`"toast":"Saved"`)) {
				t.Fatal("readiness lost callback presentation")
			}
			stats = pageStats(t, process)
			if stats["loadReads"] != 2 || stats["writes"] != 1 || stats["loadActive"] != 0 || stats["loadCanceled"] != 0 {
				t.Fatalf("HTTP callback canceled before readiness: %v", stats)
			}
			if _, err = h.uiCall(t.Context(), "example-page", "home", in); err == nil {
				t.Fatal("old HTTP callback checkpoint executed again")
			}
			if pageStats(t, process)["writes"] != 1 {
				t.Fatal("stale callback wrote twice")
			}
			t.Logf("PASS UI%d HTTP cold/resume/action with 200 ms finite read", version)
		})
	}
}

func TestPageNativeFiniteLoadStillStartsWithLoading(t *testing.T) {
	h, process, output := pageProcessManager(t)
	pageFixture(t, h, "loading", map[string]any{"enabled": true, "delay": 200})
	tree := pageOpenTest(t, h, pageWho)
	if tree.Snapshot().View.Sections[0].Items[0].Text != "loading" || pageStats(t, process)["snapshotTransport"] != 0 {
		t.Fatal("native mount waited for snapshot readiness")
	}
	pageAwait(t, func() bool { return pageTree(h, tree.Session()).Snapshot().View.Sections[0].Items[0].Text == "loaded" })
	if len(output.all()) < 2 || pageStats(t, process)["loadCanceled"] != 0 {
		t.Fatal("native finite load did not publish its existing delta path")
	}
	t.Log("PASS native initial loading and loaded delta")
}

func TestPageHTTPFiniteErrorAndBoundedTimeout(t *testing.T) {
	for _, scenario := range []string{"error", "timeout", "action-timeout"} {
		t.Run(scenario, func(t *testing.T) {
			h, process, _ := pageProcessManager(t)
			options := map[string]any{"enabled": true, "delay": 200, "fail": scenario == "error"}
			if scenario != "error" {
				options["timeout"] = 25
			}
			options["cached"] = scenario == "action-timeout"
			pageFixture(t, h, "loading", options)
			var in uiRequest
			in.Context.UIVersion = 6 // A snapshot-only UI6 is not a native stream.
			result, err := h.uiCall(t.Context(), "example-page", "home", in)
			if scenario == "error" {
				if err != nil {
					t.Fatal(err)
				}
				loadedHTTP(t, result, "error")
				return
			}
			if scenario == "action-timeout" {
				if err != nil {
					t.Fatal(err)
				}
				in.State = result.View.State
				in.Action = result.View.Sections[0].Items[1].Action.ID
				_, err = h.uiCall(t.Context(), "example-page", "home", in)
			}
			if err == nil {
				t.Fatal("timed-out HTTP readiness returned a successful View")
			}
			pageAwait(t, func() bool { return pageStats(t, process)["loadActive"] == 0 })
			stats := pageStats(t, process)
			if stats["loadCanceled"] != 1 {
				t.Fatalf("timeout retained UI loading: %v", stats)
			}
			if scenario == "action-timeout" {
				h.uiPages.mu.Lock()
				var receipt json.RawMessage
				for _, s := range h.uiPages.sessions {
					for _, saved := range s.receipts {
						receipt = saved.frame
					}
				}
				h.uiPages.mu.Unlock()
				if !bytes.Contains(receipt, []byte(`"status":"ambiguous"`)) || stats["writes"] != 1 {
					t.Fatalf("readiness timeout lost the ambiguous callback fence: %s %v", receipt, stats)
				}
				if _, err := h.uiCall(t.Context(), "example-page", "home", in); err == nil || pageStats(t, process)["writes"] != 1 {
					t.Fatal("old timed-out callback executed again")
				}
			}
			t.Log("PASS bounded readiness failure and cancellation")
		})
	}
}

func TestPageHTTPCancelPendingFiniteMount(t *testing.T) {
	h, process, _ := pageProcessManager(t)
	pageFixture(t, h, "loading", map[string]any{"enabled": true, "delay": 200})
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Millisecond)
	defer cancel()
	var in uiRequest
	in.Context.UIVersion = 6
	if _, err := h.uiCall(ctx, "example-page", "home", in); err == nil {
		t.Fatal("canceled HTTP mount returned a View")
	}
	pageAwait(t, func() bool { return pageStats(t, process)["loadActive"] == 0 })
	stats := pageStats(t, process)
	if stats["disposes"] != 1 || stats["loadCanceled"] != 1 {
		t.Fatalf("canceled finite factory retained: %v", stats)
	}
	h.uiPages.mu.Lock()
	left := len(h.uiPages.sessions)
	h.uiPages.mu.Unlock()
	if left != 0 {
		t.Fatal("retired finite-load reservation leaked")
	}
	t.Log("PASS canceled HTTP mount disposed and released")
}

func TestPageHTTPFiniteResumePausesIntervals(t *testing.T) {
	h, process, _ := pageProcessManager(t)
	pageFixture(t, h, "loading", map[string]any{"enabled": true})
	var in uiRequest
	in.Context.UIVersion = 6
	result, err := h.uiCall(t.Context(), "example-page", "home", in)
	if err != nil {
		t.Fatal(err)
	}
	before := pageStats(t, process)
	// Wait only for the independent headless-service tick, with pageAwait's deadline.
	pageAwait(t, func() bool { return pageStats(t, process)["serviceTicks"] > before["serviceTicks"]+3 })
	after := pageStats(t, process)
	if after["uiTicks"] != before["uiTicks"] || !strings.HasPrefix(result.View.State, legacyPagePrefix) {
		t.Fatalf("HTTP completion retained hidden UI subscription: before=%v after=%v", before, after)
	}
	t.Log("PASS hidden intervals paused; headless service remains independent")
}
