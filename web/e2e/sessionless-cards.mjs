// Actual-app acceptance for requests made by personad ask outside a session.
// Usage: node web/e2e/sessionless-cards.mjs <mockWebOrigin> <artifactDir>
// The caller owns the mock hub and web server. All writes stay inside this fixture.
import assert from "node:assert/strict";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const [address, artifacts, ...extra] = process.argv.slice(2);
assert.ok(address && artifacts && !extra.length, "Supply the mock web origin and artifact directory");
const base = new URL(address);
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname), "Use a loopback mock server");
assert.ok(["http:", "https:"].includes(base.protocol) && !base.username && !base.password && !base.search && !base.hash && base.pathname === "/", "Use an origin without credentials or a route");
assert.equal((await (await fetch(new URL("/api/v1/health", base))).json()).version, "mock", "Only the existing mock hub is allowed");
const output = resolve(artifacts);
await mkdir(output, { recursive: true });
const result = { status: "running", checks: [], screenshots: [], runs: [] };
const machine = { id: "example-computer", name: "Example computer", online: true };
const card = (id, kind = "permission") => ({
  id, machine_id: machine.id, session_id: "", kind, tool_name: "personad_ask", agent: "script", agent_label: "Script",
  cwd: "/code/example", summary: "Publish the draft?", created_at: "2026-01-01T10:00:00Z", expires_at: "2099-01-01T10:00:00Z",
  ...(kind === "question" ? { questions: [{ question: "Choose the format", header: "Script", options: [{ label: "Plain text" }, { label: "Markdown" }], multi_select: false }] } : {}),
});
const browser = await chromium.launch({ channel: "chrome", headless: true });
const deadline = setTimeout(() => void browser.close(), 180_000);
try {
  for (const lang of ["en", "zh-Hans", "ja"]) for (const theme of ["light", "dark"]) {
    const labels = JSON.parse(await readFile(new URL(`../src/i18n/locales/${lang}/chat-interactions.json`, import.meta.url)));
    const name = `${lang}-${theme}`;
    const run = { name, writes: [], errors: [], blocked: [], mockHellos: 0 };
    result.runs.push(run);
    let cards = [card("script-permission"), card("script-question", "question")];
    const context = await browser.newContext({ viewport: { width: 1280, height: 860 }, colorScheme: theme, serviceWorkers: "block" });
    await context.addInitScript(({ lang, theme }) => {
      localStorage.setItem("persona.token", "fixture-token");
      localStorage.setItem("persona.language", lang);
      localStorage.setItem("persona.theme", theme);
    }, { lang, theme });
    await context.route("**/*", async (route) => {
      const req = route.request(), url = new URL(req.url());
      if (url.origin !== base.origin) { run.blocked.push(url.pathname); return route.abort(); }
      if (!["GET", "HEAD"].includes(req.method())) {
        run.writes.push({ method: req.method(), path: url.pathname, body: req.postDataJSON() });
        const id = url.pathname.match(/^\/api\/v1\/interactions\/([^/]+)\/resolve$/)?.[1];
        if (id) cards = cards.filter((ix) => ix.id !== id);
        return route.fulfill({ json: { ok: true } });
      }
      if (!url.pathname.startsWith("/api/")) return route.continue();
      const path = url.pathname.replace(/^\/api\/v1/, "");
      const data = {
        "/sessions": { sessions: [], total: 0, has_more: false, revision: "fixture" },
        "/machines": { machines: [machine] }, "/interactions": { interactions: cards },
        "/assistant": { machine_id: machine.id, conversations: [] }, "/todos": { todos: [] },
        "/mutes": { spaces: [], sessions: [], allowed: [] }, "/pins": { spaces: [], sessions: [], sections: [] },
        "/agents/sessions": { sessions: [] }, "/agents/prefs": {}, "/managers": { managers: [], default_id: "" },
        "/personas": { personas: [] }, "/assistant/personas": { personas: [] }, "/workspaces": { workspaces: [] }, "/extensions": { extensions: [] },
      };
      return route.fulfill({ json: data[path] ?? {} });
    });
    await context.routeWebSocket(/\/api\/v1\/ws(?:\?|$)/, (socket) => {
      const upstream = socket.connectToServer();
      upstream.onMessage((message) => {
        const event = JSON.parse(String(message));
        if (event.type !== "hello") return;
        assert.equal(event.server_version, "mock");
        run.mockHellos++;
        socket.send(JSON.stringify({ type: "hello", server_version: "mock", protocol: 1, features: [], hub_name: "Example hub" }));
        socket.send(JSON.stringify({ type: "machines", machines: [machine] }));
        socket.send(JSON.stringify({ type: "sessions", machine_id: machine.id, sessions: [] }));
      });
      socket.onMessage((message) => {
        const event = JSON.parse(String(message));
        if (event.type === "app_ping") socket.send(JSON.stringify({ type: "pong", id: event.id }));
        // No client action is forwarded to the mock hub.
      });
    });
    const page = await context.newPage();
    page.setDefaultTimeout(10_000);
    page.on("pageerror", (error) => run.errors.push(error.message));
    page.on("console", (message) => { if (message.type() === "error") run.errors.push(message.text()); });
    const shot = async (state) => {
      await page.evaluate(() => document.fonts.ready);
      await page.evaluate(async () => {
        await new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)));
        await Promise.all(document.getAnimations().filter((animation) => Number.isFinite(animation.effect?.getComputedTiming().endTime)).map((animation) => animation.finished.catch(() => {})));
      });
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "No horizontal overflow");
      const file = `${name}-${state}.png`;
      await page.screenshot({ path: join(output, file) });
      result.screenshots.push(file);
    };
    await page.goto(new URL("/overview", base).href);
    const main = page.locator("main");
    await main.locator('a[href="/requests/script-permission"]').waitFor();
    await page.waitForFunction((dark) => document.documentElement.classList.contains("dark") === dark, theme === "dark");
    await shot("overview");
    await main.locator('a[href="/requests/script-permission"]').click();
    await page.waitForURL("**/requests/script-permission");
    const permission = main.locator('[data-card="script-permission"]');
    await permission.getByRole("button", { name: labels.allow, exact: true }).waitFor();
    await shot("permission");
    await permission.getByRole("button", { name: labels.allow, exact: true }).click();
    await main.locator('[data-answered="script-permission"]').waitFor();
    assert.deepEqual(run.writes.filter((w) => w.path.endsWith("/resolve")), [{ method: "POST", path: "/api/v1/interactions/script-permission/resolve", body: { allow: true } }]);
    await shot("answered");
    await page.goto(new URL("/inbox", base).href);
    await main.locator('a[href="/requests/script-question"]').waitFor();
    await main.locator('a[href="/requests/script-question"]').click();
    await page.waitForURL("**/requests/script-question");
    const question = main.locator('[data-card="script-question"]');
    await question.getByText("Choose the format", { exact: true }).waitFor();
    await shot("question");
    await question.getByRole("button", { name: "Markdown", exact: true }).click();
    await question.getByRole("button", { name: labels.send_answer, exact: true }).click();
    await main.locator('[data-answered="script-question"]').waitFor();
    assert.deepEqual(run.writes.filter((w) => w.path.endsWith("/resolve"))[1], { method: "POST", path: "/api/v1/interactions/script-question/resolve", body: { answers: { "Choose the format": "Markdown" } } });
    // Direct notification/deep links must open the same card at phone width too.
    cards = [card("notification-request")];
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(new URL("/requests/notification-request", base).href);
    await main.locator('[data-card="notification-request"]').waitFor();
    await shot("phone-deep-link");
    await page.goto(new URL("/requests/no-longer-pending", base).href);
    await main.getByRole("status").waitFor();
    assert.deepEqual(run.errors, []);
    assert.deepEqual(run.blocked, []);
    assert.ok(run.mockHellos > 0, "The real app completed the mock WebSocket handshake");
    result.checks.push(`${name}: overview permission, inbox question, answer receipts, phone deep link, ended state`);
    console.log(`PASS ${result.checks.at(-1)}`);
    await context.close();
  }
  result.status = "pass";
} catch (error) {
  result.status = "fail";
  result.error = error.stack;
  throw error;
} finally {
  clearTimeout(deadline);
  await browser.close();
  await writeFile(join(output, "result.json"), JSON.stringify(result, null, 2) + "\n");
}
