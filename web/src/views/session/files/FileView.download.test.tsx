// @vitest-environment jsdom
// Downloading a paged text file: the node sends text a page at a time, so a Blob of the pages read here could be a
// truncated copy (a read cap, or a page that fails). The download goes through the hub's file/download instead,
// which serves the whole file; a Blob is built only from content that is fully loaded.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
// SidePanel's task and subagent panels pull in the whole chat view; nothing here opens them.
vi.mock("../../assistant/TaskPanel", () => ({ AgentPanel: () => null, TaskPanel: () => null }));
vi.mock("../../chat/SubagentPanel", () => ({ SubagentPanel: () => null }));
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { t } from "../../../i18n";
import type { FileContent } from "../../../lib/files";
import { bytes } from "../../../lib/format";

const pages: ((path: string) => Promise<unknown>)[] = [];
const requested: string[] = [];
vi.mock("../../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../../lib/api")>()),
  api: (path: string) => {
    requested.push(path);
    const next = pages.shift();
    return next ? next(path) : Promise.reject(new Error("unexpected request " + path));
  },
}));
vi.mock("../../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../../ui/atoms")>()), toast: () => {} }));

const { FileView } = await import("./FileView");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const text = (over: Partial<FileContent>): FileContent => ({ path: "/w/big.log", name: "big.log", size: 300 << 20, mime: "text/plain", truncated: false, is_dir: false, ...over });
const byLabel = (label: string) =>
  [...document.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === label || b.textContent?.trim() === label) as HTMLButtonElement | undefined;
const flush = async () => {
  for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
};

// jsdom's Blob has no text(); FileReader reads it.
const readBlob = (b: Blob) =>
  new Promise<string>((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(r.error);
    r.readAsText(b);
  });

let host: HTMLDivElement;
let root: Root;
let clicked: { href: string; name: string }[] = [];
let blobs: Blob[] = [];

