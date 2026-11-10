// Extension markets (docs/extensions/extensions.md "Extension marketplace"): markets the hub reads (an index persona-extensions.json on a site or a GitHub
// repository), their catalog, and installing, upgrading and removing from it. A full-rights device may: the package is
// pinned by the index's sha256 and nothing installs before the user has seen what it may do (the preview's
// permissions digest). The admin UI has the same routes under /api/v1/admin/extensions/-/ (its api.ts), the same words
// (here).
import { api, enc, pastHub } from "./api";
import { itemHeaders } from "./manager-choice";
import { extRoot, listOf } from "./extension-api";
import { changed, type HubExtension, type HubExtensionDetail, type ExtensionPermission } from "./extensions";
import { previewConsent, type DependencyState, type PackMember } from "./extension-model";
import { t } from "../i18n";

/** One market the hub reads (its token, if any, is never sent back). */
export interface Market {
  id: string;
  spec: string;
  name: string;
  description?: string;
  homepage?: string;
  has_token: boolean;
  /** How many extensions its index has. */
  extensions: number;
  warnings?: string[];
  /** Why the last read failed; the extensions it had before stay listed. */
  error?: string;
  fetched_at?: string;
  added_at?: string;
  /** How its index is signed (a hub with market signatures, #1848). */
  signature?: MarketSignature;
  /** The official market every hub has (#2351), listed first; absent on other markets and older hubs. */
  official?: boolean;
}

/** A catalog's verification status. key_changed is an official pin mismatch, never a request to trust a key. */
export interface MarketSignature {
  state: string;
  key_id?: string;
}

/** One extension of a market, as the hub sees it against what is installed. */
export interface CatalogItem {
  market: string;
  market_name: string;
  id: string;
  name: string;
  version: string;
  description?: string;
  publisher?: string;
  homepage?: string;
  tags?: string[];
  contributes?: Record<string, string[]>;
  permissions?: ExtensionPermission[];
  /** Why this hub cannot install it (no package for its platform, needs a newer hub). */
  problem?: string;
  /** The installed version, if any. */
  installed?: string;
  installed_source?: string;
  from_this_market?: boolean;
  upgradable?: boolean;
  /** Source metadata alone never reserves an installed alias. */
  replaces?: string;
  /** Installed predecessor only after the hub verifies this market's ownership. */
  rename_from?: string;
}

export interface Catalog {
  markets: Market[];
  extensions: CatalogItem[];
  platform?: string;
}

/** What installing (or upgrading) would do, before it does: POST …/catalog/preview. */
export interface MarketPreview {
  preview_id: string;
  manifest: { id: string; name: string; version: string; description?: string; publisher?: string; homepage?: string; contributes?: Record<string, unknown[]>; extension_pack?: PackMember[] };
  depends?: DependencyState[];
  bundle?: MarketPreview[];
  unresolved?: string[];
  permissions: ExtensionPermission[];
  permissions_digest: string;
  warnings?: string[];
  expires_at: string;
  upgrade?: boolean;
  /** The committed predecessor explicitly being upgraded, never a new install target. */
  rename_from?: string;
  from?: string;
  more_permissions?: boolean;
  added_permissions?: ExtensionPermission[];
  market?: string;
}

// ── the hub ─────────────────────────────────────────────

/** The hub's market routes: /extensions/-, or /plugins/- on a hub from before #1844. */
const M = () => `${extRoot()}/-`;

/** A market as either kind of hub sends it: how many extensions under `extensions` (or `plugins`). */
export const marketView = (m: Market & { plugins?: number }): Market => {
  const { plugins, ...rest } = m;
  return { ...rest, ...(m.official ? { name: officialMarketName() } : {}), extensions: m.extensions ?? plugins ?? 0 };
};

/** The official market is Official extensions in the page's language (#5017), whatever its index (or an older hub) calls it. */
export const officialMarketName = () => t("lib-extension-market.official_name");

