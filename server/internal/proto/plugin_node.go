package proto

import "encoding/json"

// Plugins' programs on computers (docs/plans/plan-extensions.md"7", #1830): what the hub and a node say to each other
// about them (extmgr/extension_nodes.go, node/plugins.go). The hub keeps what should run where; the node does it.

// PluginNodeProgram is one plugin version that should be on a machine: Digest is the sha256 of the package the hub
// sends it (a tar.gz of the installed package, with that platform's program only).
type PluginNodeProgram struct {
	StorageID string `json:"storage_id,omitempty"`
	Revision  int64  `json:"identity_revision,omitempty"`
	ID        string `json:"id"`
	Version   string `json:"version"`
	Digest    string `json:"digest"`
}

// PluginNodeIdentity binds a public identity to one durable installation slot.
// Revision is the committed installation's updated_at in Unix nanoseconds.
type PluginNodeIdentity struct {
	// InstalledAt distinguishes an explicit fresh installation from an identity upgrade.
	InstalledAt int64 `json:"installed_at,omitempty"`
	// Retired is reply-only: a tombstone preventing delayed operations from reopening a removed slot.
	Retired   bool   `json:"retired,omitempty"`
	ID        string `json:"id"`
	StorageID string `json:"storage_id"`
	Revision  int64  `json:"revision"`
}

// PluginNodeIdentityRequest is also the authenticated capability probe when Slots is empty.
type PluginNodeIdentityRequest struct {
	Slots []PluginNodeIdentity `json:"slots,omitempty"`
}

type PluginNodeIdentityReply struct {
	Revision  int64                `json:"revision,omitempty"`
	Slots     []PluginNodeIdentity `json:"slots,omitempty"`
	Supported bool                 `json:"supported"`
}

func (p PluginNodeProgram) StorageKey() string {
	if p.StorageID != "" {
		return p.StorageID
	}
	return p.ID
}

func (p PluginNodeStart) StorageKey() string {
	if p.StorageID != "" {
		return p.StorageID
	}
	return p.ID
}

// PluginNodeHave: plugin.node.have, whether the node has that package (and its digest matches).
type PluginNodeHave struct {
	Have bool `json:"have"`
}

// PluginNodePutBegin: plugin.node.put.begin, a package on its way in chunks (plugin.node.put.chunk, …commit).
type PluginNodePutBegin struct {
	PluginNodeProgram
	Size int64 `json:"size"`
}

// PluginNodePutChunk: one chunk, at its offset.
type PluginNodePutChunk struct {
	UploadID string `json:"upload_id"`
	Offset   int64  `json:"offset"`
	Data     []byte `json:"data"`
}

// PluginNodeStart: plugin.node.start, run (or keep running) that version of the plugin's program. Init is the
// initialize the program gets (configuration and the secrets its computers get: kept in memory only).
type PluginNodeStart struct {
	StorageID string            `json:"storage_id,omitempty"`
	Revision  int64             `json:"identity_revision,omitempty"`
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	Command   string            `json:"command"` // inside the package
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	RPC       bool              `json:"rpc"`
	HTTP      bool              `json:"http,omitempty"`
	// Keep: "lease" (stopped once Lease runs out without a renewal) or "always".
	Keep         string          `json:"keep"`
	LeaseSeconds int             `json:"lease_seconds,omitempty"`
	Init         json.RawMessage `json:"init"`
}

// PluginNodeSync: plugin.node.sync, everything that should run on the machine; the node stops the rest and removes
// their packages, and the data of plugins no longer installed at all (not in Installed).
type PluginNodeSync struct {
	// Revision fences even an empty uninstall snapshot against a newer installed run.
	Revision       int64                `json:"revision,omitempty"`
	Identity       bool                 `json:"identity,omitempty"`
	InstalledSlots []PluginNodeIdentity `json:"installed_slots,omitempty"`
	Programs       []PluginNodeProgram  `json:"programs"`
	Installed      []string             `json:"installed"`
}

// PluginNodeState is how a plugin's program is on a machine: State is host's (running, starting, stopped, error…),
// or "missing" (no package of that version), "lease_expired" (the hub did not renew it in time).
type PluginNodeState struct {
	Revision int64  `json:"identity_revision,omitempty"`
	ID       string `json:"id"`
	Version  string `json:"version"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
}

// PluginNodeStates: what plugin.node.sync and plugin.node.status answer.
type PluginNodeStates struct {
	Programs []PluginNodeState `json:"programs"`
}

// PluginNodeCall: plugin.node.call, a method of the plugin protocol to the program.
type PluginNodeCall struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// PluginNodeRenew: plugin.node.renew, the hub is still there: the programs it keeps on a lease go on.
type PluginNodeRenew struct {
	IDs          []string `json:"ids"`
	LeaseSeconds int      `json:"lease_seconds"`
}

// PluginNodeEvent is the event plugin.node.event: something a program said (event), for the hub to pass on.
type PluginNodeEvent struct {
	Revision int64           `json:"identity_revision,omitempty"`
	ID       string          `json:"id"`
	Event    string          `json:"event"`
	Params   json.RawMessage `json:"params,omitempty"`
}
