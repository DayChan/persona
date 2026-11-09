import { describe, expect, it, vi } from "vitest";
import { itemsToCommands, nodesFor, setKnownTree, getAsk, answerAsk, subscribeAsk, nodeCommands } from "./node-palette";
import { contextCommands } from "./palette";
import { nodeActions, type MenuNode, type NodeActionDeps } from "./node-actions";
import { buildTerminalTree } from "./terminals";
import { getState } from "./store";

vi.mock("../ui/atoms", () => ({ toast: vi.fn(), copyText: vi.fn() }));

const deps = (over: Partial<NodeActionDeps> = {}): NodeActionDeps => ({
  post: vi.fn(async () => ({})),
  renameWorkspace: vi.fn(async () => {}),
  renameTab: vi.fn(async () => {}),
  launchers: vi.fn(async () => []),
  newAgent: vi.fn(),
  copy: vi.fn(),
  notify: vi.fn(),
  changed: vi.fn(),
  ...over,
});

const tab: MenuNode = { kind: "tab", mid: "m1", host: "herdr", label: "build", workspaceId: "w1", tabId: "w1:t2", paneId: "w1:p2", agent: "", sessionId: "", cwd: "/home/me/proj", branch: "main" };

/** Answers the next question the commands ask. */
function answerWith(value: string | boolean) {
  const off = subscribeAsk(() => {
    const ask = getAsk();
    if (!ask) return;
    off();
    answerAsk();
    (ask.resolve as (v: unknown) => void)(value);
  });
}

const flush = () => new Promise((r) => setTimeout(r, 0));

describe("a node's items as palette commands", () => {
  it("lists them under the section, the submenus flattened and the danger ones marked", () => {
    const cmds = itemsToCommands("Workspace · build", tab, nodeActions(tab, deps()));
    const titles = cmds.map((c) => c.title);
    expect(titles).toContain("重命名");
    expect(titles).toContain("新建窗格 · 向右拆分");
    expect(titles).toContain("Git · 拉取");
    expect(titles).toContain("Git · 复制分支名");
    expect(titles).toContain("复制路径");
    expect(cmds.every((c) => c.section === "Workspace · build")).toBe(true);
    expect(new Set(cmds.map((c) => c.id)).size).toBe(cmds.length);
    expect(cmds.find((c) => c.title === "关闭标签页")?.danger).toBe(true);
    expect(cmds.find((c) => c.title === "复制路径")?.danger).toBeFalsy();
  });

  it("a rename asks for the name, and commits a changed one", async () => {
    const d = deps();
    const cmd = itemsToCommands("s", tab, nodeActions(tab, d)).find((c) => c.title === "重命名")!;
    answerWith("deploy");
    cmd.run();
    await flush();
    expect(d.renameTab).toHaveBeenCalledWith(expect.objectContaining({ tabId: "w1:t2" }), "deploy");

    const d2 = deps();
    const again = itemsToCommands("s", tab, nodeActions(tab, d2)).find((c) => c.title === "重命名")!;
    answerWith("build"); // the name it has: nothing to do
    again.run();
    await flush();
    expect(d2.renameTab).not.toHaveBeenCalled();
  });

  it("a close asks once more, and does nothing unless it is confirmed", async () => {
    const d = deps();
    const close = itemsToCommands("s", tab, nodeActions(tab, d)).find((c) => c.title === "关闭标签页")!;
    answerWith(false);
    close.run();
    await flush();
    expect(d.post).not.toHaveBeenCalled();
    answerWith(true);
    close.run();
    await flush();
    expect(d.post).toHaveBeenCalledWith("m1", "tabs/close", { tab_id: "w1:t2", kind: "herdr" });
  });

  it("a push asks with its own question, and pushes only when confirmed (#1883)", async () => {
    const d = deps();
    const push = itemsToCommands("s", tab, nodeActions(tab, d)).find((c) => c.title === "Git · 推送")!;
    let asked: { title?: string; body?: string; label?: string } = {};
    const off = subscribeAsk(() => {
      const a = getAsk();
      if (a?.kind === "confirm") asked = { title: a.title, body: a.body, label: a.label };
    });
    answerWith(false);
    push.run();
    await flush();
    off();
    expect(asked).toEqual({ title: "推送", body: "main 会推到它的上游分支。不会强制推送：远端有更新的提交时会停下并说明。", label: "确认推送" });
    expect(d.post).not.toHaveBeenCalled();
    answerWith(true);
    push.run();
    await flush();
    expect(d.post).toHaveBeenCalledWith("m1", "panes/git", { op: "push", pane: "w1:p2" });
    const pull = itemsToCommands("s", tab, nodeActions(tab, d)).find((c) => c.title === "Git · 拉取")!;
    pull.run();
    await flush();
    expect(d.post).toHaveBeenCalledWith("m1", "panes/git", { op: "pull", pane: "w1:p2" });
  });

  it("a plain item just runs", async () => {
    const d = deps();
    itemsToCommands("s", tab, nodeActions(tab, d)).find((c) => c.title === "复制路径")!.run();
    await flush();
    expect(d.copy).toHaveBeenCalledWith("/home/me/proj");
  });
});

