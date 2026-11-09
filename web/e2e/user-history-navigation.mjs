// Actual web-app acceptance with an invented paginated conversation. No real hub or conversation is read.
// Usage: node web/e2e/user-history-navigation.mjs <loopback origin> <artifact directory>
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const [address, directory] = process.argv.slice(2);
const base = new URL(address);
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname));
assert.ok(!base.username && !base.password && !base.search && !base.hash && base.pathname === "/");
const output = resolve(directory); await mkdir(output, { recursive: true });
const COUNT = 36, MID = "example-node", SID = "synthetic-history", RAIL = "[data-nav-rail]";
const inputs = Array.from({ length: COUNT }, (_, i) => ({ id: `test-input-${String(i).padStart(3, "0")}#0`, seq: i * 4 + 1 }));
const items = inputs.flatMap((input, i) => [
  { ...input, role: "user", kind: "text", client_msg_id: `test-send-${i}`, queued: i % 8 === 0,
    text: `Navigation input ${i + 1}: Keep the synthetic layout readable.${i % 3 === 0 ? '\n<persona-context kind="context" title="Fixture">Invented context only.</persona-context>' : ""}` },
  { id: `test-answer-${i}#0`, seq: input.seq + 1, role: "assistant", kind: "text", text: `Answer ${i + 1}.\n\n${Array.from({ length: 7 }, (_, n) => `Synthetic paragraph ${n + 1}. This invented test conversation supplies enough content to scroll. No account, private conversation or real project is used.`).join("\n\n")}` },
  { id: `test-auto-${i}#0`, seq: input.seq + 2, role: "user", kind: "text", text: "Synthetic automatic supervision", injected: { kind: "supervision", line: "Synthetic automatic supervision" }, source: { kind: "supervision" } },
  { id: `test-compact-${i}#0`, seq: input.seq + 3, role: "system", kind: "notice", text: "Synthetic compaction boundary" },
]);
const index = { total: COUNT, inputs, history: "synthetic-history-v1" };
const session = { machine_id: MID, session_id: SID, title: "Synthetic full-history navigation", agent: "claude", status: "idle", state: "idle", capabilities: { read: true, send: true, terminal: false } };
const machine = { id: MID, name: "Example computer", online: true };
const results = { status: "running", realConversationUsed: false, messagesSent: 0, forwardedWrites: 0, runs: [], errors: [] };
let browser;
const deadline = setTimeout(() => { console.error("TIMEOUT full-history navigation"); process.exit(1); }, 180_000);

function pageFor(before = 0) {
  const eligible = inputs.filter((input) => !before || input.seq < before);
  const start = eligible.length > 2 ? eligible.at(-2).seq : eligible[0]?.seq ?? 0;
  return { items: items.filter((item) => item.seq >= start && (!before || item.seq < before)), has_more: eligible.length > 2, history: index.history, to_seq: before ? before - 1 : items.at(-1).seq };
}

