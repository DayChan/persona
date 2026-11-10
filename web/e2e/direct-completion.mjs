// Real Assistant app with synthetic HTTP/WS only; never connects to a user's hub or conversation.
// node web/e2e/direct-completion.mjs <web URL> --shots <directory> [--chromium <executable>]
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => { const i = args.indexOf(name); return i < 0 ? undefined : args[i + 1]; };
const base = args[0]?.replace(/\/$/, "");
const shots = flag("--shots");
if (!base || !shots) {
  console.error("usage: node web/e2e/direct-completion.mjs <web URL> --shots <directory> [--chromium <executable>]");
  process.exit(2);
}
const origin = new URL(base);
assert(["127.0.0.1", "localhost", "[::1]"].includes(origin.hostname) && origin.protocol === "http:", "only an owned loopback HTTP web server is allowed");
assert(!origin.username && !origin.password && !origin.search && !origin.hash, "the web URL must not contain credentials");
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => { console.error("TIMEOUT direct-completion browser verification (180s)"); process.exit(124); }, 180_000);
const AT = "2026-11-10T08:00:00Z";
const sid = "direct-completion-demo";
const words = {
  "zh-Hans": ["打开", "直接完成", "按推荐来"],
  en: ["Open", "Finish now", "Take recommendation"],
  ja: ["開く", "ここで完了", "おすすめで進める"],
};

async function run(browser, size, theme, language, failFirst = false) {
  const context = await browser.newContext({ viewport: size === "wide" ? { width: 1280, height: 900 } : { width: 390, height: 844 }, colorScheme: theme, reducedMotion: "reduce" });
  await context.addInitScript(({ theme, language }) => {
    localStorage.setItem("persona.token", "synthetic-demo-token");
    localStorage.setItem("persona.language", language);
    localStorage.setItem("persona.theme", theme);
  }, { theme, language });
  const session = { machine_id: "demo", session_id: sid, title: "Example conversation", assistant: true, state: "idle", status: "idle", updated_at: AT };
  const agent = { machine_id: "demo", session_id: "demo-worker", title: "Example task Agent", state: "idle", status: "idle", updated_at: AT, state_since: AT };
  let todo = { id: "demo-decision", title: "Example rollout decision", origin: sid, created_at: AT, updated_at: AT, agents: [{ machine_id: "demo", session_id: agent.session_id, linked_at: AT }], supervision: { supervisor: sid, state: "needs_user" }, ask: { outcome: "decide", verdict: "A decision is needed: stop here or schedule another rollout.", recommend: "Schedule another demo rollout.", asked_at: AT } };
  const writes = [];
  await context.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace(/^\/api\/v1/, "");
    if (path === `/assistant/conversations/${sid}/today`) {
      await route.fulfill({ contentType: "application/json", body: "{}" });
      return;
    }
    if (request.method() !== "GET") {
      writes.push({ path, method: request.method(), body: request.postDataJSON() });
      assert.equal(path, "/todos/demo-decision", "no Agent send, resume or recommendation acceptance");
      assert.equal(request.method(), "PATCH");
      assert.deepEqual(request.postDataJSON(), { done: true });
      if (failFirst && writes.length === 1) {
        await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "Synthetic completion failure" }) });
        return;
      }
      todo = { ...todo, done_at: AT, ask: undefined };
      await route.fulfill({ contentType: "application/json", body: JSON.stringify({ todo }) });
      return;
    }
    let body = {};
    if (path === "/machines") body = { machines: [{ id: "demo", name: "Example computer", online: true, os: "linux" }] };
    else if (path === "/sessions") body = { sessions: [session, agent] };
    else if (path === "/interactions") body = { interactions: [] };
    else if (path === "/assistant") body = { machine_id: "demo", main: sid, conversations: [session], tasks: [], memory: [] };
    else if (path === "/todos") body = { todos: [todo] };
    else if (path === `/sessions/demo/${sid}`) body = { session };
    else if (path.endsWith("/feed")) body = { items: [] };
    else if (path.endsWith("/items")) body = { items: [], has_more: false };
    else if (path === "/extensions/ui") body = { slots: [] };
    else if (path === "/extensions") body = { extensions: [] };
    else if (path === "/managers") body = { managers: [] };
    await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
  });
  await context.routeWebSocket("**/api/v1/ws**", (ws) => {
    ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", hub_name: "Example hub", features: [] }));
    ws.onMessage((raw) => {
      const message = JSON.parse(String(raw));
      assert(!/send|resume|message/.test(String(message.type) + String(message.method || "")), "no Agent work over WebSocket");
      if (message.type === "app_ping") ws.send(JSON.stringify({ type: "pong" }));
    });
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(String(error)));
  page.setDefaultTimeout(12_000);
  try {
    await page.goto(`${base}/`);
    const pending = page.locator('[data-tray-entry="pending"]');
    await pending.click();
    const row = page.locator('[data-brief-row="accept"]');
    await row.waitFor();
    const buttons = row.getByRole("button");
    assert.deepEqual(await buttons.allTextContents(), words[language]);
    const finish = buttons.nth(1);
    const primary = buttons.nth(2);
    assert((await finish.getAttribute("class")).includes("shadow-btn"), "completion stays secondary");
    assert((await primary.getAttribute("class")).includes("bg-ink"), "recommendation keeps primary emphasis");
    const geometry = await buttons.evaluateAll((els) => els.map((el) => ({ height: el.clientHeight, textHeight: getComputedStyle(el).lineHeight, width: el.getBoundingClientRect().width })));
    assert(geometry.every((g) => g.height === geometry[0].height), "button text does not wrap");
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "no horizontal page overflow");
    assert.deepEqual(errors, []);
    await page.screenshot({ path: join(shots, `web-${size}-${theme}-${language}-before.png`) });
    await finish.click();
    if (failFirst) {
      await page.getByText(/Synthetic completion failure/).waitFor();
      assert.equal(await row.count(), 1, "failure leaves the decision in Pending");
      assert.equal(await finish.isEnabled(), true, "failure allows retry");
      await page.screenshot({ path: join(shots, `web-${size}-${theme}-${language}-failure.png`) });
      await finish.click();
    }
    await row.waitFor({ state: "detached" });
    assert(todo.done_at && !todo.ask, "todo finished and ask removed");
    assert.equal(writes.length, failFirst ? 2 : 1);
    await page.screenshot({ path: join(shots, `web-${size}-${theme}-${language}-after.png`) });
    await page.reload();
    await page.locator('[data-tray-entry="pending"]').click();
    assert.equal(await row.count(), 0, "completed decision stays gone after reload");
    assert.deepEqual(errors, []);
    console.log(`PASS ${size} ${theme} ${language}: done_at=${todo.done_at}, pending=0, PATCH=${writes.length}, agent_messages=0, resumes=0, accept=0`);
  } finally {
    await context.close();
  }
}

let browser;
try {
  browser = await chromium.launch({ executablePath: flag("--chromium") || "/usr/bin/google-chrome", headless: true });
  for (const theme of ["light", "dark"]) {
    await run(browser, "wide", theme, "zh-Hans", theme === "light");
    await run(browser, "narrow", theme, "zh-Hans");
  }
  for (const language of ["en", "ja"]) {
    await run(browser, "wide", "light", language);
    await run(browser, "narrow", "light", language);
  }
  console.log("PASS direct-completion browser verification");
} catch (error) {
  console.error("FAIL direct-completion browser verification", error);
  process.exitCode = 1;
} finally {
  if (browser) await browser.close();
  clearTimeout(watchdog);
}
