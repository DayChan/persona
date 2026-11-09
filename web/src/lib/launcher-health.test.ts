import { describe, expect, it } from "vitest";
import { blockReason, initialLauncher, launcherBlock, NOT_IN_TERMINAL, usableFirst } from "./launcher-health";
import type { Launcher } from "./types";

// A launcher's health on the computer "New agent" is for (#1643, docs/server/launcher-health.md): only a blocked record
// grays it out, with a reason for its class; an older hub sends none and nothing changes.
const l = (name: string, health?: Launcher["health"]): Launcher => ({ name, ...(health ? { health } : {}) });

describe("launcherBlock", () => {
  it("is nothing for an older hub, a working launcher, or one the hub is still retrying", () => {
    expect(launcherBlock(l("claude"))).toBeNull();
    expect(launcherBlock(undefined)).toBeNull();
    expect(launcherBlock(l("claude", { state: "ok", checked_at: "2026-11-09T08:00:00Z" }))).toBeNull();
    expect(launcherBlock(l("a", { state: "failing", kind: "timeout" }))).toBeNull();
    expect(launcherBlock(l("a", { state: "repairing", kind: "auth" }))).toBeNull();
  });

  it("says a launcher the terminal can't run is not available there", () => {
    const b = launcherBlock(l("work_gw", { state: "blocked", kind: NOT_IN_TERMINAL, error: "  bash: work_gw: command not found\n" }))!;
    expect(b.notInTerminal).toBe(true);
    expect(b.reason).toBe("这台电脑的终端里用不了");
    expect(b.title).toBe("终端里用不了");
    expect(b.detail).toBe("bash: work_gw: command not found");
  });

  it("gives other blocked classes their own line, and a generic one for a class it doesn't know", () => {
    const kinds = ["auth", "model_route", "cli_outdated", "timeout", "other"];
    const reasons = kinds.map((kind) => launcherBlock(l("a", { state: "blocked", kind }))!.reason);
    expect(new Set(reasons).size).toBe(kinds.length);
    expect(reasons).not.toContain(blockReason(NOT_IN_TERMINAL));
    expect(launcherBlock(l("a", { state: "blocked", kind: "from_a_newer_hub" }))!.reason).toBe(blockReason("other"));
    const none = launcherBlock(l("a", { state: "blocked", error: "   " }))!;
    expect(none.kind).toBe("");
    expect(none.notInTerminal).toBe(false);
    expect(none.detail).toBeUndefined();
    expect(none.title).toBe("这台电脑上用不了");
  });
});

describe("initialLauncher", () => {
  const blocked = l("work_gw", { state: "blocked", kind: NOT_IN_TERMINAL });
  const list = [blocked, l("claude", { state: "ok" }), l("sonnetcc")];

  it("keeps the last one used unless this computer can't start it now", () => {
    expect(initialLauncher("sonnetcc", list)).toBe("sonnetcc");
    expect(initialLauncher("work_gw", list)).toBe("claude");
    expect(initialLauncher("gone", list)).toBe("claude");
    expect(initialLauncher(null, list)).toBe("claude");
  });

  it("still has one to start with when every launcher is blocked", () => {
    const all = [blocked, l("team_gw", { state: "blocked", kind: "auth" })];
    expect(initialLauncher("team_gw", all)).toBe("team_gw");
    expect(initialLauncher(undefined, all)).toBe("work_gw");
    expect(initialLauncher("x", [])).toBe("");
  });
});

describe("usableFirst", () => {
  it("puts the blocked ones after the rest, each group in the hub's order", () => {
    const list = [l("work_gw", { state: "blocked", kind: NOT_IN_TERMINAL }), l("claude", { state: "ok" }), l("team_gw", { state: "blocked", kind: "auth" }), l("slow", { state: "failing" })];
    expect(usableFirst(list).map((x) => x.name)).toEqual(["claude", "slow", "work_gw", "team_gw"]);
    expect(usableFirst([])).toEqual([]);
  });
});
