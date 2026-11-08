// Actual production app, synthetic loopback hub; no account, registry or real settings are contacted.
// Build web first, then: node web/e2e/extension-identity.mjs <absolute-output-directory>
import assert from "node:assert/strict";
import { createServer as httpServer } from "node:http";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";
import { preview } from "vite";
import { ownedCleanup } from "./extension-mirrors-cleanup.mjs";

const web = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const output = process.argv[2];
assert.ok(output && isAbsolute(output) && !resolve(output).startsWith(resolve(web, "..") + "/"));
const oldID = "claude-bypass-allow", newID = "agent-bypass-allow";
const market = { id: "fixture-market", name: "Synthetic market", spec: "https://example.com/market/", has_token: false, extensions: 1 };
const item = { market: market.id, market_name: market.name, id: newID, name: "Bypass auto-allow", version: "0.2.24",
  installed: "0.2.23", installed_source: "installed", from_this_market: true, upgradable: true,
  replaces: oldID, rename_from: oldID, permissions: [] };
const consent = { preview_id: "identity-preview", manifest: { id: newID, name: item.name, version: item.version },
  upgrade: true, rename_from: oldID, from: item.installed, permissions: [], permissions_digest: "a".repeat(64),
  warnings: [`This update changes the extension ID from ${oldID} to ${newID}. Explicit confirmation is required; settings and data stay with the same installation.`],
  expires_at: new Date(Date.now() + 600_000).toISOString() };
