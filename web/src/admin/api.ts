import { CSRF_HEADERS } from "../lib/compatibility";
// The admin API client (docs/operations/admin.md "API"): a session cookie, not the web client's bearer token. Writes carry the
// session's CSRF token, which lives in memory only (GET state hands it out again after a reload).
import { currentLanguage, t } from "../i18n";
import { previewConsent } from "../lib/extension-model";
import { catalogOf, catalogQuery, marketView, type Catalog, type Market, type ExtensionRemovalResult } from "../lib/extension-market";
import type { AddedMachine, AdminExtension, AdminExtensions, AssistantCards, ConnectorRepair, ExtensionMetrics, ExtensionPreview, LinkPair, LogLine, LogsPage, MeshView, MetricsOverview, MetricsWindow, UsageSource, UsageSourceInput, UsageSources, UsageSourceType, UsageTestResult } from "./types";

export class AdminError extends Error {
  status: number;
  code: string;
  retryAfter: number;
  body: Record<string, unknown>;
  constructor(status: number, message: string, body: Record<string, unknown>, retryAfter = 0) {
    super(message);
    this.status = status;
    this.code = typeof body.code === "string" ? body.code : "";
    this.retryAfter = retryAfter;
    this.body = body;
  }
}

export type Transport = (url: string, init: RequestInit) => Promise<Response>;

let transport: Transport = (url, init) => fetch(url, init);
let csrf = "";
let onLoginRequired: () => void = () => {};

export function setTransport(t: Transport) {
  transport = t;
}

export function setCsrf(token: string | undefined) {
  csrf = token ?? "";
}

export function handleLoginRequired(fn: () => void) {
  onLoginRequired = fn;
}

type Method = "GET" | "POST" | "PUT" | "DELETE";

export async function adminApi<T>(path: string, method: Method = "GET", json?: unknown): Promise<T> {
  // the hub writes its error text in this language (docs/protocol/i18n.md "Protocol")
  const headers: Record<string, string> = { "Accept-Language": currentLanguage() };
  let body: string | undefined;
  if (method !== "GET") {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(json ?? {});
    if (csrf) CSRF_HEADERS.forEach(name => headers[name] = csrf);
  }
  return answer<T>(await transport(`/api/v1/admin${path}`, { method, headers, body, credentials: "same-origin", cache: "no-store" }));
}

// adminUpload posts a file as it is (application/octet-stream): the one admin write whose body is not JSON (an extension
// package, docs/operations/admin.md "Extensions"). query carries what the body cannot.
export async function adminUpload<T>(path: string, file: Blob, query: Record<string, string>): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/octet-stream", "Accept-Language": currentLanguage() };
  if (csrf) CSRF_HEADERS.forEach(name => headers[name] = csrf);
  const q = new URLSearchParams(Object.entries(query).filter(([, v]) => v)).toString();
  return answer<T>(await transport(`/api/v1/admin${path}?${q}`, { method: "POST", headers, body: file, credentials: "same-origin", cache: "no-store" }));
}

async function answer<T>(res: Response): Promise<T> {
  const text = await res.text();
  let data: Record<string, unknown> = {};
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = { error: text };
  }
  if (!res.ok) {
    const fromBody = Number(data.retry_after);
    const fromHeader = Number(res.headers.get("Retry-After"));
    const retry = Number.isFinite(fromBody) && fromBody > 0 ? fromBody : Number.isFinite(fromHeader) && fromHeader > 0 ? fromHeader : 0;
    const err = new AdminError(res.status, typeof data.error === "string" && data.error ? data.error : `HTTP ${res.status}`, data, retry);
    if (res.status === 401 && err.code === "login_required") {
      setCsrf("");
      onLoginRequired();
    }
    throw err;
  }
  return data as T;
}

