/// <reference types="vitest/config" />
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { localeDirs, preloadScript } from "./src/startup-preload";
import { textScale } from "./src/lib/text-scale";
import { classicWorkerProblem } from "./src/sw-entry-check";
import { i18nCatalogs } from "./i18n-catalogs";
import { pageTokensPlugin } from "./page-tokens";
import { thirdPartyLicenses } from "./third-party-licenses";

// `npm run dev` talks to a hub through Vite's proxy: PERSONA_HUB_URL (eval "$(python3 ../scripts/persona_config.py env)").
const environment = (globalThis as { process?: { env: Record<string, string | undefined> } }).process?.env ?? {};
const hub = environment.PERSONA_HUB_URL || "http://127.0.0.1:18660";

// The hub admin UI (docs/operations/admin.md) is its own page, admin/index.html. Its code goes under admin/assets/, which the hub
// serves only to sources inside admin.access; the client's bundle never references it.
// The admin UI's strings (src/i18n/locales/<lang>/admin.ts and its admin-*.json) are admin code too.
const adminStrings = /\/src\/i18n\/locales\/[^/]+\/(?:admin-[\w-]+\.json|_meta\.json\?catalog=admin)$/;
const adminOnly = (ids: string[]) =>
  ids.length > 0 && ids.every((id) => id.includes("/src/admin/") || id.endsWith("/admin/index.html") || (adminStrings.test(id) || id.startsWith("\0i18n-catalog:admin:")));

// Dev only: /admin and its client-side routes get admin/index.html (Vite's own fallback is the root index.html).
const adminFallback: Plugin = {
  name: "admin-fallback",
  apply: "serve",
  configureServer(server) {
    server.middlewares.use((req, _res, next) => {
      const r = req as unknown as { url?: string };
      const [path, query] = (r.url ?? "").split(/\?(.*)/s);
      if ((path === "/admin" || path.startsWith("/admin/")) && !path.split("/").pop()!.includes(".")) {
        r.url = "/admin/index.html" + (query ? `?${query}` : "");
      }
      next();
    });
  },
};

// Build only: main.tsx loads the language catalog, then React, then App, each a dynamic import that starts only when
// the one before is in, so a cold first load waits for them one after another. This lets the client page's <head>
// start fetching all of them at once (modulepreload fetches and parses, it does not run them: App still runs after
// the catalog is set). The catalog is the preferred language's, picked like i18n/index.ts preferredLanguage (the
// script is src/startup-preload.ts, tested against it); the live screen (/screen/<machine>, screen/screen.ts) never
// loads App, so it gets only the catalog.
const startupPreload: Plugin = {
  name: "startup-preload",
  apply: "build",
  transformIndexHtml: {
    order: "post",
    handler(html, ctx) {
      if (!ctx.bundle || ctx.chunk?.name !== "main") return html; // not admin/index.html
      const chunks = Object.values(ctx.bundle).filter((c) => c.type === "chunk");
      // react-dom/client shares a chunk with other modules (no facade): look a module up by the chunk holding it.
      const byModule = (path: string) => chunks.find((c) => c.moduleIds.some((id) => id.endsWith(path)));
      const find = (path: string) => {
        const chunk = byModule(path);
        if (!chunk) throw new Error(`startup-preload: no chunk holds ${path}`);
        return chunk;
      };
      // Each group: the chunks of its roots and everything they import statically, the JS and the CSS, minus the
      // files the page already loads (main and its static imports have their tags; a page group skips the shared one).
      const group = (roots: string[], loaded: Set<string>) => {
        const files: string[] = [];
        const visit = (chunk: (typeof chunks)[number]) => {
          if (loaded.has(chunk.fileName)) return;
          loaded.add(chunk.fileName);
          files.push(chunk.fileName, ...(chunk.viteMetadata?.importedCss ?? []));
          for (const name of chunk.imports) visit(chunks.find((c) => c.fileName === name)!);
        };
        roots.forEach((r) => visit(find(r)));
        return files;
      };
      const langs = localeDirs(fileURLToPath(new URL("./src/i18n/locales", import.meta.url)));
      const main = new Set([ctx.chunk.fileName, ...ctx.chunk.imports]);
      const shared = group(["/react-dom/client.js", "/src/lib/pwa.ts"], main);
      const page = (roots: string[]) => group(roots, new Set([...main, ...shared]));
      const files = {
        shared,
        app: page(["/src/ui/ExtensionThemePicker.tsx", "/src/App.tsx"]),
        screen: page(["/src/screen/ScreenView.tsx"]),
        catalogs: Object.fromEntries(Object.keys(langs).map((lang) => [lang, find(`i18n-catalog:client:${fileURLToPath(new URL(`./src/i18n/locales/${lang}/_meta.json`, import.meta.url))}.js`).fileName])),
        matches: Object.fromEntries(Object.entries(langs).flatMap(([lang, ms]) => ms.map((m) => [m.toLowerCase(), lang]))),
      };
      const script = preloadScript(files);
      return { html, tags: [{ tag: "script", children: script, injectTo: "head" }] };
    },
  },
};

