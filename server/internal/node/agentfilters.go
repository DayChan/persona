package node

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The extensions' agent filters (docs/extensions/extensions.md "Agent filters", #2428): sessions another platform runs on this
// computer (a cloud agent platform's chats and automations start codex and claude here, hundreds a week) are left out of the list, by
// marks in the head of their files. The assistant's sessions and the kept ones never go; neither do agents in a host
// (herdr's panes, runtimes): the filters are for transcripts found on disk only.

// setAgentFilters takes the hub's filters (node.configure agent_filters); the next list has them applied.
func (n *Node) setAgentFilters(filters []proto.AgentFilter) {
	n.mu.Lock()
	n.agentFilters = filters
	n.mu.Unlock()
	offlineCache.Lock()
	offlineCache.at = time.Time{}
	offlineCache.Unlock()
}

// agentFiltered says whether a filter takes the session in the file at path, of agent "claude" or "codex".
func (n *Node) agentFiltered(agent, path string) bool {
	n.mu.Lock()
	filters := n.agentFilters
	n.mu.Unlock()
	if len(filters) == 0 || path == "" {
		return false
	}
	return proto.AgentFiltered(filters, agent, sessionMarks(path), n.home)
}

// marksHead is how much of a file's head sessionMarks reads: a Codex rollout puts tens of KB of instructions before the
// first prompt (an assistant's AGENTS.md: 74 KB), a Claude transcript its first prompt and environment within a few.
const marksHead = 512 << 10

type marksEntry struct {
	size    int64
	marks   proto.SessionMarks
	settled bool      // the first prompt was read: what the head says no longer changes
	used    time.Time // the last time sessionMarks handed these marks out
}

// The callers ask for every file in their window on each scan (the offline list every 20 s, Codex's every 5 s), so an
// entry nobody asked for in marksIdle is a file that left the window or was deleted. A sweep at most every
// marksSweepEvery drops those; reading the head again later gives the same marks.
const (
	marksIdle       = 10 * time.Minute
	marksSweepEvery = time.Minute
)

var marksCache = struct {
	sync.Mutex
	m     map[string]*marksEntry // path → marks
	swept time.Time
}{m: map[string]*marksEntry{}}

var marksNow = time.Now // tests move the clock

// sessionMarks reads what the head of a Claude transcript or Codex rollout says of where the session came from:
// Codex's originator, Claude's added directories (the environment attachment after the first prompt) and the first
// user message. Kept once the first prompt is in; read again while the file is new and growing.
func sessionMarks(path string) proto.SessionMarks {
	st, err := os.Stat(path)
	if err != nil {
		marksCache.Lock()
		delete(marksCache.m, path)
		marksCache.Unlock()
		return proto.SessionMarks{}
	}
	now := marksNow()
	marksCache.Lock()
	sweepMarksLocked(now)
	if e, ok := marksCache.m[path]; ok && (e.settled || e.size == st.Size()) {
		e.used = now
		m := e.marks
		marksCache.Unlock()
		return m
	}
	marksCache.Unlock()
	e := readSessionMarks(path)
	e.size, e.used = st.Size(), now
	marksCache.Lock()
	marksCache.m[path] = &e
	marksCache.Unlock()
	return e.marks
}

// sweepMarksLocked drops the entries nobody asked for in marksIdle, at most once every marksSweepEvery.
func sweepMarksLocked(now time.Time) {
	if now.Sub(marksCache.swept) < marksSweepEvery {
		return
	}
	marksCache.swept = now
	for path, e := range marksCache.m {
		if now.Sub(e.used) > marksIdle {
			delete(marksCache.m, path)
		}
	}
}

func readSessionMarks(path string) marksEntry {
	var e marksEntry
	f, err := os.Open(path)
	if err != nil {
		return e
	}
	defer f.Close()
	r := bufio.NewReaderSize(io.LimitReader(f, marksHead), 64<<10)
	var tm transcript.Meta
	afterFirst := 0 // lines read since the first prompt: the environment attachment comes right after it
	read := 0
	for afterFirst < 8 {
		line, err := r.ReadBytes('\n')
		read += len(line)
		if len(line) > 0 {
			markLine(bytes.TrimSpace(line), &e.marks)
			if tm.FirstUser == "" {
				transcript.ParseLine(0, line, &tm)
			} else {
				afterFirst++
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				break
			}
			// The end of the file: more may come, read it again when it grows. The end of the head: that is all.
			e.marks.First, e.settled = tm.FirstUser, read >= marksHead
			return e
		}
		if tm.FirstUser != "" && e.marks.Originator != "" { // a Codex rollout: session_meta comes first
			break
		}
	}
	e.marks.First, e.settled = tm.FirstUser, tm.FirstUser != ""
	return e
}

// markLine takes the marks one line carries: a Codex session_meta's originator, a Claude environment attachment's
// additional working directories.
func markLine(line []byte, m *proto.SessionMarks) {
	switch {
	case m.Originator == "" && bytes.Contains(line, []byte(`"session_meta"`)):
		var l struct {
			Type    string `json:"type"`
			Payload struct {
				Originator string `json:"originator"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &l) == nil && l.Type == "session_meta" {
			m.Originator = l.Payload.Originator
		}
	case m.AddedDirs == nil && bytes.Contains(line, []byte(`"additionalWorkingDirectories"`)):
		var l struct {
			Type       string `json:"type"`
			Attachment struct {
				Type     string `json:"type"`
				Snapshot struct {
					Dirs []string `json:"additionalWorkingDirectories"`
				} `json:"snapshot"`
			} `json:"attachment"`
		}
		if json.Unmarshal(line, &l) == nil && l.Type == "attachment" && l.Attachment.Type == "environment" {
			m.AddedDirs = append([]string{}, l.Attachment.Snapshot.Dirs...)
		}
	}
}