const MESSAGES: Record<string, string> = {
  bad_password: t("admin-api.err_bad_password"),
  weak_password: t("admin-api.err_weak_password"),
  already_set: t("admin-api.err_already_set"),
  setup_local_only: t("admin-api.err_setup_local_only"),
  setup_code_required: t("admin-api.err_setup_code_required"),
  bad_setup_code: t("admin-api.err_bad_setup_code"),
  login_required: t("admin-api.err_login_required"),
  csrf: t("admin-api.err_csrf"),
  tls_required: t("admin-api.err_tls_required"),
  widen_cli_only: t("admin-api.err_widen_cli_only"),
  from_env: t("admin-api.err_from_env"),
  would_lock_you_out: t("admin-api.err_would_lock_you_out"),
  invalid_cidr: t("admin-api.err_invalid_cidr"),
  invalid_id: t("admin-api.err_invalid_id"),
  invalid_ssh_host: t("admin-api.err_invalid_ssh_host"),
  invalid_name: t("admin-api.err_invalid_name"),
  invalid_proxy: t("admin-api.err_invalid_proxy"),
  invalid_kind: t("admin-api.err_invalid_kind"),
  ssh_unsupported: t("admin-api.err_ssh_unsupported"),
  not_service: t("admin-api.err_not_service"),
  exists: t("admin-api.err_exists"),
  not_active: t("admin-api.err_not_active"),
  not_link: t("admin-api.err_not_link"),
  not_in_group: t("admin-api.err_not_in_group"),
  not_supervised: t("admin-api.err_not_supervised"),
  not_implemented: t("admin-api.err_not_implemented"),
  invalid_address: t("admin-api.err_invalid_address"),
  unreachable: t("admin-api.err_unreachable"),
  invalid_code: t("admin-api.err_invalid_code"),
  no_plugins: t("admin-api.err_no_plugins"),
  busy: t("admin-api.err_busy"),
  too_large: t("admin-api.err_too_large"),
  confirm_required: t("admin-api.err_confirm_required"),
  no_markets: t("admin-api.err_no_markets"),
};

// errorText is what a person reads for a failed call. Codes not listed here (pair_failed, member_hub,
// center_unreachable…) carry a message the hub wrote for people: shown as it is.
export function errorText(e: unknown): string {
  if (e instanceof AdminError) {
    if (e.code === "rate_limited") return e.retryAfter ? t("admin-api.rate_limited_seconds", { count: e.retryAfter }) : t("admin-api.rate_limited");
    return MESSAGES[e.code] ?? e.message;
  }
  if (e instanceof TypeError) return t("admin-api.hub_unreachable");
  return e instanceof Error ? e.message : String(e);
}

// ── the center and connectors (docs/archive/operations/plan-hub-center.md 6) ──

const enc = encodeURIComponent;

export const getMesh = () => adminApi<MeshView>("/mesh");

// setCenter makes hubId (this hub's own id too) the group's center. confirm: go ahead although the center now in place
// cannot be reached (409 confirm_required without it; that hub becomes a member when it comes back).
export const setCenter = (hubId: string, confirm = false) => adminApi<MeshView>(`/peers/${enc(hubId)}/center`, "POST", { confirm });

// addLinkMachine: the computer dials the hub (its persona-link, paired with linkPair next). Live at once.
export const addLinkMachine = (m: { id: string; name: string; icon: string }) => adminApi<AddedMachine>("/machines", "POST", { kind: "link", ...m });

// addServiceMachine: the hub dials the computer's connector. The hub pairs with the pasted link first and only then
// writes hub.json; 502 pair_failed says why in words.
export const addServiceMachine = (m: { id: string; name: string; icon: string; pair_link: string; address?: string; proxy?: string }) =>
  adminApi<AddedMachine>("/machines", "POST", { kind: "service", ...m });

export const linkPair = (id: string) => adminApi<LinkPair>(`/machines/${enc(id)}/link-pair`, "POST");

export const repairConnector = (id: string, pairLink: string, address?: string) =>
  adminApi<ConnectorRepair>(`/machines/${enc(id)}/connector`, "POST", { pair_link: pairLink, address: address || undefined });

