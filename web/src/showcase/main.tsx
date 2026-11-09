// Dev only (showcase.html, not built): every page of the component showcase extension drawn by the real ExtensionUI from its
// snapshots, for screenshots next to the other apps' (#1852). The hub is faked: drawing the extension's page "showcase"
// with state <page> answers that page's view; a press answers the same view again (nothing is stored). UI version 6
// (#829): a control's change answers its component with the new value (the "fail" action answers an error, to see
// it put back); the pictures' paths are served by the dev server from src/showcase/images (vite.config.ts).
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { initI18n } from "../i18n";
import "../styles.css";
import type { UIItem, UIView } from "../lib/extension-ui";
import type { UIRowControl } from "../lib/extension-ui-v6";

// The catalog loads before the modules that call t() as they load (docs/web/web.md "Internationalization"), as in main.tsx.
await initI18n();
const [{ ExtensionUI }, { SHOWCASE, showcaseKind }, { Toasts }] = await Promise.all([import("../views/settings/ExtensionView"), import("./snapshots"), import("../ui/atoms")]);

const realFetch = window.fetch.bind(window);
window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  if (url.includes("/-/ui/")) {
    const body = JSON.parse(String(init?.body ?? "{}")) as { state?: string; action?: string; values?: Record<string, string> };
    // a diff's hidden lines (UI version 5): made-up lines, the file 90 long
    if (body.action === "context" && body.values?.path) {
      const lines = Array.from({ length: 90 }, (_, i) => `\t// ${body.values!.path} line ${i + 1}`);
      return new Response(JSON.stringify({ lines: { path: body.values.path, side: "new", start: 1, lines, total: 90 } }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    // a control (UI version 6): the failing one says so; any other answers its component with the value chosen
    if (body.action === "fail") return new Response(JSON.stringify({ error: "the device did not answer", code: "plugin_failed" }), { status: 502, headers: { "Content-Type": "application/json" } });
    const changed = body.values ? controlled(SHOWCASE[body.state ?? ""], body.values) : undefined;
    if (changed) {
      await new Promise((r) => setTimeout(r, 400)); // as a device takes a moment
      return new Response(JSON.stringify({ items: [changed] }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    const view = SHOWCASE[body.state ?? ""] ?? SHOWCASE.home;
    return new Response(JSON.stringify({ view: { ...view, state: body.state } }), { status: 200, headers: { "Content-Type": "application/json" } });
  }
  return realFetch(input, init);
};

/** The component holding a control the values name, with the control (a row's, or a tile's of a grid) set to it. */
function controlled(view: UIView | undefined, values: Record<string, string>): UIItem | undefined {
  const set = (c: UIRowControl): UIRowControl | undefined => {
    if (!(c.name in values)) return undefined;
    const v = values[c.name];
    return c.kind === "switch" ? { ...c, on: v === "true" } : { ...c, value: Number(v), display: undefined };
  };
  for (const s of view?.sections ?? [])
    for (const it of s.items) {
      if (!it.key) continue;
      const c = it.control && set(it.control);
      if (c) return { ...it, control: c };
      const tiles = it.tiles?.map((tl) => {
        const tc = tl.control && set(tl.control);
        return tc ? { ...tl, control: tc, active: tc.on } : tl;
      });
      if (tiles && tiles.some((tl, i) => tl !== it.tiles![i])) return { ...it, tiles };
    }
  return undefined;
}

const q = new URLSearchParams(location.search);
const only = q.get("only");
const width = Number(q.get("width")) || (only ? 390 : 440);
const some = q.get("pages")?.split(",").filter((n) => n in SHOWCASE);
const names = only ? [only] : some?.length ? some : Object.keys(SHOWCASE);

function Page({ name }: { name: string }) {
  const kind = showcaseKind(name);
  const ui = kind === "card" ? "showcase-card" : kind === "widget" ? "showcase-widget" : "showcase";
  return (
    <section data-page={name} className="flex flex-col gap-2">
      {!only && <p className="px-1 font-mono text-[11px] text-ink-3">{name}</p>}
      <div className={kind === "page" ? "" : "rounded-card bg-surface py-2 shadow-card"}>
        <ExtensionUI extension="showcase" ui={ui} state={name} compact={kind !== "page"} history={false} title="组件展示" /* i18n-ignore: dev-only page; the showcase extension's own name, as its snapshots spell it */ />
      </div>
    </section>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <main className="min-h-dvh bg-page p-4 text-ink" style={{ width }}>
      <div className="flex flex-col gap-8">
        {names.map((n) => (
          <Page key={n} name={n} />
        ))}
      </div>
      <Toasts />
    </main>
  </StrictMode>,
);
