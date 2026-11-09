// How the service worker (src/sw.ts) answers a page load, kept apart so every branch can be tested without a worker.
// The hub's index.html when it answers in time (kept for next time); the cached shell when the hub is slow, unreachable
// or fails. But when the server deliberately answers the page load with a redirect or "not signed in" (an expired
// sign-in in front of the hub sending the browser to its login page), that answer goes to the browser as is: serving the
// cached shell there would swallow the redirect, and the shell's API calls would then all fail with 401.

/** True when a page load's response is the server sending the browser somewhere else or refusing it. A navigation
 * fetch uses redirect: "manual", so a redirect arrives as an "opaqueredirect" (status 0); `redirected` and a plain 3xx
 * cover a fetch that followed it or a server that answers without one. */
export function mustFollow(r: Response): boolean {
  return r.type === "opaqueredirect" || r.redirected || (r.status >= 300 && r.status < 400) || r.status === 401 || r.status === 403;
}

/** Only a fresh, successful, unredirected index.html becomes the cached shell (never a login page reached by a redirect). */
export function isShell(r: Response): boolean {
  return r.ok && !r.redirected && (r.headers.get("Content-Type") ?? "").startsWith("text/html");
}

/** What to hand the browser for a response mustFollow accepted. A followed redirect can't answer a navigation (its
 * redirect mode is "manual"), so the browser is sent to where it ended up instead. */
export function toBrowser(r: Response): Response {
  return r.redirected && r.url ? Response.redirect(r.url, 302) : r;
}

export interface ShellDeps {
  /** The page load itself (fetch(event.request)). */
  fetch: () => Promise<Response>;
  /** The cached shell, if any. */
  cached: () => Promise<Response | undefined>;
  /** Keeps a fresh shell (and its assets). */
  keep: (r: Response) => Promise<unknown>;
  waitUntil: (p: Promise<unknown>) => void;
  timeoutMs: number;
}

export async function shellResponse(d: ShellDeps): Promise<Response> {
  const net = d.fetch().then((r) => {
    if (isShell(r)) d.waitUntil(d.keep(r.clone()).catch(() => {}));
    return r;
  });
  d.waitUntil(net.catch(() => {}));
  const saved = await d.cached();
  if (!saved) return net.then(toBrowser);
  let timer: ReturnType<typeof setTimeout> | undefined;
  const late = new Promise<Response>((resolve) => (timer = setTimeout(() => resolve(saved), d.timeoutMs)));
  try {
    return await Promise.race([net.then((r) => (r.ok && !r.redirected ? r : mustFollow(r) ? toBrowser(r) : saved), () => saved), late]);
  } finally {
    clearTimeout(timer);
  }
}
