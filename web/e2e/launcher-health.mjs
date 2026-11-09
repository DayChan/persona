// Screenshots and checks of "New agent" graying out a launcher this computer's terminal can't run (#1643), and of a chat
// whose start the node refused with a code (the hub's whole sentence under "Couldn't start", #1642).
//
// Needs the web page against the fake hub with MOCK_LAUNCHER_HEALTH=1, and Chrome (playwright-core drives it):
//
//   MOCK_LAUNCHER_HEALTH=1 MOCK_TERMINAL_PANES=1 MOCK_LANG=<zh-Hans|en|ja> PORT=18795 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18795 npx vite --port 5195 --strictPort &)
//   node web/e2e/launcher-health.mjs http://127.0.0.1:5195 [--shots <dir>] [--locale zh-CN|en-US|ja-JP]
//
// Runs wide (1280) and phone width (390), light and dark: the dialog with work_gw (not in the terminal) and team_gw (its key
// rejected) grayed out and said under the launchers (every line whole, none cut off), work_gw's "How to fix", the failed
// chat (in MOCK_LANG's words), and the Run agent here menu of a free shell with the blocked ones last, with why. Prints
// one line per check and a verdict; exits 1 on a failure.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const base = (args[0] ?? "").replace(/\/$/, "");
const shots = flag("--shots");
const locale = flag("--locale") ?? "zh-CN";
if (!base) {
  console.error("usage: node web/e2e/launcher-health.mjs <web URL> [--shots <dir>] [--locale zh-CN|en-US|ja-JP]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });

const FAILED = "/s/macbook/f1643000-0000-4000-8000-000000000001"; // MOCK_LAUNCHER_HEALTH's refused start
const lang = locale.startsWith("en") ? "en" : locale.startsWith("ja") ? "ja" : "zh";
// words of the hub's sentence (MOCK_LANG matching --locale), the help's link and the menu's names, per language
const WORDS = {
  zh: { reason: "这台电脑的终端里用不了", provider: "模型供应商", actions: (n) => `${n} 的操作`, runHere: "在这里运行 Agent…", legacy: "详情缓存" },
  en: { reason: "Not available in this computer's terminal", provider: "model provider", actions: (n) => `Actions for ${n}`, runHere: "Run agent here…", legacy: "no cached details" },
  ja: { reason: "このコンピューターのターミナルでは使えません", provider: "モデルプロバイダー", actions: (n) => `${n}の操作`, runHere: "ここでエージェントを実行…", legacy: "キャッシュがありません" },
}[lang];
const SIZES = { wide: { width: 1280, height: 860 }, narrow: { width: 390, height: 844 } };
const RUNS = [["wide", "light"], ["wide", "dark"], ["narrow", "light"], ["narrow", "dark"]];

const ok = (msg) => console.log(`ok  ${msg}`);
function fail(msg) {
  console.log(`FAIL ${msg}`);
  process.exit(1);
}

async function shot(page, name, tag) {
  if (!shots) return;
  await page.waitForTimeout(400); // the dialog's and the help's entrances
  await page.screenshot({ path: join(shots, `${name}-${tag}.png`) });
}

const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

async function openNewAgent(page, tag, narrow) {
  // the sidebar's (or, at phone width, the agent list's) New agent button
  const name = /^(新建 Agent|New agent|新規エージェント)$/;
  let add = page.getByRole("button", { name }).filter({ visible: true });
  if (narrow) {
    // at phone width the sidebar is a drawer off the screen: the Agents page has the button
    await page.goto(`${base}/agents`);
    add = page.locator("main").getByRole("button", { name }).filter({ visible: true });
  }
  await add.first().click({ timeout: 8000 }).catch(async (e) => {
    if (shots) await page.screenshot({ path: join(shots, `failed-${tag}.png`) });
    fail(`${tag}: no New agent button (${String(e).split("\n")[0]})`);
  });
  await page.locator('[role="dialog"]').waitFor({ timeout: 8000 }).catch(() => fail(`${tag}: the New agent dialog did not open`));
}

async function run(browser, size, theme) {
  const tag = `${size}-${theme}`;
  const ctx = await browser.newContext({ viewport: SIZES[size], deviceScaleFactor: 2, hasTouch: size === "narrow", colorScheme: theme, locale });
  const page = await ctx.newPage();
  await page.goto(`${base}/?token=mock`);
  await page.waitForSelector("main textarea", { timeout: 15000 }).catch(() => fail(`${tag}: did not pair with the fake hub`));
  await page.evaluate((t) => localStorage.setItem("persona.theme", t), theme);
  await page.reload();
  await page.waitForSelector("main textarea", { timeout: 15000 });

  await openNewAgent(page, tag, size === "narrow");
  const blocked = page.locator('[data-testid="new-agent-blocked"]');
  await blocked.waitFor({ timeout: 8000 }).catch(() => fail(`${tag}: no line under the launchers for the grayed-out ones`));
  const lines = await blocked.locator("li").count();
  if (lines !== 2) fail(`${tag}: ${lines} grayed-out launchers said, want work_gw and team_gw`);
  ok(`${tag}: work_gw and team_gw are said under the launchers`);
  const disabled = await page.locator('button[aria-disabled="true"]').evaluateAll((els) => els.map((e) => e.textContent?.trim()));
  if (!disabled.includes("work_gw") || !disabled.includes("team_gw")) fail(`${tag}: grayed-out chips ${JSON.stringify(disabled)}`);
  const picked = await page.locator('button[aria-pressed="true"][aria-disabled="true"]').count();
  if (picked) fail(`${tag}: a grayed-out launcher is picked by default`);
  ok(`${tag}: both chips are grayed out and neither is picked`);
  if ((await sideways(page)) > 0) fail(`${tag}: the page scrolls sideways`);
  // each line wraps instead of ending in an ellipsis: a phone has no hover for the chip's tooltip
  const clipped = await blocked.locator("li, li *").evaluateAll((els) => els.filter((e) => e.scrollWidth > e.clientWidth + 1).map((e) => e.textContent));
  if (clipped.length) fail(`${tag}: a line under the launchers is cut off: ${JSON.stringify(clipped)}`);
  ok(`${tag}: every line under the launchers is whole`);
  // MOCK_LAUNCHER_HEALTH=default: Model settings' default runs work_gw, so the Default chip is marked (still the choice)
  // and work_gw's line comes first, tagged as the one this start uses
  if (await page.locator('[data-testid="new-agent-default-blocked"]').count()) {
    const first = blocked.locator("li").first();
    if (!(await first.getAttribute("data-runs")) || !((await first.textContent()) ?? "").includes("work_gw")) fail(`${tag}: the Default chip is marked but work_gw's line is not first and tagged`);
    const pressed = await page.locator('[data-testid="new-agent-default-blocked"]').locator("xpath=ancestor::button[1]").getAttribute("aria-pressed");
    if (pressed !== "true") fail(`${tag}: the marked Default chip is not the choice`);
    ok(`${tag}: Default is marked, still chosen, and work_gw's line is first`);
  }
  await shot(page, "web-newagent", tag);

  await blocked.locator("li").first().locator("button").click();
  const help = page.locator('[data-testid="launcher-blocked-help"]');
  await help.waitFor({ timeout: 4000 }).catch(() => fail(`${tag}: How to fix did not open`));
  const text = (await help.textContent()) ?? "";
  if (!text.includes("work_gw") || !text.includes("~/.bashrc")) fail(`${tag}: the help does not say what to do: ${text.slice(0, 120)}`);
  ok(`${tag}: How to fix says where to define it`);
  await help.scrollIntoViewIfNeeded();
  await shot(page, "web-newagent-help", tag);
  // team_gw's (its key rejected): the cause itself is said in the help, whole, under its title
  await blocked.locator("li").filter({ hasText: "team_gw" }).locator("button").click();
  const reason = page.locator('[data-testid="launcher-blocked-reason"]');
  await reason.waitFor({ timeout: 4000 }).catch(() => fail(`${tag}: team_gw's help does not say its cause`));
  ok(`${tag}: team_gw's help says its cause: ${await reason.textContent()}`);
  await help.scrollIntoViewIfNeeded();
  await shot(page, "web-newagent-help-other", tag);

  // a fresh load (the dialog may still be leaving through history at phone width), then the chat opened inside the page
  // once the session list is in: a cold load of its URL can show the older-hub banner for a moment
  await page.goto(base + "/").catch(() => page.goto(base + "/"));
  await page.waitForSelector("main textarea", { timeout: 15000 });
  await page.waitForFunction((path) => Boolean(document.querySelector(`a[href$="${path}"]`)), FAILED, { timeout: 8000 }).catch(() => {});
  await page.evaluate((path) => {
    history.pushState(null, "", path);
    dispatchEvent(new PopStateEvent("popstate"));
  }, FAILED);
  const coded = page.locator('[data-testid="start-error-coded"]');
  await coded.waitFor({ timeout: 8000 }).catch(() => fail(`${tag}: the chat does not show the coded start failure`));
  const sentence = (await coded.textContent()) ?? "";
  if (!sentence.includes("work_gw") || !sentence.includes(WORDS.provider)) fail(`${tag}: the sentence is cut, missing or not in ${lang}: ${sentence}`);
  ok(`${tag}: the chat shows the hub's whole sentence`);
  await page.getByText(WORDS.legacy).waitFor({ state: "detached", timeout: 6000 }).catch(() => fail(`${tag}: the older-hub banner is still over the chat`));
  ok(`${tag}: no older-hub banner over the chat`);
  await shot(page, "web-chat-coded", tag);

  // Run agent here on a free shell (MOCK_TERMINAL_PANES=1): the blocked ones last, each with why, still a choice
  if (!process.env.SKIP_RUN_HERE) await runHere(page, tag);
  await ctx.close();
}

async function runHere(page, tag) {
  await page.goto(`${base}/terminal`);
  // a free shell's row; its ⋯ shows under the pointer (always on a touch screen)
  const row = page.locator("main a").filter({ hasText: "zsh" }).filter({ visible: true }).first();
  await row.waitFor({ timeout: 8000 }).catch(() => fail(`${tag}: no free shell on the Terminal page (run the mock with MOCK_TERMINAL_PANES=1)`));
  await row.hover().catch(() => {});
  const more = row.locator("xpath=..").getByRole("button", { name: WORDS.actions("zsh") });
  await more.click({ timeout: 4000 }).catch(() => fail(`${tag}: no ⋯ on the shell's row`));
  await page.getByRole("menuitem", { name: WORDS.runHere }).click({ timeout: 4000 }).catch(() => fail(`${tag}: no Run agent here in the shell's menu`));
  const rows = page.getByRole("menuitem").filter({ hasText: /_gw|claude|sonnetcc/ });
  await rows.first().waitFor({ timeout: 6000 }).catch(() => fail(`${tag}: the launchers did not load in Run agent here`));
  const names = await rows.evaluateAll((els) => els.map((e) => ({ text: e.textContent ?? "", disabled: e.getAttribute("aria-disabled") === "true" })));
  const at = (n) => names.findIndex((r) => r.text.startsWith(n));
  if (at("work_gw") < at("claude") || at("team_gw") < at("claude")) fail(`${tag}: the blocked launchers are not after the usable ones: ${JSON.stringify(names)}`);
  if (names.some((r) => r.disabled)) fail(`${tag}: a launcher is disabled in Run agent here`);
  if (at("work_gw") < 0 || !names[at("work_gw")].text.includes(WORDS.reason)) fail(`${tag}: work_gw's row does not say why: ${JSON.stringify(names)}`);
  ok(`${tag}: Run agent here lists work_gw and team_gw last, with why, still a choice`);
  await shot(page, "web-runhere", tag);
  await page.keyboard.press("Escape");
}

const browser = await chromium.launch({ channel: "chrome", headless: true });
try {
  for (const [size, theme] of RUNS) await run(browser, size, theme);
} finally {
  await browser.close();
}
console.log("PASS launcher health");
