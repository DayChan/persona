// Shapes of the hub's admin API (docs/operations/admin.md "API", docs/archive/operations/plan-hub-mesh.md 2.7 for peers).

export type Access = "loopback" | "lan" | "cidrs" | "any";

export interface AdminState {
  password_set: boolean;
  logged_in: boolean;
  can_setup: boolean;
  // No password yet and the request came over loopback (maybe a reverse proxy on the hub machine relaying the
  // internet): setup also needs the one-time code the hub logs / `personad hub admin setup-code` prints.
  setup_code_required?: boolean;
  csrf?: string;
  hub_name: string;
  version: string;
  session_expires_at?: string;
}

export interface Restart {
  required: boolean;
  reasons: string[];
}

export interface Endpoint {
  url: string;
  kind: string; // ipv6 | name | lan | ipv4
  port: number;
  scheme: string;
  seen_at?: string;
}

export interface TLSInfo {
  listen: string;
  port: number;
  fp: string;
}

export interface Overview {
  hub: {
    hub_id: string;
    name: string;
    name_set: boolean;
    default_name: string;
    version: string;
    started_at: string;
    os: string;
    arch: string;
    container: boolean;
    can_restart: boolean;
  };
  listen: string;
  public_url: string;
  tls: TLSInfo | null;
  endpoints: Endpoint[];
  machines: { total: number; online: number };
  devices: number;
  restart: Restart;
}

export interface DoctorItem {
  level: "ok" | "problem" | "info";
  item: string;
  detail: string;
}

export interface DoctorReport {
  items: DoctorItem[];
  next: string[];
  ran_at: string;
}

export type MachineState = "active" | "pending_add" | "pending_remove";

// How a hub reaches a computer (docs/archive/operations/plan-hub-center.md 4, 5). service: the hub dials the computer's connector; link:
// the computer's persona-link dials the hub; hub: through a member hub of the group (configured on that member only).
export type Via = "local" | "service" | "link" | "ssh" | "tailcat" | "hub";

// One way to a computer (plan 5 "paths"): which hub it goes through, the hub.json entry there, whether it works now.
export interface MachinePath {
  via: Via | string;
  hub_id: string;
  id?: string;
  ok: boolean;
  rtt_ms?: number;
  active?: boolean;
  error?: string;
}

export interface AdminMachine {
  id: string;
  name: string;
  kind: string; // local | service | link | ssh | tailcat | hub
  icon?: string;
  ssh_host?: string;
  online: boolean;
  error?: string;
  transport?: string;
  node_version?: string;
  node_mismatch?: boolean;
  os?: string;
  last_seen?: string;
  link?: { version?: string; address?: string; since?: string } | null;
  state: MachineState;
  tailcat?: boolean;
  address?: string; // service: the connector's host:port
  proxy?: string; // service: socks5://… it is dialed through (no password)
  hub_id?: string; // the hub it is reached through now
  paths?: MachinePath[];
}

export interface AddedMachine {
  machine: AdminMachine;
  restart: Restart;
}

// POST machines/{id}/connector: a service computer's connector paired again.
export interface ConnectorRepair {
  ok: boolean;
  address: string;
  version: string;
  restart_required: boolean;
}

export interface MachinesView {
  machines: AdminMachine[];
  tls: boolean;
  restart: Restart;
}

export interface LinkPair {
  pair_url: string;
  expires_at: string;
  command: string;
}

export interface Device {
  id: string;
  name: string;
  kind: string;
  role: string;
  created_at?: string;
  last_seen?: string;
  revoked_at?: string;
  machine_id?: string;
}

export interface PhonePair {
  pair_url: string;
  url: string;
  expires_at: string;
  fp?: string;
  qr: { size: number; rows: string[] };
}

export interface NetworkView {
  listen: string;
  public_url: string;
  public_url_from_env?: boolean; // PERSONA_PUBLIC_URL sets it (Docker's .env): shown, not editable here
  tls: TLSInfo | null;
  advertise: string[];
  advertise_ipv6: boolean;
  advertise_lan: boolean;
  endpoints: Endpoint[];
  security: { trusted_cidrs: string[]; allow_public_plaintext: boolean };
  admin: { access: Access; cidrs: string[]; session_hours: number; from_env: boolean; container: boolean };
  you: { address: string; tls: boolean; allowed: Record<Access, boolean> };
  restart: Restart;
}

// refused: the other hub no longer accepts our credential (most likely it disconnected us on its side).
export type PeerState = "connected" | "connecting" | "offline" | "refused";

export interface Peer {
  hub_id: string;
  name: string;
  state: PeerState;
  address?: string;
  rtt_ms?: number;
  since?: string;
  error?: string;
  machines: number; // machines the peer reaches directly and owns
  role?: Role;
}

// The group of hubs (docs/archive/operations/plan-hub-center.md 2, 6): exactly one center serves every client and keeps all state;
// members pass requests and their computers on to it. A lone hub is its own center.
export type Role = "center" | "member";

export interface MeshHub {
  hub_id: string;
  name: string;
  role: Role;
  online: boolean;
  self?: boolean;
  paired?: boolean; // paired with this hub directly
}

export interface MeshView {
  role: Role;
  center: { hub_id: string; name: string; online: boolean };
  term: number;
  hubs: MeshHub[];
}

export interface DiscoveredHub {
  hub_id: string;
  name: string;
  addresses: string[];
  fp: string;
  version?: string;
}

