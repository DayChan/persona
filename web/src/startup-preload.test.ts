// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { brotliDecompressSync, gzipSync } from "node:zlib";
import { LANG_MATCHES, LANGS, preferredLanguage } from "./i18n";
import { brotliCopies, BROTLI_MIN_SIZE, gzipBytes, idleImports, localeDirs, preloadScript, sentBytes, startupNamespaceGaps } from "./startup-preload";

const langs = localeDirs(join(__dirname, "i18n/locales"));

const files = {
  shared: ["assets/react.js"],
  app: ["assets/App.js", "assets/App.css"],
  screen: ["assets/ScreenView.js"],
  catalogs: Object.fromEntries(Object.keys(langs).map((lang) => [lang, `assets/${lang}.js`])),
  matches: Object.fromEntries(Object.entries(langs).flatMap(([lang, ms]) => ms.map((m) => [m.toLowerCase(), lang]))),
};

// Runs the inline script on a <head> holding only the page's <base> (index.html always has one, set by the hub) and
// returns the hrefs it added, in order.
function run(base = "/"): string[] {
  document.head.innerHTML = `<base href="${base}" />`;
  new Function(preloadScript(files))();
  return [...document.head.querySelectorAll("link")].map((l) => l.getAttribute("href")!.replace(/^\.\//, ""));
}

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
  document.head.innerHTML = "";
});

describe("startup preload script", () => {
  it("has a catalog for every client language", () => {
    expect(Object.keys(langs).sort()).toEqual([...LANGS].sort());
    expect(files.matches).toEqual(LANG_MATCHES);
  });

  it.each([
    [null, ["zh-TW"]],
    [null, ["ja_JP"]],
    [null, ["fr-FR", "zh-CN"]],
    [null, []],
    ["ja", ["en-US"]],
    ["en", ["zh-CN"]],
    ["klingon", ["ja"]],
  ])("picks the catalog preferredLanguage picks (stored %s, system %j)", (stored, languages) => {
    if (stored) localStorage.setItem("persona.language", stored);
    vi.spyOn(navigator, "languages", "get").mockReturnValue(languages);
    vi.spyOn(navigator, "language", "get").mockReturnValue(languages[0] ?? "");
    expect(run()[0]).toBe(files.catalogs[preferredLanguage()]);
  });

  it("preloads App on client pages and only the screen view on the live screen", () => {
    expect(run()).toEqual([files.catalogs[preferredLanguage()], ...files.shared, ...files.app]);
    window.history.pushState(null, "", "/screen/mac");
    try {
      expect(run()).toEqual([files.catalogs[preferredLanguage()], ...files.shared, ...files.screen]);
      window.history.pushState(null, "", "/persona/screen/mac");
      expect(run("/persona/")).toEqual([files.catalogs[preferredLanguage()], ...files.shared, ...files.screen]);
      window.history.pushState(null, "", "/persona/");
      expect(run("/persona/")).toEqual([files.catalogs[preferredLanguage()], ...files.shared, ...files.app]);
    } finally {
      window.history.pushState(null, "", "/");
    }
  });
});

describe("brotli copies", () => {
  it("writes name.br beside each file of 1 KB or more, decoding to it, the same bytes every time", async () => {
    const dir = mkdtempSync(join(tmpdir(), "startup-br-"));
    mkdirSync(join(dir, "assets"));
    const big = "export const greeting = \"hello\";\n".repeat(100);
    writeFileSync(join(dir, "assets/App-1.js"), big);
    writeFileSync(join(dir, "assets/small-2.js"), "x".repeat(BROTLI_MIN_SIZE - 1));
    const st = await brotliCopies(dir, ["assets/App-1.js", "assets/small-2.js"]);
    const br = readFileSync(join(dir, "assets/App-1.js.br"));
    expect(st).toEqual({ files: 1, before: big.length, after: br.length });
    expect(brotliDecompressSync(br).toString()).toBe(big);
    expect(existsSync(join(dir, "assets/small-2.js.br"))).toBe(false);
    expect(await sentBytes(dir, ["assets/App-1.js", "assets/small-2.js"])).toBe(br.length + BROTLI_MIN_SIZE - 1);
    await brotliCopies(dir, ["assets/App-1.js"]);
    expect(readFileSync(join(dir, "assets/App-1.js.br")).equals(br)).toBe(true);
    rmSync(dir, { recursive: true });
  });
});

describe("idle tier", () => {
  it("finds the modules lazyPage loads at idle, not those it loads on intent or when shown", () => {
    const code = [
      'const Palette = lazyPage(() => import("./views/Palette").then((m) => m.Palette), { idle: true });',
      'const Settings = lazyPage(() => import("./views/Settings").then((m) => m.Settings), { routes: ["settings"] });',
      "const Chart = lazyPage(() => import('./Chart').then((m) => m.Chart), {\n  idle: true,\n});",
      'const Diff = lazyPage(() => import("./DiffView").then((m) => m.DiffView));',
      'const idle = { idle: true };',
      'const Inbox = lazyPage(() => import("./views/Inbox").then((m) => m.Inbox), { idle: true, routes: ["inbox"] });',
      'const Kb = lazyPage(() => import("./views/Kb").then((m) => m.Kb), { routes: ["kb"], idle: true });',
      'const Todos = lazyPage(() => import("./views/Todos").then((m) => m.Todos), { idle: false, routes: ["todos"] });',
      "export function lazyPage(load, opts = {}) {}",
    ].join("\n");
    expect(idleImports(code)).toEqual(["./views/Palette", "./Chart", "./views/Inbox", "./views/Kb"]);
  });

  it.each([
    ['lazyPage(() => codeViews().then((m) => m.Header), { idle: true })', "its loader is not"],
    ['lazyPage(() => import("./Header"), opts)', "object literal"],
  ])("refuses an idle page it cannot count: %s", (code, why) => {
    expect(() => idleImports(code)).toThrow(why);
  });

  it("counts each file gzipped at the best level, or as it is under 1 KB", async () => {
    const dir = mkdtempSync(join(tmpdir(), "idle-gz-"));
    mkdirSync(join(dir, "assets"));
    const big = "export const greeting = \"hello\";\n".repeat(100);
    writeFileSync(join(dir, "assets/Palette-1.js"), big);
    writeFileSync(join(dir, "assets/small-2.js"), "x".repeat(BROTLI_MIN_SIZE - 1));
    expect(await gzipBytes(dir, ["assets/Palette-1.js", "assets/small-2.js"])).toBe(gzipSync(big, { level: 9 }).length + BROTLI_MIN_SIZE - 1);
    rmSync(dir, { recursive: true });
  });
});

describe("startup namespace check", () => {
  it("names each namespace the first screen's code reads that the startup list leaves out", () => {
    const code = ['t("common.ok");t("page.title");x(`page.${n}`);y("settings.json");z("other.key")', 'w(\'menu.open\')'];
    const namespaces = ["common", "page", "menu", "settings", "unread"];
    expect(startupNamespaceGaps(code, namespaces, ["common"])).toEqual(["menu", "page", "settings"]);
    expect(startupNamespaceGaps(code, namespaces, ["common", "menu", "page", "settings"])).toEqual([]);
  });
});
