package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Managers (docs/assistant/managers.md): every bus identity of role manager is one, all equal, each a whole assistant. The
// hub keeps the roster (bus_identities, migration 0419: name, its token's hash, where it said it runs, when it was
// last connected), the one-time install codes a new manager trades for its token, and which manager answers what
// belongs to none (the default, at first the assistant the hub starts). All of it is the hub's own: identities,
// connections and auth.

// The default manager: the assistant the hub starts (assistantwire.AssistantID), unless the user picked another.
const managerDefaultKey = "managers.default"

// installCodeTTL is how long an install code is good for; it works once.
const installCodeTTL = 10 * time.Minute

// installCodePrefix starts every install code, so it is never mistaken for a token of another kind.
const installCodePrefix = "mi_"

var installCodeShape = lazyre.New(`^mi_[0-9a-f]{32}$`)

// managerStore is what the roster needs of the store beyond busStore (internal/db/bus.go on Postgres).
type managerStore interface {
	busStore
	BusIdentityByID(ctx context.Context, id string) (*db.BusIdentity, error)
	SeeBusIdentity(ctx context.Context, id string, where *db.BusIdentity) error
	RenameBusIdentity(ctx context.Context, id, name string) (bool, error)
	AddManagerInstallCode(ctx context.Context, hash string, c db.ManagerInstallCode) error
	RedeemManagerInstallCode(ctx context.Context, codeHash, tokenHash string) (*db.ManagerInstallCode, string, error)
	ExpireManagerInstallCode(ctx context.Context, hash string) (bool, error)
	ManagerInstallCodeByHash(ctx context.Context, hash string) (*db.ManagerInstallCode, error)
	PendingManagerInstalls(ctx context.Context) ([]db.ManagerInstallCode, error)
	Setting(ctx context.Context, key string, out any) (bool, error)
	PutSetting(ctx context.Context, key string, v any) error
}

var _ managerStore = (*db.DB)(nil)

func (h *Hub) managerStore() managerStore {
	s, _ := h.busStore().(managerStore)
	return s
}

// managerInfo is one manager as GET /api/v1/managers has it.
type managerInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Online  bool   `json:"online"`
	Default bool   `json:"default,omitempty"`
	// Builtin: the assistant this hub starts beside itself; it cannot be removed (hub.json assistant.external).
	Builtin  bool       `json:"builtin,omitempty"`
	Host     string     `json:"host,omitempty"`
	OS       string     `json:"os,omitempty"`
	Arch     string     `json:"arch,omitempty"`
	Deploy   string     `json:"deploy,omitempty"`
	Version  string     `json:"version,omitempty"`
	Since    *time.Time `json:"since,omitempty"`     // connected since (online only)
	LastSeen *time.Time `json:"last_seen,omitempty"` // last connected (offline: "last synced")
	// Installing: made with POST /api/v1/managers, its code not traded yet; until when the code is good (a time past:
	// it expired, and POST …/code gives a new one). Dropped db.ManagerInstallKept after it expired.
	Installing *time.Time `json:"installing,omitempty"`
	// NeedsUpdate: it is online with a build from before manager.json (no config.own in its hello): the hello no
	// longer carries the manager's defaults, so it runs on its built-in ones until it is updated.
	NeedsUpdate bool `json:"needs_update,omitempty"`
}

// defaultManager is the manager the entries that belong to none go to (Siri, the shortcuts, the assistant page as
// the app opens): the user's pick when it is still in the roster, else the assistant.
func (h *Hub) defaultManager(ctx context.Context) string {
	id, _ := h.defaultManagerOrErr(ctx)
	return id
}

