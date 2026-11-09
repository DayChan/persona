// Synthetic main-conversation regression. All API and socket data are fixtures;
// no live hub, real conversation, or personal information is used.
// Usage: node web/e2e/main-history.mjs <web URL> --hub <mock hub URL> --shots <directory>
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => { const index = args.indexOf(name); return index < 0 ? undefined : args[index + 1]; };
const base = args[0]?.replace(/\/$/, "");
const hub = flag("--hub")?.replace(/\/$/, "");
const shots = flag("--shots");
assert(base && hub && shots, "usage: main-history.mjs <web URL> --hub <mock hub URL> --shots <directory>");
const profile = await (await fetch(`${hub}/api/v1/assistant`, { headers: { Authorization: "Bearer mock" }, signal: AbortSignal.timeout(10000) })).json();
const conversation = profile.conversations[0];
const sid = conversation.session_id;
const items = [];
for (let question = 1; question <= 3; question++) {
  const seq = question * 20;
  const ts = `2026-11-09T10:${String(question).padStart(2, "0")}:00Z`;
  const user = { id: `question-${question}#text`, seq, ts, role: "user", kind: "text", text: `Question ${question}: keep this real user message visible.`, client_msg_id: `client-${question}` };
  items.push(user, { id: `answer-${question}`, seq: seq + 1, ts, role: "assistant", kind: "text", text: "The assistant received this question." });
  if (question < 3) {
    items.push({ id: `compact-${question}`, seq: seq + 2, ts, role: "system", kind: "notice", text: "Context compacted", compact: { trigger: "auto", pre_tokens: 170000 } });
  }
  for (let n = 0; n < 6; n++) items.push({ id: `supervision-${question}-${n}`, seq: seq + 3 + n, ts, role: "user", kind: "text", text: "Synthetic automatic supervision input", injected: { kind: "system", state: "supervise", line: "Automatic task supervision" } });
}
mkdirSync(shots, { recursive: true });
const browser = await chromium.launch({ executablePath: process.env.CHROME_BIN || "/opt/google/chrome/chrome", args: ["--no-sandbox", "--disable-dev-shm-usage"] });
try {
  for (const theme of ["light", "dark"]) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: theme });
    await context.addInitScript((theme) => {
      localStorage.setItem("persona.token", "mock");
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", theme);
    }, theme);
    const page = await context.newPage();
    const errors = [];
    const pages = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.routeWebSocket("**/*", (socket) => socket.onMessage((raw) => {
      if (JSON.parse(String(raw)).type === "client_hello") socket.send(JSON.stringify({ type: "hello", server_version: "synthetic", features: [] }));
    }));
    await page.route("**/api/**", async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      let data;
      if (url.pathname === "/api/v1/assistant") data = { ...profile, notes_effective: "", main: conversation, conversations: [{ ...conversation, title: "Synthetic main history" }] };
      else if (url.pathname.endsWith(`/${sid}/items`)) {
        const before = Number(url.searchParams.get("before_seq"));
        const after = url.searchParams.has("after_seq");
        const slice = after ? [] : before ? items.filter((item) => item.seq < before) : items.filter((item) => item.seq >= 40);
        data = { items: slice, has_more: !before && !after, history: "synthetic-history", to_seq: 68 };
        pages.push({ before, count: slice.length });
      } else if (url.pathname.endsWith(`/${sid}/feed`)) data = { items: [] };
      else if (url.pathname === "/api/v1/todos") data = { todos: [] };
      else if (request.method() !== "GET") data = {};
      if (data !== undefined) return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(data) });
      const response = await route.fetch({ url: hub + url.pathname + url.search });
      return route.fulfill({ response });
    });
    await page.goto(`${base}/persona/${sid}`, { waitUntil: "domcontentloaded", timeout: 30000 });
    await page.waitForSelector('[data-user-bubble][data-items*="question-3"]', { timeout: 15000 });
    const scroller = page.locator("[data-chat-scroll]");
    await scroller.evaluate((el) => { el.scrollTop = 0; });
    await page.waitForSelector('[data-user-bubble][data-items*="question-1"]', { timeout: 15000 });
    assert.equal(await page.locator("[data-user-bubble]").count(), 3, `${theme}: all real questions remain bubbles`);
    assert.equal(await page.locator('[data-compact-divider]').count(), 2, `${theme}: both compactions remain visible`);
    assert.equal(await page.locator('[data-injected="system"]').count(), 18, `${theme}: supervision remains inspectable`);
    assert(pages.some((page) => page.before > 0), `${theme}: older history was requested`);
    await page.setViewportSize({ width: 1280, height: 2400 });
    await scroller.evaluate((el) => { el.scrollTop = 0; });
    await page.locator("main").screenshot({ path: join(shots, `main-history-${theme}.png`) });
    assert.deepEqual(errors, [], `${theme}: no page errors`);
    console.log(`PASS ${theme}: 3 real user bubbles, 2 compactions, 18 supervision rows, older-page recovery; ${JSON.stringify(pages)}`);
    await context.close();
  }
} finally {
  await browser.close();
}
console.log("PASS synthetic main history");
