package hub

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/nwverse/persona/server/internal/bus"
)

// ---- merged lists ----

// mergedLists are the list routes whose answer the hub merges over every manager (route → the array that is the
// list); each item gets "manager". The rest of the answer is the default manager's.
var mergedLists = map[string]string{
	"GET /api/v1/inbox":      "items",
	"GET /api/v1/todos":      "todos",
	"GET /api/v1/extensions": "extensions",
	"GET /api/v1/plugins":    "plugins",
	"GET /api/v1/connectors": "connectors",
}

// listAnswer is one manager's answer to a list request.
type listAnswer struct {
	id     string
	status int
	header http.Header
	body   []byte
}

func (a listAnswer) ok() bool { return a.status >= 200 && a.status < 300 }

// bufferedWriter keeps what a handler writes (a manager's answer to a list request).
type bufferedWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedWriter() *bufferedWriter { return &bufferedWriter{header: http.Header{}} }

func (b *bufferedWriter) Header() http.Header { return b.header }
func (b *bufferedWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}
func (b *bufferedWriter) Flush() {} // a relayed answer flushes as it streams; this one is written whole

func (b *bufferedWriter) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

// listManagers are the managers a merged list asks: the default first, then the others connected now, by id.
func (h *Hub) listManagers(ctx context.Context) []string {
	def := h.defaultOwner(ctx)
	out := []string{def}
	var rest []string
	for _, p := range h.busRoster() {
		if p.Role == bus.RoleManager && p.ID != def {
			rest = append(rest, p.ID)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// mergeList answers a list request: a request for a manager (one it names, an extension's: its own, impliedManager) gets
// that manager's answer, else every manager is asked (ask, one after the other: the managers are few) and the lists under key merged, the default's first. Every
// item is stamped with its manager. A manager that does not answer is left out; when none does, the default's
// answer is given as it is.
func (h *Hub) mergeList(w http.ResponseWriter, r *http.Request, key string, ask func(w http.ResponseWriter, r *http.Request, id string)) {
	ids := []string{}
	named, err := impliedManager(r)
	if err != nil {
		writeErrCode(w, http.StatusForbidden, codeOtherManager, err)
		return
	}
	if named != "" {
		id, ok := h.owner(w, r, h.byRequest)
		if !ok {
			return
		}
		ids = append(ids, id)
	} else {
		ids = h.listManagers(r.Context())
	}
	body, err := readBody(w, r)
	if err != nil {
		return
	}
	var answers []listAnswer
	for _, id := range ids {
		bw := newBufferedWriter()
		ask(bw, withBody(r, body), id)
		answers = append(answers, listAnswer{id: id, status: cmp.Or(bw.status, http.StatusOK), header: bw.header, body: bw.body.Bytes()})
	}
	writeMerged(w, key, answers)
}

// writeMerged writes answers merged (see mergeList).
func writeMerged(w http.ResponseWriter, key string, answers []listAnswer) {
	var base *listAnswer
	var items []json.RawMessage
	var top map[string]json.RawMessage
	for i := range answers {
		a := &answers[i]
		if !a.ok() {
			continue
		}
		var t map[string]json.RawMessage
		if json.Unmarshal(a.body, &t) != nil {
			continue
		}
		raw, has := t[key]
		var list []json.RawMessage
		if has && json.Unmarshal(raw, &list) != nil {
			continue
		}
		for _, it := range list {
			items = append(items, stampManager(it, a.id))
		}
		if base == nil {
			base, top = a, t
		}
	}
	if base == nil { // nobody answered: the default's answer as it is
		a := answers[0]
		copyHeader(w.Header(), a.header)
		w.WriteHeader(a.status)
		_, _ = w.Write(a.body)
		return
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	list, _ := json.Marshal(items)
	top[key] = list
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(top)
	copyHeader(w.Header(), base.header)
	w.Header().Del("Content-Length")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(base.status)
	_, _ = w.Write(buf.Bytes())
}

func copyHeader(dst, src http.Header) {
	for k, v := range src {
		dst[k] = v
	}
}

// stampManager is item (a JSON object) with "manager": id; anything else as it is.
func stampManager(item json.RawMessage, id string) json.RawMessage {
	t := bytes.TrimSpace(item)
	if len(t) < 2 || t[0] != '{' {
		return item
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(t, &fields) != nil {
		return item
	}
	idJSON, _ := json.Marshal(id)
	if _, has := fields[ManagerQuery]; has {
		fields[ManagerQuery] = idJSON
		out, _ := json.Marshal(fields)
		return out
	}
	var b bytes.Buffer
	b.WriteString(`{"manager":`)
	b.Write(idJSON)
	if len(fields) > 0 {
		b.WriteByte(',')
		b.Write(bytes.TrimSpace(t[1:]))
	} else {
		b.WriteByte('}')
	}
	return b.Bytes()
}

// readBody reads r's body (a list request has none, or a small one) so every manager can be asked with it; it
// answered when it fails.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	var b bytes.Buffer
	if _, err := b.ReadFrom(http.MaxBytesReader(w, r.Body, busHTTPLimit)); err != nil {
		writeBodyErr(w, err, err)
		return nil, err
	}
	return b.Bytes(), nil
}

// withBody is r with body to read again.
func withBody(r *http.Request, body []byte) *http.Request {
	r2 := r.Clone(r.Context())
	if body == nil {
		r2.Body = http.NoBody
	} else {
		r2.Body = readCloser{bytes.NewReader(body)}
	}
	return r2
}

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }

// notifyManagers sends every connected manager a one-way event (what the hub saw that is no one manager's: the apps
// it has, a mute, a source with something new); errBusOffline when none is connected.
func (h *Hub) notifyManagers(event string, params any) error {
	sent := false
	for _, p := range h.busRoster() {
		if p.Role == bus.RoleManager && h.notifyBus(p.ID, event, params) == nil {
			sent = true
		}
	}
	if !sent {
		return errBusOffline
	}
	return nil
}