// ── the assistant's cards (docs/operations/admin.md "Assistant cards"): one switch, live at once; the preview never calls the hub ──

export const getAssistantCards = () => adminApi<AssistantCards>("/assistant");
export const setPersonaView = (on: boolean) => adminApi<AssistantCards>("/assistant", "PUT", { persona_view: on });

// ── extensions (docs/operations/admin.md "Extensions"): install, upgrade, roll back, uninstall, switch ──

// The routes' root: /extensions, or /plugins on a hub from before #1844 (docs/extensions/extensions.md "Naming and compatibility"). Found out once,
// by the list: a 404 there means the older hub, and every call after uses its routes.
type Root = "/extensions" | "/plugins";
type ListAnswer = AdminExtensions & { plugins?: AdminExtension[] };
let rootFound: Promise<{ root: Root; list?: ListAnswer }> | null = null;

function findRoot(): Promise<{ root: Root; list?: ListAnswer }> {
  rootFound ??= adminApi<ListAnswer>("/extensions").then(
    (list) => ({ root: "/extensions" as Root, list }),
    (e) => {
      if (e instanceof AdminError && e.status === 404) return { root: "/plugins" as Root };
      rootFound = null; // not an answer about the routes (logged out, offline): ask again next time
      throw e;
    },
  );
  return rootFound;
}

/** The routes' root, for the calls below. */
const extRoot = () => findRoot().then((f) => f.root);

/** Only for tests: forget which routes the hub has. */
export function resetExtensionRoot() {
  rootFound = null;
}

export async function getExtensions(): Promise<AdminExtensions> {
  const read = (r: ListAnswer): AdminExtensions => ({ ...r, extensions: r.extensions ?? r.plugins ?? [] });
  const found = await findRoot();
  // the probe's own answer serves the first call; later ones (after a change) ask again
  if (found.list) {
    const list = found.list;
    rootFound = Promise.resolve({ root: found.root });
    return read(list);
  }
  return read(await adminApi<ListAnswer>(found.root));
}

// previewExtension stages source (dir: / file: / https://…#sha256= / remote:) on the hub: what it may do, before anything
// installs. upgrade: the installed extension's id when this is its new version.
export const previewExtension = async (source: string, upgrade?: string) =>
  adminApi<ExtensionPreview>(`${await extRoot()}/preview`, "POST", { source, upgrade: upgrade || undefined });

export const uploadExtension = async (file: File, upgrade?: string) =>
  adminUpload<ExtensionPreview>(`${await extRoot()}/upload`, file, { name: file.name, upgrade: upgrade ?? "" });

export const installExtension = async (pv: ExtensionPreview) =>
  adminApi<AdminExtension>(`${await extRoot()}/install`, "POST", previewConsent(pv));

export const upgradeExtension = async (id: string, pv: ExtensionPreview) =>
  adminApi<AdminExtension>(`${await extRoot()}/${enc(pv.rename_from || id)}/upgrade`, "POST", previewConsent(pv));

export const rollbackExtension = async (id: string) => adminApi<AdminExtension>(`${await extRoot()}/${enc(id)}/rollback`, "POST");

export const setExtensionEnabled = async (id: string, enabled: boolean) => adminApi<AdminExtension>(`${await extRoot()}/${enc(id)}/enabled`, "PUT", { enabled });

export const uninstallExtension = async (id: string, keepData: boolean) =>
  adminApi<ExtensionRemovalResult>(`${await extRoot()}/${enc(id)}`, "DELETE", { keep_data: keepData });

// ── extension markets (docs/extensions/extensions.md "Extension markets"): the apps' routes under /extensions/-/, behind the admin session. Installing
// and upgrading from a market's preview go through installExtension / upgradeExtension above (the preview's id and digest).

