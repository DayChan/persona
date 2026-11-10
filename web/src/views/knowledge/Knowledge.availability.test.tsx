// @vitest-environment jsdom
// The rendered list uses the real REST error decoder, not a page-specific substitute.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { ListPane } from "./KBList";
import { lists, searches } from "./cache";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  lists.clear();
  searches.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  lists.clear();
  searches.clear();
});

it.each([
  ["kb_disabled", undefined, "lib-api.knowledge_disabled"],
  ["kb_unavailable", undefined, "lib-api.knowledge_unavailable"],
  ["kb_read_only", undefined, "lib-api.knowledge_read_only"],
  ["internal", "the knowledge base is off", "lib-api.knowledge_disabled"],
  ["assistant_offline", undefined, "lib-api.manager_offline"],
] as const)("shows %s as an error, not an empty knowledge base or hub outage", async (code, detail, key) => {
  const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
    code, detail, error: "Request failed",
  }), { status: code === "kb_read_only" ? 403 : 503 }));
  vi.stubGlobal("fetch", fetcher);
  await act(async () => root.render(
    <ListPane kind="projects" detailOpen={false} reloadKey={0} onToggleSidebar={() => {}} onOpen={() => {}} />,
  ));
  expect(host.textContent).toContain(t(key));
  expect(host.textContent).not.toContain("HTTP 503");
  expect(host.textContent).not.toContain(t("knowledge-kblist.empty_projects"));
  expect(host.textContent).not.toContain("the knowledge base is off");
  expect(fetcher).toHaveBeenCalledTimes(1);
});
