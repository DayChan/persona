package extmgr

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
)

func (h *Manager) pageRemoveLocked(s *pageSession) {
	if s.closed {
		return
	}
	s.closed = true
	delete(h.uiPages.sessions, s.id)
}
func (h *Manager) pageDispose(s *pageSession) {
	s.disposeOnce.Do(func() {
		if s.release != nil {
			defer s.release()
		}
		// Cancel an awaiting mount before waiting for its RPC dispatch to finish;
		// its guest factory can remain unresolved until dispose reaches the bridge.
		if s.mountCancel != nil {
			s.mountCancel()
		}
		if s.mountDone != nil {
			<-s.mountDone
		}
		if s.run == 0 || s.process.Generation() != s.run {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.process.Call(ctx, "ui.page.dispose", map[string]any{"session": s.id}, nil)
	})
}
func (h *Manager) pageDropExtension(ext string) {
	ps := &h.uiPages
	ps.mu.Lock()
	var gone []*pageSession
	for _, s := range ps.sessions {
		if ext == "" || s.extension == ext {
			h.pageRemoveLocked(s)
			gone = append(gone, s)
		}
	}
	ps.mu.Unlock()
	h.pageDisposeAll(gone)
}
func pageBatch(sessions []*pageSession, before, work func(*pageSession)) {
	jobs := make(chan *pageSession)
	var workers sync.WaitGroup
	for i := 0; i < min(8, len(sessions)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for s := range jobs {
				work(s)
			}
		}()
	}
	for _, s := range sessions {
		if before != nil {
			before(s)
		}
		jobs <- s
	}
	close(jobs)
	workers.Wait()
}
func (h *Manager) pageDisposeAll(gone []*pageSession) {
	pageBatch(gone, func(s *pageSession) {
		h.pageEmit(s.who, streamFrame("closed", "", s.id))
	}, h.pageDispose)
}

func (h *Manager) pageDisconnect(who extwire.UIPrincipal) {
	ps := &h.uiPages
	ps.mu.Lock()
	var paused []*pageSession
	for _, s := range ps.sessions {
		if samePageOwner(s.who, who) && s.who.Connection == who.Connection {
			s.who.Connection = ""
			s.visibility++
			s.recovering = false
			s.suspended = true
			s.expires = time.Now().Add(5 * time.Minute)
			if s.pauseDone == nil {
				s.pauseDone = make(chan struct{})
				paused = append(paused, s)
			}
		}
	}
	ps.mu.Unlock()
	h.pagePauseAll(paused)
}

func (h *Manager) pagePause(s *pageSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := s.process.Call(ctx, "ui.page.suspend", map[string]any{"session": s.id}, nil)
	ps := &h.uiPages
	ps.mu.Lock()
	if s.pauseDone != nil {
		close(s.pauseDone)
		s.pauseDone = nil
	}
	ps.mu.Unlock()
	return err
}
func (h *Manager) pagePauseAll(paused []*pageSession) {
	pageBatch(paused, nil, func(s *pageSession) { _ = h.pagePause(s) })
}
func (h *Manager) pageSweep(now time.Time) {
	ps := &h.uiPages
	ps.mu.Lock()
	var gone []*pageSession
	for _, s := range ps.sessions {
		if (!s.expires.IsZero() && !now.Before(s.expires)) || s.run != 0 && s.process.Generation() != s.run {
			h.pageRemoveLocked(s)
			gone = append(gone, s)
		}
	}
	ps.mu.Unlock()
	h.pageDisposeAll(gone)
}
func (h *Manager) pageBusReset() {
	ps := &h.uiPages
	ps.mu.Lock()
	var paused []*pageSession
	for _, s := range ps.sessions {
		if !s.legacy && !s.suspended {
			s.who.Connection = ""
			s.visibility++
			s.recovering = false
			s.suspended = true
			s.expires = time.Now().Add(5 * time.Minute)
			s.pauseDone = make(chan struct{})
			paused = append(paused, s)
		}
	}
	ps.mu.Unlock()
	if len(paused) > 0 {
		go h.pagePauseAll(paused)
	}
}

func (h *Manager) pageLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			h.pageDropExtension("")
			return
		case now := <-ticker.C:
			h.pageSweep(now)
		}
	}
}