try {
  browser = await chromium.launch({ channel: "chrome", headless: true });
  for (const theme of ["light", "dark"]) for (const size of ["wide", "narrow"]) {
    const run = { name: `${size}-${theme}`, serverTotal: COUNT, initialBodies: 0, beforeTicks: 0, afterTicks: 0, olderReads: 0, pageErrors: [], screenshots: [] };
    results.runs.push(run);
    const context = await browser.newContext({ viewport: size === "wide" ? { width: 1600, height: 900 } : { width: 1024, height: 820 }, colorScheme: theme, locale: "en-US", hasTouch: false, serviceWorkers: "block" });
    await context.addInitScript(({ theme }) => { localStorage.setItem("persona.token", "synthetic-only"); localStorage.setItem("persona.language", "en"); localStorage.setItem("persona.theme", theme); }, { theme });
    let release;
    const ready = new Promise((done) => { release = done; });
    await context.route("**/*", async route => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== base.origin) return route.abort("blockedbyclient");
      if (!["GET", "HEAD"].includes(request.method())) return route.fulfill({ json: { ok: true } });
      if (!url.pathname.startsWith("/api/")) return route.continue();
      const path = url.pathname.replace(/^\/api\/v1/, "");
      if (path.endsWith("/items") && url.searchParams.get("user_inputs") === "1") { await ready; return route.fulfill({ json: index }); }
      if (path.endsWith("/items")) {
        const before = Number(url.searchParams.get("before_seq") ?? 0);
        if (before) run.olderReads++;
        const after = Number(url.searchParams.get("after_seq") ?? 0);
        return route.fulfill({ json: after ? { items: [], history: index.history, to_seq: items.at(-1).seq, more_after: false } : pageFor(before) });
      }
      const json = {
        "/sessions": { sessions: [session], total: 1, has_more: false }, [`/sessions/${MID}/${SID}/info`]: session,
        "/machines": { machines: [machine] }, "/assistant": { machine_id: MID, conversations: [] },
        "/interactions": { interactions: [] }, "/todos": { todos: [] }, "/mutes": { spaces: [], sessions: [], allowed: [] },
        "/pins": { spaces: [], sessions: [], sections: [] }, "/agents/sessions": { sessions: [] }, "/agents/prefs": {},
        "/managers": { managers: [], default_id: "" }, "/personas": { personas: [] }, "/assistant/personas": { personas: [] },
        "/workspaces": { workspaces: [] }, "/extensions": { extensions: [] },
      }[path] ?? {};
      return route.fulfill({ json });
    });
    await context.routeWebSocket("**/*", socket => {
      assert.equal(new URL(socket.url()).host, base.host);
      socket.send(JSON.stringify({ type: "hello", server_version: "mock", protocol: 1, features: [] }));
      socket.send(JSON.stringify({ type: "machines", machines: [machine] }));
      socket.send(JSON.stringify({ type: "sessions", machine_id: MID, sessions: [session] }));
      socket.onMessage(raw => { const event = JSON.parse(String(raw)); if (event.type === "app_ping") socket.send(JSON.stringify({ type: "pong", id: event.id })); });
    });
    const page = await context.newPage(); page.setDefaultTimeout(20_000);
    page.on("pageerror", error => run.pageErrors.push(error.message));
    await page.goto(new URL(`/s/${MID}/${SID}`, base).href);
    await page.locator(`${RAIL} button[data-tick]`).first().waitFor();
    await page.waitForFunction(() => document.querySelectorAll("[data-user-bubble]").length === 2);
    const chat = page.locator("[data-chat-scroll]"); const box = await chat.boundingBox();
    await page.mouse.move(box.x + 40, box.y + box.height / 2); await page.mouse.wheel(0, -80);
    await page.waitForFunction(() => Number(getComputedStyle(document.querySelector("[data-nav-rail]")).opacity) > 0.95);
    run.initialBodies = await page.locator("[data-user-bubble]").count();
    run.beforeTicks = await page.locator(`${RAIL} button[data-tick]`).count(); assert.equal(run.beforeTicks, 2);
    const shot = async state => { const name = `history-${run.name}-${state}.png`; await page.screenshot({ path: join(output, name) }); run.screenshots.push(name); };
    await shot("before-two-loaded");
    release();
    await page.waitForFunction(([rail, total]) => document.querySelector(rail)?.dataset.navTotal === String(total) && document.querySelector(rail)?.dataset.navSource === "server", [RAIL, COUNT]);
    run.afterTicks = await page.locator(`${RAIL} button[data-tick]`).count(); assert.equal(run.afterTicks, COUNT);
    assert.equal(await page.locator("[data-user-bubble]").count(), 2, "Index loading must not load every transcript body");
    assert.equal(run.olderReads, 0);
    const widths = await page.locator(`${RAIL} [data-nav-mark]`).evaluateAll(marks => marks.map(mark => parseFloat(mark.style.width)));
    assert.ok(widths.every(width => width === 6), "All idle marks stay equally short");
    await shot("after-server-total");
    const first = page.locator(`${RAIL} [data-tick='0']`);
    await first.hover(); assert.equal(run.olderReads, 0, "Hover does not fetch old transcript bodies");
    await page.locator("[data-nav-preview]").waitFor(); await shot("hover");
    await first.click();
    await page.waitForFunction(([id]) => {
      const row = document.querySelector(`[data-line='seg:${id}']`)?.firstElementChild;
      const sc = document.querySelector("[data-chat-scroll]");
      return row && sc && row.getBoundingClientRect().top - sc.getBoundingClientRect().top >= -2 && row.getBoundingClientRect().top - sc.getBoundingClientRect().top <= 32;
    }, [inputs[0].id]);
    assert.ok(run.olderReads > 0, "The unloaded target must fetch earlier pages before positioning");
    assert.equal(await page.locator(`${RAIL} button[data-tick]`).count(), COUNT);
    await page.mouse.move(box.x + 40, box.y + box.height / 2);
    await page.waitForFunction(() => !document.querySelector("[data-nav-preview]"));
    await page.waitForFunction(() => [...document.querySelectorAll("[data-nav-mark]")].every(mark => parseFloat(getComputedStyle(mark).width) === 6));
    await shot("oldest-loaded-and-positioned");
    assert.deepEqual(run.pageErrors, []);
    run.oldestPositioned = true;
    console.log(`PASS ${run.name}: initial user bodies=${run.initialBodies}, ticks ${run.beforeTicks}->${run.afterTicks}, server total=${run.serverTotal}, oldest loaded/positioned=true, older reads=${run.olderReads}`);
    await context.close();
  }
  results.status = "pass";
} catch (error) { results.status = "fail"; results.errors.push(error.message); process.exitCode = 1; }
finally {
  clearTimeout(deadline); await browser?.close();
  await writeFile(join(output, "results.json"), JSON.stringify(results, null, 2));
  console.log(`${results.status.toUpperCase()} full-history user navigation`);
}
