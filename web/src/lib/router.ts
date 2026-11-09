import { assistantPath } from "./compatibility";
// A tiny history router: the hub serves index.html for every path that isn't a file.
import { useSyncExternalStore } from "react";
import { takeOverlayEntry } from "./back-close";
import { BASE, stripBase } from "./base";
import { decodeURIComponentSafe } from "./uri";

// Persona's main conversation is the front door (like the app, which opens on the assistant): `/` and `/persona` open it, `/persona/<id>` is
// one conversation, `/persona/new` starts a one-off one, and "Overview" lives at `/overview`. `/extension/<id>/<ui>` is an extension's page
// (docs/extensions/extensions.md "UI slots" nav_page).
/** What the middle of an agent's page shows (docs/web/web.md "Terminal ⇄ chat"): its conversation, or its terminal in place. */
export type SessionView = "chat" | "term";

export type Route =
  | { name: "home" }
  | { name: "request"; id: string }
  | { name: "workspace-board" }
  | { name: "persona-new" }
  // `view` is the page's `?view=chat|term`; without it the page shows what it showed last (lib/session-view.ts)
  | { name: "session"; mid: string; sid: string; view?: SessionView }
  // one herdr tab (a slot) of a workspace; `sid` picks its pane (lib/herdr-tree.ts resolveTab)
  | { name: "tab"; mid: string; space: string; slot: string; sid?: string; view?: SessionView }
  | { name: "persona"; sid?: string }
  // "All chats"; `origin` filters it (lib/conversation-origins.ts): the sidebar's "Automatic" rows open it so
  | { name: "history"; origin?: string }
  // the health space (docs/assistant/health.md): its home, or one of its conversations
  | { name: "health"; sid?: string }
  | { name: "profile" }
  // `manager`: whose item it is when that is another manager than the default (`?manager=`, docs/assistant/managers.md
  // "Clients"): two managers may list the same id
  | { name: "inbox"; id?: string; manager?: string }
  | { name: "messages"; id?: string }
  | { name: "routines" }
  | { name: "kb"; kind?: string; id?: string }
  | { name: "machines" }
  | { name: "machine"; mid: string }
  // one space of a machine by its name (lib/space-page.ts): its agents, the todos it hangs on, what can be done to it
  | { name: "space"; mid: string; space: string }
  // the terminal browser (docs/integrations/terminal.md "Standalone terminals"): every pane of every machine; `id` is the terminal opened (an agent
  // session's id, or "pane:<pane>" for a plain shell)
  | { name: "terminal"; mid?: string; id?: string }
  | { name: "settings" }
  | { name: "agents"; id?: string }
  // the phone's "Agents" page (#1996): the sidebar's Agents block on a page of its own, next to the assistant
  | { name: "agent-list" }
  // the agents archived from the Agents list (lib/agent-archives.ts), to find and restore
  | { name: "agent-archives" }
  | { name: "todos"; id?: string }
  | { name: "extension"; extension: string; ui: string };

/** `?view=term|chat` of an agent page's address; anything else is no choice. */
export function parseView(search: string): SessionView | undefined {
  const v = new URLSearchParams(search).get("view");
  return v === "term" || v === "chat" ? v : undefined;
}

export function parse(path: string, search = ""): Route {
  // a malformed escape must still route: parse runs outside every error boundary
  const p = path.split("/").filter(Boolean).map(decodeURIComponentSafe);
  const view = parseView(search);
  if (p[0] === "s" && p[1] && p[2]) return { name: "session", mid: p[1], sid: p[2], ...(view && { view }) };
  if (p[0] === "t" && p[1] && p[2] && p[3]) return { name: "tab", mid: p[1], space: p[2], slot: p[3], sid: p[4] || undefined, ...(view && { view }) };
  if (!p.length) return { name: "persona" };
  if (assistantPath(p[0])) return p[1] === "new" && !p[2] ? { name: "persona-new" } : { name: "persona", sid: p[1] };
  if (p[0] === "workspace-board") return { name: "workspace-board" };
  if (p[0] === "requests" && p[1]) return { name: "request", id: p[1] };
  if (p[0] === "overview") return { name: "home" };
  if (p[0] === "history") return p[1] ? { name: "history", origin: p[1] } : { name: "history" };
  if (p[0] === "health") return { name: "health", sid: p[1] || undefined };
  if (p[0] === "assistant") return { name: "profile" };
  if (p[0] === "inbox") {
    const manager = p[1] ? new URLSearchParams(search).get("manager") : null;
    return manager ? { name: "inbox", id: p[1], manager } : { name: "inbox", id: p[1] };
  }
  if (p[0] === "messages") return { name: "messages", id: p[1] };
  if (p[0] === "routines") return { name: "routines" };
  if (p[0] === "kb") return { name: "kb", kind: p[1], id: p[2] };
  if (p[0] === "space" && p[1] && p[2]) return { name: "space", mid: p[1], space: p[2] };
  if (p[0] === "machines") return p[1] ? { name: "machine", mid: p[1] } : { name: "machines" };
  if (p[0] === "terminal") return p[1] && p[2] ? { name: "terminal", mid: p[1], id: p[2] } : { name: "terminal" };
  if (p[0] === "a") return { name: "agents", id: p[1] };
  if (p[0] === "agents") return p[1] === "archived" ? { name: "agent-archives" } : { name: "agent-list" };
  if (p[0] === "settings") return { name: "settings" };
  // /plugin/<id>/<ui>: the same page under its name before #1845 (bookmarks)
  if ((p[0] === "extension" || p[0] === "plugin") && p[1] && p[2]) return { name: "extension", extension: p[1], ui: p[2] };
  if (p[0] === "todos") {
    const q = typeof window !== "undefined" ? new URLSearchParams(window.location.search).get("id") : null;
    return { name: "todos", id: p[1] || q || undefined };
  }
  return { name: "home" };
}