export const listMarkets = () => api<{ markets?: Market[] }>(`${M()}/markets`).then((r) => (r.markets ?? []).map(marketView));
export const addMarket = (spec: string, token?: string) => api<Market>(`${M()}/markets`, { method: "POST", json: token ? { spec, token } : { spec } }).then(marketView);
export const removeMarket = (id: string) => api<{ ok: boolean }>(`${M()}/markets/${enc(id)}`, { method: "DELETE" });
export const refreshMarket = (id: string) => api<Market>(`${M()}/markets/${enc(id)}/refresh`, { method: "POST" }).then(marketView);
/** A new token ("" takes it away); the hub reads the index again with it. */
export const setMarketToken = (id: string, token: string) => api<Market>(`${M()}/markets/${enc(id)}/token`, { method: "PUT", json: { token } }).then(marketView);

export function catalogQuery(q?: string, market?: string, refresh?: boolean): string {
  const p = new URLSearchParams();
  if (q?.trim()) p.set("q", q.trim());
  if (market) p.set("market", market);
  if (refresh) p.set("refresh", "1");
  const s = p.toString();
  return s ? `?${s}` : "";
}

// A refresh waits for every market to be read again, a git market's fetch up to 3 minutes (extension/market_git.go).
// `manager` (here and below): whose extensions (another manager's upgrade, docs/assistant/managers.md "Clients"); none: the default's.
export const getCatalog = (q?: string, market?: string, refresh?: boolean, manager?: string) =>
  api<Catalog & { plugins?: CatalogItem[] }>(`${M()}/catalog${catalogQuery(q, market, refresh)}`, { ...(refresh ? { timeout: pastHub(180_000) } : {}), headers: itemHeaders(manager) }).then(catalogOf);
/** A catalog as either kind of hub sends it (the list under `extensions`, or `plugins`). */
export const catalogOf = (c: Catalog & { plugins?: CatalogItem[] }): Catalog => {
  const markets = (c.markets ?? []).map(marketView);
  const official = new Set(markets.filter((m) => m.official).map((m) => m.id));
  const extensions = listOf<CatalogItem>(c).map((it) => (official.has(it.market) ? { ...it, market_name: officialMarketName() } : it));
  return { markets, extensions, ...(c.platform ? { platform: c.platform } : {}) };
};
/** Both keys: a hub from before #1844 reads `plugin`, a newer one `extension` (or `plugin`). */
export const previewFromMarket = (market: string, extension: string, manager?: string) =>
  api<MarketPreview>(`${M()}/catalog/preview`, { method: "POST", json: { market, extension, plugin: extension }, headers: itemHeaders(manager) });
export const installPreview = (pv: MarketPreview, manager?: string) =>
  api<HubExtensionDetail>(`${M()}/install`, { method: "POST", json: previewConsent(pv), headers: itemHeaders(manager) }).then(changed);
export const upgradePreview = (id: string, pv: MarketPreview, manager?: string) =>
  api<HubExtensionDetail>(`${extRoot()}/${enc(pv.rename_from || id)}/-/upgrade`, { method: "POST", json: previewConsent(pv), headers: itemHeaders(manager) }).then(changed);
export interface ExtensionRemovalResult {
  ok: boolean;
  /** The extension is gone, but these files need manual cleanup. Older hubs omit this. */
  warnings?: string[] | null;
}
/** Only one installed from a market (`origin` market:…); keepData leaves ~/.persona/extensions/data/<id>/. */
export const uninstallExtension = (id: string, keepData = false, cascade = false, members = false) => {
  const q = new URLSearchParams();
  if (keepData) q.set("keep_data", "1");
  if (cascade) q.set("cascade", "1");
  if (members) q.set("members", "1");
  return api<ExtensionRemovalResult>(`${extRoot()}/${enc(id)}/-/${q.size ? `?${q}` : ""}`, { method: "DELETE" }).then(changed);
};

// ── words and rules ─────────────────────────────────────

/** The market an extension was installed from: `market:<market id>/<extension id>`; "" otherwise. */
export function marketOf(origin?: string): string {
  const m = /^market:([^/]+)\//.exec(origin ?? "");
  return m ? m[1] : "";
}

/**
 * How many of `manager`'s installed extensions upgrade from its market `id`: removing the market stops their upgrades
 * (docs/extensions/extensions.md "Auto-upgrade", #1895). The hub's merged list names every item's manager; another
 * manager's copies are left out, since a market's id is the same wherever its address is registered, and an item
 * without one is the manager's (a list from one manager). null when the hub does not say (one from before `market`: a
 * market extension without it).
 */
