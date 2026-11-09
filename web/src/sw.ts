// The web client's service worker (docs/web/web.md "Install as an app"): built on its own to /sw.js (vite.config.ts) and
// registered by lib/pwa.ts. It makes the installed app start fast and still open when the hub can't be reached: the
// shell (index.html) comes from the hub when it answers in time, else from the cache (but a sign-in redirect or 401/403
// reaches the browser); hashed assets from the cache.
// The API is never touched (lib/sw-routes.ts).
import { clickNotification, type ClickClients } from "./lib/sw-click";
import { ASSET_CACHE, ASSET_LIMIT, assetsIn, SHELL_CACHE, SHELL_TIMEOUT_MS, strategy } from "./lib/sw-routes";
import { isShell, shellResponse } from "./lib/sw-shell";

// The tsconfig has the DOM's types, not the worker's: the few this file uses.
interface ExtendableEvent extends Event {
  waitUntil(p: Promise<unknown>): void;
}
interface NotificationEvent extends ExtendableEvent {
  notification: Notification;
  /** The button clicked; "" for the notification itself. */
  action: string;
}
interface FetchEvent extends ExtendableEvent {
  request: Request;
  respondWith(r: Promise<Response>): void;
}
const worker = self as unknown as {
  location: Location;
  skipWaiting(): Promise<void>;
  clients: ClickClients & { claim(): Promise<void> };
  addEventListener(type: "install" | "activate", f: (e: ExtendableEvent) => void): void;
  addEventListener(type: "notificationclick", f: (e: NotificationEvent) => void): void;
  addEventListener(type: "fetch", f: (e: FetchEvent) => void): void;
};

// Keeps a fresh shell and the assets it loads up front.
async function keepShell(res: Response) {
  const html = await res.clone().text();
  await (await caches.open(SHELL_CACHE)).put("/", res);
  const assets = await caches.open(ASSET_CACHE);
  await Promise.all(
    assetsIn(html).map(async (u) => {
      if (await assets.match(u)) return;
      const r = await fetch(u);
      if (r.ok) await assets.put(u, r);
    }),
  );
  await trim(assets);
}

// Oldest first: Cache.keys() is in insertion order.
async function trim(cache: Cache) {
  const keys = await cache.keys();
  await Promise.all(keys.slice(0, Math.max(0, keys.length - ASSET_LIMIT)).map((k) => cache.delete(k)));
}

worker.addEventListener("install", (e) => {
  e.waitUntil(
    fetch("/", { cache: "no-cache" })
      .then((r) => (isShell(r) ? keepShell(r) : undefined))
      .catch(() => {})
      .then(() => worker.skipWaiting()),
  );
});

worker.addEventListener("activate", (e) => {
  const mine = [SHELL_CACHE, ASSET_CACHE];
  e.waitUntil(
    caches
      .keys()
      .then((names) => Promise.all(names.filter((n) => n.startsWith("persona-") && !mine.includes(n)).map((n) => caches.delete(n))))
      .then(() => worker.clients.claim()),
  );
});

worker.addEventListener("fetch", (e) => {
  const req = e.request;
  switch (strategy(new URL(req.url), worker.location.origin, req.method, req.mode)) {
    case "shell":
      return e.respondWith(shell(e));
    case "asset":
      return e.respondWith(asset(req));
    case "static":
      return e.respondWith(fresh(req));
  }
});

// The hub's shell when it answers within SHELL_TIMEOUT_MS (and it's kept for next time); the cached one when it's slow,
// unreachable or fails; a redirect or 401/403 (an expired sign-in) goes to the browser so it follows it
// (lib/sw-shell.ts). Without a cached shell, whatever the hub does.
function shell(e: FetchEvent): Promise<Response> {
  return shellResponse({
    fetch: () => fetch(e.request),
    cached: () => caches.match("/", { cacheName: SHELL_CACHE }),
    keep: keepShell,
    waitUntil: (p) => e.waitUntil(p),
    timeoutMs: SHELL_TIMEOUT_MS,
  });
}

async function asset(req: Request): Promise<Response> {
  const cache = await caches.open(ASSET_CACHE);
  // A hashed file is the same whoever asks: keepShell kept it with a plain fetch, the page asks for its module script
  // with an Origin, and a server answering `Vary: Origin` (vite preview, a CORS proxy) would make that a miss.
  const hit = await cache.match(req, { ignoreVary: true });
  if (hit) return hit;
  const r = await fetch(req);
  if (r.ok) await cache.put(req, r.clone()).then(() => trim(cache)).catch(() => {});
  return r;
}

// The icons and the manifest: the hub's, kept with the shell; the kept copy when the hub can't be reached.
async function fresh(req: Request): Promise<Response> {
  const cache = await caches.open(SHELL_CACHE);
  try {
    const r = await fetch(req);
    if (r.ok) await cache.put(req, r.clone()).catch(() => {});
    return r;
  } catch {
    return (await cache.match(req)) ?? Response.error();
  }
}

// A notification the page had the worker show (lib/notify.ts postNotification: where the page can't make one itself, or
// a permission card's, with Allow / Deny): clicking it brings an open window of the app forward on the agent's
// conversation, or opens one; a button is answered by an open window without bringing it forward (lib/sw-click.ts).
worker.addEventListener("notificationclick", (e) => {
  e.notification.close();
  e.waitUntil(clickNotification(worker.clients, e.notification.data as { url?: string; ix?: string } | null, e.action));
});
