// The inline <head> script vite.config.ts's startup-preload plugin writes into index.html, the brotli copies it
// writes of the files that script and the page load, and its checks of the startup namespaces and the first screen's
// size. It lives here, with no imports of the client code, so the config
// can load it and a test can check it against src/i18n/index.ts: the languages it has catalogs for (LANGS) and how it
// picks one (preferredLanguage) must stay the same as the client's.
import { readdirSync, readFileSync } from "node:fs";
import { readFile, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { promisify } from "node:util";
import { brotliCompress, constants, gzip as gzipCompress } from "node:zlib";

/** The languages under `dir` (src/i18n/locales): code → the language subtags its _meta.json matches. Build time only. */
export function localeDirs(dir: string): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const d of readdirSync(dir, { withFileTypes: true })) {
    if (!d.isDirectory()) continue;
    try {
      out[d.name] = (JSON.parse(readFileSync(join(dir, d.name, "_meta.json"), "utf8")) as { matches: string[] }).matches;
    } catch {
      // not a language directory
    }
  }
  return out;
}

export interface PreloadFiles {
  shared: string[];
  app: string[];
  screen: string[];
  catalogs: Record<string, string>;
  /** language subtag → language code (each _meta.json's matches) */
  matches: Record<string, string>;
}

/** The script: it adds a modulepreload (or style preload) link for the preferred language's catalog and the page's files. */
export function preloadScript(files: PreloadFiles): string {
  return `(function () {
  var files = ${JSON.stringify(files)};
  function add(href) {
    var l = document.createElement("link");
    if (/\\.css$/.test(href)) {
      l.rel = "preload";
      l.as = "style";
    } else l.rel = "modulepreload";
    l.crossOrigin = "";
    l.href = "./" + href;
    document.head.appendChild(l);
  }
  var lang = null;
  try { lang = localStorage.getItem("persona.language"); } catch (e) {}
  if (!Object.prototype.hasOwnProperty.call(files.catalogs, lang)) {
    var tag = (navigator.languages && navigator.languages[0]) || navigator.language || "";
    var primary = tag.trim().replace(/_/g, "-").split("-")[0].toLowerCase();
    lang = Object.prototype.hasOwnProperty.call(files.matches, primary) ? files.matches[primary] : "en";
  }
  var base = new URL(document.baseURI).pathname;
  var path = location.pathname.indexOf(base) === 0 ? location.pathname.slice(base.length - 1) : location.pathname;
  var screen = /^\\/screen\\/[^/]/.test(path);
  [files.catalogs[lang]].concat(files.shared, screen ? files.screen : files.app).forEach(add);
})();`;
}

/** The smallest file worth a brotli copy: server/internal/webasset MinSize, below which the hub compresses nothing. */
export const BROTLI_MIN_SIZE = 1024;

const brotli = promisify(brotliCompress);

/**
 * Writes name.br beside each of `files` (paths below `dir`) of BROTLI_MIN_SIZE bytes or more, when brotli makes it
 * smaller: quality 11 with the input's size as hint, so the same file always gives the same bytes. The hub sends
 * these as stored to a browser that takes br (server/internal/hub/web.go serveBrotli); Go has no brotli encoder, so
 * the build makes them. Returns the files it wrote and their bytes before and after. Build time only.
 */
export async function brotliCopies(dir: string, files: string[]): Promise<{ files: number; before: number; after: number }> {
  const out = { files: 0, before: 0, after: 0 };
  await Promise.all(files.map(async (file) => {
    const data = await readFile(join(dir, file));
    if (data.length < BROTLI_MIN_SIZE) return;
    const br = await brotli(data, { params: { [constants.BROTLI_PARAM_QUALITY]: 11, [constants.BROTLI_PARAM_SIZE_HINT]: data.length } });
    if (br.length >= data.length) return;
    await writeFile(join(dir, `${file}.br`), br);
    out.files++;
    out.before += data.length;
    out.after += br.length;
  }));
  return out;
}

/**
 * The most a cold first screen of the client page may send, in bytes, as sentBytes counts it over the page's own files,
 * the shared and App groups and one language's startup catalog (vite.config.ts startupPreload fails the build past
 * it): the heaviest language's at #2689 (ja, 559,565 B) plus 3%. Raise it on purpose, in the change that needs it.
 */
export const FIRST_SCREEN_BUDGET = 576_352;

/** What the built-in hub sends for `files` (paths below `dir`) to a browser that takes br: each one's brotli copy, or
 *  the file itself when it has none. Build time only. */