export function upgradingFrom(id: string, list: Pick<HubExtension, "manager" | "auto_upgrade" | "market">[], manager: string): number | null {
  const own = list.filter((e) => (e.manager ?? manager) === manager);
  if (own.some((e) => e.auto_upgrade !== undefined && !e.market)) return null;
  return own.filter((e) => e.market === id).length;
}

/**
 * A market read with git (`git:<url>[#branch]`, or a bare `git@host:path.git`): the hub's own SSH credentials, no
 * token. The address itself is never checked here: the hub reads it and says what is wrong (#1373).
 */
export const isGitMarket = (spec: string) => /^(git:|git@|ssh:\/\/)/.test(spec.trim());

/** The icon a market gets: its kind of source. */
export const marketKind = (spec: string): "github" | "git" | "web" => (spec.startsWith("github:") ? "github" : isGitMarket(spec) ? "git" : "web");

/**
 * A GitHub market without a token: GitHub allows 60 reads an hour per public IP, shared by every device behind the
 * same network, so this is the market an error leaves fixable here. A token raises the limit to 5,000, public repos
 * included.
 */
export const wantsGitHubToken = (m: Pick<Market, "spec" | "has_token">) => isGitHubMarket(m) && !m.has_token;

/** A market read through GitHub's API (github:owner/repo), the kind a GitHub token is for. */
export const isGitHubMarket = (m: Pick<Market, "spec">) => m.spec.startsWith("github:");

/**
 * GitHub's page for a new fine-grained token, filled in for a market (docs/extensions/extensions.md "GitHub rate limits"): a name, what it
 * is for, a year. It needs no permission at all: such a token always reads public repositories, which is what lifts the
 * hourly limit; a private market's repository is chosen on that page.
 */
export const GITHUB_TOKEN_URL =
  "https://github.com/settings/personal-access-tokens/new?" +
  new URLSearchParams({
    name: "Persona hub extension markets",
    description: "Lets a Persona hub read its GitHub extension markets (read-only).",
    expires_in: "365",
  }).toString();

/** The words under the add form's address field, and its placeholder: the three ways to write one. */
export const MARKET_SPEC_PLACEHOLDER = t("lib-extension-market.spec_placeholder");
export const MARKET_SPEC_HINT = t("lib-extension-market.spec_hint");

export type ItemState = { kind: "install" | "upgrade" | "installed" | "problem" | "elsewhere"; label: string; tone: "accent" | "green" | "orange" | "red" | "gray" };

/** Where a catalog extension stands on this hub, and what the button does. */
export function itemState(it: CatalogItem): ItemState {
  if (it.installed && it.upgradable) {
    if (it.problem) return { kind: "problem", label: t("lib-extension-market.installed_version", { version: it.installed }), tone: "gray" };
    return { kind: "upgrade", label: t("lib-extension-market.upgrade_to", { version: it.version }), tone: "accent" };
  }
  if (it.installed) {
    if (it.installed_source && it.installed_source !== "installed") return { kind: "elsewhere", label: it.installed_source === "builtin" ? t("lib-extension-market.builtin") : t("lib-extension-market.in_hub_json"), tone: "gray" };
    return { kind: "installed", label: t("lib-extension-market.installed_version", { version: it.installed }), tone: "green" };
  }
  if (it.problem) return { kind: "problem", label: t("lib-extension-market.cant_install"), tone: "orange" };
  return { kind: "install", label: t("lib-extension-market.not_installed"), tone: "gray" };
}

/** Uninstalling from here: only an extension this market installed. */
export const canUninstall = (it: CatalogItem) => Boolean(it.installed && it.from_this_market);

/** A market's line under its name: how many extensions, when it was read, whether it has a token. */
export function marketLine(m: Market, ago: (iso?: string) => string): string {
  return [t("lib-extension-market.extensions_count", { count: m.extensions }), m.fetched_at ? t("lib-extension-market.read_ago", { ago: ago(m.fetched_at) }) : t("lib-extension-market.never_read"), m.has_token ? t("lib-extension-market.has_token") : "", signatureText(m.signature)]
    .filter(Boolean)
    .join(" · ");
}

const SIGNATURES: Record<string, string> = {
  verified: t("lib-extension-market.sig_verified"),
  signed: t("lib-extension-market.sig_verified"),
  unsigned: t("lib-extension-market.sig_unsigned"),
  invalid: t("lib-extension-market.sig_invalid"),
  missing: t("lib-extension-market.sig_missing"),
  key_changed: t("lib-extension-market.sig_key_changed"),
};