describe("the node the user is on", () => {
  const res = {
    hosts: [{
      kind: "herdr", stream: true, control: true, new_tab: true,
      workspaces: [{ id: "w1", label: "proj", cwd: "/home/me/proj", branch: "main" }],
      tabs: [{ id: "w1:t2", workspace_id: "w1", label: "shell" }],
      panes: [{ pane: "w1:p2", terminal: "pane:w1:p2", tab_id: "w1:t2", workspace_id: "w1", title: "zsh", cwd: "/home/me/proj" }],
    }],
  };

  it("a terminal page's pane is its tab and its workspace", () => {
    setKnownTree("m1", buildTerminalTree(res, []));
    const where = nodesFor({ route: { name: "terminal", mid: "m1", id: "pane:w1:p2" } });
    expect(where.map((w) => [w.section, w.node.kind])).toEqual([["Workspace · zsh", "tab"], ["Workspace · proj", "workspace"]]);
    expect(where[0].node).toMatchObject({ tabId: "w1:t2", paneId: "w1:p2", agent: "" });
  });

  it("is registered with the palette, and offers nothing where there is no node", () => {
    setKnownTree("m1", buildTerminalTree(res, []));
    const cmds = contextCommands({ route: { name: "terminal", mid: "m1", id: "pane:w1:p2" } });
    expect(cmds.some((c) => c.title === "关闭标签页" && c.section === "Workspace · zsh")).toBe(true);
    expect(nodeCommands({ route: { name: "home" } })).toEqual([]);
    expect(nodeCommands({ route: { name: "terminal", mid: "m1", id: "pane:w9:p9" } })).toEqual([]);
  });

  it("offers 新建 worktree… for the workspace when its host can make worktrees (#2431)", () => {
    const withWt = { hosts: [{ ...res.hosts[0], worktree: true }] };
    setKnownTree("m1", buildTerminalTree(withWt, []));
    const cmds = contextCommands({ route: { name: "terminal", mid: "m1", id: "pane:w1:p2" } });
    expect(cmds.filter((c) => c.title === "新建 worktree").map((c) => c.section)).toEqual(["Workspace · proj"]);
    // an older node does not say, so the command is not there
    setKnownTree("m1", buildTerminalTree(res, []));
    expect(contextCommands({ route: { name: "terminal", mid: "m1", id: "pane:w1:p2" } }).some((c) => c.title === "新建 worktree")).toBe(false);
  });

  it("finds the workspace of an agent's session in the page's list too", () => {
    setKnownTree("m1", buildTerminalTree({ hosts: [{ ...res.hosts[0], worktree: true }] }, []));
    const session = { machine_id: "m1", session_id: "s1", cwd: "/home/me/proj", title: "t", status: "idle", space_kind: "herdr", space_id: "w1", space_name: "proj", pane_id: "w1:p1" };
    const state = getState();
    state.sessions = { m1: [session as never] };
    const where = nodesFor({ route: { name: "session", mid: "m1", sid: "s1" }, pane: { mid: "m1", sid: "s1" } });
    expect(where.find((w) => w.node.kind === "workspace")?.node.canWorktree).toBe(true);
  });
});
