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
// POST to the connector by the machine's own sender on every change, and again after an info read that says the
// connector holds another one (a post that failed, a connector that restarted). A computer that may not have it gets an
// empty one (attentionFor).

// SetAttention takes the assistant's latest summary (without Rev or WebURL: they are filled in per computer) and, when it
// changed, sends it to every Persona Link that takes it and does not have it yet.
func (d *Devices) SetAttention(a link.Attention) {
	if old := d.attention.Swap(&a); old != nil && reflect.DeepEqual(*old, a) {
		return // the assistant looks often; only a change goes out
	}
	d.spreadAttention()
}

// spreadAttention sends the latest summary wherever it is wanted, in the background: each link on its own, each
// service machine through its sender (attentionLoop).
func (d *Devices) spreadAttention() {
	for _, l := range d.links.list() {
		go l.sendAttention()
	}
	for _, m := range d.cfgMachines() {
		if m.cfg.Kind == kindService && m.dialNode == nil {
			m.wakeAttention()
		}
	}
}

// attentionFor is what computer id gets: the latest summary with webURL, its Rev a hash of everything else in it; nil
// when there is nothing to send. A computer that may not have it (waiting to be confirmed, a work computer: the summary
// names every agent's work) gets an empty summary instead: told nothing, and what it had is taken back. While the hub
// cannot say which computers wait to be confirmed, nothing new goes anywhere: what a computer has stays, and one that
// has nothing gets nothing (an empty summary now and the full one back later would ring every item again).
func (d *Devices) attentionFor(id, webURL string) *link.Attention {
	a := d.attention.Load()
	pending, err := d.PendingMachineIDs(context.Background())
	if a == nil || err != nil {
		return nil
	}
	out := *a
	if slices.Contains(pending, id) || d.IsWork(id) {
		out, webURL = link.Attention{Items: []link.AttentionItem{}}, ""
	}
	out.Rev, out.WebURL = "", webURL
	b, _ := json.Marshal(out)
	sum := sha256.Sum256(b)
	out.Rev = hex.EncodeToString(sum[:8])
	return &out
}

// readyForAttention: the link's endpoints went out, so a summary may follow them; the latest goes now.
func (l *mgrLink) readyForAttention() {
	l.attMu.Lock()
	l.attReady = true
	l.attMu.Unlock()
	l.sendAttention()
}

// sendAttention sends the link the summary for its computer (attentionFor), unless its hello did not ask for one, its
// endpoints have not gone out yet, or it has this one already. The web address is the hub's public one, else the hub
// address the link went to.
func (l *mgrLink) sendAttention() {
	if !l.attention {
		return
	}
	l.attMu.Lock()
	defer l.attMu.Unlock()
	if !l.attReady {
		return
	}
	a := l.m.d.attentionFor(l.m.cfg.ID, cmp.Or(l.m.d.config().WebURL, l.address))
	if a == nil || a.Rev == l.attSent {
		return
	}
	if l.send(link.Message{Type: "attention", Attention: a}) == nil {
		l.attSent = a.Rev
	}
}

// wakeAttention has the machine's sender look at the summary soon; a wake already waiting covers this one.
func (m *Machine) wakeAttention() {
	select {
	case m.attWake <- struct{}{}:
	default:
	}
}

// attentionLoop is a service machine's sender of the summary: one post at a time, whenever woken, until ctx ends (the
// machine is let go or the manager stops), which also ends a post under way. A connector slow to take it holds up
// only its own posts, never the info reads of the machines (probeServices).
func (m *Machine) attentionLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.attWake:
		}
		pctx, cancel := context.WithTimeout(ctx, 2*connectorDialTimeout)
		m.postAttention(pctx)
		cancel()
	}
}

// postAttention posts the summary for the machine (attentionFor) to its connector when its last info read said it
// takes one and holds another; one that fails is posted again after the next info read. Never while the connector
// holds this hub off (401, 429: as its info reads and streams). Only the machine's sender calls it (tests aside).
func (m *Machine) postAttention(ctx context.Context) {
	m.mu.Lock()
	st := m.svc
	m.mu.Unlock()
	if !st.ok || !st.attention || st.invalid || time.Now().Before(st.wait) || m.cfg.Address == "" {
		return
	}
	a := m.d.attentionFor(m.cfg.ID, m.d.config().WebURL)
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
