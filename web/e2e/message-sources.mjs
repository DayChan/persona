// Source presentation in the real web app against an isolated mock hub. All transcript data is synthetic.
// node web/e2e/message-sources.mjs <web URL> --shots <directory> [--cdp <CDP URL>]
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";
const args = process.argv.slice(2);
const option = (name) => args[args.indexOf(name) + 1];
const base = args[0];
const shots = args.includes("--shots") ? option("--shots") : undefined;
const cdp = args.includes("--cdp") ? option("--cdp") : undefined;
if (!base || !shots) throw new Error("Usage: <web URL> --shots <directory> [--cdp <CDP URL>]");
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => { console.error("TIMEOUT message-source web E2E after 90 seconds"); process.exit(1); }, 90000);
const browser = cdp ? await chromium.connectOverCDP(cdp) : await chromium.launch({ channel: "chrome", headless: true });
const item = (seq, text, extra = {}) => ({ id: `test-${seq}#0`, seq, ts: "2026-11-09T06:00:00Z", role: "user", kind: "text", text, ...extra });
// i18n-ignore: synthetic conversation content and source titles for visual acceptance.
const items = [
  item(1, "今天的任务继续推进，有需要我决定的再告诉我。", { client_msg_id: "synthetic-user" }),
  item(2, "好，后台进展会按来源归到这里。", { role: "assistant" }),
  item(3, "巡检完成：3 个任务按计划推进，暂时不需要你处理。", { source: { kind: "patrol", title: "督办巡检", conversation_id: "synthetic-patrol" } }),
  item(4, "已核对测试结果和验收记录，下一轮继续跟进。", { source: { kind: "patrol", title: "督办巡检", conversation_id: "synthetic-patrol" } }),
  item(5, "README 的截图需要更新。\n建议重新生成浅色和深色截图。\n测试与文档检查已通过。\n图片生成后再核对网页预览。\n完成后会把结果同步到此测试对话。", { source: { kind: "conversation", title: "开源巡检", conversation_id: "synthetic-conversation" } }),
  item(6, "早报已整理：今天有两件待办，摘要见下方。", { source: { kind: "followup", title: "早报跟进" } }),
  item(7, "📋 督办：你派出去的任务有 1 个要你处理\n这是 Persona 的督办消息\n<persona-supervision level=\"triage\" />", { source: { kind: "supervision" }, injected: { kind: "system", state: "supervise", level: "triage", line: "Supervision" } }),
  item(8, "巡检已处理，没有新问题。", { role: "assistant" }),
];
let activePage;
try {
  for (const width of [1280, 390]) for (const theme of ["light", "dark"]) {
    const context = await browser.newContext({ viewport: { width, height: 1100 }, locale: "zh-CN", colorScheme: theme });
    const page = await context.newPage(); activePage = page;
    page.setDefaultTimeout(15000);
    const errors = []; page.on("pageerror", (e) => errors.push(e.message));
    await context.addInitScript((scheme) => { localStorage.setItem("persona.theme", scheme); localStorage.setItem("persona.lang", "zh-Hans"); }, theme);
    await page.route("**/api/v1/**/items?*", async (route) => route.fulfill({ json: { items, from_seq: 1, to_seq: 8, has_more: false, more_after: false } }));
    await page.routeWebSocket("**/api/v1/ws*", (socket) => {
      const server = socket.connectToServer();
      socket.onMessage((message) => server.send(message));
      server.onMessage((message) => {
        let data; try { data = JSON.parse(String(message)); } catch { socket.send(message); return; }
        if (data.type === "items" || data.type === "delta") return;
        socket.send(message);
      });
    });
    await page.goto(`${base}/?token=mock`);
    await page.locator('[data-message-source="conversation"]').waitFor();
    await page.evaluate((scheme) => document.documentElement.classList.toggle("dark", scheme === "dark"), theme);
    assert.equal(await page.locator("[data-user-bubble]").count(), 1, "only the genuine user has a user bubble");
    assert.equal(await page.locator('[data-message-source="patrol"] .chat-source-label').count(), 1, "same-source messages share their label");
    assert.match(await page.locator('[data-message-source="conversation"] .chat-source-label').innerText(), /@开源巡检/);
    assert.match(await page.locator('[data-supervision] > button').innerText(), /@督办巡检/);
    const toggle = page.locator('[data-message-source="conversation"] .chat-source-toggle');
    assert.equal(await toggle.getAttribute("aria-expanded"), "false");
    await toggle.click(); assert.equal(await toggle.getAttribute("aria-expanded"), "true");
    await toggle.click(); assert.equal(await toggle.getAttribute("aria-expanded"), "false");
    await page.evaluate(() => { const scroller = document.querySelector("[data-chat-scroll]"); if (scroller) scroller.scrollTop = 0; });
    await page.waitForFunction(() => !document.querySelector("vite-error-overlay"));
    assert.deepEqual(errors, []);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "no horizontal overflow");
    const name = `web-sources-${width === 1280 ? "wide" : "narrow"}-${theme}.png`;
    // Only the chat pane is included: other mock sidebar examples are not relevant acceptance evidence.
    await page.locator("main").screenshot({ path: join(shots, name) });
    console.log(`PASS ${width}/${theme}: user_bubbles=1 source_messages=4 same_source_labels=1 fold_toggle=ok console_errors=0 screenshot=${name}`);
    if (!cdp) await context.close();
  }
  console.log("PASS message-source web E2E (synthetic mock only)");
} catch (error) {
  if (activePage) console.error((await activePage.locator("body").innerText()).slice(0, 2500));
  console.error(`FAIL message-source web E2E: ${error.message}`); process.exitCode = 1;
} finally { if (!cdp) await browser.close(); clearTimeout(watchdog); }
// Disconnect this driver without closing the shared browser or its inspection pages.
if (cdp) process.exit(process.exitCode ?? 0);
