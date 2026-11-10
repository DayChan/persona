// Actual-app check: the main conversation's ask_user card above the composer on short and narrow windows.
// Usage: node web/e2e/ask-card-dock.mjs <web-origin> <artifactDir> [--chromium <executable>]
// Every hub request and the socket are answered in the browser with invented data; nothing reaches a hub.
// Checks, at each size: the card's answer row is never under the composer, the Pending entries stay in view, and the
// "Or write your own" field scrolls into view above the answer row once focused.
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const chromiumAt = args.includes("--chromium") ? args.splice(args.indexOf("--chromium"), 2)[1] : undefined;
const [address, artifacts, ...extra] = args;
assert.ok(address && artifacts && !extra.length, "Supply the web origin and artifact directory");
const base = new URL(address);
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname), "Use a loopback web server");
const output = resolve(artifacts);
await mkdir(output, { recursive: true });

const AT = "2026-01-01T10:00:00Z";
const MAIN = "example-main";
const MACHINE = { id: "example", name: "Computer A", online: true, os: "linux" };
const SESSION = { machine_id: MACHINE.id, session_id: MAIN, title: "Persona", state: "needs_you", status: "needs_you", cwd: "/example", updated_at: AT, started_by_persona: true, assistant: true, capabilities: { send: true, interrupt: true } };
const ITEMS = [
  { id: "u1", seq: 1, role: "user", kind: "text", text: "Move the example gateway to a new computer.", ts: AT },
  { id: "a1", seq: 2, role: "assistant", kind: "text", text: "The plan is ready; one thing is yours to decide.", ts: AT },
];
const ASK = {
  id: "example-ask", machine_id: MACHINE.id, session_id: MAIN, kind: "question", agent: "claude", tool_name: "ask_user", created_at: AT,
  questions: [{
    question: "The example gateway's migration plan is ready. Which computer should run it?",
    header: "Where",
    options: [
      { label: "Computer A (the hub's)", description: "Next to the hub: the simplest to run, but it takes this computer's memory and network." },
      { label: "Computer B (always on)", description: "Plenty of room for a long run; needs its own connection to the hub." },
      { label: "Computer C (the NAS)", description: "Always on and quiet, but slow out: the first sync takes hours." },
      { label: "Not yet, try it here first", description: "Check the settings and the data first, then pick the computer." },
    ],
  }],
};

const result = { status: "running", runs: [] };
const browser = await chromium.launch(chromiumAt ? { executablePath: chromiumAt, args: ["--no-sandbox"] } : { channel: "chrome" });
const deadline = setTimeout(() => { result.status = "timeout"; void browser.close(); }, 240_000);
try {
  for (const [width, height] of [[1280, 600], [1024, 520], [390, 664], [360, 560]]) for (const theme of ["light", "dark"]) {
    const name = `${width}x${height}-${theme}`;
    const touch = width < 500;
    const context = await browser.newContext({ viewport: { width, height }, locale: "en-US", colorScheme: theme, reducedMotion: "reduce", isMobile: touch, hasTouch: touch });
    await context.addInitScript(`localStorage.setItem('persona.token','example');localStorage.setItem('persona.language','en');localStorage.setItem('persona.theme','${theme}');`);
    await context.route(/^https?:\/\/(?!127\.0\.0\.1|localhost|\[::1\])/, (route) => route.abort());
    await context.route("**/api/v1/**", (route) => {
      if (route.request().method() !== "GET") return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
      const path = new URL(route.request().url()).pathname.replace("/api/v1", "");
      const data = path === "/machines" ? { machines: [MACHINE] }
        : path === "/sessions" ? { sessions: [SESSION] }
        : path === "/interactions" ? { interactions: [ASK] }
        : path === "/assistant" ? { machine_id: MACHINE.id, main: MAIN, conversations: [SESSION], tasks: [], memory: [] }
        : path.endsWith("/feed") ? { items: [], plans: {}, task_cards: [] }
        : path === "/todos" ? { todos: [] }
        : path.endsWith("/items") ? { items: ITEMS, has_more: false }
        : /^\/sessions\/[^/]+\/[^/]+$/.test(path) ? { session: SESSION }
        : {};
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(data) });
    });
    await context.routeWebSocket("**/api/v1/ws**", (ws) => {
      ws.send(JSON.stringify({ type: "hello", server_version: "example", hub_name: "Example hub", features: [] }));
      ws.onMessage((m) => { if (String(m).includes("app_ping")) ws.send(JSON.stringify({ type: "pong" })); });
    });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(new URL("/", base).href);
    await page.locator("[data-assistant-actionbar]").waitFor({ timeout: 15_000 });
    if (!(await page.locator(`[data-card="${ASK.id}"] input`).count())) await page.locator('[data-tray-entry="pending"]').click();
    const custom = page.locator(`[data-card="${ASK.id}"] input`);
    await custom.waitFor({ timeout: 10_000 });
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
    // which of an element's corners and center are its own on screen (not under the composer, another card part or off it)
    const seen = () => page.evaluate((id) => {
      const card = document.querySelector(`[data-card="${id}"]`);
      // a capsule's (fully round) corners are not its own: its center is enough
      const own = (el, corners = true) => {
        const r = el.getBoundingClientRect();
        const center = [r.left + r.width / 2, r.top + r.height / 2];
        return (corners ? [[r.left + 4, r.top + 3], center, [r.right - 4, r.bottom - 3]] : [center])
          .every(([x, y]) => { const top = document.elementFromPoint(x, y); return Boolean(top && (top === el || el.contains(top))); });
      };
      const send = [...card.querySelectorAll("[data-card-actions] button")].at(-1);
      const entries = [...document.querySelectorAll("[data-tray-entry]")];
      return { send: own(send), entries: entries.length === 3 && entries.every((e) => own(e, false)), custom: own(card.querySelector("input")) };
    }, ASK.id);
    const first = await seen();
    await page.screenshot({ path: `${output}/${name}-initial.png` });
    await custom.click();
    await page.keyboard.type("Computer D");
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
    const typed = await seen();
    await page.screenshot({ path: `${output}/${name}-typed.png` });
    const run = { name, first, typed, errors };
    result.runs.push(run);
    console.log(JSON.stringify(run));
    assert.ok(first.send, `${name}: the answer row is under something before scrolling`);
    assert.ok(first.entries, `${name}: the Pending entries are not in view`);
    assert.ok(typed.custom && typed.send, `${name}: the own-answer field or the send button is covered after focusing it`);
    assert.ok(await page.locator(`[data-card="${ASK.id}"] [data-card-actions] button`).last().isEnabled(), `${name}: send is not enabled after typing`);
    assert.deepEqual(errors, [], `${name}: page errors`);
    await context.close();
  }
  result.status = "pass";
} catch (error) {
  result.status = result.status === "timeout" ? "timeout" : "fail";
  result.error = String(error);
} finally {
  clearTimeout(deadline);
  await writeFile(`${output}/results.json`, JSON.stringify(result, null, 2));
  await browser.close().catch(() => {});
  console.log(`${result.status.toUpperCase()} ask card dock check${result.error ? `: ${result.error.split("\n")[0]}` : ""}`);
  process.exit(result.status === "pass" ? 0 : 1);
}
