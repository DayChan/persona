// The component showcase extension's pages (schema/extension/examples/showcase, copied from the extension's snapshots/, #1852):
// the views every app draws for its screenshots, so the four apps can be compared page by page. UI version 6's pages
// (#829) are in ./v6 until the extension's own snapshots have them (persona-extensions, then copied next to the others);
// their pictures (a ref, served by the extension's ui.image) are ./images, and the hub's rewriting of a ref into its own
// path is done here as the hub does it.
import type { UIView } from "../lib/extension-ui";
import { readView } from "../lib/extension-ui";

const files = import.meta.glob("../../../schema/extension/examples/showcase/*.json", { eager: true, import: "default" });
const v6 = import.meta.glob("./v6/*.json", { eager: true, import: "default" });

/** The showcase extension's id: the pictures' paths name it. */
export const SHOWCASE_EXTENSION = "showcase";

/**
 * The hub's path for one of the showcase's pictures: a real hub signs a token (base64url JSON, so it starts "eyJ"); the
 * fake one writes "dev-showcase." and the ref, which the dev server answers with the ref's file (vite.config.ts).
 */
export const showcaseImagePath = (ref: string) => `/api/v1/extensions/${SHOWCASE_EXTENSION}/-/image/dev-showcase.${ref}`;

/**
 * A view as the hub sends it to an app of UI version 6: every picture's ref (an image's src, a row's thumbnail and
 * avatar, a tile's image) given the hub's path beside it.
 */
export function withImagePaths(raw: unknown): unknown {
  if (Array.isArray(raw)) return raw.map(withImagePaths);
  if (!raw || typeof raw !== "object") return raw;
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
    const pic = (k === "src" || k === "thumbnail" || k === "avatar" || k === "image") && !!v && typeof v === "object" && typeof (v as { ref?: unknown }).ref === "string";
    out[k] = pic ? { ...(v as object), path: showcaseImagePath((v as { ref: string }).ref) } : withImagePaths(v);
  }
  return out;
}

/** Page name (the file's name) → its view, in the files' order. */
export const SHOWCASE: Record<string, UIView> = Object.fromEntries(
  [...Object.entries(files), ...Object.entries(v6)]
    .map(([path, raw]) => [path.split("/").pop()!.replace(/\.json$/, ""), readView(withImagePaths(raw))] as const)
    .sort(([a], [b]) => a.localeCompare(b)),
);

/** The UI version 6 pages' names (the screenshots of this version take them). */
export const SHOWCASE_V6: readonly string[] = Object.keys(v6).map((p) => p.split("/").pop()!.replace(/\.json$/, "")).sort();

/** How the page is drawn: a card on "Overview", a widget (small as a card), or a full page. */
export function showcaseKind(name: string): "card" | "widget" | "page" {
  if (name === "card") return "card";
  if (name.startsWith("widget-") || name.startsWith("shortcut-")) return "widget";
  return "page";
}
