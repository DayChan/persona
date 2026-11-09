// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { GitStatus } from "../../lib/files";
import { StashesView, useGitWrite } from "./GitWrite";
import { t } from "../../i18n";
import { toast, Toasts } from "../../ui/atoms";

vi.mock("../../ui/atoms", async (original) => ({
  ...(await original<typeof import("../../ui/atoms")>()),
  toast: vi.fn(),
}));

(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;
vi.hoisted(() => {
  window.matchMedia = ((media: string) => ({
    matches: false,
    media,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});
const status: GitStatus = {
  is_repo: true,
  root: "/projects/demo",
  branch: "main",
  ahead: 0,
  behind: 0,
  commits: [],
  files: [],
  stashes: 1,
};
const A = {
  index: 0,
  oid: "a".repeat(40),
  message: "On main: selected stash A",
  date: "2026-01-01T00:00:00Z",
};
const B = {
  index: 0,
  oid: "b".repeat(40),
  message: "On main: external stash B",
  date: "2026-01-02T00:00:00Z",
};
type Entry = Omit<typeof A, "oid"> & { oid?: string };
let server: Entry[],
  requests: Record<string, unknown>[],
  fail: string | undefined;
let root: Root, host: HTMLDivElement;
const onStatus = vi.fn();
const json = (x: unknown, code = 200) =>
  new Response(JSON.stringify(x), {
    status: code,
    headers: { "Content-Type": "application/json" },
  });
async function flush() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
}
function Harness() {
  const writer = useGitWrite(
    "/machines/computer/git",
    "/projects/demo",
    onStatus,
  );
  return (
    <>
      <StashesView
        s={status}
        writer={writer}
        onBack={() => {}}
        onClose={() => {}}
      />
      <Toasts />
    </>
  );
}
async function click(text: string, scope: ParentNode = host) {
  const button = [...scope.querySelectorAll<HTMLButtonElement>("button")].find(
    (x) =>
      x.textContent?.trim() === text || x.getAttribute("aria-label") === text,
  );
  expect(button, text).toBeTruthy();
  await act(async () => button!.click());
  await flush();
}
beforeEach(async () => {
  server = [{ ...A }];
  requests = [];
  fail = undefined;
  onStatus.mockClear();
  vi.mocked(toast).mockClear();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, init: RequestInit = {}) => {
      const body = JSON.parse(String(init.body));
      requests.push(body);
      if (["drop", "pop", "apply"].includes(body.op)) {
        if (fail) return json({ code: fail, error: "Synthetic refusal" }, 409);
        if (server[body.index]?.oid !== body.oid)
          return json(
            { code: "git_stash_stale", error: "Refresh and reselect" },
            409,
          );
        if (body.op !== "apply") server.splice(body.index, 1);
      }
      return json({
        status: body.op === "list" ? null : status,
        stashes: server.map((x, index) => ({ ...x, index })),
      });
    }),
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<Harness />));
  await flush();
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

it("carries the confirmed object ID and refreshes a moved selection without deleting the new top", async () => {
  await click(t("session-gitwrite.stash_drop"));
  const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
  expect(dialog.textContent).toContain(A.message);
  server = [{ ...B }, { ...A, index: 1 }];
  await click(t("session-gitwrite.stash_drop"), dialog);
  expect(requests.find((x) => x.op === "drop")).toEqual({
    cwd: "/projects/demo",
    op: "drop",
    index: 0,
    oid: A.oid,
  });
  expect(server.map((x) => x.oid)).toEqual([B.oid, A.oid]);
  expect(requests.at(-1)?.op).toBe("list");
  expect(host.textContent).toContain(B.message);
  expect(document.body.textContent).toContain(
    t("session-gitwrite.refused_git_stash_stale"),
  );
  expect(toast).not.toHaveBeenCalled();
  await click(t("session-gitwrite.ok"), document);
  // Explicitly reselect A at its refreshed position; do not retry the old position.
  const row = [...host.querySelectorAll("div.flex.items-center")].find(
    (x) =>
      x.textContent?.includes(A.message) &&
      x.querySelector("button[aria-label]"),
  )!;
  await click(t("session-gitwrite.stash_drop"), row);
  await click(
    t("session-gitwrite.stash_drop"),
    document.querySelector("[role=dialog]")!,
  );
  expect(requests.filter((x) => x.op === "drop").at(-1)).toMatchObject({
    index: 1,
    oid: A.oid,
  });
  expect(server.map((x) => x.oid)).toEqual([B.oid]);
  expect(toast).toHaveBeenCalledWith(t("session-gitwrite.dropped"), "ok");
});

it.each(["apply", "pop"] as const)(
  "%s binds the object and recovers if an external drop removes the selection",
  async (op) => {
    server = [];
    await click(t(`session-gitwrite.stash_${op}`));
    expect(requests.find((x) => x.op === op)).toMatchObject({
      index: 0,
      oid: A.oid,
    });
    expect(requests.at(-1)?.op).toBe("list");
    expect(host.textContent).toContain(t("session-gitwrite.no_stashes"));
    expect(document.body.textContent).toContain(
      t("session-gitwrite.refused_git_stash_stale"),
    );
  },
);

it.each(["apply", "pop", "drop"] as const)(
  "preserves healthy %s and cancellation",
  async (op) => {
    await click(t("session-gitwrite.stash_drop"));
    await click(t("common.cancel"), document.querySelector("[role=dialog]")!);
    expect(requests).toHaveLength(1);
    await click(t(`session-gitwrite.stash_${op}`));
    if (op === "drop")
      await click(
        t("session-gitwrite.stash_drop"),
        document.querySelector("[role=dialog]")!,
      );
    expect(requests.at(-1)).toMatchObject({ op, index: 0, oid: A.oid });
    expect(server).toHaveLength(op === "apply" ? 1 : 0);
    expect(onStatus).toHaveBeenCalledExactlyOnceWith(status);
  },
);

it.each([undefined, "", "short", "--all"])(
  "gates missing or invalid old-node identities (%s) before any write",
  async (oid) => {
    server = [{ ...A, oid }];
    await click(t("session-gitwrite.stash_refresh"));
    expect(host.textContent).toContain(t("session-gitwrite.stash_old_node"));
    for (const key of ["stash_apply", "stash_pop", "stash_drop"]) {
      await click(t(`session-gitwrite.${key}`));
      const button = [
        ...host.querySelectorAll<HTMLButtonElement>("button"),
      ].find(
        (x) =>
          x.textContent?.trim() === t(`session-gitwrite.${key}`) ||
          x.getAttribute("aria-label") === t(`session-gitwrite.${key}`),
      )!;
      expect(button.disabled).toBe(true);
    }
    expect(requests.every((x) => x.op === "list")).toBe(true);
    expect(onStatus).not.toHaveBeenCalled();
    // An upgraded node can supply identities on a manual read.
    server = [{ ...A }];
    await click(t("session-gitwrite.stash_refresh"));
    expect(host.textContent).not.toContain(
      t("session-gitwrite.stash_old_node"),
    );
  },
);

it.each(["git_conflict", "git_busy", "git_stash_unsupported"])(
  "retains errors and refresh/reselect after %s",
  async (code) => {
    fail = code;
    await click(t("session-gitwrite.stash_pop"));
    expect(requests.at(-1)?.op).toBe("list");
    expect(server).toHaveLength(1);
    expect(document.body.textContent).toContain(
      t(`session-gitwrite.refused_${code}`),
    );
    expect(toast).not.toHaveBeenCalled();
    await click(t("session-gitwrite.ok"), document);
    fail = undefined;
    await click(t("session-gitwrite.stash_pop"));
    expect(server).toHaveLength(0);
  },
);