/** How the market's index is signed (#1848); "" from a hub that does not say. */
export function signatureText(s?: MarketSignature): string {
  if (!s?.state) return "";
  return SIGNATURES[s.state] ?? t("lib-extension-market.sig_state", { state: s.state });
}

/** A market's address as people read it. */
export function specLabel(spec: string): string {
  if (spec.startsWith("github:")) return spec.slice("github:".length);
  if (spec.startsWith("git:")) return spec.slice("git:".length);
  try {
    const u = new URL(spec);
    return u.host + (u.pathname === "/" ? "" : u.pathname);
  } catch {
    return spec;
  }
}

/** The catalog item's title line: its version, publisher and market. */
export const itemLine = (it: CatalogItem) => [it.version, it.publisher, it.market_name].filter(Boolean).join(" · ");

/** A preview's version as it changes: "1.0.0 → 1.1.0" for an upgrade. */
export const previewVersion = (pv: MarketPreview) => (pv.upgrade && pv.from ? `${pv.from} → ${pv.manifest.version}` : pv.manifest.version);

/** Consent names the identity transition even when no additional permissions are requested. */
export const renameConsent = (pv: Pick<MarketPreview, "manifest" | "rename_from">) => pv.rename_from
  ? t("lib-extension-market.rename_detail", { from: pv.rename_from, to: pv.manifest.id }) : "";

/** The button that agrees: explicit rename, install, or upgrade. */
export const agreeLabel = (pv: MarketPreview) => pv.rename_from ? t("lib-extension-market.agree_rename")
  : (pv.upgrade ? (pv.more_permissions ? t("lib-extension-market.agree_upgrade_more") : t("lib-extension-market.upgrade")) : t("lib-extension-market.agree_install"));

// ── the settings route (?tab=extensions&extension=…) ──────────

/**
 * What the extensions tab shows, from its `extension` parameter: an extension's id (its page), `<id>/ui/<ui>` (a page it draws),
 * `-market` (the catalog), `-market/<market>/<extension>` (one extension of a market), `-markets` (the markets).
 * An extension's id never starts with "-" or has "/" (the hub keeps `-` for itself).
 */
export type ExtensionsRoute =
  | { kind: "list" }
  // manager: whose extension it is (several managers: the same id may be installed under each, docs/assistant/managers.md); none
  // is the default's. Written `<id>@<manager>`.
  | { kind: "extension"; id: string; manager?: string }
  | { kind: "page"; id: string; ui: string; manager?: string }
  | { kind: "catalog" }
  // manager: whose copy an upgrade is for (another manager's extension page opened it); none: the default's
  | { kind: "item"; market: string; id: string; manager?: string }
  | { kind: "markets" };

export function extensionsRoute(sub: string | null | undefined): ExtensionsRoute {
  if (!sub) return { kind: "list" };
  if (sub === "-market") return { kind: "catalog" };
  if (sub === "-markets") return { kind: "markets" };
  const item = /^-market\/([^/]+)\/([^/@]+)(?:@([^/]+))?$/.exec(sub);
  if (item) return { kind: "item", market: item[1], id: item[2], ...(item[3] ? { manager: item[3] } : {}) };
  const page = /^([^/@-][^/@]*)(?:@([^/]+))?\/ui\/([^/]+)$/.exec(sub);
  if (page) return { kind: "page", id: page[1], ui: page[3], ...(page[2] ? { manager: page[2] } : {}) };
  if (sub.startsWith("-") || sub.includes("/")) return { kind: "list" };
  const [id, manager] = sub.split("@", 2);
  return { kind: "extension", id, ...(manager ? { manager } : {}) };
}

export function routeSub(r: ExtensionsRoute): string | null {
  switch (r.kind) {
    case "list":
      return null;
    case "extension":
      return r.manager ? `${r.id}@${r.manager}` : r.id;
    case "page":
      return `${r.id}${r.manager ? `@${r.manager}` : ""}/ui/${r.ui}`;
    case "catalog":
      return "-market";
    case "item":
      return `-market/${r.market}/${r.id}${r.manager ? `@${r.manager}` : ""}`;
    case "markets":
      return "-markets";
  }
}
