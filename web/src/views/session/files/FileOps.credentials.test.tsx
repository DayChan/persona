// @vitest-environment jsdom
// Exercise the mounted hook and real uploadToDir together: mocking the helper hides its token lookup.
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../../../lib/api";
import { getState } from "../../../lib/store";
import { useFileOps, type FileOps } from "./FileOps";
import { t } from "../../../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
window.matchMedia ??= ((media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;

class UploadXHR {
  static requests: UploadXHR[] = [];
  url = "";
  headers: Record<string, string> = {};
  file?: File;
  status = 0;
  responseText = "";
  aborted = false;
  ignoreAbort = false;
  upload: { onprogress?: (e: { loaded: number; total: number; lengthComputable: boolean }) => void } = {};
  onload?: () => void;
  onerror?: () => void;
  onabort?: () => void;
  open(_method: string, url: string) { this.url = url; }
  setRequestHeader(key: string, value: string) { this.headers[key] = value; }
  getResponseHeader() { return null; }
  send(file: File) { this.file = file; UploadXHR.requests.push(this); }
  abort() { this.aborted = true; if (!this.ignoreAbort) this.onabort?.(); }
  progress(sent: number) { this.upload.onprogress?.({ loaded: sent, total: this.file!.size, lengthComputable: true }); }
  answer(status = 200) {
    this.status = status;
    this.responseText = JSON.stringify(status === 409 ? { code: "exists", error: "exists", name: this.file!.name } : { path: `/work/${this.file!.name}`, name: this.file!.name, size: this.file!.size });
    this.onload?.();
  }
}

let host: HTMLDivElement, root: Root, ops: FileOps;
const changed = vi.fn();
const file = (name: string) => new File(["synthetic file contents"], name);
const requests = () => UploadXHR.requests;
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const dialog = () => document.querySelector('[role="dialog"]');
const tray = () => host.querySelector("section");
function Harness() {
  ops = useFileOps({ mid: "demo-machine", cwd: "/work", onChanged: changed });
  return <div data-same-panel>{ops.ui}</div>;
}
async function select(...files: File[]) { await act(async () => ops.upload(files, "/work")); await flush(); }
async function replace(token = "example-credential-B", storage = true) {
  await act(async () => {
    if (storage) {
      const oldValue = localStorage.getItem("persona.token");
      localStorage.setItem("persona.token", token);
      window.dispatchEvent(new StorageEvent("storage", { key: "persona.token", oldValue, newValue: token, storageArea: localStorage }));
    } else setToken(token);
  });
  await flush();
}
async function click(text: string) {
  const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === text || b.getAttribute("aria-label") === text);
  expect(button, text).toBeTruthy();
  await act(async () => button!.click());
  await flush();
}
beforeEach(async () => {
  setToken("example-credential-A");
  Object.assign(getState(), { features: ["upload_dir", "file_edit"] });
  UploadXHR.requests = [];
  vi.stubGlobal("XMLHttpRequest", UploadXHR);
  changed.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<StrictMode><Harness /></StrictMode>));
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  setToken("");
  vi.unstubAllGlobals();
});

