package hub

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// The admin page's peers API (docs/archive/operations/plan-hub-mesh.md 2.7): registerPeerAdmin registers the routes
// on the admin mux, behind the admin login.

// peerItem is one paired hub as the admin page lists it.
type peerItem struct {
	HubID    string     `json:"hub_id"`
	Name     string     `json:"name"`
	State    string     `json:"state"` // connected | connecting | offline | refused
	Address  string     `json:"address,omitempty"`
	RTTMS    *int       `json:"rtt_ms,omitempty"`
	Since    *time.Time `json:"since,omitempty"`
	Error    string     `json:"error,omitempty"`
	Machines int        `json:"machines"` // the computers in its hub.json (a member's go through it to the center)
	Role     string     `json:"role"`     // center | member (plan-hub-center.md 6)
}

type discoveredHub struct {
	HubID     string    `json:"hub_id"`
	Name      string    `json:"name"`
	Addresses []string  `json:"addresses"`
	TLSPort   int       `json:"tls_port,omitempty"`
	FP        string    `json:"fp"`
	Version   string    `json:"version,omitempty"`
	Seen      time.Time `json:"-"`
}

func (h *Hub) registerPeerAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/peers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"peers": h.peerItems(), "discovered": h.discoveredHubs()})
	})
	mux.HandleFunc("POST /api/v1/admin/peers/code", func(w http.ResponseWriter, r *http.Request) {
		code, err := h.newPeerCode(r.Context())
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errNoTLS) {
				status = http.StatusConflict
			}
			writeErr(w, status, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, code)
	})
	mux.HandleFunc("POST /api/v1/admin/peers", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code string `json:"code"`
		}
		if err := decodeJSONBody(w, r, &req, 16<<10); err != nil || strings.TrimSpace(req.Code) == "" {
			writeBodyErr(w, err, errors.New("code is required"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		p, err := h.joinPeer(ctx, req.Code)
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, errNoTLS) {
				status = http.StatusConflict
			}
			writeErr(w, status, err)
			return
		}
		// The link comes up within moments: answer with it when it does.
		deadline := time.Now().Add(5 * time.Second)
		for h.peerLinkTo(p.HubID) == nil && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
			case <-h.meshWait():
			case <-time.After(200 * time.Millisecond):
			}
		}
		for _, it := range h.peerItems() {
			if it.HubID == p.HubID {
				writeJSON(w, it)
				return
			}
		}
		writeJSON(w, peerItem{HubID: p.HubID, Name: p.Name, State: "connecting"})
	})
	mux.HandleFunc("POST /api/v1/admin/peers/probe", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Address string `json:"address"`
		}
		if err := decodeJSONBody(w, r, &req, 4<<10); err != nil || strings.TrimSpace(req.Address) == "" {
			writeBodyErr(w, err, errors.New("address is required"))
			return
		}
		info, err := probeHub(r.Context(), req.Address)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, info)
	})
	mux.HandleFunc("DELETE /api/v1/admin/peers/{hub_id}", func(w http.ResponseWriter, r *http.Request) {
		ok, err := h.unpairPeer(r.Context(), r.PathValue("hub_id"), true)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeErr(w, http.StatusNotFound, errors.New("not paired with that hub"))
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/admin/routes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, h.meshRoutes())
	})
	mux.HandleFunc("GET /api/v1/admin/mesh", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, h.meshSummary())
	})
	// Set as center (plan-hub-center.md 2): {"confirm": true} when the center now in place cannot be reached.
	mux.HandleFunc("POST /api/v1/admin/peers/{hub_id}/center", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Confirm bool `json:"confirm"`
		}
		// No body means confirm=false; a malformed or oversized one is refused rather than read as that.
		if err := decodeJSONBody(w, r, &req, 4<<10); err != nil && !errors.Is(err, io.EOF) {
			writeBodyErr(w, err, err)
			return
		}
		target := r.PathValue("hub_id")
		if target != h.hubID() && !h.inGroup(target) {
			writeErrCode(w, http.StatusNotFound, "not_in_group", i18n.Err("hub-peer_admin.not_in_group"))
			return
		}
		if _, err := h.setCenter(r.Context(), target, req.Confirm); err != nil {
			code := "unreachable"
			if errors.Is(err, errCenterOffline) {
				code = "confirm_required"
			}
			writeErrCode(w, http.StatusConflict, code, err)
			return
		}
		writeJSON(w, h.meshSummary())
	})
}

