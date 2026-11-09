// @vitest-environment jsdom
import { renderToStaticMarkup } from "react-dom/server";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { ExtensionNavPage } from "./ExtensionSlots";

const data = vi.hoisted(() => ({ place: undefined as { extension: string; id: string; title?: string; extension_name?: string } | undefined }));
vi.mock("../../lib/extension-slots", () => ({ useExtensionPlace: () => data.place, useExtensionSlot: () => [] }));
vi.mock("../settings/ExtensionView", () => ({ ExtensionUI: () => null }));

/** The page's top bar: its title, then the extension's name unless that only says the title again (#3115). */
function header(place: typeof data.place) {
  data.place = place;
  const html = renderToStaticMarkup(<ExtensionNavPage extension="example" ui="usage" onToggleSidebar={() => {}} />);
  return html.slice(html.indexOf("<header"), html.indexOf("</header>"));
}

const count = (html: string, text: string) => html.split(text).length - 1;

// the code views and the extension page are their own chunks (lib/lazy-page.ts, #1794): in before anything is drawn
beforeAll(() => loadPages(), 60_000);

describe("extension page top bar (#3115)", () => {
  it("writes a name that is the page's title once", () => {
    expect(count(header({ extension: "example", id: "usage", title: "示例", extension_name: "示例" }), "示例")).toBe(1);
    // the same apart from case and spaces around it
    expect(count(header({ extension: "example", id: "usage", title: "Usage", extension_name: " usage " }).toLowerCase(), "usage")).toBe(1);
  });

  it("still writes a different extension name after the title", () => {
    const html = header({ extension: "example", id: "usage", title: "Token 用量", extension_name: "模型用量" });
    expect(html).toContain("Token 用量");
    expect(html).toContain("模型用量");
  });
});
