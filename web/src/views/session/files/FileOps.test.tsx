// @vitest-environment jsdom
// The Files panel's actions (#4883): a delete is asked first and then offers Undo, which puts it back; an upload whose
// name is taken asks and goes again as answered; what the hub can't do is not offered.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
});
const features = vi.hoisted(() => ({ list: ["upload_dir", "file_edit"] }));
vi.mock("../../../lib/store", async (original) => {
  const real = await original<typeof import("../../../lib/store")>();
  return { ...real, useStore: <T,>(pick: (s: { features: string[] }) => T) => pick({ features: features.list }) };
});
vi.mock("../../../lib/file-ops", async (original) => {
  const real = await original<typeof import("../../../lib/file-ops")>();
  return { ...real, uploadToDir: vi.fn(), trashEntries: vi.fn(), restoreEntries: vi.fn() };
});
import { ApiError, setToken } from "../../../lib/api";
import { restoreEntries, trashEntries, uploadToDir } from "../../../lib/file-ops";
import { useFileOps, type FileOps } from "./FileOps";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
let ops: FileOps;
const changed = vi.fn();
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => await Promise.resolve());
};
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const click = async (label: string) => {
  const button = [...dialog()!.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
  expect(button, label).toBeTruthy();
  await act(async () => button!.click());
  await flush();
};

function Harness() {
  ops = useFileOps({ mid: "m", cwd: "/home/me/x", onChanged: changed });
  return <>{ops.ui}</>;
}

beforeEach(async () => {
  setToken("example-file-ops-credential");
  features.list = ["upload_dir", "file_edit"];
  vi.mocked(uploadToDir).mockReset();
  vi.mocked(trashEntries).mockReset();
  vi.mocked(restoreEntries).mockReset();
  changed.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<Harness />));
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  setToken("");
});

describe("useFileOps", () => {
  it("asks before a delete, then offers 撤销, which puts it back", async () => {
    vi.mocked(trashEntries).mockResolvedValue([{ path: "/home/me/x/a.txt", trash_id: "t1" }]);
    vi.mocked(restoreEntries).mockResolvedValue(["/home/me/x/a.txt"]);
    await act(async () => ops.trash([{ path: "/home/me/x/a.txt", name: "a.txt", dir: false }]));
    expect(dialog()?.textContent).toContain("a.txt");
    expect(trashEntries).not.toHaveBeenCalled(); // asked first
    await click("移到废纸篓");
    expect(trashEntries).toHaveBeenCalledWith("m", ["/home/me/x/a.txt"], "/home/me/x");
    expect(changed).toHaveBeenCalledWith(["/home/me/x"]);
    // said in the panel, not in the page's toasts over the chat box
    const status = host.querySelector<HTMLElement>('[role="status"]')!;
    expect(status.textContent).toContain("「a.txt」已移到废纸篓");
    const undo = [...status.querySelectorAll("button")].find((b) => b.textContent?.trim() === "撤销");
    await act(async () => undo!.click());
    await flush();
    expect(restoreEntries).toHaveBeenCalledWith("m", ["t1"]);
    expect(status.textContent).toContain("放回原处");
  });

  it("asks about a name already there, and sends it again as answered", async () => {
    vi.mocked(uploadToDir)
      .mockRejectedValueOnce(new ApiError(409, "exists", { code: "exists", name: "a.txt" }))
      .mockResolvedValueOnce({ path: "/home/me/x/a 2.txt", name: "a 2.txt", size: 1 });
    await act(async () => ops.upload([new File(["x"], "a.txt")], "/home/me/x"));
    await flush();
    expect(dialog()?.textContent).toContain("a.txt");
    await click("保留两者");
    expect(vi.mocked(uploadToDir).mock.calls[1][2]).toMatchObject({ dir: "/home/me/x", conflict: "keep" });
    expect(changed).toHaveBeenCalledWith(["/home/me/x"]);
  });

  it("skips an upload the user does not want to replace", async () => {
    vi.mocked(uploadToDir).mockRejectedValueOnce(new ApiError(409, "exists", { code: "exists", name: "a.txt" }));
    await act(async () => ops.upload([new File(["x"], "a.txt")], "/home/me/x"));
    await flush();
    await click("不传");
    expect(uploadToDir).toHaveBeenCalledTimes(1);
    expect(dialog()).toBeNull();
  });

  it("offers only what the hub can do", async () => {
    const x = { path: "/home/me/x/a.txt", name: "a.txt", dir: false };
    expect(ops.rowItems(x, () => {}).map((i) => i.key)).toEqual(["open", "download", "rename", "move", "copy", "trash"]);
    features.list = ["file_stream"];
    await act(async () => root.render(<Harness />));
    expect(ops.rowItems(x, () => {}).map((i) => i.key)).toEqual(["open", "download", "copy"]);
    expect(ops.canUpload).toBe(false);
  });
});