const markets = async () => `${await extRoot()}/-/markets`;
export const getMarkets = async () => adminApi<{ markets?: Market[] }>(await markets()).then((r) => (r.markets ?? []).map(marketView));
export const addMarket = async (spec: string, token?: string) => adminApi<Market>(await markets(), "POST", token ? { spec, token } : { spec }).then(marketView);
export const removeMarket = async (id: string) => adminApi<{ ok: boolean }>(`${await markets()}/${enc(id)}`, "DELETE");
export const refreshMarket = async (id: string) => adminApi<Market>(`${await markets()}/${enc(id)}/refresh`, "POST").then(marketView);
export const setMarketToken = async (id: string, token: string) => adminApi<Market>(`${await markets()}/${enc(id)}/token`, "PUT", { token }).then(marketView);
export const getCatalog = async (q?: string, market?: string, refresh?: boolean) =>
  adminApi<Catalog>(`${await extRoot()}/-/catalog${catalogQuery(q, market, refresh)}`).then(catalogOf);
/** Both keys: a hub from before #1844 reads `plugin`, a newer one `extension` (or `plugin`). */
export const previewFromMarket = async (market: string, extension: string) =>
  adminApi<ExtensionPreview>(`${await extRoot()}/-/catalog/preview`, "POST", { market, extension, plugin: extension });

// ── how the extensions run (docs/extensions/extensions.md "Activity"): counts and logs, on a hub from #1857 on (older ones 404) ──

export const getMetricsOverview = (window: MetricsWindow = "24h") => adminApi<MetricsOverview>(`/extensions/-/metrics?window=${window}`);

export const getExtensionMetrics = (id: string, window: MetricsWindow) => adminApi<ExtensionMetrics>(`/extensions/${enc(id)}/metrics?window=${window}`);

export const getExtensionLogs = (id: string, q = "", since?: number) => {
  const p = new URLSearchParams();
  if (q) p.set("q", q);
  if (since !== undefined) p.set("since", String(since));
  const qs = p.toString();
  return adminApi<LogsPage>(`/extensions/${enc(id)}/logs${qs ? `?${qs}` : ""}`);
};

/** Hands each new line of an extension's log (after since) to onLine until the returned stop; onError when it breaks. */
export type LogFollower = (id: string, since: number, onLine: (l: LogLine) => void, onError: () => void) => () => void;

// The real one is the server-sent events stream (the browser reconnects with Last-Event-ID); the mock hub sets its own.
let follower: LogFollower = (id, since, onLine, onError) => {
  const es = new EventSource(`/api/v1/admin/extensions/${enc(id)}/logs/stream?since=${since}`, { withCredentials: true });
  es.onmessage = (ev) => {
    try {
      onLine(JSON.parse(ev.data) as LogLine);
    } catch {
      // a line that is not one: skipped
    }
  };
  es.onerror = () => {
    if (es.readyState === EventSource.CLOSED) onError();
  };
  return () => es.close();
};

export function setLogFollower(f: LogFollower) {
  follower = f;
}

export const followExtensionLogs: LogFollower = (...args) => follower(...args);

// ── usage sources (docs/server/usage-windows.md "Account-pool gateways"): the gateways whose usage windows join the sidebar's ──

export const getUsageSources = () => adminApi<UsageSources>("/usage-sources");
export const saveUsageSettings = (s: { enabled?: boolean; poll_minutes?: number }) => adminApi<UsageSources>("/usage-sources", "PUT", s);
export const addUsageSource = (s: UsageSourceInput) => adminApi<{ source: UsageSource }>("/usage-sources", "POST", s);
export const editUsageSource = (id: string, s: UsageSourceInput) => adminApi<{ source: UsageSource }>(`/usage-sources/${enc(id)}`, "PUT", s);
export const removeUsageSource = (id: string) => adminApi<{ ok: boolean }>(`/usage-sources/${enc(id)}`, "DELETE");
// testUsageSource reads once, read-only, and saves nothing. With id the saved key is used, when the address is the saved one.
export const testUsageSource = (s: { id?: string; type: UsageSourceType; url: string; key?: string }) => adminApi<UsageTestResult>("/usage-sources/test", "POST", s);
