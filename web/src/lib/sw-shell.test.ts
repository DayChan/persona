import { afterEach, describe, expect, it, vi } from "vitest";
import { isShell, mustFollow, shellResponse, type ShellDeps } from "./sw-shell";

const html = (body = "<html>new</html>", status = 200) => new Response(body, { status, headers: { "Content-Type": "text/html; charset=utf-8" } });
// What a navigation fetch (redirect: "manual") returns for a redirect: status 0, type "opaqueredirect"; not constructible.
const opaqueRedirect = () => Object.defineProperty(new Response(null, { status: 200 }), "type", { value: "opaqueredirect" }) as Response;
const opaque = (r: Response) => Object.defineProperties(r, { ok: { value: false }, status: { value: 0 } }) as Response;
// A redirect the fetch followed: the login page, 200, `redirected` and its final URL.
const followed = (url: string) => Object.defineProperties(html("<html>login</html>"), { redirected: { value: true }, url: { value: url } }) as Response;

function run(net: () => Promise<Response>, cached: Response | undefined, timeoutMs = 3500) {
  const kept: Response[] = [];
  const waits: Promise<unknown>[] = [];
  const deps: ShellDeps = {
    fetch: net,
    cached: async () => cached,
    keep: async (r) => kept.push(r),
    waitUntil: (p) => waits.push(p),
    timeoutMs,
  };
  return { res: shellResponse(deps), kept, settle: () => Promise.all(waits) };
}

afterEach(() => vi.useRealTimers());

describe("service worker page loads", () => {
  it("serves the hub's fresh shell and keeps it", async () => {
    const { res, kept, settle } = run(async () => html(), html("<html>old</html>"));
    expect(await (await res).text()).toBe("<html>new</html>");
    await settle();
    expect(kept).toHaveLength(1);
    expect(await kept[0].text()).toBe("<html>new</html>");
  });

  it("hands a sign-in redirect to the browser instead of the cached shell", async () => {
    const redirect = opaque(opaqueRedirect());
    const { res, kept, settle } = run(async () => redirect, html("<html>old</html>"));
    expect(await res).toBe(redirect);
    await settle();
    expect(kept).toHaveLength(0);
  });

  it("hands a plain 3xx, 401 and 403 to the browser", async () => {
    for (const status of [302, 303, 307, 401, 403]) {
      const r = new Response(null, { status, headers: status < 400 ? { Location: "https://login.example.com/" } : {} });
      const { res, kept, settle } = run(async () => r, html("<html>old</html>"));
      expect((await res).status).toBe(status);
      await settle();
      expect(kept).toHaveLength(0);
    }
  });

  it("sends the browser where a followed redirect ended, and never keeps the login page as the shell", async () => {
    const { res, kept, settle } = run(async () => followed("https://login.example.com/?rd=x"), html("<html>old</html>"));
    const r = await res;
    expect(r.status).toBe(302);
    expect(r.headers.get("Location")).toBe("https://login.example.com/?rd=x");
    await settle();
    expect(kept).toHaveLength(0);
  });

  it("follows a redirect or 401 even without a cached shell", async () => {
    const redirect = opaque(opaqueRedirect());
    expect(await run(async () => redirect, undefined).res).toBe(redirect);
    expect((await run(async () => new Response(null, { status: 401 }), undefined).res).status).toBe(401);
  });

  it("starts from the cached shell offline", async () => {
    const saved = html("<html>old</html>");
    const { res, kept, settle } = run(() => Promise.reject(new TypeError("Failed to fetch")), saved);
    expect(await res).toBe(saved);
    await settle();
    expect(kept).toHaveLength(0);
  });

  it("starts from the cached shell when the hub fails or is slow", async () => {
    const saved = html("<html>old</html>");
    for (const status of [500, 502, 503, 404]) expect(await run(async () => html("err", status), saved).res).toBe(saved);

    vi.useFakeTimers();
    const slow = run(() => new Promise<Response>(() => {}), saved);
    await vi.advanceTimersByTimeAsync(3500);
    expect(await slow.res).toBe(saved);
  });

  it("without a cached shell offline, the error reaches the browser", async () => {
    await expect(run(() => Promise.reject(new TypeError("Failed to fetch")), undefined).res).rejects.toThrow("Failed to fetch");
  });

  it("decides what is a redirect or refusal and what is a shell", () => {
    expect(mustFollow(opaque(opaqueRedirect()))).toBe(true);
    expect(mustFollow(followed("https://login.example.com/"))).toBe(true);
    expect(mustFollow(new Response(null, { status: 401 }))).toBe(true);
    expect(mustFollow(new Response(null, { status: 403 }))).toBe(true);
    expect(mustFollow(new Response(null, { status: 500 }))).toBe(false);
    expect(mustFollow(html())).toBe(false);
    expect(isShell(html())).toBe(true);
    expect(isShell(followed("https://login.example.com/"))).toBe(false);
    expect(isShell(new Response("{}", { headers: { "Content-Type": "application/json" } }))).toBe(false);
  });
});
