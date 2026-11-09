package devices

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/link"
)

// The "Needs you" summary for Persona Link (internal/link/attention.go, docs/operations/link.md "Needs you"): the
// assistant works it out and hands it over (SetAttention); this file sends it to the computers whose Persona Link
// takes it. Active mode: an attention message on the link, after its endpoints and on every change. Passive mode: a
// POST to the connector on every change, and again after an info read that says the connector holds another one (a
// post that failed, a connector that restarted).

// SetAttention takes the assistant's latest summary (without Rev or WebURL: they are filled in per computer) and, when it
// changed, sends it to every Persona Link that takes it and does not have it yet.
func (d *Devices) SetAttention(a link.Attention) {
	if old := d.attention.Swap(&a); old != nil && reflect.DeepEqual(*old, a) {
		return // the assistant looks often; only a change goes out
	}
	d.spreadAttention()
}

// spreadAttention sends the latest summary wherever it is wanted, each in the background.
func (d *Devices) spreadAttention() {
	for _, l := range d.links.list() {
		go l.sendAttention()
	}
	for _, m := range d.cfgMachines() {
		if m.cfg.Kind == kindService && m.dialNode == nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*connectorDialTimeout)
				defer cancel()
				m.postAttention(ctx)
			}()
		}
	}
}

// attentionFor is the latest summary as sent with webURL, its Rev a hash of everything else in it; nil before the
// assistant handed one over.
func (d *Devices) attentionFor(webURL string) *link.Attention {
	a := d.attention.Load()
	if a == nil {
		return nil
	}
	out := *a
	out.Rev, out.WebURL = "", webURL
	b, _ := json.Marshal(out)
	sum := sha256.Sum256(b)
	out.Rev = hex.EncodeToString(sum[:8])
	return &out
}

// attentionWithheld: computer id gets no summary. One waiting to be confirmed (or while the hub cannot say whether
// it is) gets nothing of the assistant's (hub/machine_trust.go), and a work computer may not read personal data
// through the manager (machines[].trust); the summary names every agent's work.
func (d *Devices) attentionWithheld(id string) bool {
	pending, err := d.PendingMachineIDs(context.Background())
	return err != nil || slices.Contains(pending, id) || d.IsWork(id)
}

// readyForAttention: the link's endpoints went out, so a summary may follow them; the latest goes now.
func (l *mgrLink) readyForAttention() {
	l.attMu.Lock()
	l.attReady = true
	l.attMu.Unlock()
	l.sendAttention()
}

// sendAttention sends the link the latest summary, unless its hello did not ask for one, the computer may not have it
// (attentionWithheld), its endpoints have not gone out yet, or it has this one already. The web address is the hub's
// public one, else the hub address the link went to.
func (l *mgrLink) sendAttention() {
	if !l.attention || l.m.d.attentionWithheld(l.m.cfg.ID) {
		return
	}
	l.attMu.Lock()
	defer l.attMu.Unlock()
	if !l.attReady {
		return
	}
	a := l.m.d.attentionFor(cmp.Or(l.m.d.config().WebURL, l.address))
	if a == nil || a.Rev == l.attSent {
		return
	}
	if l.send(link.Message{Type: "attention", Attention: a}) == nil {
		l.attSent = a.Rev
	}
}

// postAttention posts the latest summary to the machine's connector, after the post under way if there is one. One
// post waits at most: it reads the summary once it has the turn, so it posts any change made while it waited.
func (m *Machine) postAttention(ctx context.Context) {
	if !m.attQueued.CompareAndSwap(false, true) {
		return
	}
	m.attPost.Lock()
	m.attQueued.Store(false)
	defer m.attPost.Unlock()
	m.postAttentionLocked(ctx)
}

// postAttentionLocked posts the latest summary to the machine's connector when its last info read said it takes one
// and holds another; one that fails is posted again after the next info read. Never while the connector holds this
// hub off (401, 429: as its info reads and streams) or the computer may not have it (attentionWithheld). The caller
// holds m.attPost.
func (m *Machine) postAttentionLocked(ctx context.Context) {
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if !st.ok || !st.attention || st.invalid || time.Now().Before(st.wait) || m.cfg.Address == "" ||
		m.d.attentionWithheld(m.cfg.ID) {
		return
	}
	a := m.d.attentionFor(m.d.config().WebURL)
	if a == nil || a.Rev == st.attRev {
		return
	}
	cred, gen, err := m.connectorCred(ctx)
	if err != nil {
		return // the info read says why
	}
	err = m.postConnectorAttention(ctx, cred, gen, a)
	m.mu.Lock()
	if !m.svcMovedOnLocked(gen) {
		m.svc.attRev = ""
		if err == nil {
			m.svc.attRev = a.Rev
		}
	}
	m.mu.Unlock()
	if err != nil {
		m.d.logf("[%s] connector: Needs you summary not taken: %v", m.cfg.ID, err)
	}
}

// postConnectorAttention is POST link.ConnectorAttentionPath with cred (of pairing gen); a refusal counts as an info
// read's does (refused).
func (m *Machine) postConnectorAttention(ctx context.Context, cred *connectorCred, gen int, a *link.Attention) error {
	tr, err := connectorTransport(cred.fp, m.cfg.Proxy)
	if err != nil {
		return err
	}
	defer tr.CloseIdleConnections()
	body, _ := json.Marshal(a)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+m.cfg.Address+link.ConnectorAttentionPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cred.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := connectorClient(tr).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		herr := connectorError(resp)
		m.refused(gen, resp.StatusCode, herr, false)
		return herr
	}
	return nil
}
