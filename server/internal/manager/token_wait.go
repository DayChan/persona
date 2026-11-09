package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/link"
)

// revokedPoll is how often a manager whose token was refused looks for a new one in manager.json.
var revokedPoll = 10 * time.Second

// revokedRetry is how long a manager whose token was refused waits, with no new one, before it tries the same token
// again: the hub said so explicitly (CloseRevoked, 401 token_revoked), but a hub restored from a backup may know it
// again, and trying once in ten minutes costs nothing.
var revokedRetry = 10 * time.Minute

// awaitRestart waits, after the hub refused the token of the manager in dir (before is its manager.json then), until
// it should start again: a new token in manager.json, or the hub no longer refusing the token. Nothing of the manager
// runs meanwhile (no bots, connectors, extensions, inbox): the sources it had may be another manager's now. Every
// revokedRetry the token alone is tried with a bare dial (probeToken), never the whole manager.
func awaitRestart(ctx context.Context, dir string, before *assistant.ManagerConfig, out io.Writer) error {
	return awaitRestartWith(ctx, before, func() string {
		c, err := assistant.ReadManagerConfig(dir)
		if err != nil {
			return ""
		}
		return c.Token
	}, out)
}

// awaitRestartWith is awaitRestart with token reading the manager's token where it keeps it ("" when it does not
// read).
func awaitRestartWith(ctx context.Context, before *assistant.ManagerConfig, token func() string, out io.Writer) error {
	id := ""
	if before != nil {
		id = before.ID
	}
	wait := revokedRetry
	for {
		renewed, err := waitToken(ctx, token, before, wait)
		if err != nil {
			return err
		}
		if renewed {
			fmt.Fprintf(out, "manager: [%s] manager.json 里有了新 token，重新连 hub\n", id)
			return nil
		}
		// Only a dial the hub took (the upgrade went through: it accepted the token) brings it back. Refused again:
		// the next try in revokedRetry. Anything else (the hub unreachable, a proxy's answer, a 503) says nothing about
		// the token: tried again sooner, never taken for yes.
		err = probeToken(ctx, before)
		switch {
		case err == nil:
			fmt.Fprintf(out, "manager: [%s] hub 又认这个 token 了：重新连 hub\n", id)
			return nil
		case errors.Is(err, bus.ErrRevoked), errors.Is(err, bus.ErrRejected): // refused (a plain 401 from an older hub)
			wait = revokedRetry
		default:
			wait = min(revokedRetry, probeAgain)
		}
	}
}

// probeAgain is how soon the token is tried again after a probe that got no verdict (the hub unreachable).
var probeAgain = time.Minute

// probeToken dials the hub's bus with c's token and hangs up at once: nil when the hub took it (the upgrade went
// through), bus.ErrRevoked while it still refuses it, another error when it said nothing about it. Nothing is said on
// the connection.
var probeToken = func(ctx context.Context, c *assistant.ManagerConfig) error {
	if c == nil {
		return nil
	}
	var client *http.Client
	if c.Fingerprint != "" {
		client = link.Pinned(c.Fingerprint)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ws, err := bus.Dial(ctx, c.Hub, c.Token, client)
	if err != nil {
		return err
	}
	_ = ws.Close(websocket.StatusNormalClosure, "probe")
	return nil
}

// waitNewToken returns once manager.json in dir has a token other than before's (renewed), after most (not renewed:
// try the same token again), or with ctx's error.
func waitNewToken(ctx context.Context, dir string, before *assistant.ManagerConfig, most time.Duration) (renewed bool, err error) {
	return waitToken(ctx, func() string {
		c, err := assistant.ReadManagerConfig(dir)
		if err != nil {
			return ""
		}
		return c.Token
	}, before, most)
}

// waitToken is waitNewToken with token reading the token ("" when it does not read: not renewed).
func waitToken(ctx context.Context, token func() string, before *assistant.ManagerConfig, most time.Duration) (renewed bool, err error) {
	t := time.NewTicker(revokedPoll)
	defer t.Stop()
	deadline := time.NewTimer(most)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return false, nil
		case <-t.C:
		}
		if tok := token(); tok != "" && (before == nil || tok != before.Token) {
			return true, nil
		}
	}
}

// waitJoined returns once dir has a manager.json, or with ctx's error.
func waitJoined(ctx context.Context, dir string) error {
	for {
		renewed, err := waitNewToken(ctx, dir, nil, time.Hour)
		if err != nil {
			return err
		}
		if renewed {
			return nil
		}
	}
}