// defaultManagerOrErr is defaultManager saying when it is the assistant only because the store did not answer.
func (h *Hub) defaultManagerOrErr(ctx context.Context) (string, error) {
	s := h.managerStore()
	if s == nil {
		return assistantwire.AssistantID, nil
	}
	var id string
	ok, err := s.Setting(ctx, managerDefaultKey, &id)
	if err != nil {
		return assistantwire.AssistantID, err
	}
	if !ok || id == "" {
		return assistantwire.AssistantID, nil
	}
	b, err := s.BusIdentityByID(ctx, id)
	if err != nil {
		return assistantwire.AssistantID, err
	}
	if b == nil || b.Role != bus.RoleManager {
		return assistantwire.AssistantID, nil
	}
	return id, nil
}

// managerList is the roster: every manager, then the ones being installed; the default first, then by name.
func (h *Hub) managerList(ctx context.Context) ([]managerInfo, error) {
	s := h.managerStore()
	if s == nil {
		return nil, errors.New("no database")
	}
	idents, err := h.busIdentities(ctx, s)
	if err != nil {
		return nil, err
	}
	def := h.defaultOwner(ctx)
	live := map[string]busPresence{}
	for _, p := range h.busRoster() {
		live[p.ID] = p
	}
	out := []managerInfo{}
	for _, b := range idents {
		if b.Role != bus.RoleManager {
			continue
		}
		m := managerInfo{ID: b.ID, Name: b.Name, Default: b.ID == def, Builtin: h.builtinManager(b.ID),
			Host: b.Host, OS: b.OS, Arch: b.Arch, Deploy: b.Deploy, Version: b.Version, LastSeen: b.LastSeen}
		if p, ok := live[b.ID]; ok {
			since := p.Since
			m.Online, m.Since = true, &since
			m.Host, m.OS, m.Arch, m.Deploy, m.Version = p.Host, p.OS, p.Arch, p.Deploy, p.Version
			m.NeedsUpdate = !slices.Contains(p.Capabilities, assistantwire.CapabilityConfigOwn)
		}
		out = append(out, m)
	}
	pending, err := s.PendingManagerInstalls(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range pending {
		if c.ManagerID == assistantwire.AssistantID { // the built-in manager's binding code: it is in the roster already
			continue
		}
		until := c.ExpiresAt
		out = append(out, managerInfo{ID: c.ManagerID, Name: c.Name, Installing: &until})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		if (out[i].Installing == nil) != (out[j].Installing == nil) {
			return out[i].Installing == nil
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// builtinManager: id is the assistant this hub starts itself.
func (h *Hub) builtinManager(id string) bool {
	return id == assistantwire.AssistantID && !h.cfg.Assistant.External
}

// managersChanged tells the apps that asked for it (feature "managers", on ?features= or in client_hello) that the
// roster changed: they read GET /api/v1/managers again. Not in the event log: an app that reconnects reads the list
// anyway.
func (h *Hub) managersChanged() {
	h.ownersChanged() // the default may be another one now (ownership.go)
	b := []byte(`{"type":"managers"}`)
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.wants("managers") {
			c.queue(b)
		}
	}
}

// newInstallCode makes a one-time code for manager id (a new one, or one moving to another place) and keeps its hash.
func (h *Hub) newInstallCode(ctx context.Context, id, name string) (string, time.Time, error) {
	s := h.managerStore()
	if s == nil {
		return "", time.Time{}, errors.New("no database")
	}
	if !busManagerID.MatchString(id) {
		return "", time.Time{}, fmt.Errorf("bad manager id %q", id)
	}
	code := installCodePrefix + randomHex(16)
	until := time.Now().Add(installCodeTTL).UTC().Truncate(time.Second)
	if err := s.AddManagerInstallCode(ctx, hashToken(code), db.ManagerInstallCode{ManagerID: id, Name: name, ExpiresAt: until}); err != nil {
		return "", time.Time{}, err
	}
	return code, until, nil
}

// redeemInstallCode trades an install code for its manager's bus token, once. A manager that had a token gets a
// new one (moved, or installed again): the old one stops working and a connection that came with it is hung up. The
// code is used and the token saved in one transaction, so a save that fails (or a client that went away) leaves the
// code good for a retry.
func (h *Hub) redeemInstallCode(ctx context.Context, code string) (id, name, token string, err error) {
	s := h.managerStore()
	if s == nil {
		return "", "", "", errors.New("no database")
	}
	if !installCodeShape.MatchString(code) {
		return "", "", "", errInstallCode
	}
	tok := busTokenPrefix + randomHex(24)
	c, name, err := s.RedeemManagerInstallCode(ctx, hashToken(code), hashToken(tok))
	if err != nil {
		return "", "", "", err
	}
	if c == nil {
		return "", "", "", errInstallCode
	}
	h.busHangUp(c.ManagerID, bus.CloseRevoked, "its token was replaced")
	h.logf("managers: [%s] traded its install code", c.ManagerID)
	h.managersChanged()
	return c.ManagerID, name, tok, nil
}

var errInstallCode = errors.New("this install code is unknown, expired or used already")

var (
	errBuiltinNotService = errors.New("the built-in manager is not a service of its own on this hub: set assistant.mode " +
		"to service (persona-hub manager-mode service) and switch the hub over (persona-hub switch) first")
	errBuiltinRemote   = errors.New("the built-in manager's code is traded only from this hub's machine or its local network")
	errBuiltinKind     = errors.New("this is the built-in manager's code: trade it with persona-manager join --builtin")
	errNotBuiltinKind  = errors.New("this is not the built-in manager's code (persona-hub manager-code prints one)")
	errJoinUnavailable = errors.New("the hub cannot trade install codes right now: try again")
)

// builtinJoinRefused says why code may not be traded by r's join now, without spending it (status 0: no reason
// here, the redemption judges). A code is traded only by the join of its kind (builtin: persona-manager join
// --builtin, 409 either way); the built-in manager's only while it is a service of its own here (409), and only from
// this hub's machine or its local network (403). A code the store cannot look up is refused (503): the checks must
// not be skipped.
func (h *Hub) builtinJoinRefused(r *http.Request, code string, builtin bool) (status int, errCode string, err error) {
	s := h.managerStore()
	if s == nil || !installCodeShape.MatchString(code) {
		return 0, "", nil
	}
	c, err := s.ManagerInstallCodeByHash(r.Context(), hashToken(code))
	if err != nil {
		return http.StatusServiceUnavailable, "", err
	}
	isBuiltin := c != nil && c.ManagerID == assistantwire.AssistantID
	switch {
	case c == nil: // unknown, expired or used: the redemption says so (and counts it)
		return 0, "", nil
	case isBuiltin && !builtin:
		return http.StatusConflict, "builtin_code", errBuiltinKind
	case !isBuiltin && builtin:
		return http.StatusConflict, "not_builtin_code", errNotBuiltinKind
	case isBuiltin && !h.cfg.Assistant.ServiceBound():
		return http.StatusConflict, "builtin_not_service", errBuiltinNotService
	case isBuiltin && h.sourceOf(r) == sourcePublic:
		return http.StatusForbidden, "builtin_local_only", errBuiltinRemote
	}
	return 0, "", nil
}

// refusePlaintext answers a request from the internet over plaintext that carries install code code (the join,
// /i/<code>): 403 tls_required, and the code voided (voidPlaintextCode). A source past its limit gets 429
// rate_limited first, as over TLS, and the store is not asked.
func (h *Hub) refusePlaintext(w http.ResponseWriter, r *http.Request, code string) {
	key := authSourceKey(r)
	if retry := h.devtok.failures.retry(key, time.Now(), h.cfg.Security); retry > 0 {
		retryAfter(w, retry)
		h.securityError(w, r, http.StatusTooManyRequests, "rate_limited", 0)
		return
	}
	h.voidPlaintextCode(r.Context(), key, code)
	h.securityError(w, r, http.StatusForbidden, "tls_required", 0)
}

// voidPlaintextCode voids an install code that came over plaintext from the internet (refused): it crossed the
// internet in the clear, so whoever saw it on the way must not trade it over TLS. The manager shows as its code
// expired, and the app offers a new one. A code that voided nothing (not one, unknown or used already) counts against
// the source key, as an unknown code over TLS does, so no source makes the store look codes up unbounded. The store
// not answering counts nothing (as on the bus: an outage must not lock an address out), and is logged at most once
// a minute.
func (h *Hub) voidPlaintextCode(ctx context.Context, key, code string) {
	s := h.managerStore()
	if s == nil || code == "" {
		return
	}
	if !installCodeShape.MatchString(code) {
		h.devtok.failures.fail(key, time.Now(), h.cfg.Security)
		return
	}
	ok, err := s.ExpireManagerInstallCode(ctx, hashToken(code))
	switch {
	case err != nil:
		if h.bus.voidErrLog.Allow(time.Now()) {
			h.logf("managers: could not void an install code sent over plaintext from the internet: %v", err)
		}
	case ok:
		h.logf("managers: voided an install code sent over plaintext from the internet")
		h.managersChanged()
	default:
		h.devtok.failures.fail(key, time.Now(), h.cfg.Security)
	}
}

// newManagerID is an id for a manager made in the app: m- and six hex digits, not one in the roster.
func (h *Hub) newManagerID(ctx context.Context) (string, error) {
	s := h.managerStore()
	pending, err := s.PendingManagerInstalls(ctx)
	if err != nil {
		return "", err
	}
	for range 8 {
		id := "m-" + randomHex(3)
		b, err := s.BusIdentityByID(ctx, id)
		if err != nil {
			return "", err
		}
		if b == nil && !slices.ContainsFunc(pending, func(c db.ManagerInstallCode) bool { return c.ManagerID == id }) {
			return id, nil // nor one made and still waiting for its install
		}
	}
	return "", errors.New("no free manager id")
}

// bidiFormat: r is a bidirectional formatting character (marks, embeddings, overrides, isolates).
func bidiFormat(r rune) bool {
	switch {
	case r == 0x061C, r == 0x200E, r == 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// managerName checks a manager's name: 1–40 characters, no control characters (C0 and C1) and no bidirectional
// formatting characters (they would let a name show as another in the apps).
func managerName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("name is required")
	}
	if len([]rune(s)) > 40 {
		return "", errors.New("name is longer than 40 characters")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", errors.New("name has a control character")
		}
		if bidiFormat(r) {
			return "", errors.New("name has a bidirectional formatting character")
		}
	}
	return s, nil
}

// managerRoutes are the roster's endpoints for the apps (docs/protocol/protocol.md "Managers"). Reading is open to every
// signed-in device; changing it needs a full device (allowed()).
func (h *Hub) managerRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/managers", func(w http.ResponseWriter, r *http.Request) {
		list, err := h.managerList(r.Context())
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		out := map[string]any{"managers": list, "default": h.defaultOwner(r.Context())}
		if conflicts := h.managerBotConflicts(list); len(conflicts) > 0 {
			out["bot_conflicts"] = conflicts
		}
		writeJSON(w, out)
	})
	// New manager: a name → its id and a one-time install code (ten minutes). It shows in the roster as installing
	// until the code is traded (POST /api/v1/bus/join).
	api.HandleFunc("POST /api/v1/managers", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSONBody(w, r, &req, 4<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		name, err := managerName(req.Name)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if h.managerStore() == nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
			return
		}
		id, err := h.newManagerID(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		code, until, err := h.newInstallCode(r.Context(), id, name)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.logf("managers: [%s] made, waiting for its install", id)
		h.managersChanged()
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"manager": managerInfo{ID: id, Name: name, Installing: &until}, "code": code, "expires_at": until,
			"commands": h.managerCommands(r, id, code)})
	})
	// A new install code for a manager there is: to install it again somewhere else (the old token stops working
	// once the code is traded), or a fresh one for a pending install whose code ran out.
	api.HandleFunc("POST /api/v1/managers/{id}/code", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		info, ok := h.findManager(w, r, id)
		if !ok {
			return
		}
		if info.Builtin {
			writeErrCode(w, http.StatusConflict, "builtin_manager", errors.New("the hub runs this manager itself"))
			return
		}
		code, until, err := h.newInstallCode(r.Context(), id, info.Name)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"code": code, "expires_at": until, "commands": h.managerCommands(r, id, code)})
	})
	// {"name"?, "default"?: true}: rename it, make it the default.
	api.HandleFunc("PATCH /api/v1/managers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req struct {
			Name    *string `json:"name"`
			Default *bool   `json:"default"`
		}
		if err := decodeJSONBody(w, r, &req, 4<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		info, ok := h.findManager(w, r, id)
		if !ok {
			return
		}
		if info.Installing != nil {
			writeErrCode(w, http.StatusConflict, "installing", errors.New("this manager is not installed yet"))
			return
		}
		s := h.managerStore()
		if req.Name != nil {
			name, err := managerName(*req.Name)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			if _, err := s.RenameBusIdentity(r.Context(), id, name); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		if req.Default != nil {
			if !*req.Default {
				writeErr(w, http.StatusBadRequest, errors.New("make another manager the default instead"))
				return
			}
			if err := s.PutSetting(r.Context(), managerDefaultKey, id); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		h.managersChanged()
		info, _ = h.managerByID(r.Context(), id)
		writeJSON(w, map[string]any{"manager": info})
	})
	// Move one of hub.json's machines to this manager ({"machine_id"}): the old one lets it go, this one connects it.
	api.HandleFunc("POST /api/v1/managers/{id}/machines", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MachineID string `json:"machine_id"`
		}
		if err := decodeJSONBody(w, r, &req, 4<<10); err != nil || req.MachineID == "" {
			writeBodyErr(w, err, errors.New("machine_id is required"))
			return
		}
		id := r.PathValue("id")
		if _, ok := h.installedManager(w, r, id); !ok {
			return
		}
		from, err := h.moveMachine(r.Context(), req.MachineID, id)
		switch {
		case errors.Is(err, errNotMovable):
			writeErrCode(w, http.StatusBadRequest, "not_movable", err)
		case errors.Is(err, errUnknownManager): // removed meanwhile
			writeErrCode(w, http.StatusNotFound, "no_manager", err)
		case errors.Is(err, errOwnersUnknown):
			writeErrCode(w, http.StatusServiceUnavailable, "owners_unknown", err)
		case errors.Is(err, errKeeperAway): // the manager that keeps it, or the one it goes to, is away (machines_kept.go)
			writeErrCode(w, http.StatusServiceUnavailable, "assistant_offline", err)
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err)
		default:
			writeJSON(w, map[string]any{"machine_id": req.MachineID, "manager": id, "from": from})
		}
	})
	// Move a source ({"source": "mail:<account>", "from"?: its manager, the default when not said}) to this manager.
	api.HandleFunc("POST /api/v1/managers/{id}/sources", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Source string `json:"source"`
			From   string `json:"from"`
		}
		if err := decodeJSONBody(w, r, &req, 4<<10); err != nil || req.Source == "" {
			writeBodyErr(w, err, errors.New("source is required"))
			return
		}
		id := r.PathValue("id")
		if _, ok := h.installedManager(w, r, id); !ok {
			return
		}
		from := cmp.Or(req.From, h.defaultOwner(r.Context()))
		if _, ok := h.installedManager(w, r, from); !ok {
			return
		}
		if from == id {
			writeJSON(w, map[string]any{"source": req.Source, "manager": id, "from": from})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		src, err := h.moveSource(ctx, req.Source, from, id)
		var coded *rpc.Error
		switch {
		case errors.Is(err, errMoveOffline):
			writeErrCode(w, http.StatusServiceUnavailable, assistantwire.CodeAssistantOffline, err)
		case errors.Is(err, errUnknownManager): // removed meanwhile
			writeErrCode(w, http.StatusNotFound, "no_manager", err)
		case errors.Is(err, errSourceInDoubt): // the new one may have it: moving it again finds out
			writeErrCode(w, http.StatusBadGateway, "source_in_doubt", err)
		case src == "" && errors.As(err, &coded) && coded.Code == extwire.CodeNoSource:
			writeErrCode(w, http.StatusNotFound, coded.Code, err)
		case src == "" && errors.As(err, &coded) && (coded.Code == extwire.CodeSourceExists || coded.Code == extwire.CodeSourceKind):
			writeErrCode(w, http.StatusConflict, coded.Code, err)
		case src != "" && err != nil: // the new one has it; the old one may still too
			writeErrCode(w, http.StatusBadGateway, "source_not_released", err)
		case err != nil:
			writeErr(w, http.StatusBadGateway, err)
		default:
			writeJSON(w, map[string]any{"source": src, "manager": id, "from": from})
		}
	})
	// Revoke: its token stops working and its connection is hung up; a pending install is called off. Not the
	// default (make another one the default first) and not the one the hub runs itself.
	api.HandleFunc("DELETE /api/v1/managers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		info, ok := h.findManager(w, r, id)
		if !ok {
			return
		}
		switch {
		case info.Builtin:
			writeErrCode(w, http.StatusConflict, "builtin_manager", errors.New("the hub runs this manager itself"))
			return
		case info.Default:
			writeErrCode(w, http.StatusConflict, "default_manager", errors.New("make another manager the default first"))
			return
		}
		// Installed or not, its codes and then its identity go in one transaction (DeleteBusIdentity): a join that
		// commits while this runs leaves no manager behind, and one connected meanwhile is hung up. A manager never
		// installed has no identity (false): calling it off is still a 200. Once the moves to or from it under way
		// are done; the ones after find it gone (ownership_moves.go).
		drop := r.URL.Query().Get("drop_machines") == "1"
		err := h.removingManager(func() error {
			// What it keeps in its own list is in its manager.json only (machines_kept.go): moved away first, or
			// dropped on purpose. Checked with the moves held off, from a record that was read.
			if keeps, known := h.keepsAnyKnown(r.Context(), id); !drop && (keeps || !known) {
				if !known {
					return errOwnersUnknown
				}
				return errKeepsMachines
			}
			if err := h.releaseManagerDedicatedMachines(r.Context(), id); err != nil {
				return err
			}
			if _, err := h.revokeBusToken(r.Context(), id); err != nil {
				return err
			}
			h.releaseMachines(r.Context(), id) // its machines go back to the carrier, their sessions with them (ownership_moves.go)
			h.forgetManagerState(id)           // what it reported, its cards too (manager_state.go)
			return nil
		})
		switch {
		case errors.Is(err, errKeepsMachines):
			writeErrCode(w, http.StatusConflict, "keeps_machines", i18n.Err("hub-admin_machines.keeps_machines"))
			return
		case errors.Is(err, errOwnersUnknown):
			writeErrCode(w, http.StatusServiceUnavailable, "owners_unknown", err)
			return
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.logf("managers: [%s] removed", id)
		h.managersChanged()
		writeJSON(w, map[string]any{"ok": true})
	})
}

