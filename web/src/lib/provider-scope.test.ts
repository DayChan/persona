import { describe, expect, it } from "vitest";
import {
  addableWork,
  choosable,
  formScope,
  harnessGroups,
  harnessLabel,
  keyMachines,
  newScope,
  scopeBody,
  scopeFromView,
  scopeProblem,
  scopeShown,
  scopeSummary,
  type ScopeForm,
  type ScopeView,
} from "./provider-scope";

const machines = [{ id: "mbp" }, { id: "box" }, { id: "new", trust: "pending" as const }];
const form = (over: Partial<ScopeForm> = {}): ScopeForm => ({ ...newScope(), ...over });
const stored: ScopeView = { share: "selected", scope_machines: ["mbp"], delivery: "on_demand", own_service: "skip", key_machines: ["mbp"] };

describe("scope form", () => {
  it("opens an existing provider's scope and none from an older hub", () => {
    expect(scopeFromView({})).toBeNull();
    expect(scopeFromView(null)).toBeNull();
    expect(scopeFromView(stored)).toMatchObject({ share: "selected", machines: ["mbp"], work: [], delivery: "on_demand", own_service: "skip" });
    expect(scopeFromView({ share: "all", scope_machines: ["w1"], delivery: "push", own_service: "use" })).toMatchObject({ machines: [], work: ["w1"], delivery: "push", own_service: "use" });
  });

  it("shows the scope for a new provider only when the hub says it takes one, for an existing one when it has one", () => {
    expect(scopeShown(null, ["launcher_scope"])).toBe(true);
    expect(scopeShown(null, ["builtin_presets"])).toBe(false);
    expect(scopeShown({ share: "all" }, [])).toBe(true);
    expect(scopeShown({}, ["launcher_scope"])).toBe(false);
  });

  it("needs a computer when the computers are chosen", () => {
    expect(scopeProblem(form({ share: "selected" }))).toBe("至少选一台电脑");
    expect(scopeProblem(form({ share: "selected", machines: ["mbp"] }))).toBe("");
    expect(scopeProblem(form({ share: "none" }))).toBe("");
  });

  it("offers confirmed computers and keeps chosen ones that are not listed", () => {
    expect(choosable(machines, [])).toEqual(["mbp", "box"]);
    expect(choosable(machines, ["new", "gone"])).toEqual(["mbp", "box", "new", "gone"]);
  });

  it("knows work computers only when the hub marks them", () => {
    expect(addableWork(machines, [])).toEqual([]);
    expect(addableWork([...machines, { id: "w", trust: "work" as never }], [])).toEqual(["w"]);
    expect(addableWork(machines, ["w1"])).toEqual(["w1"]);
  });

  it("lists the computers that can have their own key", () => {
    expect(keyMachines(form({ share: "none" }), machines)).toEqual([]);
    expect(keyMachines(form({ share: "selected", machines: ["box"] }), machines)).toEqual(["box"]);
    expect(keyMachines(form({ share: "all", work: ["w1"] }), machines)).toEqual(["mbp", "box", "w1"]);
  });
});