let browser, vite, mock, currentPage, timedOut = false, verdict = "FAIL";
const cleanup = ownedCleanup(() => ({ browser, vite, mock }));
const calls = [], screenshots = [];
const deadline = setTimeout(() => {
  timedOut = true;
  console.error("WEB_EXTENSION_IDENTITY TIMEOUT");
  const force = setTimeout(() => process.exit(1), 5_000);
  void cleanup().then(() => { clearTimeout(force); process.exit(1); });
}, 180_000);
try {
  await mkdir(output, { recursive: true });
  mock = httpServer(async (req, res) => {
    try {
      const path = new URL(req.url, "http://127.0.0.1").pathname;
      const json = (data, status = 200) => { res.writeHead(status, { "Content-Type": "application/json" }); res.end(JSON.stringify(data)); };
      if (path === "/api/v1/admin/state") return json({ password_set: true, logged_in: true, csrf: "synthetic-csrf", hub_name: "Fixture hub" });
      if (path === "/api/v1/admin/overview") return json({ hub: { name: "Fixture hub", can_restart: false, hub_id: "fixture", started_at: "2026-11-08T00:00:00Z" }, restart: { needed: false }, tls: null });
      if (path === "/api/v1/managers") return json({ default: "assistant", managers: [{ id: "assistant", name: "Hub manager", online: true, builtin: true }] });
      if (path.endsWith("/catalog/preview")) {
        let raw = ""; for await (const chunk of req) raw += chunk;
        const body = JSON.parse(raw); assert.equal(body.extension, newID);
        calls.push({ kind: "preview", path, body }); return json(consent);
      }
      if (path.endsWith("/catalog")) return json({ markets: [market], extensions: [item] });
      if (path.endsWith("/markets")) return json({ markets: [market] });
      if (path.endsWith("/upgrade") && req.method === "POST") {
        let raw = ""; for await (const chunk of req) raw += chunk;
        const body = JSON.parse(raw);
        assert.ok(path.includes("/" + oldID + "/"), "The owner must upgrade the actual installed predecessor");
        assert.equal(body.preview_id, consent.preview_id); assert.equal(body.permissions_digest, consent.permissions_digest);
        calls.push({ kind: "upgrade", path, body }); return json({ id: newID, name: item.name, version: item.version, source: "installed", enabled: true, status: "stopped" });
      }
      if (path.endsWith("/install") && req.method === "POST") return json({ error: "Rename is not a fresh install" }, 409);
      if (path.endsWith("/extensions")) return json({ extensions: [], can_install: true, markets: true });
      return json({ machines: [], sessions: [], agents: [], groups: [], items: [], interactions: [], todos: [], workflows: [],
        extensions: [], conversations: [], providers: [], channels: [], personas: [], count: 0, total: 0 });
    } catch (error) {
      res.writeHead(500, { "Content-Type": "application/json" }); res.end(JSON.stringify({ error: String(error) }));
    }
  });
  await new Promise((done, reject) => { mock.once("error", reject); mock.listen(0, "127.0.0.1", done); });
  const mockURL = `http://127.0.0.1:${mock.address().port}`;
  const reserve = httpServer();
  await new Promise((done) => reserve.listen(0, "127.0.0.1", done));
  const port = reserve.address().port;
  await new Promise((done) => reserve.close(done));
  vite = await preview({ root: web, plugins: [{ name: "identity-fixture-admin-routes", configurePreviewServer(server) {
    server.middlewares.use((req, _res, next) => {
      if (req.url?.startsWith("/admin/") && !req.url.split("?")[0].split("/").pop().includes(".")) req.url = "/admin/index.html";
      next();
    });
  } }], preview: { host: "127.0.0.1", port, strictPort: true, proxy: { "/api": { target: mockURL, changeOrigin: true, ws: true } } } });
  const appURL = `http://127.0.0.1:${vite.httpServer.address().port}`;
  browser = await chromium.launch({ channel: "chrome", headless: true, args: ["--no-sandbox"] });
  for (const surface of ["settings", "admin"]) for (const width of [1440, 390]) for (const scheme of ["light", "dark"]) {
    const context = await browser.newContext({ viewport: { width, height: 1100 }, locale: "en-US", colorScheme: scheme });
    await context.addInitScript(({ scheme }) => {
      localStorage.setItem("persona.token", "synthetic-browser-token");
      localStorage.setItem("persona.language", "en"); localStorage.setItem("persona.theme", scheme);
    }, { scheme });
    await context.route("**/*", (route) => new URL(route.request().url()).origin === appURL ? route.continue() : route.abort());
    await context.routeWebSocket("**/api/v1/ws?*", (ws) => ws.onMessage((raw) => {
      const message = JSON.parse(String(raw));
      if (message.type === "client_hello") {
        ws.send(JSON.stringify({ type: "hello", server_version: "fixture", hub_name: "Fixture hub", features: ["extensions", "managers"], resumed: false }));
        ws.send(JSON.stringify({ type: "machines", machines: [] })); ws.send(JSON.stringify({ type: "sessions", sessions: [] }));
      } else if (message.type === "app_ping") ws.send(JSON.stringify({ type: "pong" }));
    }));
    const page = await context.newPage(), errors = [];
    currentPage = page;
    page.on("pageerror", (error) => errors.push(error.message));
    const before = calls.filter((call) => call.kind === "upgrade").length;
    await page.goto(surface === "admin" ? `${appURL}/admin/extensions` : `${appURL}/settings?tab=extensions&extension=-market/${market.id}/${oldID}`);
    await page.evaluate((dark) => document.documentElement.classList.toggle("dark", dark), scheme === "dark");
    await page.getByRole("button", { name: surface === "admin" ? "Upgrade" : "Update to 0.2.24", exact: true }).last().click();
    const dialog = page.getByRole("dialog");
    await dialog.getByText("Extension identity change", { exact: true }).waitFor();
    await page.evaluate(async () => {
      await document.fonts.ready;
      await Promise.all(document.getAnimations().filter((animation) => Number.isFinite(animation.effect?.getComputedTiming().iterations))
        .map((animation) => animation.finished.catch(() => {})));
    });
    assert.ok((await dialog.textContent()).includes(oldID)); assert.ok((await dialog.textContent()).includes(newID));
    assert.equal(calls.filter((call) => call.kind === "upgrade").length, before, "Preview must not commit the rename");
    const button = dialog.getByRole("button", { name: "Confirm rename", exact: true });
    assert.ok(await button.isEnabled());
    assert.equal(await button.evaluate((node) => node.scrollWidth > node.clientWidth + 1), false, "Confirmation text must fit on one line");
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false);
    const filename = `${surface}-${width}-${scheme}.png`; await page.screenshot({ path: join(output, filename), fullPage: true }); screenshots.push(filename);
    await button.click(); await dialog.waitFor({ state: "hidden" });
    assert.equal(calls.filter((call) => call.kind === "upgrade").length, before + 1);
    assert.deepEqual(errors, []); await context.close();
  }
  verdict = "PASS";
} catch (error) {
  console.error(error);
  if (currentPage && !currentPage.isClosed()) {
    await currentPage.screenshot({ path: join(output, "failure.png"), fullPage: true });
    await writeFile(join(output, "failure.txt"), await currentPage.locator("body").innerText());
  }
  process.exitCode = 1;
} finally {
  const errors = await cleanup();
  if (errors.length) { console.error(errors); verdict = "FAIL"; process.exitCode = 1; }
  if (timedOut) verdict = "TIMEOUT";
  await writeFile(join(output, "result.json"), JSON.stringify({ verdict, screenshots, preview_before_commit: true, calls }, null, 2));
  clearTimeout(deadline); console.log(`WEB_EXTENSION_IDENTITY ${verdict}`);
}