// Build only: sw.js must not import anything (src/sw-entry-check.ts); the build fails, saying why and how to fix it.
const classicWorker: Plugin = {
  name: "classic-worker",
  apply: "build",
  generateBundle(_, bundle) {
    const sw = bundle["sw.js"];
    const problem = sw?.type === "chunk" ? classicWorkerProblem(sw.code, [...sw.imports, ...sw.dynamicImports]) : undefined;
    if (problem) this.error(problem);
  },
};

// KaTeX's CSS (src/ui/stream/math.ts) lists each font as woff2, woff and ttf. Every browser the build targets (es2022)
// takes woff2, so the other two are only bytes in every personad: drop them from @font-face, and Rollup no longer
// emits those 40 files. The build fails if a KaTeX update writes its fonts some other way.
const katexFonts: Plugin = {
  name: "katex-fonts",
  enforce: "pre",
  transform(code, id) {
    if (!/\/katex\/dist\/katex(?:\.min)?\.css$/.test(id.split("?")[0])) return;
    const css = code.replace(/,\s*url\([^)]*\)\s*format\(["'](?:woff|truetype)["']\)/g, "");
    if (/\.(?:woff|ttf)\b/.test(css) || !css.includes(".woff2")) this.error(`katex-fonts: ${id} still lists woff or ttf fonts, or no woff2`);
    return { code: css, map: null };
  },
};

export default defineConfig({
  plugins: [katexFonts, i18nCatalogs, pageTokensPlugin, react(), tailwindcss(), adminFallback, startupPreload, classicWorker, thirdPartyLicenses],
  // Text size (src/lib/text-scale.ts): font sizes become calc(size * var(--text-scale, 1)), after Tailwind wrote them.
  css: { postcss: { plugins: [textScale()] } },
  server: {
    host: "127.0.0.1",
    proxy: { "/api": { target: hub, changeOrigin: true, ws: true } },
  },
  // The bundle's files are relative to the page's <base>, which the hub points at the sub-path a reverse proxy serves
  // it under (docs/web/web.md "Subpath"); at the root that is "/", as before.
  base: "./",
  // The terminal loads xterm only when opened: pre-bundle it, or the first open in dev re-optimizes and reloads the page.
  optimizeDeps: { include: ["@xterm/xterm", "@xterm/addon-fit", "@xterm/addon-clipboard", "@xterm/addon-web-links", "@xterm/addon-search", "@xterm/addon-webgl"] },
  // Tests run in zh-Hans, the source language, so their expectations read like the interface (src/i18n/test-setup.ts).
  // Node 25+ has its own localStorage, undefined without --localstorage-file, and it hides jsdom's: turn it off.
  test: { setupFiles: ["src/i18n/test-setup.ts"], execArgv: ["--no-experimental-webstorage"] },
  build: {
    target: "es2022",
    sourcemap: false,
    chunkSizeWarningLimit: 900,
    rollupOptions: {
      // The service worker (src/sw.ts) is its own entry at a fixed /sw.js: its scope is the folder it's served from.
      input: { main: "index.html", admin: "admin/index.html", sw: "src/sw.ts" },
      output: {
        entryFileNames: (chunk) => (chunk.name === "sw" ? "sw.js" : chunk.name === "admin" ? "admin/assets/[name]-[hash].js" : "assets/[name]-[hash].js"),
        chunkFileNames: (chunk) => (adminOnly(chunk.moduleIds) ? "admin/assets/[name]-[hash].js" : "assets/[name]-[hash].js"),
        assetFileNames: (asset) => ((asset.names ?? []).some((n) => n.startsWith("admin")) ? "admin/assets/[name]-[hash][extname]" : "assets/[name]-[hash][extname]"),
      },
    },
  },
});