export interface PeersView {
  peers: Peer[];
  discovered: DiscoveredHub[];
}

export interface PeerCode {
  code: string;
  expires_at: string;
}

// POST peers/probe: who answers at an address, to check against what the other hub's admin page shows before
// pasting its peer code. tls_port 0 and an empty fp: that hub has no TLS, so it cannot link.
export interface PeerProbe {
  hub_id: string;
  name: string;
  tls_port: number;
  fp: string;
  version?: string;
}

// ── extensions (docs/extensions/extensions.md "The hub's API", docs/operations/admin.md "Extensions") ──

export interface PermissionLine {
  key: string; // runs_code | network | hub_api | launcher_keys | secrets
  title: string;
  detail?: string;
}

export interface AdminExtension {
  id: string;
  name: string;
  kind: string;
  status: string; // running | stopped | starting | needs_setup | disabled | incompatible | error
  error?: string;
  version?: string;
  source?: string; // builtin | hub.json | installed
  enabled: boolean;
  description?: string;
  contributes?: Record<string, string[]>;
  needs_setup?: string[];
  warnings?: string[];
  publisher?: string;
  homepage?: string;
  origin?: string;
  previous_version?: string;
  read_only?: boolean;
  secrets?: { name: string; title: string; required?: boolean; set: boolean }[];
  permissions: PermissionLine[];
}

export interface AdminExtensions {
  extensions: AdminExtension[];
  can_install: boolean;
  upload_most: number;
}

export interface ExtensionPreview {
  bundle?: ExtensionPreview[];
  unresolved?: string[];
  depends?: import("../lib/extension-model").DependencyState[];
  preview_id: string;
  manifest: { id: string; name: string; version: string; description?: string; publisher?: string; homepage?: string; contributes?: Record<string, unknown[]> };
  permissions: PermissionLine[];
  permissions_digest: string;
  warnings?: string[];
  expires_at: string;
  upgrade?: boolean;
  rename_from?: string;
  from?: string;
  more_permissions?: boolean;
  added_permissions?: PermissionLine[];
  /** The market it comes from, when previewed from one (docs/extensions/extensions.md "Extension markets"). */
  market?: string;
}

// ── how the extensions run (docs/extensions/extensions.md "Activity") ──

export type MetricsWindow = "1h" | "24h" | "7d";

export interface MetricsSummary {
  calls: number;
  errors: number;
  error_rate: number;
  /** null without a timed call */
  p50_ms: number | null;
  p95_ms: number | null;
  p99_ms: number | null;
  codes?: Record<string, number>;
}

export interface ExitNote {
  at: string;
  reason: string;
}

export interface ExtensionMetrics {
  extension: string;
  window: MetricsWindow;
  from: string;
  to: string;
  step_seconds: number;
  total: MetricsSummary;
  methods: (MetricsSummary & { method: string })[];
  series: { at: string; calls: number; errors: number; p95_ms: number | null }[];
  starts: number;
  stops: number;
  exits: number;
  exit_reasons?: Record<string, number>;
  recent_exits: ExitNote[];
  counters?: Record<string, number>;
}

export interface ExtensionOverview {
  id: string;
  calls: number;
  errors: number;
  error_rate: number;
  p95_ms: number | null;
  starts: number;
  exits: number;
  last_exit?: ExitNote;
}

export interface MetricsOverview {
  window: MetricsWindow;
  from: string;
  to: string;
  extensions: ExtensionOverview[];
  /** The old paths' calls (/api/v1/plugins…, #1924): the compatibility layer goes once they stop. */
  legacy_calls: { route: string; calls: number; since_start: number }[];
}

export interface LogLine {
  seq: number;
  at: string;
  /** program: its own stderr; hub: the hub's lines about it */
  source: "program" | "hub" | string;
  text: string;
}

export interface LogsPage {
  lines: LogLine[];
  next: number;
  kept: number;
}

/** GET/PUT /api/v1/admin/assistant: hub.json's assistant.persona_view (docs/operations/admin.md "Assistant cards"). */
export interface AssistantCards {
  persona_view: boolean;
}

// ── usage sources (docs/server/usage-windows.md "Account-pool gateways", docs/operations/admin.md): account-pool gateways feeding the sidebar's usage bars ──

export type UsageSourceType = "relay" | "generic";

export interface UsageSeen {
  accounts: number;
  windows: number;
  /** accounts with a drawable window, by provider */
  providers: Record<string, number>;
  /** accounts the gateway listed at all */
  listed: number;
}

export interface UsageSourceStatus {
  ok_at?: string;
  tried_at?: string;
  /** the code of the last failed read (unauthorized, unreachable…); empty when it worked */
  error?: string;
  seen?: UsageSeen;
}

export interface UsageSource {
  id: string;
  name: string;
  type: UsageSourceType;
  url: string;
  /** whether a key is saved: the key itself is never sent back */
  has_key: boolean;
  enabled: boolean;
  status: UsageSourceStatus;
}

export interface UsageSources {
  enabled: boolean;
  poll_minutes: number;
  sources: UsageSource[];
  limit: number;
}

export interface UsageSourceInput {
  name?: string;
  type?: UsageSourceType;
  url?: string;
  /** a new key; left out or empty keeps the saved one */
  key?: string;
  clear_key?: boolean;
  enabled?: boolean;
}

export type UsageTestResult = ({ ok: true } & Pick<UsageSeen, "accounts" | "windows" | "providers" | "listed">) | { ok: false; code: string };
