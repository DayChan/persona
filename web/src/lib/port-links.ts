// Links to a port of the computer an agent runs on (docs/operations/ports.md "Recognizing links in replies"; the
// same rules as iOS's PortLinks): what an agent prints for the dev server it started (http://localhost:5173/,
// http://127.0.0.1:8000/docs, http://0.0.0.0:3000, http://[::1]:4173, the computer's own name, or a bare
// localhost:3000/x), which the web shows in the preview panel (docs/web/preview.md), never on this computer's localhost.

export interface PortLink {
  port: number;
  /** Path, query and fragment ("/" at least). */
  path: string;
  secure: boolean;
}

/** Hosts that always mean "that computer" (URL.hostname keeps the brackets of an IPv6 address). */
const LOOPBACK = new Set(["localhost", "127.0.0.1", "0.0.0.0", "[::1]", "[::]"]);

/** The most chips one answer gets: a reply listing more ports is a table, not a server it just started. */
export const MAX_PORT_LINKS = 8;

/** The computer's name with and without .local, lowercased (iOS PortLinks.names). */
function names(hostnames: readonly string[]): Set<string> {
  const out = new Set<string>();
  for (const h of hostnames) {
    const n = h.trim().toLowerCase();
    if (!n) continue;
    out.add(n);
    if (n.endsWith(".local")) out.add(n.slice(0, -".local".length));
    else if (!n.includes(".")) out.add(`${n}.local`);
  }
  return out;
}

function parse(url: string): URL | null {
  try {
    return new URL(url);
  } catch {
    return null;
  }
}

/** The port link `url` is: http(s) to a loopback host, *.localhost, or one of `hostnames` (the computer's name),
 *  with an explicit port. Null for anything else: another host, a LAN address, no port. */
export function portLink(url: string, hostnames: readonly string[] = []): PortLink | null {
  const u = parse(url);
  if (!u || (u.protocol !== "http:" && u.protocol !== "https:")) return null;
  const host = u.hostname.toLowerCase();
  if (!(LOOPBACK.has(host) || host.endsWith(".localhost") || names(hostnames).has(host))) return null;
  const port = Number(u.port);
  if (!Number.isInteger(port) || port < 1 || port > 65535) return null;
  return { port, path: u.pathname + u.search + u.hash || "/", secure: u.protocol === "https:" };
}

/** http(s) URLs in prose, Markdown links and <autolinks>: they end at whitespace, quotes, brackets and CJK punctuation. */
// i18n-ignore: CJK punctuation that ends a URL in an agent's reply
const SCHEME_URL = /\bhttps?:\/\/[^\s<>"'`()[\]{}，。；！？、（）「」]+/gi;
/** Bare `localhost:3000/x`, as servers often print it; not part of a longer name (`mylocalhost:3000`) or path. */
const BARE = /(^|[^A-Za-z0-9_./:@-])((?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]):\d{1,5}(?:\/[A-Za-z0-9_.~!$&'()*+,;=:@%/?#-]*)?)/g;
/** Sentence punctuation after a link is not part of it. */
const TRAILING = /[.,;:!?)]+$/;

const same = (a: PortLink, b: PortLink) => a.port === b.port && a.path === b.path && a.secure === b.secure;

/** The port links in `text`, in order, one per port and path, at most MAX_PORT_LINKS. */
export function portLinks(text: string, hostnames: readonly string[] = []): PortLink[] {
  if (!text.includes(":")) return [];
  const out: PortLink[] = [];
  const add = (l: PortLink | null) => {
    if (l && out.length < MAX_PORT_LINKS && !out.some((x) => same(x, l))) out.push(l);
  };
  const found: { at: number; end: number; link: PortLink | null }[] = [];
  for (const m of text.matchAll(SCHEME_URL)) {
    const s = m[0].replace(TRAILING, "");
    found.push({ at: m.index, end: m.index + m[0].length, link: portLink(s, hostnames) });
  }
  for (const m of text.matchAll(BARE)) {
    const at = m.index + m[1].length;
    if (found.some((f) => at < f.end && at >= f.at)) continue;
    found.push({ at, end: at + m[2].length, link: portLink(`http://${m[2].replace(TRAILING, "")}`) });
  }
  for (const f of found.sort((a, b) => a.at - b.at)) add(f.link);
  return out;
}

/** What a chip says: `:5173`, with the path when the link has one (`:8000/docs`), cut at 24 characters. */
export function portLabel(l: PortLink): string {
  const path = l.path === "/" ? "" : l.path;
  return `:${l.port}${[...path].length > 24 ? [...path].slice(0, 23).join("") + "…" : path}`;
}

/** The address a port link stands for on its computer, for copying. */
export function portURL(l: PortLink): string {
  return `${l.secure ? "https" : "http"}://localhost:${l.port}${l.path}`;
}