describe("scopeBody", () => {
  it("sends nothing on a PUT when nothing changed", () => {
    expect(scopeBody(scopeFromView(stored)!, stored, machines)).toEqual({});
    // a legacy provider stays legacy until a field is changed
    const legacy: ScopeView = { share: "all", delivery: "push", own_service: "use", legacy_scope: true };
    expect(scopeBody(scopeFromView(legacy)!, legacy, machines)).toEqual({});
  });

  it("sends only what changed on a PUT, the machines with the share", () => {
    const f = scopeFromView(stored)!;
    expect(scopeBody({ ...f, delivery: "push" }, stored, machines)).toEqual({ delivery: "push" });
    expect(scopeBody({ ...f, own_service: "use" }, stored, machines)).toEqual({ own_service: "use" });
    expect(scopeBody({ ...f, machines: ["box", "mbp"] }, stored, machines)).toEqual({ scope_machines: ["box", "mbp"] });
    // reordering is not a change
    expect(scopeBody({ ...f, machines: ["mbp"] }, { ...stored, scope_machines: ["mbp"] }, machines)).toEqual({});
    expect(scopeBody({ ...f, share: "all" }, stored, machines)).toEqual({ share: "all", scope_machines: [] });
    expect(scopeBody({ ...f, share: "none" }, stored, machines)).toEqual({ share: "none", scope_machines: [] });
  });

  it("sends only what differs from the hub's defaults on a POST", () => {
    expect(scopeBody(newScope(), null, machines)).toEqual({});
    expect(scopeBody(form({ share: "selected", machines: ["box"] }), null, machines)).toEqual({ share: "selected", scope_machines: ["box"] });
    expect(scopeBody(form({ delivery: "push" }), null, machines)).toEqual({ delivery: "push" });
    // own service follows assign until chosen
    expect(scopeBody(form({ own_service: "use" }), null, machines)).toEqual({ own_service: "use" });
    expect(scopeBody(form({ own_service: "use" }), null, machines, true)).toEqual({});
    expect(scopeBody(form({ own_service: "skip" }), null, machines, true)).toEqual({ own_service: "skip" });
  });

  it("sends typed keys for computers in scope and \"\" for removed ones", () => {
    const f = { ...scopeFromView(stored)!, keys: { mbp: " sk-mbp ", box: "sk-box", gone: "" } };
    expect(scopeBody(f, stored, machines)).toEqual({ machine_keys: { mbp: "sk-mbp" } });
    expect(scopeBody({ ...f, keys: {}, remove: ["mbp"] }, stored, machines)).toEqual({ machine_keys: { mbp: "" } });
    // a typed key wins over the removal
    expect(scopeBody({ ...f, keys: { mbp: "sk-new" }, remove: ["mbp"] }, stored, machines)).toEqual({ machine_keys: { mbp: "sk-new" } });
    expect(scopeBody(form({ keys: { box: "sk-box" } }), null, machines)).toEqual({ machine_keys: { box: "sk-box" } });
  });
});

describe("scopeSummary", () => {
  const name = (id: string) => ({ mbp: "MacBook", box: "Devbox" })[id] ?? id;
  it("says where, how the key travels and whether it also runs where signed in", () => {
    expect(scopeSummary({ share: "all", delivery: "on_demand", own_service: "skip" })).toBe("所有电脑 · 用到时再取");
    expect(scopeSummary({ share: "selected", scope_machines: ["mbp", "box"], delivery: "push", own_service: "use" }, name)).toBe("MacBook、Devbox · 每台存一份 · 已登录的电脑也用");
    expect(scopeSummary({ share: "all", scope_machines: ["w1"], delivery: "push" })).toBe("所有电脑 + 1 台工作电脑 · 每台存一份");
    expect(scopeSummary({ share: "none", delivery: "push" })).toBe("只留在 hub");
    expect(scopeSummary({ share: "all", delivery: "push", own_service: "use", legacy_scope: true })).toBe("所有电脑，包括工作电脑 · 每台存一份 · 已登录的电脑也用");
  });

  it("reads a form as the view it saves", () => {
    expect(formScope(form({ share: "selected", machines: ["box"] }), true)).toEqual({ share: "selected", scope_machines: ["box"], delivery: "on_demand", own_service: "use" });
  });
});

describe("harnessGroups", () => {
  it("groups by harness in a fixed order, keeping the order within each", () => {
    const list = [
      { name: "a", kind: "api" as const },
      { name: "c1" },
      { name: "x1", agent: "codex" },
      { name: "c2", agent: "claude" },
      { name: "o", agent: "omp" },
      { name: "z", agent: "zed" },
      { name: "g", agent: "agy" },
    ];
    expect(harnessGroups(list).map((g) => [g.harness, g.items.map((p) => p.name)])).toEqual([
      ["claude", ["c1", "c2"]],
      ["codex", ["x1"]],
      ["agy", ["g"]],
      ["omp", ["o"]],
      ["api", ["a"]],
      ["zed", ["z"]],
    ]);
    expect(harnessLabel("claude")).toBe("Claude Code");
    expect(harnessLabel("omp")).toBe("Oh My Pi");
    expect(harnessLabel("api")).toBe("直接 API");
  });
});
