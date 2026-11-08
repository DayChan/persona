package extmgr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/denohost"
	"github.com/nwverse/persona/server/internal/extension/egress"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

func (ps *extensions) sourceSpec(ctx context.Context, p *extension.Extension, s *host.Spec) error {
	if ps.h == nil || ps.h.cfg == nil || ps.h.cfg.ExtensionRuntime == nil {
		return i18n.Err("extension-source.runtime_required")
	}
	if p.Record.Source != extension.SourceInstalled && p.Record.Source != extension.SourceDev {
		return errors.New("source runtime requires a contained installed or development package")
	}
	// Old grants can survive an executable-to-source upgrade. Never let that
	// migration put the manager's master token or native runtime authority in a guest.
	if p.Record.Granted.FullHub() || p.Record.Granted.AgentRuntimes {
		return errors.New("source runtime requires scoped hub grants without native runtime authority")
	}
	cfg := *ps.h.cfg.ExtensionRuntime
	check := func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := cfg.Verify(ctx); err != nil {
			return err
		}
		_, err := extension.CheckSourceBundleContext(ctx, p.Dir, p.Manifest.Runtime.Source)
		return err
	}
	if err := check(ctx); err != nil {
		return err
	}
	if _, err := denohost.InstallAdapter(filepath.Join(ps.root, "runtime-adapters")); err != nil {
		return err
	}
	s.Command = cfg.Path
	s.RPCBounds = rpc.Limits{FrameBytes: 8 << 20, ConcurrentRequests: 8, PendingCalls: 128}
	s.Args = denohost.Args(denohost.BundlePath, denohost.AdapterPath, "http://127.0.0.1:3128")
	s.Check = check
	// The adapter is verified again by content when preparing each new sandbox.
	s.Wrap = func(string, []string) (string, []string, []string, error) {
		return "", nil, nil, errors.New("source runtime was not confined")
	}
	return nil
}

func (ps *extensions) confineSource(ctx context.Context, p *extension.Extension, s *host.Spec) error {
	// No trusted-market default, owner switch, test noDefault or proxy-only fallback applies.
	if ps.h == nil || ps.h.cfg == nil || ps.h.cfg.ExtensionRuntime == nil {
		return i18n.Err("extension-source.runtime_required")
	}
	development, err := sandbox.SourceDevelopmentResources(selfExecutable())
	if err != nil {
		return err
	}
	if development {
		s.Args = denohost.DevelopmentArgs(s.Args)
	}
	cfg := *ps.h.cfg.ExtensionRuntime
	px, err := ps.egress()
	if err != nil {
		return err
	}
	id := p.Record.ID
	run := filepath.Join(ps.root, "run", p.Record.StorageKey())
	sock := filepath.Join(run, "proxy.sock")
	if err := checkSocketPath(sock); err != nil {
		return err
	}
	if err := os.MkdirAll(run, 0o700); err != nil {
		return err
	}
	if init := s.Init; init != nil {
		s.Init = func(ctx context.Context) (extension.InitializeParams, error) {
			v, err := init(ctx)
			if v.Hub.URL != "" {
				v.Hub.URL = "http://" + egress.HubHost
			}
			return v, err
		}
	}
	s.Env = append(s.Env, "DENO_NO_UPDATE_CHECK=1", "DENO_NO_PROMPT=1", "DENO_DIR=/tmp/deno")
	s.Prepare = func(command string, args []string) (string, []string, []string, []*os.File, func(), error) {
		adapter, err := denohost.InstallAdapter(filepath.Join(ps.root, "runtime-adapters"))
		if err != nil {
			return "", nil, nil, nil, nil, err
		}
		bundle, err := extension.SnapshotSourceBundleContext(ctx, p.Dir, p.Manifest.Runtime.Source)
		if err != nil {
			return "", nil, nil, nil, nil, err
		}
		dispose := func() { _ = bundle.Close() }
		mounts, err := denohost.Mounts(cfg, adapter, "/proc/self/fd/3", selfExecutable())
		if err != nil {
			return "", nil, nil, nil, dispose, err
		}
		for i := range mounts {
			if mounts[i].Target == denohost.BundlePath {
				mounts[i] = sandbox.Mount{Target: denohost.BundlePath, DataFD: 3}
			}
		}
		g := p.Record.Granted
		px.Register(id, egress.Rule{Hosts: g.Network, Hub: len(g.Hub) > 0, Targets: ps.settingsTargets(id)})
		if err := px.ListenRuntimeUnix(id, sock); err != nil {
			px.Unregister(id)
			return "", nil, nil, nil, dispose, err
		}
		limits := p.Manifest.Runtime.Resources.Limits()
		plan := sandbox.Plan{ID: id, Command: denohost.EnginePath, Args: args, Self: selfExecutable(), ProxySocket: sock, Scope: true, Strict: true, ReadOnly: mounts, MemoryMB: limits.MemoryMB, CPUPercent: limits.CPUPercent, Env: os.Environ()}
		if development {
			// Every activation/restart warns. No settings or live config are changed.
			ps.logf("DEVELOPMENT_ONLY_RESOURCE_FALLBACK extension=%s: %s", id, i18n.T(i18n.Default, "extension-source.development_resources"))
			plan.Scope = false
			plan.Development = sandbox.DefaultDevelopmentLimits()
		}
		name, a, env := sandbox.Linux(sandbox.Probe().Bwrap, plan)
		return name, a, env, []*os.File{bundle}, dispose, nil
	}
	return nil
}