export async function sentBytes(dir: string, files: string[]): Promise<number> {
  const sizes = await Promise.all(files.map((file) => stat(join(dir, `${file}.br`)).catch(() => stat(join(dir, file)))));
  return sizes.reduce((sum, s) => sum + s.size, 0);
}

/**
 * The most the idle tier may send (src/lib/lazy-page.ts: the chunks of each `lazyPage(…, { idle: true })` and what they
 * import beyond the first screen), in bytes as gzipBytes counts them (vite.config.ts startupPreload fails the build past
 * it): 29,143 B at #2798 plus 3% (28,085 B at #2880; #2798 moved ModelIdField, which the model popover now shares with
 * the idle ModelPicker, out of the first screen into a chunk of its own). Every other lazy page loads on intent, not at
 * idle; raise it on purpose, in the change that needs it.
 */
export const IDLE_TIER_BUDGET = 30_017;

const gzip = promisify(gzipCompress);

/** What the built-in hub sends for `files` (paths below `dir`, none with a brotli copy) once the release precompressed
 *  them (server/internal/webasset): each one gzipped at the best level, or the file itself under BROTLI_MIN_SIZE. Build
 *  time only. */
export async function gzipBytes(dir: string, files: string[]): Promise<number> {
  const sizes = await Promise.all(files.map(async (file) => {
    const data = await readFile(join(dir, file));
    return data.length < BROTLI_MIN_SIZE ? data.length : (await gzip(data, { level: 9 })).length;
  }));
  return sizes.reduce((sum, n) => sum + n, 0);
}

// The arguments of the call whose "(" ends just before `from` in `code`, as written and split at its top-level commas;
// null when it never closes (not a call).
function callArgs(code: string, from: number): string[] | null {
  const args: string[] = [];
  let depth = 0;
  let start = from;
  for (let i = from; i < code.length; i++) {
    const c = code[i];
    if (c === '"' || c === "'" || c === "`") {
      for (i++; i < code.length && code[i] !== c; i++) if (code[i] === "\\") i++;
    } else if ("([{".includes(c)) depth++;
    else if (")]}".includes(c) && depth-- === 0) return [...args, code.slice(start, i)].filter((a) => a.trim());
    else if (c === "," && !depth) {
      args.push(code.slice(start, i));
      start = i + 1;
    }
  }
  return null;
}

/**
 * The modules a source file (`code`) loads at idle: the "x" of each `lazyPage(() => import("x")…, { idle: true })`, as
 * written, wherever `idle` stands among its options. Throws on a lazyPage marked idle whose loader is written another
 * way, and on one whose options are not an object literal: either would load at idle without counting against
 * IDLE_TIER_BUDGET. Build time only.
 */
export function idleImports(code: string): string[] {
  const specs: string[] = [];
  for (const m of code.matchAll(/(?<!function\s*)\blazyPage\(/g)) { // each call, not lazyPage's own definition
    const [load, opts] = callArgs(code, m.index + m[0].length) ?? [];
    if (opts === undefined) continue;
    const call = `lazyPage(${load.trim().slice(0, 60)}…)`;
    if (!opts.trim().startsWith("{")) throw new Error(`${call}: write its options as an object literal, so the idle tier's budget sees whether it is idle`);
    if (!/\bidle\b(?!\s*:\s*false\b)/.test(opts)) continue;
    const spec = /^\(\s*\)\s*=>\s*import\(\s*(["'])([^"'`]+)\1\s*\)/.exec(load.trim())?.[2];
    if (!spec) throw new Error(`${call} is idle, but its loader is not \`() => import("…")\`, so the idle tier's budget cannot count it`);
    specs.push(spec);
  }
  return specs;
}

/**
 * The client namespaces the first screen's chunks (`code`) name in a key literal ("ns.…", 'ns.…' or `ns.…) that
 * `startup` (src/i18n/startup-namespaces.json) leaves out, sorted. Their keys could be read before the rest of the
 * catalog is in (src/i18n/index.ts startI18n), so the build fails on any. Build time only.
 */
export function startupNamespaceGaps(code: string[], namespaces: Iterable<string>, startup: Iterable<string>): string[] {
  const known = new Set(namespaces);
  const listed = new Set(startup);
  const gaps = new Set<string>();
  for (const c of code) for (const [, ns] of c.matchAll(/["'`]([a-z0-9][a-z0-9-]*)\./g)) if (known.has(ns) && !listed.has(ns)) gaps.add(ns);
  return [...gaps].sort();
}
