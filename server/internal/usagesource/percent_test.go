package usagesource

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A gateway's used_percent is already 0-100: it is kept as sent, only clamped, so a hub feeding another hub hands on
// its own numbers and a client that truncates shows 29, not 28.
func TestGenericUsedPercentIsKeptExact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"accounts":[{"provider":"codex","windows":[
		  {"label":"45m","used_percent":29},{"label":"1h","used_percent":57},{"label":"5h","used_percent":7},
		  {"label":"1d","used_percent":150},{"label":"7d","used_percent":-3},{"label":"30d","used_percent":22.5}]}]}`)
	}))
	defer srv.Close()
	res, err := Fetch(context.Background(), Config{Type: TypeGeneric, URL: srv.URL, Key: testKey}, now)
	if err != nil || len(res.Accounts) != 1 {
		t.Fatalf("%v %+v", err, res.Accounts)
	}
	want := []float64{29, 57, 7, 100, 0, 22.5}
	got := res.Accounts[0].Windows
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i, w := range got {
		if w.UsedPercent != want[i] {
			t.Errorf("%s: used %v, want exactly %v", w.Label, w.UsedPercent, want[i])
		}
	}
}

// A relay's ratio times 100 carries binary noise (0.29*100 = 28.999999999999996); it is rounded off, while a real
// fraction of a percent stays.
func TestRelayPercentHasNoBinaryNoise(t *testing.T) {
	g := newGateway(t, `{"accounts":[{"id":"c1","name":"x","provider":"codex","status":"active"}]}`, echoUsage(`{"accounts":[{"account_id":"c1","windows":[
	  {"kind":"five_hour","used_ratio":0.29,"checked_at":"2026-11-05T08:00:00Z"},
	  {"kind":"daily","used_ratio":0.225,"checked_at":"2026-11-05T08:00:00Z"},
	  {"kind":"weekly","remaining":71,"limit":100,"checked_at":"2026-11-05T08:00:00Z"},
	  {"kind":"monthly","remaining":2,"limit":3,"checked_at":"2026-11-05T08:00:00Z"}]}]}`))
	res, err := Fetch(context.Background(), g.cfg(), now)
	if err != nil || len(res.Accounts) != 1 {
		t.Fatalf("%v %+v", err, res.Accounts)
	}
	want := map[string]float64{"5h": 29, "1d": 22.5, "7d": 29}
	for _, w := range res.Accounts[0].Windows {
		if w.Label == "30d" {
			if d := w.UsedPercent - 100.0/3; d > 1e-9 || d < -1e-9 {
				t.Errorf("30d: used %v, want 33.333333333", w.UsedPercent)
			}
			continue
		}
		if w.UsedPercent != want[w.Label] {
			t.Errorf("%s: used %v, want exactly %v", w.Label, w.UsedPercent, want[w.Label])
		}
		delete(want, w.Label)
	}
	if len(want) != 0 {
		t.Errorf("windows missing: %v (got %+v)", want, res.Accounts[0].Windows)
	}
}

func TestPercentHelpers(t *testing.T) {
	for in, want := range map[float64]float64{29: 29, 57: 57, 7: 7, 150: 100, -3: 0, 33.333333333: 33.333333333} {
		if got, ok := clampPercent(in); !ok || got != want {
			t.Errorf("clampPercent(%v) = %v %v, want %v", in, got, ok, want)
		}
	}
	for in, want := range map[float64]float64{0.29: 29, 0.57: 57, 0.07: 7, 0.225: 22.5, 1.5: 100, -0.1: 0, 1e300: 100} {
		if got, ok := percent(in); !ok || got != want {
			t.Errorf("percent(%v) = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, ok := clampPercent(bad); ok {
			t.Errorf("clampPercent(%v) ok", bad)
		}
		if _, ok := percent(bad); ok {
			t.Errorf("percent(%v) ok", bad)
		}
	}
	// every whole percent a gateway can send survives both helpers
	for i := 0; i <= 100; i++ {
		if got, _ := clampPercent(float64(i)); got != float64(i) {
			t.Errorf("clampPercent(%d) = %v", i, got)
		}
		if got, _ := percent(float64(i) / 100); got != float64(i) {
			t.Errorf("percent(%d/100) = %v", i, got)
		}
	}
}
