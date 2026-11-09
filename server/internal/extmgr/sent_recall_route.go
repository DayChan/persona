package extmgr

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/nwverse/persona/server/internal/db"
)

// The route that takes back a message the ledger has a row for (docs/protocol.md "Connector API", docs/connectors.md
// "Sent as the user", #5278). The core is RecallSent (sent_recall.go); this is who may ask and how the user agreed.

// mayReadLedger says whether the request is from who may read the ledger of what was sent as the user: one of the
// user's own full devices, or the owner on the hub machine. Never an answer-only device, a pairing token (a script or an
// AI the user paired) or an extension's token, whatever its scopes: the ledger holds the text of what was written in the
// user's name.
func mayReadLedger(ctx context.Context) bool {
	return (byOwnDevice(ctx) || principalOf(ctx).Owner) && principalOf(ctx).Extension == ""
}

// byUsersOwnHand says whether the request is what the user did by hand on their own device: a full device token the
// hub found, never a pairing token, an answer-only device, an extension, or the owner's master token. It is for the acts
// that need no card because the user made them themselves (#1190: a reply typed on the device, a message taken back
// there), and for those only. The owner's token is not enough: any process on the hub machine can read it, and what it
// does cannot be told from a person's in the audit.
func byUsersOwnHand(ctx context.Context) bool {
	return byOwnDevice(ctx) && principalOf(ctx).Extension == ""
}

// errRecallNotConfirmed is a recall request that did not say the user agreed.
var errRecallNotConfirmed = errors.New(`the user has not agreed: send {"confirm": true} once they have`)

// recallStatus is the status each reason a recall did not happen is answered with (docs/connectors.md "Sent as the
// user", Failures); the body carries the reason's code. An error that is not a RecallError is a plain 500.
var recallStatus = map[string]int{
	RecallNotFound:        http.StatusNotFound,
	RecallNotRecallable:   http.StatusConflict,
	RecallTooOld:          http.StatusConflict,
	RecallAlreadyRecalled: http.StatusConflict,
	RecallUnavailable:     http.StatusServiceUnavailable,
	RecallRefused:         http.StatusBadGateway,
	RecallUnknown:         http.StatusGatewayTimeout,
	RecallNoDatabase:      http.StatusServiceUnavailable,
}

// recallLocks shares one recall of a ledger row (RecallSent leaves that to its caller). Every confirmed request that
// finds the flight on its way waits for its actual row and error, never calling the connector itself; the most any
// request waits is one recall's time, however many come at once. The zero value is ready.
type recallLocks struct {
	mu   sync.Mutex
	busy map[int64]*recallFlight
}

type recallFlight struct {
	done chan struct{}
	row  db.SentAsUser
	err  error
}

// do runs run once for row id, or shares the flight already running. first is true only for the caller that ran it,
// which uses that to write one audit record. A waiter's own cancellation ends only its wait, not the shared recall.
func (l *recallLocks) do(ctx context.Context, id int64, run func() (db.SentAsUser, error)) (row db.SentAsUser, first bool, err error) {
	l.mu.Lock()
	if f := l.busy[id]; f != nil {
		l.mu.Unlock()
		select {
		case <-f.done:
			return f.row, false, f.err
		case <-ctx.Done():
			return db.SentAsUser{}, false, ctx.Err()
		}
	}
	if l.busy == nil {
		l.busy = map[int64]*recallFlight{}
	}
	f := &recallFlight{done: make(chan struct{}), err: errors.New("recall ended without a result")}
	l.busy[id] = f
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.busy, id)
		close(f.done)
		l.mu.Unlock()
	}()
	f.row, f.err = run()
	return f.row, true, f.err
}

// sentRecallRoutes: POST /api/v1/connectors/sent/{id}/recall with {"confirm": true}.
func (h *Manager) sentRecallRoutes(api *http.ServeMux) {
	api.HandleFunc("POST /api/v1/connectors/sent/{id}/recall", func(w http.ResponseWriter, r *http.Request) {
		// Only the user's own device, by hand: not the owner's token (which reads the ledger), a pairing token, an
		// answer-only device or an extension (docs/connectors.md "Sent as the user", Recall).
		if !byUsersOwnHand(r.Context()) {
			writeErrCode(w, http.StatusForbidden, "own_device_only", errors.New("a message can only be taken back on your own device, signed in to the app: "+
				"not with the owner's token on the hub machine, a pairing token, an answer-only device or an extension"))
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			writeErrCode(w, http.StatusBadRequest, "bad_request", errors.New("the id of a row of the ledger, a number from 1"))
			return
		}
		var in struct {
			Confirm bool `json:"confirm"`
		}
		if err := decodeJSONBody(w, r, &in, 4<<10); err != nil && !errors.Is(err, io.EOF) {
			writeBodyErr(w, err, errors.New(`the body is {"confirm": true}`))
			return
		}
		// The user agreed in the app's dialog on their own device (that it is their own device is byUsersOwnHand, from
		// the hub's authentication, not the client's word), and the request says so: no card is shown (#1190, as for a
		// reply the user writes by hand). Without it nothing is tried, and the request does not take a place in line.
		if !in.Confirm {
			writeErrCode(w, http.StatusBadRequest, "confirm_required", errRecallNotConfirmed)
			return
		}
		row, first, err := h.recalls.do(r.Context(), id, func() (db.SentAsUser, error) {
			// confirm:true on the user's own full device is the user's agreement, already checked above.
			return h.RecallSent(r.Context(), id, func(context.Context, db.SentAsUser) error { return nil })
		})
		if err != nil {
			var re *RecallError
			if errors.As(err, &re) {
				if status, ok := recallStatus[re.Code]; ok {
					var extra map[string]any
					if re.Code == RecallAlreadyRecalled { // the row, as the ledger has it now, for a screen that was behind
						extra = map[string]any{"item": h.sentItems(r.Context(), []db.SentAsUser{row})[0]}
					}
					writeCode(w, status, re.Code, re.Error(), extra)
					return
				}
			}
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		// Who took it back, like a sign-in or a switch is recorded: the connector in the plugin column, and which row and
		// kind of message; never its text. Only the caller that ran the shared recall records it; waiters get the same
		// answer without making a second audit row. The rest left everything as it was.
		if first {
			h.auditConnector(r.Context(), row.Connector, auditConnectorRecall, map[string]any{"row": row.ID, "action": row.Action})
		}
		writeJSON(w, map[string]any{"ok": true, "item": h.sentItems(r.Context(), []db.SentAsUser{row})[0]})
	})
}
