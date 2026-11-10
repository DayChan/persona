import { describe, expect, it } from "vitest";
import { canSwitchToBypass, EFFORTS, effortOptions, effortTitle, hasEffortOnly, hasModes, isMuted, modeTitle, renameBody, SWITCHABLE_MODES, switchableModes, toggleMute } from "./session-actions";
import type { Session } from "./types";

const s = (over: Partial<Session> = {}): Session => ({ machine_id: "mac", session_id: "s1", title: "修登录", cwd: "/home/me/app", status: "idle", space_name: "app", slot_id: "p1", slot_name: "login", ...over });
const none = { spaces: [] as string[], sessions: [] as string[], allowed: [] as string[] };

describe("mutes", () => {
  it("mutes and unmutes an agent on its own", () => {
    const on = toggleMute(s(), none);
    expect(on.sessions).toEqual(["mac/s1"]);
    expect(isMuted(s(), on)).toBe(true);
    const off = toggleMute(s(), on);
    expect(off.sessions).toEqual([]);
    expect(isMuted(s(), off)).toBe(false);
  });

  it("lets one agent of a muted space notify again, and mutes it back", () => {
    const space = { ...none, spaces: ["mac/app"] };
    expect(isMuted(s(), space)).toBe(true);
    const allowed = toggleMute(s(), space);
    expect(allowed.allowed).toEqual(["mac/s1"]);
    expect(isMuted(s(), allowed)).toBe(false);
    const back = toggleMute(s(), allowed);
    expect(back.allowed).toEqual([]);
    expect(back.sessions).toEqual([]);
    expect(isMuted(s(), back)).toBe(true);
  });

  it("keeps everything else as it was", () => {
    const m = { spaces: ["mac/other"], sessions: ["mac/s9"], allowed: ["mac/s8"] };
    const on = toggleMute(s(), m);
    expect(on).toEqual({ spaces: ["mac/other"], sessions: ["mac/s9", "mac/s1"], allowed: ["mac/s8"] });
  });
});