describe("file upload credential ownership", () => {
  it.each(["success", "error", "conflict", "abort"])("retires a two-file queue on real storage replacement despite late %s", async (late) => {
    const first = file("first-A.txt"), second = file("queued-A.txt");
    await select(first, second);
    expect(requests()).toHaveLength(1);
    const old = requests()[0];
    expect(old.file).toBe(first);
    expect(old.headers.Authorization).toBe("Bearer example-credential-A");
    old.ignoreAbort = true;
    const panel = host.querySelector("[data-same-panel]");
    await replace();
    expect(host.querySelector("[data-same-panel]")).toBe(panel);
    expect(old.aborted).toBe(true);
    expect(tray()).toBeNull();
    expect(dialog()).toBeNull();
    const fresh = file("fresh-B.txt");
    await select(fresh);
    expect(requests()).toHaveLength(2);
    const current = requests()[1];
    expect(current.file).toBe(fresh);
    expect(current.headers.Authorization).toBe("Bearer example-credential-B");
    await act(async () => {
      old.progress(20);
      if (late === "error") old.onerror?.();
      else if (late === "abort") old.onabort?.();
      else old.answer(late === "conflict" ? 409 : 200);
    });
    await flush();
    expect(requests()).toHaveLength(2);
    expect(changed).not.toHaveBeenCalled();
    expect(host.textContent).not.toContain("A.txt");
    expect(dialog()).toBeNull();
    await act(async () => current.answer());
    await flush();
    expect(changed).toHaveBeenCalledExactlyOnceWith(["/work"]);
    expect(host.innerHTML).not.toContain("example-credential");
  });

  it.each([409, 500])("retires actionable conflict/failure after HTTP %s and rejects saved old controls", async (status) => {
    await select(file("retired-A.txt"));
    await act(async () => requests()[0].answer(status));
    await flush();
    expect(status === 409 ? dialog() : tray()).not.toBeNull();
    const saved = [...document.querySelectorAll("button")].find((b) => status === 409 ? b.textContent === t("files-ops.replace") : b.getAttribute("aria-label") === t("files-ops.upload_retry", { name: "retired-A.txt" }));
    expect(saved).toBeTruthy();
    await replace();
    expect(dialog()).toBeNull();
    expect(tray()).toBeNull();
    await act(async () => saved!.click());
    await flush();
    expect(requests()).toHaveLength(1);
    await select(file("fresh-B.txt"));
    expect(requests()).toHaveLength(2);
    expect(requests()[1].headers.Authorization).toBe("Bearer example-credential-B");
  });

  it("retires same-document work on sign-out and on replacement, even when the original token returns", async () => {
    await select(file("first-A.txt"), file("queued-A.txt"));
    const staleUpload = ops.upload;
    await replace("", false);
    expect(requests()[0].aborted).toBe(true);
    expect(tray()).toBeNull();
    await select(file("signed-out.txt"));
    expect(requests()).toHaveLength(1);
    await replace("example-credential-A", false);
    await act(async () => staleUpload([file("stale-handler.txt")], "/work"));
    await flush();
    expect(requests()).toHaveLength(1);
    await select(file("new-selection.txt"));
    expect(requests()).toHaveLength(2);
    await replace("example-credential-B", false);
    expect(requests()[1].aborted).toBe(true);
    expect(tray()).toBeNull();
  });

  it("binds a native chooser to the owner that opened it", async () => {
    await act(async () => ops.choose("/work"));
    await replace();
    const input = host.querySelector<HTMLInputElement>("input[type=file]")!;
    Object.defineProperty(input, "files", { configurable: true, value: [file("stale-picker-A.txt")] });
    await act(async () => input.dispatchEvent(new Event("change", { bubbles: true })));
    await flush();
    expect(requests()).toHaveLength(0);
    await act(async () => ops.choose("/work"));
    const fresh = file("fresh-picker-B.txt");
    Object.defineProperty(input, "files", { configurable: true, value: [fresh] });
    await act(async () => input.dispatchEvent(new Event("change", { bubbles: true })));
    await flush();
    expect(requests()).toHaveLength(1);
    expect(requests()[0].file).toBe(fresh);
    expect(requests()[0].headers.Authorization).toBe("Bearer example-credential-B");
  });

  it("does not dispatch admission batched with an owner change", async () => {
    await act(async () => {
      ops.upload([file("batched-A.txt")], "/work");
      setToken("example-credential-B");
    });
    await flush();
    expect(requests()).toHaveLength(0);
    expect(tray()).toBeNull();
    await select(file("fresh-B.txt"));
    expect(requests()).toHaveLength(1);
    expect(requests()[0].headers.Authorization).toBe("Bearer example-credential-B");
  });

  it("retires conflicted, failed, sending and queued files together", async () => {
    await select(file("conflict-A.txt"), file("failed-A.txt"), file("sending-A.txt"), file("queued-A.txt"));
    await act(async () => requests()[0].answer(409));
    await flush();
    await act(async () => requests()[1].answer(500));
    await flush();
    expect(requests()).toHaveLength(3);
    expect(dialog()?.textContent).toContain("conflict-A.txt");
    expect(tray()?.textContent).toContain("failed-A.txt");
    await replace();
    expect(requests()[2].aborted).toBe(true);
    expect(dialog()).toBeNull();
    expect(tray()).toBeNull();
    await select(file("fresh-B.txt"));
    expect(requests()).toHaveLength(4);
    expect(requests()[3].file?.name).toBe("fresh-B.txt");
    expect(requests()[3].headers.Authorization).toBe("Bearer example-credential-B");
  });

  it("preserves same-owner progress, serial files, per-file cancel and failure/retry", async () => {
    const first = file("first.txt"), cancelled = file("cancelled.txt"), failed = file("retry.txt");
    await select(first, cancelled, failed);
    await act(async () => requests()[0].progress(11));
    expect(host.querySelector('[aria-label="first.txt"][role="progressbar"]')?.getAttribute("aria-valuenow")).toBe(String(Math.round(11 / first.size * 100)));
    await replace("example-credential-A", false);
    expect(requests()[0].aborted).toBe(false);
    await click(t("files-ops.upload_cancel", { name: "cancelled.txt" }));
    await act(async () => requests()[0].answer());
    await flush();
    expect(requests()).toHaveLength(2);
    expect(requests()[1].file).toBe(failed);
    await act(async () => requests()[1].answer(500));
    await flush();
    await click(t("files-ops.upload_retry", { name: "retry.txt" }));
    expect(requests()).toHaveLength(3);
    expect(requests()[2].file).toBe(failed);
    await click(t("files-ops.upload_cancel", { name: "retry.txt" }));
    expect(requests()[2].aborted).toBe(true);
    expect(changed).toHaveBeenCalledTimes(1);
    expect(requests().every((x) => x.headers.Authorization === "Bearer example-credential-A")).toBe(true);
  });

  it.each([["replace", "replace"], ["keep_both", "keep"]])("preserves same-owner %s conflict handling without duplicate dispatch", async (label, conflict) => {
    const selected = file("exists.txt");
    await select(selected);
    await act(async () => requests()[0].answer(409));
    await flush();
    await click(t(`files-ops.${label}`));
    expect(requests()).toHaveLength(2);
    expect(requests()[1].file).toBe(selected);
    expect(requests()[1].url).toContain(`&conflict=${conflict}`);
    expect(requests()[1].headers.Authorization).toBe("Bearer example-credential-A");
    await act(async () => requests()[1].answer());
    await flush();
    expect(changed).toHaveBeenCalledExactlyOnceWith(["/work"]);
    expect(dialog()).toBeNull();
  });
});
