package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Bounds of dialRace, after RFC 8305 (Happy Eyeballs v2).
const (
	// raceAttemptDelay is the wait before the next address is tried while earlier attempts go on
	// (RFC 8305's recommended Connection Attempt Delay).
	raceAttemptDelay = 250 * time.Millisecond
	// raceInFlight is the most attempts of one dial open at once.
	raceInFlight = 4
	// raceReclaimAfter: with raceInFlight attempts open and addresses still waiting, the attempt that has had
	// its time longest ago is canceled to make room, after raceReclaimAfter (twice that for a retry). With no
	// address waiting, open attempts keep the dial's own limit.
	raceReclaimAfter = 10 * time.Second
	// raceRetries: a canceled attempt's address goes to the back of the line this many times, so making room
	// never takes the only address that answers (slowly) out of the race.
	raceRetries = 1
)

// dialAttempt makes one connection to target, a pinned "ip:port"; it stops when ctx ends.
type dialAttempt func(ctx context.Context, target string) (net.Conn, error)

// dialRace connects to port on one of ips, every one of them already checked by the caller; nothing is
// resolved again. Addresses alternate between families (interleave). The first attempt starts at once and
// each next one raceAttemptDelay later, or as soon as an open attempt fails, while earlier attempts go on;
// at most raceInFlight are open. The first connection made wins; every other attempt is canceled and a
// connection one of them still makes is closed. When ctx ends, every attempt stops and the error is ctx's
// (with each address's cause), even when the attempts' own timeouts are seen first.
//
// Dialing the addresses one by one let one that never answers a SYN take the whole request (#1181).
func dialRace(ctx context.Context, ips []net.IP, port string, attempt dialAttempt) (net.Conn, error) {
	if len(ips) == 0 {
		return nil, errors.New("no address to dial")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type waiting struct {
		target  string
		retries int
	}
	type result struct {
		id   int
		conn net.Conn
		err  error
	}
	type open struct {
		waiting
		id      int
		started time.Time
		cancel  context.CancelFunc
		stopped bool // canceled to make room
	}
	var queue []waiting
	for _, ip := range interleave(ips) {
		queue = append(queue, waiting{target: net.JoinHostPort(ip.String(), port)})
	}
	// Buffered for every attempt there can be, so none blocks on a race already over.
	results := make(chan result, len(queue)*(1+raceRetries))
	var live []*open
	var failed dialErrors
	ids := 0
	start := func() {
		w := queue[0]
		queue = queue[1:]
		actx, acancel := context.WithCancel(ctx)
		live = append(live, &open{waiting: w, id: ids, started: time.Now(), cancel: acancel})
		go func(id int) {
			defer acancel()
			c, err := attempt(actx, w.target)
			results <- result{id: id, conn: c, err: err}
		}(ids)
		ids++
	}
	// finish leaves the attempts still open to close what they make once they return.
	finish := func() {
		cancel()
		if n := len(live); n > 0 {
			go func() {
				for range n {
					if r := <-results; r.conn != nil {
						_ = r.conn.Close()
					}
				}
			}()
		}
	}
	// ended is ctx's error once it is over. A deadline counts from the moment it passes: a dialer's own
	// timeout at that deadline can be seen before ctx's timer has marked it done.
	ended := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if dl, ok := ctx.Deadline(); ok && !time.Now().Before(dl) {
			return context.DeadlineExceeded
		}
		return nil
	}
	over := func(err error) (net.Conn, error) {
		finish()
		if len(failed) > 0 {
			return nil, fmt.Errorf("%w; %w", err, failed)
		}
		return nil, err
	}
	reclaimAt := func(o *open) time.Time { return o.started.Add(raceReclaimAfter << o.retries) }
	timer := time.NewTimer(0)
	defer timer.Stop()
	start()
	nextAt := time.Now().Add(raceAttemptDelay)
	for {
		var wake <-chan time.Time
		var reclaim *open
		if len(queue) > 0 {
			if len(live) < raceInFlight {
				timer.Reset(time.Until(nextAt))
				wake = timer.C
			} else {
				for _, o := range live {
					if !o.stopped && (reclaim == nil || reclaimAt(o).Before(reclaimAt(reclaim))) {
						reclaim = o
					}
				}
				if reclaim != nil {
					timer.Reset(time.Until(reclaimAt(reclaim)))
					wake = timer.C
				}
			}
		}
		select {
		case r := <-results:
			if r.err == nil && r.conn == nil {
				r.err = errors.New("dial returned no connection")
			}
			requeued := false
			for i, o := range live {
				if o.id == r.id {
					live = append(live[:i], live[i+1:]...)
					requeued = o.stopped && o.retries < raceRetries
					if r.err != nil && o.stopped && ctx.Err() == nil {
						r.err = fmt.Errorf("%s: no connection within %s: %w", o.target, raceReclaimAfter<<o.retries, r.err)
					}
					break
				}
			}
			if r.err == nil {
				finish()
				return r.conn, nil
			}
			if !requeued {
				failed = append(failed, r.err)
			}
			if err := ended(); err != nil {
				return over(err)
			}
			if len(queue) == 0 && len(live) == 0 {
				return nil, failed
			}
			nextAt = time.Now() // a failure lets the next address go at once
		case <-wake:
			if reclaim != nil {
				reclaim.stopped = true
				reclaim.cancel()
				if reclaim.retries < raceRetries {
					queue = append(queue, waiting{target: reclaim.target, retries: reclaim.retries + 1})
				}
				continue
			}
			start()
			nextAt = time.Now().Add(raceAttemptDelay)
		case <-ctx.Done():
			return over(ctx.Err())
		}
	}
}

// interleave alternates ips between IPv4 and IPv6, starting with the family of the first address (RFC 8305
// section 4, First Address Family Count 1); each family keeps the resolver's order.
func interleave(ips []net.IP) []net.IP {
	if len(ips) < 2 {
		return ips
	}
	first := ips[0].To4() != nil
	var same, other []net.IP
	for _, ip := range ips {
		if (ip.To4() != nil) == first {
			same = append(same, ip)
		} else {
			other = append(other, ip)
		}
	}
	out := make([]net.IP, 0, len(ips))
	for i := 0; i < len(same) || i < len(other); i++ {
		if i < len(same) {
			out = append(out, same[i])
		}
		if i < len(other) {
			out = append(out, other[i])
		}
	}
	return out
}

// dialErrors are the failures of every attempt of one dialRace, on one line.
type dialErrors []error

func (e dialErrors) Error() string {
	if len(e) == 1 {
		return e[0].Error()
	}
	parts := make([]string, len(e))
	for i, err := range e {
		parts[i] = err.Error()
	}
	return fmt.Sprintf("%d addresses failed: %s", len(e), strings.Join(parts, "; "))
}

func (e dialErrors) Unwrap() []error { return e }
