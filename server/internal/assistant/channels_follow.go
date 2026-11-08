package assistant

import (
	"context"
	"slices"
	"time"
)

// followSession has the hub send on a conversation's items and the text it writes (session.follow), once for every
// run on it; unfollowSession is the other end.
func (s *Service) followSession(ctx context.Context, machine, session string) {
	k := [2]string{machine, session}
	c := &s.chans
	c.mu.Lock()
	if c.follows == nil {
		c.follows = map[[2]string]int{}
	}
	c.follows[k]++
	first := c.follows[k] == 1
	c.mu.Unlock()
	if first {
		s.askFollow(ctx, machine, session)
	}
}

// followRetryEvery is how long a run's watcher waits before it asks again for a follow that failed.
var followRetryEvery = 30 * time.Second

// askFollow asks for session.follow; when it fails, a run's watcher asks again later (retryFollow): nothing else does
// while the node stays connected, and a machine not known yet is not followed again when it connects.
func (s *Service) askFollow(ctx context.Context, machine, session string) {
	err := s.call(ctx, machine, DeviceSessionFollow, map[string]any{"session_id": session}, nil)
	if err != nil {
		s.logf("channels: follow %s: %v", short(session), err)
	}
	k := [2]string{machine, session}
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil || c.follows[k] <= 0 {
		delete(c.followRetry, k)
		return
	}
	if c.followRetry == nil {
		c.followRetry = map[[2]string]time.Time{}
	}
	c.followRetry[k] = time.Now().Add(followRetryEvery)
}

// retryFollow asks again for a follow of the run's conversation that failed, once it is due. The caller is a run's
// watcher, so the conversation stays followed by it while the call is out.
func (s *Service) retryFollow(machine, session string) {
	k := [2]string{machine, session}
	c := &s.chans
	c.mu.Lock()
	at, ok := c.followRetry[k]
	if !ok || time.Now().Before(at) {
		c.mu.Unlock()
		return
	}
	delete(c.followRetry, k) // one watcher asks; askFollow puts it back if it fails again
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.askFollow(ctx, machine, session)
}

func (s *Service) unfollowSession(machine, session string) {
	k := [2]string{machine, session}
	c := &s.chans
	c.mu.Lock()
	c.follows[k]--
	last := c.follows[k] <= 0
	if last {
		delete(c.follows, k)
		delete(c.followRetry, k)
	}
	c.mu.Unlock()
	if last {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.call(ctx, machine, DeviceSessionUnfollow, map[string]any{"session_id": session}, nil)
	}
}

// deviceKnown: the hub has machine id (an error: it could not be asked).
func (s *Service) deviceKnown(ctx context.Context, id string) (Device, bool, error) {
	var res struct {
		Devices []Device `json:"devices"`
	}
	if err := s.call(ctx, DeviceAll, DeviceDevicesList, map[string]any{}, &res); err != nil {
		return Device{}, false, err
	}
	for _, d := range res.Devices {
		if d.ID == id {
			return d, true, nil
		}
	}
	return Device{}, false, nil
}

// channelGoneRetry is how soon the answer of a run whose machine was removed is tried again when it did not go out.
var channelGoneRetry = time.Second

// machineRemoved: the hub says machine id is not one of its machines any more. Only when its list is complete (the
// manager reported the machines, the pending computers were read) and the id is not one it keeps out of it (waiting
// to be confirmed, leaving, absorbed by another entry); an older hub, a list it could not give, or any doubt: false,
// and the run is watched as an offline one is.
func (s *Service) machineRemoved(ctx context.Context, id string) bool {
	var res struct {
		Devices  []Device `json:"devices"`
		Complete bool     `json:"complete"`
		Others   []string `json:"others"`
	}
	if err := s.call(ctx, DeviceAll, DeviceDevicesList, map[string]any{}, &res); err != nil || !res.Complete {
		return false
	}
	if slices.Contains(res.Others, id) {
		return false
	}
	return !slices.ContainsFunc(res.Devices, func(d Device) bool { return d.ID == id })
}
