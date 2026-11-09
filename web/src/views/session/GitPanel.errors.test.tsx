// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { GitCommit, GitStatus } from "../../lib/files";
import { t } from "../../i18n";

const live = vi.hoisted(() => ({ changed: () => {} }));
vi.mock("../../lib/use-fs-live", () => ({ useFsLive: (_mid: string, _cwd: string, _paths: string[], _git: boolean, changed: (value: { git: boolean }) => void) => { live.changed = () => changed({ git: true }); } }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { toast } from "../../ui/atoms";
import { GitPanel } from "./GitPanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Each fetch waits until the test answers it: `answer` with a 200 body, `fail` with an error status, or the abort.
type Pending = { path: string; signal?: AbortSignal | null; answer: (value: unknown) => void; fail: (status: number, error: string) => void };
let pending: Pending[];
let host: HTMLDivElement;
let root: Root;
const commit = (name: string): GitCommit => ({ hash: name, short: name, subject: name, author: "Author" });
const status = (name: string): GitStatus => ({ is_repo: true, branch: "main", head: commit(name), files: [], commits: Array.from({ length: 30 }, (_, index) => commit(`${name}-${index}`)) });
const button = (text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === text || b.getAttribute("aria-label") === text || b.title === text);

beforeEach(() => {
  pending = [];
  vi.mocked(toast).mockClear();
  vi.stubGlobal(
    "fetch",
    vi.fn(
      (input: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((resolve, reject) => {
          init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
          pending.push({
            path: String(input),
            signal: init?.signal,
            answer: (value) => resolve(new Response(JSON.stringify(value))),
            fail: (code, error) => resolve(new Response(JSON.stringify({ error }), { status: code })),
          });
        }),
    ),
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<GitPanel mid="computer" sid="session" cwd="/projects/demo" onClose={() => {}} />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("GitPanel failures with a status on screen", () => {
  it("reports a failed older-commits page", async () => {
    await act(async () => pending[0]!.answer(status("head")));
    act(() => button(t("session-gitpanel.load_older_commits"))!.click());
    const page = pending.at(-1)!;
    expect(page.path).toContain("/log?");
    await act(async () => page.fail(500, "log broke"));
    expect(toast).toHaveBeenCalledWith(expect.stringMatching(/（HTTP 500: log broke）$/), "error");
    expect(host.textContent).toContain("head-0");
    expect(button(t("session-gitpanel.load_older_commits"))!.disabled).toBe(false);
  });

  it("reports a failed manual reload and keeps the shown status", async () => {
    await act(async () => pending[0]!.answer(status("head")));
    act(() => button(t("session-gitpanel.reload"))!.click());
    const read = pending.at(-1)!;
    expect(read.path).toContain("/status?");
    await act(async () => read.fail(500, "status broke"));
    expect(toast).toHaveBeenCalledWith(expect.stringMatching(/（HTTP 500: status broke）$/), "error");
    expect(host.textContent).toContain("head-0");
  });

  it("keeps a failed silent fs-live read quiet", async () => {
    await act(async () => pending[0]!.answer(status("head")));
    act(() => live.changed());
    await act(async () => pending.at(-1)!.fail(500, "status broke"));
    expect(toast).not.toHaveBeenCalled();
    expect(host.textContent).toContain("head-0");
  });

  it("reports nothing for a reload aborted by a newer read", async () => {
    await act(async () => pending[0]!.answer(status("head")));
    act(() => button(t("session-gitpanel.reload"))!.click());
    const first = pending.at(-1)!;
    await act(async () => live.changed());
    expect(first.signal?.aborted).toBe(true);
    await act(async () => pending.at(-1)!.answer(status("head")));
    expect(toast).not.toHaveBeenCalled();
  });
});
