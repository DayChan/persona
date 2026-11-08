package proto

import "time"

type Machine struct {
	ID string `json:"id"`
	// Manager is the manager it belongs to (docs/assistant/managers.md, "Ownership"); the hub stamps it on what it sends the apps.
	Manager string `json:"manager,omitempty"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // local | ssh | tailcat | link
	Icon    string `json:"icon"` // server | laptop | desktop
	SSHHost string `json:"ssh_host,omitempty"`
	Online  bool   `json:"online"`
	Error   string `json:"error,omitempty"`
	// ErrorCode is what Error is about, for a client's own words (docs/assistant/onboarding.md 5.3): node_service_down,
	// link_offline, never_connected, node_outdated; "" none of them. An unknown one shows Error.
	ErrorCode string `json:"error_code,omitempty"`
	// Trust: pending while the user has not confirmed the computer is theirs (POST …/confirm); "" is confirmed.
	Trust         string    `json:"trust,omitempty"`
	Hostname      string    `json:"hostname,omitempty"`
	OS            string    `json:"os,omitempty"`
	Arch          string    `json:"arch,omitempty"`
	Home          string    `json:"home,omitempty"`
	HerdrVersion  string    `json:"herdr_version,omitempty"`
	ClaudeVersion string    `json:"claude_version,omitempty"`
	LastSeen      time.Time `json:"last_seen,omitempty"`
	// NodeVersion is the machine's personad build. NodeMismatch: the hub offers to redeploy the node, because it runs
	// another build than the hub's (deploy.sh missed it, or it was installed on its own) or because its manager cannot
	// drive it (NodeIncompatible). Another build alone is an upgrade offer, not a fault: whether the node can be used
	// is decided by its protocol and capabilities (docs/protocol/protocol.md "Node handshake").
	NodeVersion  string `json:"node_version,omitempty"`
	NodeMismatch bool   `json:"node_mismatch,omitempty"`
	// NodeProtocol is the node protocol the node announced (0: from before the handshake). NodeIncompatible is a
	// code when its manager cannot drive it (it then refuses everything but updating it): node_protocol_old (an older
	// protocol, or a required capability missing: update the node) or node_protocol_new (it needs a newer manager).
	NodeProtocol     int    `json:"node_protocol,omitempty"`
	NodeIncompatible string `json:"node_incompatible,omitempty"`
	// Deploying: the hub is installing its own node build there (POST …/deploy); DeployError: why the last one failed.
	Deploying   bool   `json:"deploying,omitempty"`
	DeployError string `json:"deploy_error,omitempty"`
	// CanDeploy is explicit so clients can distinguish an older hub without capability reporting.
	CanDeploy    *bool  `json:"can_deploy,omitempty"`
	DeployReason string `json:"deploy_reason,omitempty"`
	DeployPhase  string `json:"deploy_phase,omitempty"`
	// System: OS and hardware, from the node's last connection (kept across hub restarts).
	System *System `json:"system,omitempty"`
	// Link: kind link, while the computer's persona-link is connected (docs/operations/link.md).
	Link *MachineLink `json:"link,omitempty"`
	// Transport is how the hub reaches it now: local | ssh | tailcat | link (empty while offline); Tailcat: it can be
	// reached over tailcat (an address is set); TailcatPath, over tailcat: direct (NAT traversal worked) | relay.
	Transport   string `json:"transport,omitempty"`
	Tailcat     bool   `json:"tailcat,omitempty"`
	TailcatPath string `json:"tailcat_path,omitempty"`
	// Engine is the terminal host the machine's node drives (EngineHerdr when never chosen); Engines, what the node
	// found on the machine (empty while offline). docs/protocol/protocol.md "Engines".
	Engine  string   `json:"engine"`
	Engines []Engine `json:"engines"`
	// EngineHosts and EngineDefault are the hosts the machine enables side by side and its default host (#2576,
	// enginehosts.go): Engine is the single engine closest to them, for older clients. A machine never set has
	// [herdr]; the empty set has no default. EngineSuggested is the set to preselect when the machine is paired
	// (what the node found available, else herdr; only while online). EngineSetsUnsupported: the node is online, too
	// old for sets, and the chosen set is not one it can run as chosen — it runs Engine until it is updated.
	EngineHosts           []string      `json:"engine_hosts"`
	EngineDefault         string        `json:"engine_default"`
	EngineSuggested       *EngineChoice `json:"engine_suggested,omitempty"`
	EngineSetsUnsupported bool          `json:"engine_sets_unsupported,omitempty"`
	// Computer: operating it through Cua Driver, while the plugin cua is on and it is a Mac (Computer).
	Computer *Computer `json:"computer,omitempty"`
	// Browser: its ego lite, while the plugin ego is on and it is a Mac (Browser).
	Browser *Browser `json:"browser,omitempty"`
	// Hub mesh (docs/server/mesh.md): the hub that connects to this machine (its owner), its name, the machine's own id
	// (~/.persona/machine-uid, empty for an old node or one never reached) and how the hub the client talks to gets
	// there. A hub without peers fills them in for its own machines too.
	HubID      string        `json:"hub_id,omitempty"`
	HubName    string        `json:"hub_name,omitempty"`
	MachineUID string        `json:"machine_uid,omitempty"`
	Route      *MachineRoute `json:"route,omitempty"`
	// Paths: every way the center has to this computer (docs/archive/operations/plan-hub-center.md 5), when there is more than one or it
	// is a member hub's; the machine detail and the admin page show them.
	Paths []MachinePath `json:"paths,omitempty"`
}

// MachineRoute is how the center reaches a machine now (docs/archive/operations/plan-hub-center.md 5): via one of its own ways (local |
// service | link | ssh | tailcat, hops 0) or through a member hub (hub, hops 1); RTTMS is the node's round trip
// when it was measured.
type MachineRoute struct {
	Via   string `json:"via"`
	Hops  int    `json:"hops"`
	RTTMS *int   `json:"rtt_ms,omitempty"`
}

// MachinePath is one way to a machine: how, through which hub (the center itself for its own), whether it can be used
// now, its last round trip, and whether it is the one in use.
type MachinePath struct {
	Via    string `json:"via"`
	HubID  string `json:"hub_id"`
	OK     bool   `json:"ok"`
	RTTMS  int    `json:"rtt_ms,omitempty"`
	Active bool   `json:"active,omitempty"`
}

// MachineLink is a live persona-link: its build, the hub URL it went to and since when.
type MachineLink struct {
	Version string    `json:"version,omitempty"`
	Address string    `json:"address,omitempty"`
	Since   time.Time `json:"since"`
}

type NodeInfo struct {
	// Protocol, ProtocolMin and Capabilities are the node protocol handshake (internal/nodeproto, docs/protocol/protocol.md
	// "Node handshake"): the node protocol it speaks, the oldest manager protocol it answers, and what it can do.
	// Nodes from before the handshake leave all three out (protocol 0); managers from before it ignore them.
	Protocol           int      `json:"protocol,omitempty"`
	ProtocolMin        int      `json:"protocol_min,omitempty"`
	Capabilities       []string `json:"capabilities,omitempty"`
	NodeUpdate         bool     `json:"node_update,omitempty"`
	NodeUpdateReason   string   `json:"node_update_reason,omitempty"`
	ModelConfiguration bool     `json:"model_configuration,omitempty"`
	Hostname           string   `json:"hostname"`
	OS                 string   `json:"os"`
	Arch               string   `json:"arch"`
	Home               string   `json:"home"`
	DataDir            string   `json:"data_dir,omitempty"`
	HerdrVersion       string   `json:"herdr_version"`
	ClaudeVersion      string   `json:"claude_version"`
	Version            string   `json:"version"`
	System             *System  `json:"system,omitempty"`
	// Program is the program the node runs as (nodeproto.ProgramPersonad / ProgramPersonaNode) and Executable the file
	// it runs from: an update brings the same kind of program, to that file. Older nodes leave both out (personad, at the
	// machine's remote_bin).
	Program    string `json:"program,omitempty"`
	Executable string `json:"executable,omitempty"`
	// Engine is the engine the node drives now; Engines, what it found (proto.Machine.Engines). Older nodes leave
	// both out.
	Engine  string   `json:"engine,omitempty"`
	Engines []Engine `json:"engines,omitempty"`
	// EngineHosts is the set of hosts the node drives now (enginehosts.go): reported by nodes that take a set in
	// engine.use (its hosts and default fields), left out by older ones — the hub then sends them only the engine.
	EngineHosts *EngineChoice `json:"engine_hosts,omitempty"`
	// MachineUID is the computer's own id (~/.persona/machine-uid, a UUIDv4 made on the node's first run): hubs that
	// both reach it tell it is the same computer (docs/server/mesh.md). Older nodes leave it out.
	MachineUID string `json:"machine_uid,omitempty"`
	// HerdrRunning and HerdrRunningKnown: whether classic herdr's server answers on this machine now, so the Agents
	// list can offer "Start" instead of waiting for a start to fail (docs/integrations/engines.md). An older node sets neither, and
	// the hub then tells "it does not run" apart from "nobody said".
	HerdrRunning      bool `json:"herdr_running,omitempty"`
	HerdrRunningKnown bool `json:"herdr_running_known,omitempty"`
	// BuiltinPrograms are the extensions' programs built into this node (cua, ego): the hub asks them over
	// plugin.node.call (node/builtin.go). An older node leaves it out, and the hub asks it by the old method names.
	BuiltinPrograms []string `json:"builtin_programs,omitempty"`
}

// System is what a machine is: OS, hardware, since when it runs (sysmon; docs/server/machines.md). Read once when the node
// starts and sent with node.info; the hub keeps the last one of every machine in Postgres.
type System struct {
	OS       string `json:"os"`                  // runtime.GOOS: linux | darwin
	Distro   string `json:"distro"`              // os-release ID (ubuntu, debian, fedora…), "macos"
	Like     string `json:"like,omitempty"`      // os-release ID_LIKE ("ubuntu debian" for Pop!_OS): for the icon
	Name     string `json:"name"`                // "Ubuntu 24.04.1 LTS", "macOS 26.6.2"
	Version  string `json:"version"`             // "24.04", "26.6.2"
	Build    string `json:"build,omitempty"`     // macOS build ("25G83")
	Kernel   string `json:"kernel"`              // "6.8.0-45-generic", "Darwin 25.6.0"
	Arch     string `json:"arch"`                // amd64 | arm64
	Model    string `json:"model,omitempty"`     // "Mac Studio", a PC's DMI product name
	CPU      string `json:"cpu,omitempty"`       // "Apple M1 Ultra", "AMD Ryzen 9 7950X"
	Cores    int    `json:"cores"`               // logical CPUs
	Memory   int64  `json:"memory"`              // bytes
	BootTime int64  `json:"boot_time,omitempty"` // Unix seconds
}
