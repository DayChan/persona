package assistant

import (
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// How fast the history import reads a source (docs/assistant/knowledge.md "History import", "Pacing"): every wait is
// drawn afresh so nothing about it is regular, calls come in batches with rests between them, and a source that
// slows down is read more slowly in turn. Two generic profiles, chosen per source in kb.history.sources:
//
//   - standard: 3–8 s between calls, a 2–10 minute rest every 50–150 calls (and whatever retry_after a source asks).
//   - careful (the default): 2–6 s skewed to the short end with an occasional 10–20 s, batches of 40–120 calls,
//     3–15 minute rests, a 30–90 minute rest every 2–4 hours, one or two quiet periods of 1–3 hours a day drawn
//     afresh each day, and 6–15 s between images.
//
// Both move to another chat every 20–200 pages. Only one request is ever in flight.

const (
	historyProfileStandard = "standard"
	historyProfileCareful  = "careful"

	historySlowFloor = 2 * time.Second // a call is slow above max(3 × its baseline, this)
	// historyVerySlow: a call is very slow above this, whatever its baseline (kept well under the 8 s a
	// source's own main-queue timeout trips at); an image download above max(3 × its baseline, this), since an image's
	// baseline is higher by nature.
	historyVerySlow      = 5 * time.Second
	historyIntervalCap   = time.Minute      // slowing down never waits longer than this between calls
	historyBaselineCalls = 20               // the baseline is the median of a day's first 20 calls of a method
	historySlowWindow    = 20               // three slow calls among the last 20: a rest, then the slowest pace
	historySlowRest      = 3                // (that three)
	historyNormalRun     = 50               // then every 50 normal calls in a row: back towards the default
	historyVerySlowRest  = 30 * time.Minute // a very slow call: a rest this long
	historyVerySlowWatch = 30 * time.Minute // and a second one this soon after the rest: stop
	historySlowStart     = 2 * time.Hour    // kb_history_resume with slow: the slowest pace this long, from its first call
	historyImageMethod   = "archive_media"
)

// historyPace is a profile with a source's overrides.
type historyPace struct {
	Profile       string
	Interval      assistantwire.KBRange // seconds
	Skew          float64               // >1 draws the interval towards its short end
	LongInterval  assistantwire.KBRange // seconds, now and then instead of Interval
	LongChance    float64
	Batch         assistantwire.KBRange // calls
	Rest          assistantwire.KBRange // minutes
	LongRestEvery assistantwire.KBRange // hours; 0: none
	LongRest      assistantwire.KBRange // minutes
	QuietPerDay   assistantwire.KBRange // periods
	QuietHours    assistantwire.KBRange // hours each
	SwitchPages   assistantwire.KBRange // pages in a chat before another
	ImageInterval assistantwire.KBRange // seconds
	PageLimit     int
}

// historyPageLimit is the page size asked for unless a source's settings say (a hint: sources cap it, one at 5).
const historyPageLimit = 50

func historyProfile(name string) historyPace {
	if name == historyProfileStandard {
		return historyPace{Profile: name, Interval: historyRange(3, 8), Skew: 1, Batch: historyRange(50, 150), Rest: historyRange(2, 10),
			SwitchPages: historyRange(20, 200), ImageInterval: historyRange(3, 8), PageLimit: historyPageLimit}
	}
	return historyPace{Profile: historyProfileCareful, Interval: historyRange(2, 6), Skew: 2, LongInterval: historyRange(10, 20), LongChance: 0.05,
		Batch: historyRange(40, 120), Rest: historyRange(3, 15), LongRestEvery: historyRange(2, 4), LongRest: historyRange(30, 90), QuietPerDay: historyRange(1, 2), QuietHours: historyRange(1, 3),
		SwitchPages: historyRange(20, 200), ImageInterval: historyRange(6, 15), PageLimit: historyPageLimit}
}

func historyRange(a, b float64) assistantwire.KBRange { return assistantwire.KBRange{a, b} }

// historyPaceFor is src's profile with its overrides.
func historyPaceFor(src assistantwire.KBHistorySource) historyPace {
	p := historyProfile(src.Profile)
	set := func(dst *assistantwire.KBRange, v *assistantwire.KBRange) {
		if v != nil {
			*dst = *v
		}
	}
	set(&p.Interval, src.IntervalSeconds)
	set(&p.Batch, src.BatchCalls)
	set(&p.Rest, src.RestMinutes)
	set(&p.LongRestEvery, src.LongRestEveryHours)
	set(&p.LongRest, src.LongRestMinutes)
	set(&p.QuietPerDay, src.QuietPeriodsPerDay)
	set(&p.QuietHours, src.QuietHours)
	set(&p.SwitchPages, src.SwitchChatPages)
	set(&p.ImageInterval, src.ImageIntervalSeconds)
	if src.PageLimit > 0 {
		p.PageLimit = src.PageLimit
	}
	// slow_factor stretches every interval and rest (after a stop the import caused, "all intervals and rests ×2").
	if f := src.SlowFactor; f > 1 {
		for _, r := range []*assistantwire.KBRange{&p.Interval, &p.LongInterval, &p.Rest, &p.LongRest, &p.ImageInterval} {
			*r = assistantwire.KBRange{r[0] * f, r[1] * f}
		}
	}
	return p
}

// historyPacer draws the waits and watches how long calls take.
type historyPacer struct {
	pace historyPace
	rng  *rand.Rand
	zone *time.Location

	mult        float64   // the interval is multiplied by it (1: the profile's), at most maxMult
	slowUntil   time.Time // the slowest pace until then (kb_history_resume with slow)
	slowPending bool      // a slow start asked for, its two hours from the next call
	// notBefore: no call before then (a rest or a pause under way, waiting is which), kept across a restart.
	notBefore time.Time
	waiting   string

	batchLeft    int       // calls before the next rest
	nextLongRest time.Time // zero: none
	quietDay     string
	quiet        [][2]time.Time

	// How long calls take: a day's first calls of each method make its baseline.
	day      string
	samples  map[string][]time.Duration
	baseline map[string]time.Duration
	recent   []bool // whether each of the last calls was slow
	normal   int    // normal calls in a row
	// watchUntil: a very slow call before then stops the import (the second within the watch).
	watchUntil time.Time
}

func newHistoryPacer(p historyPace, rng *rand.Rand, zone *time.Location, now, slowUntil time.Time) *historyPacer {
	if zone == nil {
		zone = time.UTC
	}
	pc := &historyPacer{pace: p, rng: rng, zone: zone, mult: 1, samples: map[string][]time.Duration{}, baseline: map[string]time.Duration{}}
	pc.batchLeft = pc.drawInt(p.Batch)
	pc.scheduleLongRest(now)
	if slowUntil.After(now) {
		pc.slowUntil = slowUntil
		pc.mult = pc.maxMult()
	}
	return pc
}

func (pc *historyPacer) draw(rg assistantwire.KBRange) float64 {
	return rg[0] + (rg[1]-rg[0])*pc.rng.Float64()
}

func (pc *historyPacer) drawInt(rg assistantwire.KBRange) int {
	lo, hi := int(math.Round(rg[0])), int(math.Round(rg[1]))
	if hi <= lo {
		return lo
	}
	return lo + pc.rng.IntN(hi-lo+1)
}

func historySeconds(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// maxMult is the multiplier that makes the longest interval the cap: the slowest pace.
func (pc *historyPacer) maxMult() float64 {
	if most := pc.pace.Interval[1]; most > 0 {
		return max(1, historyIntervalCap.Seconds()/most)
	}
	return 1
}

// floor is the least multiplier now: the slowest pace while a slow start lasts (or is still to start).
func (pc *historyPacer) floor(now time.Time) float64 {
	if pc.slowPending || now.Before(pc.slowUntil) {
		return pc.maxMult()
	}
	return 1
}

// interval is the wait before the next call: drawn from the profile (skewed, now and then a long one), times the
// multiplier, never longer than the cap unless the profile's own is.
func (pc *historyPacer) interval(image bool, now time.Time) time.Duration {
	p := pc.pace
	var base float64
	switch {
	case image:
		base = pc.draw(p.ImageInterval)
	case p.LongChance > 0 && pc.rng.Float64() < p.LongChance:
		base = pc.draw(p.LongInterval)
	default:
		u := pc.rng.Float64()
		if p.Skew > 1 {
			u = math.Pow(u, p.Skew)
		}
		base = p.Interval[0] + (p.Interval[1]-p.Interval[0])*u
	}
	m := max(pc.mult, pc.floor(now))
	limit := max(historyIntervalCap, historySeconds(base))
	return min(historySeconds(base*m), limit)
}

// rest is the wait before the next call for a rest that is due (batch done, long rest), zero when none; a long rest
// counts as the batch's rest as well.
func (pc *historyPacer) rest(now time.Time) (time.Duration, string) {
	if !pc.nextLongRest.IsZero() && !now.Before(pc.nextLongRest) {
		pc.scheduleLongRest(now)
		pc.batchLeft = pc.drawInt(pc.pace.Batch)
		return time.Duration(pc.draw(pc.pace.LongRest) * float64(time.Minute)), "long_rest"
	}
	if pc.batchLeft <= 0 {
		pc.batchLeft = pc.drawInt(pc.pace.Batch)
		return time.Duration(pc.draw(pc.pace.Rest) * float64(time.Minute)), "rest"
	}
	return 0, ""
}

func (pc *historyPacer) scheduleLongRest(now time.Time) {
	pc.nextLongRest = time.Time{}
	if pc.pace.LongRestEvery[1] > 0 {
		pc.nextLongRest = now.Add(time.Duration(pc.draw(pc.pace.LongRestEvery) * float64(time.Hour)))
	}
}

// quietUntil is the end of the quiet period now is in (zero: none). Each day (in the user's zone) gets its own
// periods, drawn when the day is first seen.
func (pc *historyPacer) quietUntil(now time.Time) time.Time {
	local := now.In(pc.zone)
	day := local.Format("2006-01-02")
	if day != pc.quietDay {
		pc.quietDay, pc.quiet = day, nil
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, pc.zone)
		for n := pc.drawInt(pc.pace.QuietPerDay); n > 0; n-- {
			length := time.Duration(pc.draw(pc.pace.QuietHours) * float64(time.Hour))
			if length <= 0 {
				continue
			}
			from := start.Add(time.Duration(pc.rng.Float64() * float64(24*time.Hour-length)))
			pc.quiet = append(pc.quiet, [2]time.Time{from, from.Add(length)})
		}
	}
	for _, q := range pc.quiet {
		if !now.Before(q[0]) && now.Before(q[1]) {
			return q[1]
		}
	}
	return time.Time{}
}

// switchPages is how many pages to read in a chat before moving to another.
func (pc *historyPacer) switchPages() int { return max(1, pc.drawInt(pc.pace.SwitchPages)) }

// historyVerdict is what a call's duration means.
type historyVerdict struct {
	slow, verySlow bool
	rest           time.Duration // rest this long before the next call
	stop           historyReason // stop the import, for this reason (no code: go on)
	note           historyReason // what changed, for the log and the status (no code: nothing)
}

// observe takes a call of method that took d (counted towards the batch) and says what follows.
func (pc *historyPacer) observe(method string, d time.Duration, now time.Time) historyVerdict {
	pc.batchLeft--
	day := now.In(pc.zone).Format("2006-01-02")
	if day != pc.day { // a new day: a new baseline from its first calls (yesterday's until then)
		pc.day, pc.samples = day, map[string][]time.Duration{}
	}
	if s := pc.samples[method]; len(s) < historyBaselineCalls {
		s = append(s, d)
		pc.samples[method] = s
		if len(s) == historyBaselineCalls {
			pc.baseline[method] = durationMedian(s)
		}
	}
	base := pc.baseline[method]
	slowAt := max(3*base, historySlowFloor)
	verySlowAt := historyVerySlow
	if method == historyImageMethod {
		verySlowAt = max(3*base, historyVerySlow)
	}
	ms := func(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }
	var v historyVerdict
	v.slow = d > slowAt
	v.verySlow = d > verySlowAt
	pc.recent = append(pc.recent, v.slow)
	if len(pc.recent) > historySlowWindow {
		pc.recent = pc.recent[len(pc.recent)-historySlowWindow:]
	}
	floor := pc.floor(now)
	switch {
	case v.verySlow:
		pc.normal = 0
		if now.Before(pc.watchUntil) {
			v.stop = reason("very_slow_twice", "minutes", strconv.Itoa(int(historyVerySlowWatch.Minutes())), "method", method, "ms", ms(d))
			return v
		}
		v.rest = historyVerySlowRest
		pc.watchUntil = now.Add(historyVerySlowRest + historyVerySlowWatch)
		pc.mult = max(pc.mult*2, floor)
		pc.mult = min(pc.mult, pc.maxMult())
		v.note = reason("very_slow", "method", method, "ms", ms(d), "limit", ms(verySlowAt), "minutes", strconv.Itoa(int(v.rest.Minutes())))
	case v.slow:
		pc.normal = 0
		if countTrue(pc.recent) >= historySlowRest {
			v.rest = time.Duration((10 + 10*pc.rng.Float64()) * float64(time.Minute))
			pc.mult = pc.maxMult()
			pc.recent = nil
			v.note = reason("slow_repeated", "count", strconv.Itoa(historySlowRest), "of", strconv.Itoa(historySlowWindow), "method", method,
				"ms", ms(d), "minutes", strconv.Itoa(int(v.rest.Minutes())))
		} else {
			pc.mult = min(max(pc.mult*2, floor), pc.maxMult())
			v.note = reason("slow", "method", method, "ms", ms(d), "limit", ms(slowAt), "mult", strconv.FormatFloat(pc.mult, 'f', 1, 64))
		}
	default:
		pc.normal++
		if pc.normal >= historyNormalRun && pc.mult > floor {
			pc.normal = 0
			pc.mult = max(pc.mult*0.8, floor, 1)
		}
	}
	return v
}

// historyPaceState is what a pacer keeps across a restart (status.json): a rest or pause under way, how slowly it
// reads and why, what it watches for, its batch and long rest, today's quiet periods and baseline.
type historyPaceState struct {
	NotBefore    time.Time          `json:"not_before,omitzero"`
	Waiting      string             `json:"waiting,omitempty"` // resting, paused, quiet: what NotBefore ends
	PausedSince  time.Time          `json:"paused_since,omitzero"`
	Mult         float64            `json:"mult"`
	Recent       []bool             `json:"recent,omitempty"`
	Normal       int                `json:"normal,omitempty"`
	WatchUntil   time.Time          `json:"watch_until,omitzero"`
	BatchLeft    int                `json:"batch_left"`
	NextLongRest time.Time          `json:"next_long_rest,omitzero"`
	QuietDay     string             `json:"quiet_day,omitempty"`
	Quiet        [][2]time.Time     `json:"quiet,omitempty"`
	Day          string             `json:"day,omitempty"`
	Samples      map[string][]int64 `json:"samples,omitempty"`  // ms, a day's first calls by method
	Baseline     map[string]int64   `json:"baseline,omitempty"` // ms, by method
}

func (pc *historyPacer) snapshot() historyPaceState {
	ps := historyPaceState{NotBefore: pc.notBefore, Waiting: pc.waiting, Mult: pc.mult, Recent: slices.Clone(pc.recent), Normal: pc.normal,
		WatchUntil: pc.watchUntil, BatchLeft: pc.batchLeft, NextLongRest: pc.nextLongRest, QuietDay: pc.quietDay, Quiet: slices.Clone(pc.quiet),
		Day: pc.day, Samples: map[string][]int64{}, Baseline: map[string]int64{}}
	for m, s := range pc.samples {
		for _, d := range s {
			ps.Samples[m] = append(ps.Samples[m], d.Milliseconds())
		}
	}
	for m, d := range pc.baseline {
		ps.Baseline[m] = d.Milliseconds()
	}
	return ps
}

// restore takes up a pacer's state saved earlier: never faster than it was (the multiplier, the watch), the batch
// and long rest where they were, today's quiet periods and baseline as drawn and measured.
func (pc *historyPacer) restore(ps historyPaceState, now time.Time) {
	pc.notBefore, pc.waiting = ps.NotBefore, ps.Waiting
	if ps.Mult > 0 {
		pc.mult = min(max(ps.Mult, pc.mult), pc.maxMult())
	}
	pc.recent, pc.normal, pc.watchUntil = slices.Clone(ps.Recent), ps.Normal, ps.WatchUntil
	pc.batchLeft = ps.BatchLeft
	if ps.NextLongRest.After(now) {
		pc.nextLongRest = ps.NextLongRest
	}
	if ps.QuietDay == now.In(pc.zone).Format("2006-01-02") {
		pc.quietDay, pc.quiet = ps.QuietDay, slices.Clone(ps.Quiet)
	}
	pc.day = ps.Day
	for m, s := range ps.Samples {
		for _, ms := range s {
			pc.samples[m] = append(pc.samples[m], time.Duration(ms)*time.Millisecond)
		}
	}
	for m, ms := range ps.Baseline {
		pc.baseline[m] = time.Duration(ms) * time.Millisecond
	}
}

func durationMedian(d []time.Duration) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	return s[len(s)/2]
}

func countTrue(b []bool) int {
	n := 0
	for _, x := range b {
		if x {
			n++
		}
	}
	return n
}