// meshHubItem is a hub of the group as the admin page lists it.
type meshHubItem struct {
	HubID  string `json:"hub_id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Online bool   `json:"online"`
	Self   bool   `json:"self,omitempty"`
	Paired bool   `json:"paired,omitempty"` // paired with this hub directly
}

// meshSummary is GET /api/v1/admin/mesh: this hub's role, the center, the term, and every hub of the group.
func (h *Hub) meshSummary() map[string]any {
	g := h.group()
	self := h.hubID()
	role := "center"
	if g.Center != self {
		role = "member"
	}
	center := map[string]any{"hub_id": g.Center, "name": h.hubNameOf(g.Center), "online": g.Center == self || h.meshView().reachable(g.Center)}
	hubs := []meshHubItem{}
	for _, mh := range h.meshHubs() {
		it := meshHubItem{HubID: mh.HubID, Name: mh.Name, Role: "member", Online: mh.Online, Self: mh.Self}
		if mh.Center {
			it.Role = "center"
		}
		_, it.Paired = h.pairedWith(mh.HubID)
		hubs = append(hubs, it)
	}
	return map[string]any{"role": role, "center": center, "term": g.Term, "hubs": hubs}
}

// inGroup: hub is paired with this one, or can be reached (through another hub of the group).
func (h *Hub) inGroup(hub string) bool {
	if _, ok := h.pairedWith(hub); ok {
		return true
	}
	return h.meshView().reachable(hub)
}

// peerCLIRoutes serve the peers API to `personad hub pair --peer` and `personad hub peer …` on the hub's machine, under
// /api/v1/admin/cli/: the master token, over loopback (like the admin UI's reload), no admin session.
func (h *Hub) peerCLIRoutes(open *http.ServeMux) {
	peers := http.NewServeMux()
	h.registerPeerAdmin(peers)
	open.Handle("/api/v1/admin/cli/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !remoteIP(r).IsLoopback() || tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(h.cfg.Token)) != 1 {
			writeErrCode(w, http.StatusForbidden, "forbidden", errors.New("the peers command line needs the master token over loopback"))
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = strings.Replace(r.URL.Path, "/api/v1/admin/cli/", "/api/v1/admin/", 1)
		r2.URL.RawPath = ""
		jsonRouteErrors(peers).ServeHTTP(w, r2)
	}))
}

func (h *Hub) peerItems() []peerItem {
	v := h.meshView()
	ms := &h.mesh
	ms.mu.Lock()
	var out []peerItem
	for id, p := range ms.peers {
		it := peerItem{HubID: id, Name: p.Name, State: "connecting"}
		if v != nil && v.hubs[id] != nil && v.hubs[id].rec.Name != "" {
			it.Name = v.hubs[id].rec.Name
		}
		if l := ms.links[id]; l != nil && l.ctx.Err() == nil {
			it.State, it.Address = "connected", l.address
			since := l.since
			it.Since = &since
			if rtt := l.rttMS(); rtt > 0 {
				it.RTTMS = &rtt
			}
		} else if st, ok := ms.dialErr[id]; ok && st.Error != "" {
			it.State, it.Error = "offline", st.Error
			if st.Refused {
				it.State = "refused"
			}
		}
		it.Role = "member"
		if id == ms.group.Center {
			it.Role = "center"
		}
		if r := ms.records[id]; r != nil {
			it.Machines = len(r.Machines)
		}
		out = append(out, it)
	}
	ms.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].HubID < out[j].HubID
	})
	if out == nil {
		out = []peerItem{}
	}
	return out
}

// meshRoutes is GET /api/v1/admin/routes: how every hub of the group is reached, and every computer with its paths.
func (h *Hub) meshRoutes() map[string]any {
	v := h.meshView()
	type hubRoute struct {
		HubID     string `json:"hub_id"`
		Name      string `json:"name"`
		Reachable bool   `json:"reachable"`
		Hops      int    `json:"hops"`
		RTTMS     *int   `json:"rtt_ms,omitempty"`
		Next      string `json:"next,omitempty"`
		Self      bool   `json:"self,omitempty"`
		Center    bool   `json:"center,omitempty"`
	}
	type machineRoute struct {
		ID    string     `json:"id"`
		UID   string     `json:"machine_uid,omitempty"`
		Name  string     `json:"name"`
		Paths []pathInfo `json:"paths"`
	}
	out := map[string]any{"hubs": []hubRoute{}, "machines": []machineRoute{}}
	if v == nil {
		return out
	}
	center := h.centerID()
	var hubs []hubRoute
	for id, vh := range v.hubs {
		hr := hubRoute{HubID: id, Name: vh.rec.Name, Reachable: vh.reachable, Hops: vh.hops, Next: vh.next, Self: id == v.self, Center: id == center}
		if vh.rtt >= 0 {
			rtt := vh.rtt
			hr.RTTMS = &rtt
		}
		hubs = append(hubs, hr)
	}
	sort.Slice(hubs, func(i, j int) bool {
		return hubs[i].Hops < hubs[j].Hops || hubs[i].Hops == hubs[j].Hops && hubs[i].HubID < hubs[j].HubID
	})
	machines := []machineRoute{}
	for _, m := range h.machineList() {
		mm := h.localMachine(m.ID)
		if mm == nil {
			continue
		}
		machines = append(machines, machineRoute{ID: m.ID, UID: m.MachineUID, Name: m.Name, Paths: h.pathInfos(mm)})
	}
	out["hubs"], out["machines"] = hubs, machines
	return out
}

// probeHub reads another hub's GET /api/v1/health for the admin page to check before pairing: that request only, no
// redirects, a few seconds, a small body, and only these fields of what it says.
func probeHub(ctx context.Context, address string) (map[string]any, error) {
	address = strings.TrimSpace(address)
	var bases []string
	if strings.Contains(address, "://") {
		u, err := url.Parse(address)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return nil, i18n.Err("hub-peer_admin.bad_address")
		}
		bases = []string{u.Scheme + "://" + u.Host}
	} else {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, i18n.Err("hub-peer_admin.needs_port")
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, i18n.Err("hub-peer_admin.bad_port")
		}
		hp := net.JoinHostPort(strings.Trim(host, "[]"), port)
		bases = []string{"https://" + hp, "http://" + hp}
	}
	client := &http.Client{Timeout: 5 * time.Second,
		Transport:     &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // fp is what is shown to check
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	var last error
	for _, base := range bases {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/health", nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			last = i18n.Err("hub-peer_admin.status", i18n.Args{"status": resp.Status})
			continue
		}
		var hv struct {
			HubID   string `json:"hub_id"`
			Name    string `json:"hub_name"`
			TLSPort int    `json:"tls_port"`
			FP      string `json:"fp"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(b, &hv); err != nil || hv.HubID == "" {
			last = i18n.Err("hub-peer_admin.not_a_hub")
			continue
		}
		return map[string]any{"hub_id": hv.HubID, "name": hv.Name, "tls_port": hv.TLSPort, "fp": hv.FP, "version": hv.Version}, nil
	}
	return nil, i18n.Err("hub-peer_admin.unreachable", i18n.Args{"err": i18n.Localize(last, i18n.From(ctx).Or(i18n.Default))})
}