describe("modes", () => {
  it("labels the known ones and keeps unknown ids", () => {
    expect(modeTitle("plan")).toBe("只做计划");
    expect(modeTitle("weird")).toBe("weird");
    expect(modeTitle(undefined)).toBe("");
    expect(effortTitle("xhigh")).toBe("很高");
  });

  it("never offers 全部放行 on a running session that can't reach it", () => {
    expect(SWITCHABLE_MODES.some((m) => m.id === "bypassPermissions")).toBe(false);
    expect(switchableModes(false)).toBe(SWITCHABLE_MODES);
    expect(switchableModes(undefined)).toBe(SWITCHABLE_MODES);
    expect(switchableModes(false, "claude", false)).toBe(SWITCHABLE_MODES);
    expect(canSwitchToBypass(s({ permission_mode: "acceptEdits" }))).toBe(false);
  });

  it("offers 全部放行 to an agent whose Claude was started so it can reach it, or is in it now (#1379)", () => {
    expect(canSwitchToBypass(s({ bypass_available: true, permission_mode: "plan" }))).toBe(true);
    expect(canSwitchToBypass(s({ permission_mode: "bypassPermissions" }))).toBe(true);
    const ids = switchableModes(false, "claude", true).map((m) => m.id);
    expect(ids).toEqual(["default", "acceptEdits", "plan", "auto", "bypassPermissions"]);
    expect(switchableModes(false, "claude", true).find((m) => m.id === "bypassPermissions")?.detail).not.toContain("新开");
  });

  it("lets an assistant conversation switch to every mode, 全部放行 included (#2416)", () => {
    const ids = switchableModes(true).map((m) => m.id);
    expect(ids).toEqual(["default", "acceptEdits", "plan", "auto", "bypassPermissions"]);
    expect(switchableModes(false, "agy").map((m) => m.id)).toEqual(ids);
    expect(switchableModes(true).find((m) => m.id === "bypassPermissions")?.detail).not.toContain("新开");
  });

  it("only Claude Code agents have the chips", () => {
    expect(hasModes(s())).toBe(true);
    expect(hasModes(s({ agent: "claude" }))).toBe(true);
    expect(hasModes(s({ agent: "agy", source_kind: "runtime" }))).toBe(true);
    expect(hasModes(s({ agent: "codex" }))).toBe(false);
    expect(hasModes(s({ assistant: true }))).toBe(false);
    expect(hasModes(s({ source_kind: "runtime" }))).toBe(false);
    expect(hasModes(undefined)).toBe(false);
  });

  it("follows the node's modes capability when it gives one (#1843)", () => {
    // Orca's conversations: a runtime that applies the modes
    expect(hasModes(s({ agent: "claude", source_kind: "runtime", capabilities: { read: true, send: true, modes: true } }))).toBe(true);
    // a row the node says no for (a runtime that did not declare them), whatever its agent
    expect(hasModes(s({ agent: "agy", source_kind: "runtime", capabilities: { read: true, send: true, modes: false } }))).toBe(false);
    // Claude's desktop app or another terminal: messages only
    for (const source_kind of ["claude_desktop", "claude_cli", "claude_bg"]) {
      expect(hasModes(s({ agent: "claude", source_kind, capabilities: { read: true, send: true, modes: false } }))).toBe(false);
    }
    expect(hasModes(s({ assistant: true, capabilities: { modes: true } }))).toBe(false);
    // an older node says nothing: the agent decides, as before
    expect(hasModes(s({ agent: "claude", source_kind: "runtime", capabilities: { read: true, send: true } }))).toBe(false);
    expect(hasModes(s({ agent: "claude", source_kind: "claude_desktop", capabilities: { read: true, send: true } }))).toBe(false);
  });

  it("gives a Codex thread on its app-server the effort alone, among its own levels", () => {
    const live = s({ agent: "codex", source_kind: "codex_app_server", capabilities: { read: true, send: true }, efforts: ["low", "high", "ultra"], effort: "high" });
    expect(hasModes(live)).toBe(false);
    expect(hasEffortOnly(live)).toBe(true);
    expect(effortOptions(live).map((m) => m.id)).toEqual(["low", "high", "ultra"]);
    expect(effortOptions(live).map((m) => m.title)).toEqual(["低", "高", "极高"]);
    expect(effortTitle("minimal")).toBe("极低");
    // A herdr pane joined on the app-server: no source, the node's levels say it.
    expect(hasEffortOnly(s({ agent: "codex", efforts: ["medium"] }))).toBe(true);
    // A level this app does not know yet keeps its id.
    expect(effortOptions(s({ efforts: ["turbo"] }))[0]).toEqual({ id: "turbo", title: "turbo", detail: "" });
  });

  it("offers no effort where the node lists none or the phone cannot write", () => {
    expect(hasEffortOnly(s({ agent: "codex" }))).toBe(false); // the terminal route, a compatible CLI
    expect(hasEffortOnly(s({ agent: "codex", source_kind: "codex_app_server", capabilities: { read: true }, efforts: ["low"] }))).toBe(false);
    expect(hasEffortOnly(s({ assistant: true, efforts: ["low"] }))).toBe(false);
    expect(hasEffortOnly(s({ efforts: ["low"] }))).toBe(false); // Claude Code has both chips
    expect(hasEffortOnly(undefined)).toBe(false);
    expect(effortOptions(s())).toBe(EFFORTS);
  });
});

describe("rename", () => {
  it("sends only what changed", () => {
    expect(renameBody(s(), "login", "修登录")).toBeNull();
    expect(renameBody(s(), "auth", "修登录")).toEqual({ tab_name: "auth" });
    expect(renameBody(s(), "login", "修登录页")).toEqual({ title: "修登录页" });
    expect(renameBody(s(), " auth ", " 新名 ")).toEqual({ tab_name: "auth", title: "新名" });
  });

  it("never renames a tab the agent has not got", () => {
    expect(renameBody(s({ slot_id: undefined }), "auth", "x")).toEqual({ title: "x" });
    expect(renameBody(s({ assistant: true }), "auth", "x")).toEqual({ title: "x" });
  });

  it("ignores empty names", () => {
    expect(renameBody(s(), "", "")).toBeNull();
  });
});