// managerByID is manager id as the roster has it (installing ones too).
func (h *Hub) managerByID(ctx context.Context, id string) (managerInfo, error) {
	list, err := h.managerList(ctx)
	if err != nil {
		return managerInfo{}, err
	}
	for _, m := range list {
		if m.ID == id {
			return m, nil
		}
	}
	return managerInfo{}, errNoManager
}

var errNoManager = errors.New("no such manager")

// findManager is managerByID for a handler: false once it answered (404, 503).
func (h *Hub) findManager(w http.ResponseWriter, r *http.Request, id string) (managerInfo, bool) {
	m, err := h.managerByID(r.Context(), id)
	switch {
	case errors.Is(err, errNoManager):
		writeErrCode(w, http.StatusNotFound, "no_manager", fmt.Errorf("no manager %q", clip(id, 64)))
		return m, false
	case err != nil:
		writeErr(w, http.StatusServiceUnavailable, err)
		return m, false
	}
	return m, true
}

// installedManager is findManager for a manager that is installed (not waiting for its install code).
func (h *Hub) installedManager(w http.ResponseWriter, r *http.Request, id string) (managerInfo, bool) {
	m, ok := h.findManager(w, r, id)
	if ok && m.Installing != nil {
		writeErrCode(w, http.StatusConflict, "installing", errors.New("this manager is not installed yet"))
		return m, false
	}
	return m, ok
}

