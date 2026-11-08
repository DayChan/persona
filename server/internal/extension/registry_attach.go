package extension

import (
	"context"
	"errors"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Attaching an installed package (docs/extensions/extensions.md "Attaching an installed package"): its program runs on
// its own and the manager reaches it on its sockets instead of starting it. Only Record.Remote changes. The package,
// its tools, the configuration, the secrets and the data directory stay where they are, and the installed manifest and
// what was granted go on taking effect: the program is held to the package's id and version, so attaching grants
// nothing the package did not have. Detach runs the package again.

// Attach reaches installed package id on the sockets of source (remote:rpc=<socket>,http=<socket>) from now on: the
// sockets its runtime uses, of a program that says it is this id at this version.
func (r *Registry) Attach(ctx context.Context, id, source, actor string) (*Extension, error) {
	src, err := ParseSource(source)
	if err != nil || src.Kind != "remote" {
		return nil, i18n.Err("extension-status.attach_source")
	}
	remote := Remote{RPCSocket: src.RPCSocket, HTTPSocket: src.HTTPSocket, Package: true}
	unlock, err := r.beginOperation()
	if err != nil {
		return nil, err
	}
	defer unlock()
	old, err := r.attachable(id)
	if err != nil {
		return nil, err
	}
	id = old.Record.ID
	if rt := old.Full.Runtime; rt == nil || rt.IsDeno() {
		return nil, i18n.Err("extension-status.attach_no_program", i18n.Args{"id": id})
	} else if rt.SpeaksRPC() != (remote.RPCSocket != "") || rt.HTTP != (remote.HTTPSocket != "") {
		var want []string
		if rt.SpeaksRPC() {
			want = append(want, "rpc=<socket>")
		}
		if rt.HTTP {
			want = append(want, "http=<socket>")
		}
		return nil, i18n.Err("extension-status.attach_sockets", i18n.Args{"id": id, "sockets": "remote:" + strings.Join(want, ",")})
	}
	if r.RemoteManifest == nil {
		return nil, errors.New("this hub cannot reach remote plugins")
	}
	m, err := r.RemoteManifest(ctx, remote)
	if err != nil {
		return nil, i18n.Err("extension-status.attach_unreachable", i18n.Args{"err": err.Error()})
	}
	if m.ID != id {
		return nil, i18n.Err("extension-status.wrong_id", i18n.Args{"current": m.ID, "required": id})
	}
	if m.Version != old.Record.Version {
		return nil, i18n.Err("extension-status.wrong_version", i18n.Args{"current": m.Version, "required": old.Record.Version})
	}
	row := old.Record
	row.Remote = &remote
	next, err := r.switchRemote(ctx, old, row)
	if err != nil {
		return nil, err
	}
	unlock()
	r.audit(ctx, id, "attach", actor, map[string]any{"version": row.Version, "rpc_socket": remote.RPCSocket, "http_socket": remote.HTTPSocket})
	r.changed(Change{ID: id, Kind: "attached", Old: old})
	return next, nil
}

// Detach runs attached package id from its package again; the package must still be there.
func (r *Registry) Detach(ctx context.Context, id, actor string) (*Extension, error) {
	unlock, err := r.beginOperation()
	if err != nil {
		return nil, err
	}
	defer unlock()
	old, err := r.attachable(id)
	if err != nil {
		return nil, err
	}
	id = old.Record.ID
	if !old.Record.AttachedPackage() {
		return nil, i18n.Err("extension-status.detach_not_attached", i18n.Args{"id": id})
	}
	row := old.Record
	row.Remote = nil
	next, err := r.switchRemote(ctx, old, row)
	if err != nil {
		return nil, err
	}
	unlock()
	r.audit(ctx, id, "detach", actor, map[string]any{"version": row.Version})
	r.changed(Change{ID: id, Kind: "detached", Old: old})
	return next, nil
}

// attachable is installed package id, as a snapshot, with its package there: not one installed from remote: (it has
// none).
func (r *Registry) attachable(id string) (*Extension, error) {
	p := r.Snapshot(id)
	if p == nil {
		return nil, i18n.Err("extension-status.extension_missing", i18n.Args{"id": id})
	}
	if p.Record.Source != SourceInstalled {
		return nil, i18n.Err("extension-status.attach_not_installed", i18n.Args{"id": p.Record.ID})
	}
	if p.Record.Remote != nil && !p.Record.AttachedPackage() {
		return nil, i18n.Err("extension-status.attach_remote_install", i18n.Args{"id": p.Record.ID})
	}
	if dir := r.installedDir(p.Record.StorageKey(), p.Record.Version); !hasManifest(dir) {
		return nil, i18n.Err("extension-status.package_missing", i18n.Args{"path": dir})
	}
	return p, nil
}

// switchRemote saves row, old's record with only Remote changed, and puts it in effect. Called inside an operation.
func (r *Registry) switchRemote(ctx context.Context, old *Extension, row Record) (*Extension, error) {
	row.UpdatedAt = r.now()
	if err := r.write(ctx, row); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	next := r.fromRow(row)
	r.extensions[old.Record.ID] = next
	return snapshotExtension(next), nil
}
