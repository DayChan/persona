// Synthetic Knowledge recording detail, no live hub. Run against an already running Vite server.
// BASE_URL=http://127.0.0.1:5791 PERSONA_E2E_CHROMIUM=<chrome> SHOTS_DIR=/tmp/mm/shots node web/e2e/recording-minutes.mjs
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const base = new URL(process.env.BASE_URL || "http://127.0.0.1:5791");
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname), "Use a loopback Vite server");
assert.ok(["http:", "https:"].includes(base.protocol) && !base.username && !base.password && base.pathname === "/" && !base.search && !base.hash, "Use an origin without credentials or a route");
const executablePath = process.env.PERSONA_E2E_CHROMIUM || process.env.CHROME_BIN;
assert.ok(executablePath, "Set PERSONA_E2E_CHROMIUM or CHROME_BIN");
const output = resolve(process.env.SHOTS_DIR || "/tmp/mm/shots");
await mkdir(output, { recursive: true });
const id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
const detailPath = `/kb/recordings/${id}`;
const minutesPath = `/recordings/${id}/minutes`;
const fixture = () => ({
  kind: "recordings", id, path: `recordings/${id}`,
  meta: { title: "设计评审 · 示例录音", kind: "meeting", complete: true, started_at: "2026-11-10T14:00:00+08:00", duration: 1800, device: "phone", language: "zh-Hans", revision: 3, minutes_state: "ready", minutes_asked: true },
  summary: "讨论示例项目的导出限制、回归测试和后续安排。",
  segments: [
    { start: 0, end: 240, text: "Ada：今天讨论示例项目的导出体验，以及错误提示的改进。" },
    { start: 312, end: 480, text: "Ada：超过大小限制的条目需要明确提示，不应静默跳过。" },
    { start: 600, end: 780, text: "Bo：这一版保留限制，先补充清晰的提示。" },
    { start: 900, end: 1200, text: "Bo：周五前补充回归测试。Ada 会整理评审结论。" },
    { start: 1500, end: 1800, text: "Ada：限制按单个条目还是整份录音计算，留待下次讨论。" },
  ],
  minutes: {
    version: 2, revision: 3, generated_at: "2026-11-10T06:31:00Z", title: "示例项目设计评审", when: "2026-11-10 14:00–14:30", where: "示例会议室 3", attendees: ["Ada", "Bo"],
    points: [{ text: "导出遇到大小限制时，需要明确告知哪些条目未被包含。", who: "Ada", at: 312 }],
    decisions: [{ text: "本次发布保留大小限制，补充可理解的错误提示。", at: 600 }],
    actions: [
      { id: "a1", who: "Bo", what: "补充导出大小限制的回归测试，并确认提示文案。", by: "周五前", due_date: "2026-11-13", at: 900 },
      { id: "a2", who: "Ada", what: "整理评审结论并分享给示例项目组。", at: 930, todo_id: "example-todo" },
    ],
    open: [{ text: "大小限制应该按单个条目还是整份录音计算？", at: 1500 }],
    markdown: "# 示例项目设计评审\n\n- Bo：周五前补充回归测试。",
  },
});
const browser = await chromium.launch({ executablePath, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const deadline = setTimeout(() => { console.error("FAIL recording minutes timed out"); process.exit(1); }, 120_000);
let screenshots = 0, runs = 0;

async function run(theme, lang = "zh-Hans", width = 1440) {
  const s = JSON.parse(await readFile(new URL(`../src/i18n/locales/${lang}/knowledge-kbdetail.json`, import.meta.url), "utf8"));
  const context = await browser.newContext({ viewport: { width, height: 1800 }, colorScheme: theme, timezoneId: "Asia/Shanghai", deviceScaleFactor: 1, reducedMotion: "reduce" });
  await context.addInitScript(({ theme, lang }) => {
    localStorage.setItem("persona.token", "fixture-token");
    localStorage.setItem("persona.language", lang);
    localStorage.setItem("persona.theme", theme);
  }, { theme, lang });
  let detail = fixture();
  const writes = [], errors = [];
  await context.route("**/*", (route) => new URL(route.request().url()).origin === base.origin ? route.continue() : route.abort());
  await context.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace(/^\/api\/v1/, "");
    if (path === detailPath) return route.fulfill({ json: { detail } });
    if (path === minutesPath || path === `${minutesPath}/todos`) {
      assert.equal(request.method(), "POST");
      assert.equal(request.headers()["x-persona-manager"], "assistant");
      if (path.endsWith("/todos")) {
        const body = request.postDataJSON(); writes.push({ path, body });
        assert.deepEqual(body, { version: 2, actions: ["a1"] });
        detail.minutes.actions[0].todo_id = "example-added-todo";
        return route.fulfill({ json: { todos: [{ id: "example-added-todo" }], minutes: detail.minutes } });
      }
      assert.equal(request.postData(), null);
      writes.push({ path }); detail.meta.minutes_state = "pending";
      return route.fulfill({ status: 202, json: { state: "pending" } });
    }
    assert.ok(["GET", "HEAD"].includes(request.method()), `Unexpected write ${path}`);
    const data = {
      "/kb/recordings": { items: [{ kind: "recordings", id, name: detail.meta.title, relevance: 0 }], tags: [] },
      "/sessions": { sessions: [], total: 0, has_more: false, revision: "fixture" },
      "/machines": { machines: [] }, "/interactions": { interactions: [] },
      "/assistant": { conversations: [] }, "/todos": { todos: [] },
      "/mutes": { spaces: [], sessions: [], allowed: [] }, "/pins": { spaces: [], sessions: [], sections: [] },
      "/agents/sessions": { sessions: [] }, "/agents/prefs": {},
      "/managers": { managers: [{ id: "assistant", name: "Persona", builtin: true, online: true }], default: "assistant" },
      "/personas": { personas: [] }, "/assistant/personas": { personas: [] }, "/workspaces": { workspaces: [] }, "/extensions": { extensions: [] },
    };
    return route.fulfill({ json: data[path] ?? {} });
  });
  await context.routeWebSocket(/\/api\/v1\/ws(?:\?|$)/, (ws) => {
    ws.send(JSON.stringify({ type: "hello", server_version: "mock", protocol: 1, features: ["managers"], hub_name: "Example hub" }));
    ws.send(JSON.stringify({ type: "machines", machines: [] }));
    ws.onMessage((message) => { const event = JSON.parse(String(message)); if (event.type === "app_ping") ws.send(JSON.stringify({ type: "pong", id: event.id })); });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  page.on("pageerror", (err) => errors.push(err.message));
  await page.goto(new URL(detailPath, base).href);
  const minutes = page.getByRole("region", { name: s.minutes, exact: true });
  await minutes.waitFor();
  await minutes.getByRole("heading", { name: s.minutes_open, exact: true }).waitFor();
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(350);
  assert.equal(await page.locator("vite-error-overlay").count(), 0);
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "No page overflow");
  assert.ok(await minutes.evaluate((section) => section.scrollWidth <= section.clientWidth + 1), "No minutes overflow");
  assert.ok(await minutes.locator("button").evaluateAll((buttons) => buttons.every((button) => {
    const range = document.createRange(); range.selectNodeContents(button);
    return getComputedStyle(button).whiteSpace === "nowrap" && range.getBoundingClientRect().width <= button.clientWidth + 1;
  })), "All button text fits on one line");
  if (width === 1440) {
    await page.screenshot({ path: join(output, `minutes-${theme}.png`), fullPage: true });
    screenshots++;
  }

  // Exercise the real app, not just its initial render: timestamp jump, add action, then regenerate.
  const timestamp = minutes.locator('[aria-controls="recording-line-900"]').first();
  await timestamp.click();
  await page.waitForFunction(() => document.activeElement?.id === "recording-line-900");
  const line = page.locator('[id="recording-line-900"]');
  assert.ok(await line.evaluate((element) => element.classList.contains("bg-accent-tint")));
  assert.ok(await line.evaluate((element) => { const rect = element.getBoundingClientRect(); return rect.top >= 0 && rect.bottom <= innerHeight; }), "Jump reveals the transcript line");
  await minutes.getByRole("button", { name: s.minutes_add_todo, exact: true }).click();
  await minutes.getByRole("button", { name: s.minutes_add_todo, exact: true }).waitFor({ state: "detached" });
  assert.equal(writes.filter((write) => write.path.endsWith("/todos")).length, 1);
  await minutes.getByRole("button", { name: s.minutes_regenerate, exact: true }).click();
  await minutes.getByText(s.minutes_updating, { exact: true }).waitFor();
  assert.equal(writes.filter((write) => write.path === minutesPath).length, 1);
  assert.deepEqual(errors, []);
  await context.close();
  runs++;
}

try {
  for (const theme of ["light", "dark"]) await run(theme);
  for (const lang of ["en", "zh-Hans", "ja"]) await run("dark", lang, 390);
  console.log(`PASS recording minutes: ${runs} browser runs, ${screenshots} full-page screenshots; timestamps, mutations, overflow and single-line buttons checked`);
} finally {
  clearTimeout(deadline);
  await browser.close();
}
