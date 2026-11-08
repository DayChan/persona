package transcript

import (
	"testing"
	"time"
)

func TestUsageLimitText(t *testing.T) {
	for text, want := range map[string]bool{
		"You've hit your session limit · resets 4:20pm (Asia/Shanghai)":            true,
		"You've hit your weekly limit · resets Sep 28, 4pm (America/New_York)":     true,
		"  You've hit your Opus limit · resets 9am (UTC)":                          true,
		"You've hit your monthly spend limit.":                                     true,
		"API Error: Request rejected (429) · quota reached":                        false,
		"Fixed it. When you see \"You've hit your session limit\", wait for it.":   false,
		"You've hit your stride, nice work":                                        false,
		"You’ve hit your session limit · resets 4pm (UTC)":                         true,
		"You've hit your target of 90% coverage; next I'll look at the rate limit": false,
		"You've hit your goal. The limit is next":                                  false,
		"": false,
	} {
		if got := UsageLimitText(text); got != want {
			t.Errorf("UsageLimitText(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestUsageLimitReset(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	ny, _ := time.LoadLocation("America/New_York")
	at := time.Date(2026, 9, 25, 12, 14, 24, 0, sh) // 04:14:24 UTC
	for _, c := range []struct {
		text string
		at   time.Time
		want time.Time
		ok   bool
	}{
		{"You've hit your session limit · resets 4:20pm (Asia/Shanghai)", at, time.Date(2026, 9, 25, 16, 20, 0, 0, sh), true},
		{"You've hit your session limit · resets 4pm (Asia/Shanghai)", at, time.Date(2026, 9, 25, 16, 0, 0, 0, sh), true},
		{"You've hit your session limit · resets at 4pm (Asia/Shanghai)", at, time.Date(2026, 9, 25, 16, 0, 0, 0, sh), true},
		// Earlier in the day than the error: tomorrow's.
		{"You've hit your session limit · resets 2:05am (Asia/Shanghai)", at, time.Date(2026, 9, 26, 2, 5, 0, 0, sh), true},
		{"You've hit your session limit · resets 12am (Asia/Shanghai)", at, time.Date(2026, 9, 26, 0, 0, 0, 0, sh), true},
		{"You've hit your session limit · resets 12:30pm (Asia/Shanghai)", at, time.Date(2026, 9, 25, 12, 30, 0, 0, sh), true},
		{"You've hit your weekly limit · resets Sep 28, 4pm (America/New_York)", at, time.Date(2026, 9, 28, 16, 0, 0, 0, ny), true},
		{"You've hit your weekly limit · resets Sep 28 at 4:30pm (America/New_York)", at, time.Date(2026, 9, 28, 16, 30, 0, 0, ny), true},
		{"You've hit your weekly limit · resets Jan 2, 2027, 9am (UTC)", at, time.Date(2027, 1, 2, 9, 0, 0, 0, time.UTC), true},
		// A date already past this year is next year's.
		{"You've hit your weekly limit · resets Jan 2, 9am (UTC)", time.Date(2026, 12, 30, 10, 0, 0, 0, time.UTC),
			time.Date(2027, 1, 2, 9, 0, 0, 0, time.UTC), true},
		// No time zone: the clock of a machine the reader may not be on.
		{"You've hit your usage limit. Try again at 3:05 PM.", at, time.Time{}, false},
		{"Usage limit reached · limit resets 4:20pm", at, time.Time{}, false},
		{"You've hit your session limit · resets 4:20pm (Not/AZone)", at, time.Time{}, false},
		{"You've hit your session limit · resets 13pm (UTC)", at, time.Time{}, false},
		{"You've hit your weekly limit · resets Feb 30, 4pm (UTC)", at, time.Time{}, false},
		{"You've hit your session limit · resets 4:20pm (Asia/Shanghai)", time.Time{}, time.Time{}, false},
	} {
		got, ok := UsageLimitReset(c.text, c.at)
		if ok != c.ok || !got.Equal(c.want) {
			t.Errorf("UsageLimitReset(%q) = %v %v, want %v %v", c.text, got, ok, c.want, c.ok)
		}
		if ok && got.Location() != time.UTC {
			t.Errorf("UsageLimitReset(%q): %v is not UTC", c.text, got)
		}
	}
}
