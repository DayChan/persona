// Actual-app browser fixtures; no real hub, account, registry or catalog is contacted.
// Build web first (npm --prefix web run build); this drives its production bundle through a loopback-only Vite preview.
// Usage: node web/e2e/extension-mirrors.mjs <absolute-output-directory>
import assert from "node:assert/strict";
import { createServer as httpServer } from "node:http";
import { mkdir } from "node:fs/promises";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";
import { preview } from "vite";
import { ownedCleanup } from "./extension-mirrors-cleanup.mjs";

const web = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const output = process.argv[2];
assert.ok(output && isAbsolute(output), "Pass an absolute output directory outside the repository");
assert.ok(!resolve(output).startsWith(resolve(web, "..") + "/"), "Screenshots must stay outside the repository");
let browser;
let vite;
let mock;
let timedOut = false;
let verdict = "";
const cleanup = ownedCleanup(() => ({ browser, vite, mock }));
const deadline = setTimeout(() => {
  timedOut = true;
  console.error("WEB_EXTENSION_MIRRORS TIMEOUT");
  // Keep a bound even if a close rejects or never settles. These are all owned resources.
  const exit = setTimeout(() => process.exit(1), 5_000);
  void cleanup().then(() => { clearTimeout(exit); process.exit(1); });
}, 180_000);
const empty = { registry_mirror: "", catalog_mirror: "" };
const configured = { registry_mirror: "https://registry.example.com/persona", catalog_mirror: "https://catalog.example.com/persona-extensions.json" };
const settings = new Map([["assistant", { ...configured }], ["m-two", { ...empty }]]);
const calls = [];
let failRead = false;
let failWrite = false;
let heldWrite;
let writeStarted;
try {
  await mkdir(output, { recursive: true });
  mock = httpServer(async (req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    const json = (status, data) => { res.writeHead(status, { "Content-Type": "application/json" }); res.end(JSON.stringify(data)); };
    if (url.pathname === "/api/v1/managers") return json(200, { default: "assistant", managers: [
      { id: "assistant", name: "Hub manager", online: true, builtin: true },
      { id: "m-two", name: "Second manager", online: true },
    ] });
    if (url.pathname === "/api/v1/hub/update") return json(404, { error: "Synthetic older updater" });
    if (url.pathname === "/api/v1/extensions/-/market-settings") {
      const owner = url.searchParams.get("manager");
      if (!settings.has(owner)) return json(400, { error: "Missing fixture manager" });
      if (req.method === "PUT") {
        let body = "";
        for await (const chunk of req) body += chunk;
        const value = JSON.parse(body);
        calls.push({ method: "PUT", owner, value });
        if (failWrite) return json(500, { error: "Synthetic write failure" });
        if (heldWrite) {
          writeStarted();
          await heldWrite;
        }
        settings.set(owner, value);
      } else {
        calls.push({ method: "GET", owner });
        if (failRead) return json(503, { error: "Synthetic read failure" });
      }
      return json(200, settings.get(owner));
    }
    return json(200, { machines: [], sessions: [], agents: [], groups: [], items: [], interactions: [], todos: [], workflows: [], extensions: [], conversations: [], providers: [], channels: [], personas: [], count: 0, total: 0 });
  });
  await new Promise((done, reject) => { mock.once("error", reject); mock.listen(0, "127.0.0.1", done); });
  const mockURL = `http://127.0.0.1:${mock.address().port}`;
  process.env.PERSONA_HUB_URL = mockURL;
  // Vite treats port 0 as its default; reserve a free loopback port and require it rather than borrowing a shared one.
  const reservation = httpServer();
  await new Promise((done) => reservation.listen(0, "127.0.0.1", done));
  const port = reservation.address().port;
  await new Promise((done) => reservation.close(done));
  vite = await preview({ root: web, preview: { host: "127.0.0.1", port, strictPort: true, proxy: { "/api": { target: mockURL, changeOrigin: true, ws: true } } } });
  const appURL = `http://127.0.0.1:${vite.httpServer.address().port}`;
  browser = await chromium.launch({ channel: "chrome", headless: true, args: ["--no-sandbox"] });
  for (const width of [1440, 390]) for (const scheme of ["light", "dark"]) {
    settings.set("assistant", { ...configured });
    settings.set("m-two", { ...empty });
    const context = await browser.newContext({ viewport: { width, height: 1100 }, locale: "en-US", colorScheme: scheme });
    await context.addInitScript(({ scheme }) => {
      localStorage.setItem("persona.token", "synthetic-browser-token");
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", scheme);
    }, { scheme });
    // Network containment includes unexpected third-party requests, not just API fixture paths.
    await context.route("**/*", (route) => new URL(route.request().url()).origin === appURL ? route.continue() : route.abort());
    await context.routeWebSocket("**/api/v1/ws?*", (ws) => ws.onMessage((raw) => {
      const message = JSON.parse(String(raw));
      if (message.type === "client_hello") {
        ws.send(JSON.stringify({ type: "hello", server_version: "fixture", hub_name: "Fixture hub", features: ["extensions", "managers"], resumed: false }));
        ws.send(JSON.stringify({ type: "machines", machines: [] }));
        ws.send(JSON.stringify({ type: "sessions", sessions: [] }));
      } else if (message.type === "app_ping") ws.send(JSON.stringify({ type: "pong" }));
    }));
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`${appURL}/settings?tab=hub`);
    const registry = page.getByLabel("OCI registry mirror", { exact: true });
    const catalog = page.getByLabel("Official catalog mirror", { exact: true });
    await registry.waitFor();
    await page.evaluate((dark) => document.documentElement.classList.toggle("dark", dark), scheme === "dark");
    assert.equal(await page.locator("vite-error-overlay").count(), 0);
    assert.equal(await registry.inputValue(), configured.registry_mirror);
    assert.equal(await page.getByText("This hub is too old", { exact: false }).count(), 1);
    await registry.fill("https://registry.example.com/new-prefix");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.getByText("Mirror settings saved.", { exact: true }).waitFor();
    assert.equal(settings.get("assistant").registry_mirror, "https://registry.example.com/new-prefix");
    await page.getByRole("button", { name: "Clear mirrors", exact: true }).click();
    await page.waitForFunction(() => [...document.querySelectorAll("input")].every((input) => input.value === ""));
    assert.deepEqual(settings.get("assistant"), empty);
    await page.getByRole("combobox", { name: "Extension manager", exact: true }).selectOption("m-two");
    await registry.waitFor();
    await registry.fill(configured.registry_mirror);
    await catalog.fill(configured.catalog_mirror);
    failWrite = true;
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.getByRole("alert").filter({ hasText: "Couldn't save mirror settings" }).waitFor();
    assert.equal(await page.getByText("Mirror settings saved.", { exact: true }).count(), 0);
    failWrite = false;
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.getByText("Mirror settings saved.", { exact: true }).waitFor();
    assert.deepEqual(settings.get("m-two"), configured);
    // Real category navigation must retain the configuration target, including the phone's list.
    const categories = page.getByRole("navigation", { name: "Settings categories", exact: true });
    if (width === 390) {
      await page.getByRole("button", { name: "Back to Settings", exact: true }).click();
      await categories.getByRole("button", { name: /^Devices/ }).click();
      await page.getByRole("button", { name: "Back to Settings", exact: true }).click();
      await categories.getByRole("button", { name: /^hub/ }).click();
    } else {
      await categories.getByRole("button", { name: "Devices", exact: true }).click();
      await categories.getByRole("button", { name: "hub", exact: true }).click();
    }
    await registry.waitFor();
    assert.equal(new URL(page.url()).searchParams.get("manager"), "m-two");
    assert.equal(await page.getByRole("combobox", { name: "Extension manager", exact: true }).inputValue(), "m-two");
    assert.equal(await registry.inputValue(), configured.registry_mirror);
    assert.equal(await page.getByRole("button", { name: "Clear mirrors", exact: true }).isEnabled(), true);
    const fields = await page.locator("input[type=url]").evaluateAll((inputs) => inputs.map((input) => {
      const field = input.getBoundingClientRect();
      const labelElement = input.parentElement.querySelector("span");
      const label = labelElement.getBoundingClientRect();
      const row = input.parentElement.parentElement.getBoundingClientRect();
      return { stacked: field.top >= label.bottom, fits: field.left >= row.left && field.right <= row.right, labelVisible: label.width > 0 && labelElement.scrollWidth <= labelElement.clientWidth };
    }));
    assert.ok(fields.every((f) => f.stacked && f.fits && f.labelVisible), "Field labels stay above inputs without clipped horizontal overflow");
    // Category navigation remounts an animated page; capture its final pixels, not a translucent first frame.
    await page.evaluate(async () => {
      await Promise.all(document.getAnimations().filter((a) => Number.isFinite(a.effect?.getComputedTiming().endTime)).map((a) => a.finished.catch(() => {})));
    });
    await page.screenshot({ path: join(output, `mirrors-${width}-${scheme}.png`), fullPage: true });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "No horizontal page overflow");
    const buttons = await page.getByRole("button", { name: /^(Save|Clear mirrors)$/ }).evaluateAll((elements) => elements.map((el) => ({ width: el.clientWidth, scroll: el.scrollWidth, whitespace: getComputedStyle(el).whiteSpace })));
    assert.ok(buttons.every((b) => b.scroll <= b.width && b.whitespace === "nowrap"), "Button text stays on one line");
    failRead = true;
    await page.getByRole("combobox", { name: "Extension manager", exact: true }).selectOption("assistant");
    await page.getByRole("alert").filter({ hasText: "Couldn't read mirror settings" }).waitFor();
    assert.equal(await registry.count(), 0, "Failed reads cannot overwrite unknown settings");
    failRead = false;
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await registry.waitFor();
    assert.equal(await registry.inputValue(), "");
    // A slow PUT stays uncommitted through A/B/A. A new GET must wait for it,
    // then a catalog-only save must preserve the registry mirror that just committed.
    const aReads = () => calls.filter((c) => c.method === "GET" && c.owner === "assistant").length;
    const before = aReads();
    let releaseWrite;
    heldWrite = new Promise((done) => { releaseWrite = done; });
    const started = new Promise((done) => { writeStarted = done; });
    await registry.fill(configured.registry_mirror);
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await started;
    await page.getByRole("combobox", { name: "Extension manager", exact: true }).selectOption("m-two");
    await registry.waitFor();
    await page.getByRole("combobox", { name: "Extension manager", exact: true }).selectOption("assistant");
    await registry.waitFor({ state: "detached" });
    assert.equal(aReads(), before, "Returning A must not read pre-commit values");
    assert.deepEqual(settings.get("assistant"), empty, "The fixture PUT is still uncommitted");
    releaseWrite();
    heldWrite = undefined;
    await registry.waitFor();
    assert.equal(await registry.inputValue(), configured.registry_mirror);
    assert.equal(await catalog.inputValue(), "");
    await catalog.fill(configured.catalog_mirror);
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.getByText("Mirror settings saved.", { exact: true }).waitFor();
    assert.deepEqual(settings.get("assistant"), configured, "Catalog-only edit must not overwrite the retired registry write");
    assert.deepEqual(errors, []);
    await context.close();
  }
  assert.ok(calls.some((c) => c.method === "PUT" && c.owner === "m-two"));
  verdict = `WEB_EXTENSION_MIRRORS PASS mock=${mockURL} app=${appURL} screenshots=${output}`;
} catch (error) {
  verdict = `WEB_EXTENSION_MIRRORS FAIL ${String(error.message ?? error).replace(/\s+/g, " ")}`;
  process.exitCode = 1;
} finally {
  const errors = await cleanup();
  if (errors.length) {
    process.exitCode = 1;
    verdict = `WEB_EXTENSION_MIRRORS FAIL cleanup ${errors.map((e) => String(e.message ?? e).replace(/\s+/g, " ")).join("; ")}`;
  } else {
    // A failed close cannot prove release; retain the deadline until every resource is closed.
    clearTimeout(deadline);
  }
  if (!timedOut) (process.exitCode ? console.error : console.log)(verdict);
}
