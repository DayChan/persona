package node

import (
	"bufio"
	"encoding/json"
	"io"
	"time"

	"github.com/nwverse/persona/server/internal/transcript"
)

// codexLineMax is the longest app-server line read; a var so tests can lower it.
var codexLineMax = 64 * 1024 * 1024

// read handles everything the app-server writes: responses, notifications, and requests to us (approvals…).
func (c *codexConn) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), codexLineMax)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' { // an interactive shell may print banners
			continue
		}
		var m codexMessage
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		c.receive(m)
	}
	stopUnreadAssistant(c.p.ownerOr(c.n).logger, c.p, "codex "+c.key, sc.Err()) // the exit goroutine in spawnCodex cleans up
}

func (c *codexConn) receive(m codexMessage) {
	if m.Method == "" && len(m.ID) > 0 {
		var id int64
		if json.Unmarshal(m.ID, &id) != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- codexReply{Result: m.Result, Err: m.Error}:
			default:
			}
		}
		return
	}
	if c.onMessage != nil {
		c.onMessage(m)
		return
	}
	if m.Method != "" && len(m.ID) > 0 {
		go c.serve(m)
	} else if m.Method != "" {
		c.notification(m.Method, m.Params)
	}
}

func (c *codexConn) notification(method string, params json.RawMessage) {
	n, p := c.p.ownerOr(c.n), c.p
	var ev struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Delta    string `json:"delta"`
		Turn     struct {
			ID     string          `json:"id"`
			Status string          `json:"status"`
			Error  json.RawMessage `json:"error"`
		} `json:"turn"`
		Item struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Text    string `json:"text"`
			Changes []struct {
				Path string `json:"path"`
			} `json:"changes"`
		} `json:"item"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		WillRetry bool            `json:"willRetry"`
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	c.mu.Lock()
	thread := c.threadID
	c.mu.Unlock()
	if ev.ThreadID != "" && thread != "" && ev.ThreadID != thread {
		return // a subagent's thread: its items are in the main rollout's events anyway
	}
	switch method {
	case "turn/started":
		c.mu.Lock()
		c.turnID = ev.Turn.ID
		c.mu.Unlock()
		p.mu.Lock()
		had := p.partial.Len() > 0
		p.partial.Reset()
		p.turnError = ""
		p.turnErrorKind = ""
		resumed := !p.busy
		p.busy = true
		p.lastActive = time.Now()
		p.mu.Unlock()
		if resumed {
			n.notifySessions()
		}
		if had {
			n.emitDelta(p.id, "")
		}
	case "item/started":
		if ev.Item.Type == "fileChange" {
			var paths []string
			for _, ch := range ev.Item.Changes {
				paths = append(paths, ch.Path)
			}
			c.mu.Lock()
			c.files[ev.Item.ID] = paths
			c.mu.Unlock()
		}
		if ev.Item.Type != "agentMessage" {
			return
		}
		// A new message: the last one is in the rollout by now; clear the live copy so it is not shown twice.
		p.mu.Lock()
		had := p.partial.Len() > 0
		p.partial.Reset()
		p.lastActive = time.Now()
		p.mu.Unlock()
		if had {
			n.emitDelta(p.id, "")
		}
	case "item/agentMessage/delta":
		p.mu.Lock()
		p.partial.WriteString(ev.Delta)
		text := p.partial.String()
		due := time.Since(p.lastDelta) > 80*time.Millisecond
		if due {
			p.lastDelta = time.Now()
		}
		p.lastActive = time.Now()
		p.mu.Unlock()
		if due {
			n.emitDelta(p.id, text)
		}
	case "thread/compacted":
		c.compacted()
	case "item/completed":
		c.mu.Lock()
		delete(c.files, ev.Item.ID)
		c.mu.Unlock()
		if ev.Item.Type == "contextCompaction" {
			c.compacted()
		}
		if ev.Item.Type == "agentMessage" {
			n.emitDelta(p.id, ev.Item.Text) // until the item arrives from the rollout
		}
		if f, err := n.file(p.id); err == nil {
			n.refresh(p.id, f)
		}
	case "turn/completed":
		c.mu.Lock()
		if c.turnID == ev.Turn.ID {
			c.turnID = ""
		}
		c.mu.Unlock()
		var failure, failureKind string
		if ev.Turn.Status != "interrupted" {
			if len(ev.Turn.Error) > 0 && string(ev.Turn.Error) != "null" {
				failure, failureKind = transcript.CodexTurnError(ev.Turn.Error)
			}
			if failure == "" && (ev.Turn.Status == "failed" || (ev.Turn.Status != "completed" && ev.Turn.Status != "")) {
				failure = transcript.CodexTurnFailed
			}
		}
		if failure != "" {
			n.logger.Printf("assistant codex %s: turn %s: %s", short(p.id), ev.Turn.Status, failure)
		}
		p.mu.Lock()
		p.busy = false
		p.turnError = failure
		p.turnErrorKind = failureKind
		p.lastActive = time.Now()
		p.partial.Reset()
		manual := p.manual
		p.manual, p.compacting = false, false
		p.mu.Unlock()
		if manual { // the turn the compaction ran in ended before its item came: done, unless it failed
			n.compactionEnded(p.id, snippet(failure))
		}
		if f, err := n.file(p.id); err == nil {
			n.refresh(p.id, f)
		}
		n.emitDelta(p.id, "")
		n.notifySessions()
		go c.syncTasks()
	case "error":
		if !ev.WillRetry {
			n.logger.Printf("assistant codex %s: %s", short(p.id), ev.Error.Message)
		}
	case "serverRequest/resolved":
		// Answered some other way (the turn was interrupted): drop the phone's question.
		c.mu.Lock()
		cancel := c.asks[string(ev.RequestID)]
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}