// managerJoinRoutes is the one route a new manager reaches without a credential: it trades its install code for
// its bus token (`personad manager join`). Outside the apps' auth, like the bus itself; failures count towards the
// source's limit, as a bad token does.
func (h *Hub) managerJoinRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST "+bus.JoinPath, func(w http.ResponseWriter, r *http.Request) {
		if h.sourceOf(r) == sourcePublic && r.TLS == nil && !h.cfg.Security.AllowPublicPlaintext {
			// The token would cross the internet in the clear, and the code it would be traded for just did: refused,
			// and the code voided.
			var req struct {
				Code string `json:"code"`
			}
			if b, err := io.ReadAll(io.LimitReader(r.Body, 1<<10)); err == nil {
				_ = json.Unmarshal(b, &req)
			}
			h.refusePlaintext(w, r, strings.TrimSpace(req.Code))
			return
		}
		key := authSourceKey(r)
		if retry := h.devtok.failures.retry(key, time.Now(), h.cfg.Security); retry > 0 {
			retryAfter(w, retry)
			h.securityError(w, r, http.StatusTooManyRequests, "rate_limited", 0)
			return
		}
		var req struct {
			Code string `json:"code"`
			// Builtin: the joiner binds the built-in manager's own service (persona-manager join --builtin). A code
			// is traded only by the join of its kind: the built-in manager's by that one, every other by the others.
			Builtin bool `json:"builtin,omitempty"`
		}
		if err := decodeJSONBody(w, r, &req, 1<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		// The built-in manager's code (`persona-hub manager-code`) binds a service on this hub's machine: traded only
		// from a loopback or private address, and only while the hub no longer starts the built-in manager itself (its
		// child would go on next to the service). Refused before the code is spent.
		if status, code, err := h.builtinJoinRefused(r, strings.TrimSpace(req.Code), req.Builtin); status != 0 {
			if code == "" { // the store did not answer: said in the log, not to a caller without a credential
				h.logf("managers: could not look an install code up: %v", err)
				writeErr(w, status, errJoinUnavailable)
			} else {
				writeErrCode(w, status, code, err)
			}
			return
		}
		id, name, tok, err := h.redeemInstallCode(r.Context(), strings.TrimSpace(req.Code))
		if errors.Is(err, errInstallCode) {
			h.devtok.failures.fail(key, time.Now(), h.cfg.Security)
			writeErrCode(w, http.StatusUnauthorized, "bad_install_code", err)
			return
		}
		if err != nil { // the store's error (it may name the database, its user, its address): the log's, not the caller's
			h.logf("managers: could not trade an install code: %v", err)
			writeErr(w, http.StatusServiceUnavailable, errJoinUnavailable)
			return
		}
		writeJSON(w, map[string]any{"id": id, "name": name, "token": tok, "hub_id": h.hubID()})
	})
}