/** The address of a route as this page reaches it: under the sub-path the hub is served at (lib/base.ts), if any. */
export function href(r: Route, base = BASE): string {
  return base + routePath(r);
}

/** A route's path in the app, without the sub-path (what parse reads). */
export function routePath(r: Route): string {
  switch (r.name) {
    case "request":
      return `/requests/${encodeURIComponent(r.id)}`;
    case "session":
      return `/s/${encodeURIComponent(r.mid)}/${encodeURIComponent(r.sid)}${viewQuery(r.view)}`;
    case "tab":
      return ["/t", r.mid, r.space, r.slot, ...(r.sid ? [r.sid] : [])].map((x, i) => (i ? encodeURIComponent(x) : x)).join("/") + viewQuery(r.view);
    case "persona":
      return r.sid ? `/persona/${encodeURIComponent(r.sid)}` : "/";
    case "persona-new":
      return "/persona/new";
    case "history":
      return r.origin ? `/history/${encodeURIComponent(r.origin)}` : "/history";
    case "health":
      return r.sid ? `/health/${encodeURIComponent(r.sid)}` : "/health";
    case "profile":
      return "/assistant";
    case "inbox":
      return r.id ? `/inbox/${encodeURIComponent(r.id)}${r.manager ? `?manager=${encodeURIComponent(r.manager)}` : ""}` : "/inbox";
    case "messages":
      return r.id ? `/messages/${encodeURIComponent(r.id)}` : "/messages";
    case "routines":
      return "/routines";
    case "kb":
      return ["/kb", r.kind, r.kind && r.id].filter(Boolean).map((x, i) => (i ? encodeURIComponent(x as string) : x)).join("/");
    case "machines":
      return "/machines";
    case "machine":
      return `/machines/${encodeURIComponent(r.mid)}`;
    case "space":
      return `/space/${encodeURIComponent(r.mid)}/${encodeURIComponent(r.space)}`;
    case "terminal":
      return r.mid && r.id ? `/terminal/${encodeURIComponent(r.mid)}/${encodeURIComponent(r.id)}` : "/terminal";
    case "workspace-board":
      return "/workspace-board";
    case "settings":
      return "/settings";
    case "agents":
      return r.id ? `/a/${encodeURIComponent(r.id)}` : "/a";
    case "agent-list":
      return "/agents";
    case "agent-archives":
      return "/agents/archived";
    case "todos":
      return r.id ? `/todos?id=${encodeURIComponent(r.id)}` : "/todos";
    case "extension":
      return `/extension/${encodeURIComponent(r.extension)}/${encodeURIComponent(r.ui)}`;
    default:
      return "/overview";
  }
}

const viewQuery = (view?: SessionView) => (view ? `?view=${view}` : "");

const listeners = new Set<() => void>();
// The address Back / Forward last landed on, until a page takes it (takeReturn) or the app navigates on: a page that
// remembers where it was scrolled puts it back only on a return, and a fresh visit starts at the top (#5133).
let returnedTo = "";
if (typeof addEventListener !== "undefined") {
  addEventListener("popstate", () => {
    returnedTo = location.pathname + location.search;
    listeners.forEach((l) => l());
  });
}

/** Whether the page now at the address was reached by Back / Forward (once: the next call says no). */
export function takeReturn(): boolean {
  const back = returnedTo !== "" && returnedTo === location.pathname + location.search;
  returnedTo = "";
  return back;
}

/** `r` as a string is a path in the app ("/settings?tab=…"), without the sub-path. `state` goes with the new entry
 *  (history.state), so Back / Forward to it bring it back and other ways to the same page do not have it. */
export function navigate(r: Route | string, replace = false, state: Record<string, unknown> | null = null) {
  const to = typeof r === "string" ? BASE + r : href(r);
  // the query is part of the address (/s/…?view=term): the same page with another view is somewhere else
  if (to === location.pathname + location.search) return;
  returnedTo = "";
  // From inside an open overlay (a drawer's link, a sheet's action): the new page takes the overlay's entry, so Back
  // from it goes to the page under the overlay, not to a dead copy of it (#1996). A replace keeps the entry's state:
  // an overlay open over the page keeps its marker.
  if (replace) history.replaceState(state ?? history.state, "", to);
  else if (takeOverlayEntry()) history.replaceState(state, "", to);
  else history.pushState(state, "", to);
  listeners.forEach((l) => l());
}

/** The route shows this agent's chat: its own address (/s/…) or its tab's (/t/…/<sid>). */
export function showsSession(r: Route, mid: string, sid: string): boolean {
  return (r.name === "session" || r.name === "tab") && r.mid === mid && r.sid === sid;
}

export function useRoute(): Route {
  // The query is part of the route (/todos?id=…): a change to it alone must re-render too.
  const at = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => location.pathname + location.search,
  );
  const [path, ...query] = at.split("?");
  return parse(stripBase(path!), query.join("?"));
}

// <a> that navigates in-app on a plain click and still opens a new tab with ⌘ / middle click.
export function onLinkClick(e: React.MouseEvent, to: Route, state: Record<string, unknown> | null = null) {
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
  e.preventDefault();
  navigate(to, false, state);
}
