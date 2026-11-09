package usagesource

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

func decodeJSON(data []byte, out any) error { return json.Unmarshal(data, out) }

var lengthKind = lazyre.New(`^(\d+)(m|h|d|w)$`)

// KindMinutes is the length of a window from what a gateway calls it: "five_hour" / "5h" → 300, "weekly" / "7d" →
// 10080, "hourly", "daily", "monthly" (30 days) and "<n>m|h|d|w". A kind with the length as one of its words
// ("weekly_fable", "gemini_argon_weekly") has that length too. ok is false for what is not a time window ("credits",
// a "blocked" marker, a name that says nothing about length).
func KindMinutes(kind string) (minutes int, ok bool) {
	min, _, ok := splitKind(kind)
	return min, ok
}

// splitKind returns the window length in kind and the words left over once the length words are taken out (the model
// a scoped window is for, when the gateway did not say it).
func splitKind(kind string) (minutes int, rest string, ok bool) {
	k := strings.ToLower(strings.TrimSpace(kind))
	if m := lengthKind.FindStringSubmatch(k); m != nil {
		n, err := strconv.Atoi(m[1])
		unit := map[string]int{"m": 1, "h": 60, "d": 1440, "w": 10080}[m[2]]
		// The one-year cap is on the length, not on the count: "53w" is longer than a year. Bounding n by the cap over
		// the unit keeps n*unit within the cap, so it cannot overflow even a 32-bit int.
		if err == nil && n > 0 && n <= 366*1440/unit {
			return n * unit, "", true
		}
		return 0, "", false
	}
	words := strings.FieldsFunc(k, func(r rune) bool { return r == '_' || r == '-' || r == ' ' || r == '.' || r == ':' })
	var left []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		var n int
		skip := 0
		switch {
		case minutes != 0:
		case w == "five" && i+1 < len(words) && (words[i+1] == "hour" || words[i+1] == "hours"):
			n, skip = 300, 1
		case w == "5h" || w == "fivehour" || w == "5hour":
			n = 300
		case w == "hourly" || w == "hour":
			n = 60
		case w == "daily" || w == "day":
			n = 1440
		case w == "weekly" || w == "week" || w == "7d" || (w == "seven" && i+1 < len(words) && words[i+1] == "day"):
			n = 10080
			if w == "seven" {
				skip = 1
			}
		case w == "monthly" || w == "month" || w == "30d":
			n = 43200
		}
		if n != 0 {
			minutes = n
			i += skip
			continue
		}
		left = append(left, w)
	}
	if minutes == 0 {
		return 0, "", false
	}
	return minutes, strings.Join(left, " "), true
}

// clampPercent keeps a value that is already a percentage as it is, clamped to [0,100]; ok is false for a value that
// is not a number. No arithmetic, so a gateway's 29 stays 29 and a hub feeding another hub hands on its own numbers.
func clampPercent(p float64) (float64, bool) {
	if math.IsNaN(p) || math.IsInf(p, 0) {
		return 0, false
	}
	return math.Min(100, math.Max(0, p)), true
}

// percent turns a ratio in [0,1] into a percentage, clamped; ok is false for a value that is not a number. The
// product is rounded to 1e-9 to drop binary noise (0.29*100 is 28.999999999999996), far below display precision.
func percent(ratio float64) (float64, bool) {
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return 0, false
	}
	p := math.Min(100, math.Max(0, ratio*100))
	return math.Round(p*1e9) / 1e9, true
}

// addWindow puts w into list, replacing a window of the same length and model that was checked earlier.
func addWindow(list []proto.UsageWindow, checked map[int]time.Time, w proto.UsageWindow, at time.Time) []proto.UsageWindow {
	for i := range list {
		if list[i].WindowMinutes == w.WindowMinutes && list[i].Model == w.Model {
			if at.After(checked[i]) {
				list[i] = w
				checked[i] = at
			}
			return list
		}
	}
	checked[len(list)] = at
	return append(list, w)
}

// notAfter is t, or now when t is later: a gateway whose clock runs ahead (or that sends a bogus date) would
// otherwise hand in a sample that never ages out and is always the newest in a merge.
func notAfter(t, now time.Time) time.Time {
	if t.After(now) {
		return now.UTC()
	}
	return t
}
