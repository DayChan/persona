package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the nodes ----

// managedForNodes: the launchers added in Persona with their keys and headers, for the nodes (only Messages direct API connections).
func (s *Service) managedForNodes(ctx context.Context) ([]proto.ManagedLauncher, error) {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return nil, err
	}
	return s.nodeLaunchers(stored)
}

// managedNodeCalls is the new-connection configuration callback. Failed reads send no replacement.
func (s *Service) managedNodeCalls(ctx context.Context, machine string) []devicewire.NodeCall {
	if s.machinePending(ctx, machine) {
		return nil
	}
	list, err := s.managedForNodes(ctx)
	if err != nil {
		s.logf("launchers: prepare node configuration: %v", err)
		return nil
	}
	b, err := json.Marshal(map[string]any{"launchers": list})
	if err != nil {
		return nil
	}
	return []devicewire.NodeCall{{MachineID: machine, Method: DeviceLaunchersManaged, Params: b}}
}

// nodeLaunchers prepares the entire payload before any setting write or node call.
func (s *Service) nodeLaunchers(stored []storedLauncher) ([]proto.ManagedLauncher, error) {
	out := []proto.ManagedLauncher{}
	for _, l := range stored {
		if l.Kind == "api" && !messagesAPI(l.API) {
			continue
		}
		m, err := s.openStoredLauncher(l)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *Service) openStoredLauncher(l storedLauncher) (proto.ManagedLauncher, error) {
	m := l.ManagedLauncher
	var err error
	if m.APIKey, err = s.openKey(l.KeySealed); err != nil {
		return m, i18n.Err("assistant-models.err_key_decrypt")
	}
	if m.Headers, err = s.openKey(l.HeadersSealed); err != nil {
		return m, i18n.Err("assistant-models.err_key_decrypt")
	}
	if m.Env, err = s.openKey(l.EnvSealed); err != nil {
		return m, i18n.Err("assistant-models.err_key_decrypt")
	}
	m.HasHeaders, m.HasEnv = m.Headers != "", m.Env != ""
	return m, nil
}

// pushManaged gives the online nodes (one machine when machine is not "") the launchers added in Persona; they keep
// them in memory. After a change; a node's new connection gets them right after it is configured (main.go's
// devices NodeCalls). Only this manager's own machines: the hub's devices.list has no other (docs/assistant/managers.md), and a
// new connection the hub says is another manager's gets no NodeCalls (internal/devices hubConnected). A computer
// waiting to be confirmed gets none (machine_trust.go; the hub does not list it either).
func (s *Service) pushManaged(ctx context.Context, machine string) {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		s.logf("launchers: read managed launchers: %v", err)
		return
	}
	list, err := s.nodeLaunchers(stored)
	if err != nil {
		s.logf("launchers: prepare managed launchers: %v", err)
		return
	}
	s.pushNodeLaunchers(ctx, machine, list)
}

// pushNodeLaunchers publishes a confirmed snapshot while the caller holds models.mu.
func (s *Service) pushNodeLaunchers(ctx context.Context, machine string, list []proto.ManagedLauncher) {
	for _, d := range s.devices(ctx) {
		if !d.Online || (machine != "" && d.ID != machine) || s.machinePending(ctx, d.ID) {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := s.call(cctx, d.ID, assistantwire.DeviceLaunchersManaged, map[string]any{"launchers": list}, nil); err != nil {
			s.logf("launchers: give %s the managed launchers: %v", d.ID, err)
		}
		cancel()
	}
}

// zshrcLaunchers is one machine's rc-file launchers, and how they came in (Sync.State "" before any ask).
type zshrcLaunchers struct {
	MachineID string
	Launchers []proto.Launcher
	Sync      LauncherSync
	// ClaudeVersion: Claude Code's version there, as the hub knows it ("" when it has none or never said).
	ClaudeVersion string
}

// zshrcLaunchers is every machine's rc-file launchers (~/.zshrc, ~/.bashrc) in the machines' order: as last seen (an
// offline one's from the last time, kept in settings launchers.zshrc), or asked of the online nodes again first
// (refresh, or nothing seen since the assistant started; the node caches a minute). Each machine gets
// launcherSyncTimeout, and its Sync says whether its list came in now.
func (s *Service) zshrcLaunchers(ctx context.Context, refresh bool) []zshrcLaunchers {
	st := &s.models
	st.zshrcMu.Lock()
	seen, states := st.zshrc, st.zshrcSync
	st.zshrcMu.Unlock()
	devices := s.devices(ctx)
	if seen == nil || refresh {
		seen = map[string][]proto.Launcher{}
		meta := map[string]launcherMeta{}
		if store := s.store(); store != nil {
			_, _ = store.Setting(ctx, launchersZshrcKey, &seen)
			_, _ = store.Setting(ctx, launchersMetaKey, &meta)
		}
		states = map[string]LauncherSync{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		changed := false
		for _, d := range devices {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := s.listLaunchers(ctx, d)
				mu.Lock()
				defer mu.Unlock()
				state := LauncherSync{Machine: d.ID, Name: d.Name, State: "ok"}
				if err != nil {
					state.State, state.Error = launcherSyncState(ctx, err)
				} else {
					if !slices.Equal(seen[d.ID], res.Launchers) {
						seen[d.ID] = res.Launchers
					}
					meta[d.ID], changed = launcherMeta{Shell: res.Shell, RCFile: res.RCFile, SyncedAt: time.Now()}, true
				}
				if m, ok := meta[d.ID]; ok {
					state.Shell, state.RCFile = m.Shell, cmp.Or(m.RCFile, defaultRCFile)
					if at := m.SyncedAt; !at.IsZero() {
						state.SyncedAt = &at
					}
				}
				states[d.ID] = state
			}()
		}
		wg.Wait()
		if store := s.store(); changed && store != nil {
			_ = store.PutSetting(ctx, launchersZshrcKey, seen)
			_ = store.PutSetting(ctx, launchersMetaKey, meta)
		}
		st.zshrcMu.Lock()
		st.zshrc, st.zshrcSync = seen, states
		st.zshrcMu.Unlock()
	}
	out := make([]zshrcLaunchers, 0, len(devices))
	for _, d := range devices {
		out = append(out, zshrcLaunchers{MachineID: d.ID, Launchers: seen[d.ID], Sync: states[d.ID], ClaudeVersion: d.ClaudeVersion})
	}
	return out
}
