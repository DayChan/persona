// Run the real web client against a disposable loopback mock hub started with MOCK_REWIND=safeguards.
// No shared browser profile, CDP connection or production hub is used. All waits and requests are bounded.
import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => args[args.indexOf(name) + 1];
const base = args[0];
const hub = flag("--hub");
const out = flag("--out");
if (!base || !hub || !out) throw new Error("usage: edit-last-message.mjs <web URL> --hub <mock hub URL> --out <directory>");
for (const url of [base, hub]) assert.equal(new URL(url).hostname, "127.0.0.1", "only a loopback mock is allowed");
mkdirSync(out, { recursive: true });
const sid = "a1f3c2d4-0000-4000-8000-000000000006";
const path = `/api/v1/sessions/macbook/${sid}`;
const request = async (url) => (await fetch(url, { signal: AbortSignal.timeout(5000) })).json();
const browser = await chromium.launch({ channel: "chrome", headless: true });
const results = [];
const timer = setTimeout(() => { void browser.close(); }, 120000);
try {
  for (const [theme, action] of [["light", "shortcut"], ["dark", "button"]]) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: theme });
    const page = await context.newPage();
    page.setDefaultTimeout(12000);
    const errors = [];
    const writes = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("request", (r) => { if (r.method() === "POST" && r.url().includes(path)) writes.push({ url: r.url(), body: r.postDataJSON() }); });
    await context.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (![new URL(base).origin, new URL(hub).origin].includes(url.origin)) return route.abort();
      return route.continue();
    });
    await page.addInitScript((theme) => {
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", theme);
    }, theme);
    // The first run uses the real safeguards fixture. The second edits the last remaining mock input; add the same
    // API error to its HTTP transcript, leaving prompt ids/counts intact for the mock's actual rewind handler.
    if (action === "button") await page.route(`**${path}/items*`, async (route) => {
      const response = await route.fetch();
      const data = await response.json();
      if (Array.isArray(data.items) && data.items.length) {
        const last = data.items.at(-1);
        if (last.role === "assistant") last.text = "API Error: Opus 5.5's safeguards flagged this message. Details: [frontier_llm]";
      }
      await route.fulfill({ response, json: data });
    });
    await page.goto(`${base}/?token=mock`);
    await page.locator("main textarea").waitFor();
    await page.evaluate((sid) => {
      history.pushState(null, "", `/s/macbook/${sid}`);
      dispatchEvent(new PopStateEvent("popstate"));
    }, sid);
    const input = page.locator("main textarea");
    const edit = page.getByRole("button", { name: "Edit last message", exact: true });
    await edit.waitFor();
    await page.screenshot({ path: join(out, `${theme}-error.png`) });
    assert.equal(await page.getByRole("button", { name: "Retry", exact: true }).count(), 0);
    const before = (await request(`${hub}/mock/rewinds`)).rewinds.length;
    if (action === "shortcut") {
      await input.fill("Keep this draft");
      await input.press("Escape"); await input.press("Escape");
      assert.equal((await request(`${hub}/mock/rewinds`)).rewinds.length, before);
      assert.equal(await input.inputValue(), "Keep this draft");
      await input.fill("");
      await input.press("Escape");
      assert.equal((await request(`${hub}/mock/rewinds`)).rewinds.length, before);
      await input.press("Escape");
    } else await edit.click();
    await page.waitForFunction(() => {
      const input = document.querySelector("main textarea");
      return input && input.value && !input.readOnly;
    });
    const after = (await request(`${hub}/mock/rewinds`)).rewinds;
    assert.equal(after.length, before + 1);
    assert.equal(after.at(-1).restore, "conversation");
    assert.equal(after.at(-1).occurrence, 1);
    assert.equal(after.at(-1).prompts_after, 0);
    assert.equal(writes.filter((w) => w.url.endsWith("/messages")).length, 0);
    assert.equal(writes.filter((w) => w.url.endsWith("/interrupt")).length, 0);
    if (action === "shortcut") {
      assert.equal(await input.inputValue(), "Revise this paragraph");
      assert.ok(await page.locator("main").textContent().then((text) => text.includes("Example quote")));
    }
    await page.screenshot({ path: join(out, `${theme}-editing.png`) });
    const edited = `Revised paragraph (${theme})`;
    await input.fill(edited);
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith(`${path}/messages`));
    const sent = writes.filter((w) => w.url.endsWith("/messages"));
    assert.equal(sent.length, 1);
    assert.ok(sent[0].body.text.includes(edited));
    if (action === "shortcut") assert.equal(sent[0].body.text, `> Example quote\n\n${edited}`);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    assert.deepEqual(errors, []);
    results.push({ theme, action, rewinds: 1, explicitSends: 1, consoleErrors: errors.length, pass: true });
    await context.close();
  }
  writeFileSync(join(out, "results.json"), JSON.stringify(results, null, 2) + "\n");
  console.log(`PASS edit-last-message: ${results.length} themes; one rewind and one explicit send per run`);
} finally {
  clearTimeout(timer);
  await browser.close();
}