beforeEach(() => {
  pages.length = 0;
  requested.length = 0;
  clicked = [];
  blobs = [];
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  URL.createObjectURL = (b: Blob | MediaSource) => {
    blobs.push(b as Blob);
    return "blob:x";
  };
  URL.revokeObjectURL = () => {};
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    clicked.push({ href: this.getAttribute("href") ?? "", name: this.download });
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function open(path = "/w/big.log", line?: number) {
  await act(async () => root.render(<FileView mid="m1" path={path} cwd="/w" line={line} onClose={() => {}} onOpenPath={() => {}} />));
  await flush();
}

async function click(label: string) {
  const button = byLabel(label);
  expect(button, label).toBeTruthy();
  await act(async () => button!.click());
  await flush();
}

async function openAndDownload() {
  await open();
  const button = byLabel(t("session-files-fileview.download"));
  expect(button, "download button").toBeTruthy();
  await act(async () => button!.click());
  await flush();
}

describe("FileView download", () => {
  it("saves a paged text file through file/download, not a Blob of the pages read", async () => {
    pages.push(async () => text({ text: "page one\n", next: 2 << 20, revision: "r1" }));
    pages.push(async () => text({ text: "page two\n", next: 0, revision: "r1" }));
    await openAndDownload();
    expect(blobs).toEqual([]);
    expect(clicked).toHaveLength(1);
    expect(clicked[0].href).toContain("/api/v1/machines/m1/file/download?path=%2Fw%2Fbig.log&cwd=%2Fw");
    expect(clicked[0].name).toBe("big.log");
    expect(requested).toHaveLength(1); // the rest is not read into the page first
  });

  it("does not save a partial copy when a later page would fail", async () => {
    pages.push(async () => text({ text: "page one\n", next: 2 << 20, revision: "r1" }));
    pages.push(async () => Promise.reject(new Error("boom")));
    await openAndDownload();
    expect(blobs).toEqual([]);
    expect(clicked.map((c) => c.href)).toEqual([expect.stringContaining("/file/download?")]);
  });

  it("keeps the Blob of the pages for an older node without file.chunk (no revision)", async () => {
    pages.push(async () => text({ text: "page one\n", next: 9 }));
    pages.push(async () => text({ text: "page two\n", offset: 9, next: 0 }));
    await openAndDownload();
    expect(clicked.map((c) => c.href)).toEqual(["blob:x"]); // file/download would answer 409 node_outdated there
    expect(blobs).toHaveLength(1);
    expect(await readBlob(blobs[0])).toBe("page one\npage two\n");
  });

  it("still saves a fully loaded text file from what it holds", async () => {
    pages.push(async () => text({ text: "all of it\n", size: 10, next: 0 }));
    await openAndDownload();
    expect(blobs).toHaveLength(1);
    expect(await readBlob(blobs[0])).toBe("all of it\n");
    expect(clicked).toEqual([{ href: "blob:x", name: "big.log" }]);
  });

  it.each(["empty-r1", undefined])("downloads an empty text file when the node omits text (revision %s)", async (revision) => {
    // Go's omitempty removes text and data from a successful zero-byte text response.
    pages.push(async () => text({ size: 0, mime: "text/plain; charset=utf-8", offset: 0, revision }));
    await openAndDownload();
    expect(blobs).toHaveLength(1);
    expect(blobs[0].size).toBe(0);
    expect(clicked).toEqual([{ href: "blob:x", name: "big.log" }]);
  });

  it("does not save a failed legacy read as a partial Blob and can retry the download", async () => {
    pages.push(async () => text({ text: "page one\n", next: 9 }));
    pages.push(async () => { throw new Error("synthetic download failure"); });
    await openAndDownload();
    expect(blobs).toEqual([]);
    expect(clicked).toEqual([]);
    expect(byLabel(t("session-files-fileview.download"))?.disabled).toBe(false);
    pages.push(async () => text({ text: "page two\n", next: 0 }));
    await click(t("session-files-fileview.download"));
    expect(requested[2]).toBe(requested[1]);
    expect(blobs).toHaveLength(1);
    expect(await readBlob(blobs[0])).toBe("page one\npage two\n");
  });

  it("does not export a known revision conflict after an unversioned first page", async () => {
    pages.push(async () => text({ text: "unversioned prefix\n", next: 4 }));
    pages.push(async () => text({ text: "r1 tail\n", next: 8, revision: "r1" }));
    pages.push(async () => text({ text: "r2 tail\n", next: 0, revision: "r2" }));
    await openAndDownload();
    expect(blobs).toEqual([]);
    expect(clicked).toEqual([]);
    expect(host.textContent).toContain(t("session-files-fileview.file_changed"));
    expect(byLabel(t("session-files-fileview.download"))?.disabled).toBe(true);
  });

  it("does not export a capped legacy read or a terminal truncated response", async () => {
    pages.push(async () => text({ text: "only a prefix\n", next: 64 << 20 }));
    await openAndDownload();
    expect(requested).toHaveLength(1);
    expect(blobs).toEqual([]);
    expect(clicked).toEqual([]);
    pages.push(async () => text({ text: "truncated\n", next: 0, truncated: true }));
    await open("/w/other.log");
    await click(t("session-files-fileview.download"));
    expect(blobs).toEqual([]);
    expect(clicked).toEqual([]);
  });

  it("does not download a late old-path read under the previous file name", async () => {
    let finish!: (file: FileContent) => void;
    pages.push(async () => text({ text: "old prefix\n", next: 4 }));
    pages.push(() => new Promise((resolve) => { finish = resolve; }));
    await openAndDownload();
    pages.push(async () => text({ path: "/w/other.log", text: "current file\n", next: 0 }));
    await open("/w/other.log");
    await act(async () => finish(text({ text: "old tail\n", next: 0 })));
    await flush();
    expect(blobs).toEqual([]);
    expect(clicked).toEqual([]);
  });
});

describe("FileView paging recovery", () => {
  it("offers reload after a mismatch and displays only the newly read version", async () => {
    pages.push(async () => text({ text: "OLD prefix\n", next: 4, revision: "r1" }));
    pages.push(async () => text({ text: "NEW tail\n", next: 0, revision: "r2" }));
    await open();
    await click(t("session-files-fileview.load_more"));
    expect(host.textContent).toContain(t("session-files-fileview.file_changed_detail"));
    expect(host.textContent).not.toContain("NEW tail");
    expect(byLabel(t("session-files-fileview.load_more"))).toBeUndefined();
    expect(byLabel(t("session-files-fileview.download"))?.disabled).toBe(true);
    pages.push(async () => text({ text: "NEW whole\n", next: 0, revision: "r2" }));
    await click(t("session-files-fileview.reload"));
    expect(requested[2]).not.toContain("offset=");
    expect(host.textContent).toContain("NEW whole");
    expect(host.textContent).not.toContain("OLD prefix");
    expect(byLabel(t("session-files-fileview.download"))?.disabled).toBe(false);
  });

  it("keeps the loaded prefix visible and offers retry after a page failure", async () => {
    pages.push(async () => text({ text: "kept prefix\n", next: 4, revision: "r1" }));
    pages.push(async () => { throw new Error("synthetic read failure"); });
    await open();
    await click(t("session-files-fileview.load_more"));
    expect(host.textContent).toContain("kept prefix");
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("synthetic read failure");
    pages.push(async () => text({ text: "retried tail\n", next: 0, revision: "r1" }));
    await click(t("session-files-fileview.retry"));
    expect(requested[2]).toBe(requested[1]);
    expect(host.textContent).toContain("kept prefix");
    expect(host.textContent).toContain("retried tail");
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it("can retry automatic whole-file reads without an unhandled rejection", async () => {
    const json = (over: Partial<FileContent>) => text({ path: "/w/example.json", name: "example.json", mime: "application/json", size: 20, ...over });
    pages.push(async () => json({ text: '{"ready":', next: 9, revision: "r1" }));
    pages.push(async () => { throw new Error("synthetic automatic failure"); });
    await open("/w/example.json");
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("synthetic automatic failure");
    pages.push(async () => json({ text: 'true}', next: 0, revision: "r1" }));
    await click(t("session-files-fileview.retry"));
    expect(requested[2]).toBe(requested[1]);
    expect(host.textContent).toContain('"ready"');
    expect(host.textContent).toContain("true");
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it("stops automatic JSON reads on a revision change instead of parsing a mixed document", async () => {
    const json = (over: Partial<FileContent>) => text({ path: "/w/example.json", name: "example.json", mime: "application/json", size: 20, ...over });
    pages.push(async () => json({ text: '{"old":', next: 7, revision: "r1" }));
    pages.push(async () => json({ text: 'true}', next: 0, revision: "r2" }));
    await open("/w/example.json");
    expect(host.textContent).toContain(t("session-files-fileview.file_changed"));
    expect(host.textContent).not.toContain('"old"');
    expect(byLabel(t("session-files-fileview.reload"))).toBeTruthy();
    expect(requested).toHaveLength(2);
  });

  it("retries a complete CSV read before parsing it, retaining accepted pages", async () => {
    const csv = (over: Partial<FileContent>) => text({ path: "/w/example.csv", name: "example.csv", mime: "text/csv", size: 40, ...over });
    pages.push(async () => csv({ text: "name,value\n", next: 11, revision: "r1" }));
    pages.push(async () => { throw new Error("synthetic CSV page failure"); });
    await open("/w/example.csv");
    expect(host.textContent).toContain(t("session-files-fileview.sheet_open_failed"));
    pages.push(async () => csv({ text: "synthetic,42\n", next: 0, revision: "r1" }));
    await click(t("session-files-fileview.retry"));
    expect(requested[2]).toBe(requested[1]);
    expect(host.textContent).toContain("synthetic");
    expect(host.textContent).toContain("42");
    expect(host.textContent).not.toContain(t("session-files-fileview.sheet_open_failed"));
  });

  it("keeps a capped CSV preview explicitly partial without requesting the rest", async () => {
    const chunk = "a,b\n".repeat(1 << 19); // the node's 2 MiB pages
    let parsed = 0;
    vi.stubGlobal("Worker", class {
      onmessage: ((event: MessageEvent) => void) | null = null;
      terminate() {}
      postMessage(req: { text: string }) {
        parsed = req.text.length;
        queueMicrotask(() => this.onmessage?.({ data: { sheets: [{ name: "Demo", rows: [["synthetic", "42"]], cols: 2, merges: [], widths: [], cut: false }] } } as MessageEvent));
      }
    });
    for (let i = 0; i < 32; i++) {
      const offset = i * chunk.length;
      pages.push(async () => text({ path: "/w/example.csv", name: "example.csv", mime: "text/csv", size: 66 << 20, text: chunk, offset, next: offset + chunk.length, revision: "r1", truncated: true }));
    }
    await open("/w/example.csv");
    expect(parsed).toBe(64 << 20);
    expect(host.textContent).toContain("synthetic");
    expect(host.textContent).toContain("42");
    expect(host.textContent).toContain(t("session-files-fileview.sheet_partial", { size: bytes(64 << 20) }));
    expect(host.textContent).not.toContain(t("session-files-fileview.sheet_open_failed"));
    expect(requested).toHaveLength(32);
    expect(requested[31]).toContain(`offset=${62 << 20}`);
  });

  it("rejects a changed CSV page before parsing even a bounded preview", async () => {
    const csv = (over: Partial<FileContent>) => text({ path: "/w/example.csv", name: "example.csv", mime: "text/csv", size: 66 << 20, ...over });
    pages.push(async () => csv({ text: "name,value\n", next: 11, revision: "r1" }));
    pages.push(async () => csv({ text: "changed,42\n", next: 22, revision: "r2" }));
    await open("/w/example.csv");
    expect(host.textContent).toContain(t("session-files-fileview.file_changed"));
    expect(host.textContent).not.toContain("changed,42");
    expect(byLabel(t("session-files-fileview.download"))?.disabled).toBe(true);
    expect(requested).toHaveLength(2);
  });

  it("keeps CSV continuation recovery available in source mode", async () => {
    const csv = (over: Partial<FileContent>) => text({ path: "/w/example.csv", name: "example.csv", mime: "text/csv", size: 40, ...over });
    pages.push(async () => csv({ text: "name,value\n", next: 11, revision: "r1" }));
    pages.push(async () => { throw new Error("synthetic CSV source failure"); });
    await open("/w/example.csv");
    await click(t("session-files-fileview.mode_source_file"));
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("synthetic CSV source failure");
    pages.push(async () => csv({ text: "synthetic,42\n", next: 0, revision: "r1" }));
    await click(t("session-files-fileview.retry"));
    expect(requested[2]).toBe(requested[1]);
    expect(host.textContent).toContain("synthetic,42");
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it("stops automatic line seeking after a page failure until retry", async () => {
    pages.push(async () => text({ text: "first line\n", next: 4, revision: "r1" }));
    pages.push(async () => { throw new Error("synthetic line failure"); });
    await open("/w/big.log", 2);
    expect(requested).toHaveLength(2);
    pages.push(async () => text({ text: "target line\n", next: 0, revision: "r1" }));
    await click(t("session-files-fileview.retry"));
    expect(requested).toHaveLength(3);
    expect(host.textContent).toContain("target line");
  });
});
