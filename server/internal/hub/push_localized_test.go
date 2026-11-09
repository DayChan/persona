package hub

// An extension's hub.push_localized carries one payload per language it wrote: a device in a language it left out gets
// another of them, an empty one is refused, and a push built with nothing for a language skips that language's devices
// instead of panicking the hub (an app.call runs on the bus handler's goroutine, which nothing recovers).

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// localizedHub is testLive's hub with its phone in Japanese, and the count of the pushes it starts.
func localizedHub(t *testing.T) (*Hub, *fakeAPNs, *atomic.Int64) {
	t.Helper()
	h, f := testLive(t)
	if err := h.push.register(Device{DeviceID: "phone", Name: "phone", APNSToken: "devtok", Topic: "app.example", Locale: "ja"}); err != nil {
		t.Fatal(err)
	}
	return h, f, pushAttempts(h)
}

// pushAttempts counts the pushes the hub starts (not Live Activity ones). sendBuiltWith notes each attempt before it
// hands the push to a goroutine, so the count is final once the call that pushes has returned: a push that must not go
// is checked without waiting for it.
func pushAttempts(h *Hub) *atomic.Int64 {
	var n atomic.Int64
	record := h.push.traffic
	h.push.traffic = func(k trafficKey, bytes int64, elapsed time.Duration) {
		if record != nil {
			record(k, bytes, elapsed)
		}
		if k.action == "attempt" && k.source != "liveactivity" {
			n.Add(1)
		}
	}
	return &n
}

// pushLocalized calls hub.push_localized as the assistant's manager does, failing the test (not the binary) on a panic.
func pushLocalized(t *testing.T, h *Hub, params string) error {
	t.Helper()
	return hubAppCall(t, h, extwire.HubPushLocalized, params)
}

func hubAppCall(t *testing.T, h *Hub, method, params string) error {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s %s panicked: %v", method, params, r)
		}
	}()
	_, ok, err := h.extAppCall(context.Background(), assistantwire.AssistantID,
		assistantwire.AppCall{AppID: extwire.AppHub, Method: method, Params: json.RawMessage(params)})
	if !ok {
		t.Fatalf("%s %s: not the hub's method", method, params)
	}
	return err
}

// sentPushes fails unless the hub started exactly want pushes, and returns them once APNs got them all.
func sentPushes(t *testing.T, f *fakeAPNs, attempts *atomic.Int64, want int) []apnsCall {
	t.Helper()
	if n := attempts.Load(); n != int64(want) {
		t.Fatalf("the hub started %d pushes, want %d", n, want)
	}
	f.mu.Lock()
	got := append([]apnsCall{}, f.got...)
	f.mu.Unlock()
	if want > 0 {
		got = f.calls(t, want)
	}
	if len(got) != want {
		t.Fatalf("APNs got %d pushes, want %d: %+v", len(got), want, got)
	}
	return got
}

func TestPushLocalizedFallsBackForAMissingLanguage(t *testing.T) {
	h, f, attempts := localizedHub(t)
	if err := pushLocalized(t, h, `{"payloads":{"en":{"aps":{"alert":{"title":"English title","body":"English body"}}}}}`); err != nil {
		t.Fatalf("push.localized with only en: %v", err)
	}
	got := sentPushes(t, f, attempts, 1)
	if got[0].Token != "devtok" || !strings.Contains(string(got[0].Body), "English title") {
		t.Fatalf("the Japanese phone got %s %s, want the English alert", got[0].Token, got[0].Body)
	}
}

func TestPushLocalizedPrefersFallbackThenDefault(t *testing.T) {
	payloads := func(langs ...string) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, l := range langs {
			out[l] = map[string]any{"lang": l}
		}
		return out
	}
	for _, c := range []struct {
		langs []string
		lang  i18n.Lang
		want  string
	}{
		{[]string{"ja", "en", "zh-Hans"}, i18n.Ja, "ja"},
		{[]string{"en", "zh-Hans"}, i18n.Ja, "en"},
		{[]string{"zh-Hans", "xx"}, i18n.Ja, "zh-Hans"},
		{[]string{"yy", "xx"}, i18n.Ja, "xx"},
	} {
		got := localizedPayload(payloads(c.langs...), c.lang)
		if got["lang"] != c.want {
			t.Errorf("%v for %s: got %v, want %s", c.langs, c.lang, got, c.want)
		}
	}
	if got := localizedPayload(map[string]map[string]any{"en": nil, "ja": nil}, i18n.Ja); got != nil {
		t.Errorf("only null payloads: got %v, want none", got)
	}
}

func TestPushLocalizedWithoutPayloadsIsRefused(t *testing.T) {
	for _, params := range []string{`{}`, `{"payloads":{}}`, `{"payloads":{"en":null}}`} {
		h, f, attempts := localizedHub(t)
		if err := pushLocalized(t, h, params); err == nil {
			t.Errorf("push.localized %s: no error", params)
		}
		sentPushes(t, f, attempts, 0)
	}
}

func TestSendFromSkipsALanguageWithoutPayload(t *testing.T) {
	h, f, attempts := localizedHub(t)
	// A second Japanese device: the language's payload is already known to be missing when sendBuiltWith reaches it.
	for _, d := range []Device{
		{DeviceID: "watch", Name: "watch", APNSToken: "tok-watch", Topic: "app.example", Locale: "ja"},
		{DeviceID: "tablet", Name: "tablet", APNSToken: "tok-tablet", Topic: "app.example", Locale: "en"},
	} {
		if err := h.push.register(d); err != nil {
			t.Fatal(err)
		}
	}
	build := func(l i18n.Lang) map[string]any {
		if l != i18n.En {
			return nil
		}
		return map[string]any{"aps": map[string]any{"alert": map[string]any{"title": "only en"}}}
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("sendFrom with a nil payload panicked: %v", r)
			}
		}()
		h.push.sendFrom("", build, "", nil, quietNone, nil)
	}()
	if got := sentPushes(t, f, attempts, 1); got[0].Token != "tok-tablet" {
		t.Fatalf("the push went to %s, want only the English tablet", got[0].Token)
	}

	// Nothing in any language: no push at all.
	h, f, attempts = localizedHub(t)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("sendFrom with no payload panicked: %v", r)
			}
		}()
		h.push.sendFrom("", func(i18n.Lang) map[string]any { return nil }, "", nil, quietNone, nil)
	}()
	sentPushes(t, f, attempts, 0)
}

// push.background without a payload reaches sendBuiltWith with nil too (sendAs): no panic, no push.
func TestPushBackgroundWithoutPayloadSendsNothing(t *testing.T) {
	h, f, attempts := localizedHub(t)
	if err := hubAppCall(t, h, extwire.HubPushBackground, `{"devices":["phone"]}`); err != nil {
		t.Fatalf("push.background without a payload: %v", err)
	}
	sentPushes(t, f, attempts, 0)
}
