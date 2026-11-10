import { describe, expect, it } from "vitest";
import { PLAN_PERMISSIONS } from "../../lib/feed";
import { permissionLabel } from "./PlanEdit";

describe("a plan task's permission (#3026)", () => {
  it("names Auto, which the hub may settle from Claude Code's default mode, instead of calling it 每步都问", () => {
    expect(PLAN_PERMISSIONS).toContain("auto");
    expect(permissionLabel("auto")).toBe("自动判断");
    expect(permissionLabel("default")).toBe("每步都问");
    // Every mode Edit offers reads differently.
    expect(new Set(PLAN_PERMISSIONS.map(permissionLabel)).size).toBe(PLAN_PERMISSIONS.length);
  });
});
