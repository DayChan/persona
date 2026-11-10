import { describe, expect, it } from "vitest";
import { neverConnected } from "./add-computer";
import { selectSidebarMachines } from "./sidebar-machines";
import type { Machine } from "./types";

const computers = (): Machine[] => [
  { id: "m1", name: "Laptop", online: true, hub_id: "h1", hub_name: "Home", last_seen: "2025-12-31T00:00:00Z" },
  { id: "m2", name: "Desktop", online: true, hub_id: "h2", hub_name: "Office", last_seen: "2025-12-31T00:00:00Z" },
];

describe("the sidebar's machine projection", () => {
  it("keeps the array and rows for load, memory and last-seen pushes", () => {
    const machines = computers();
    const before = selectSidebarMachines({ machines });
    expect(selectSidebarMachines({ machines })).toBe(before);
    // The wire can include telemetry the sidebar's Machine type does not read.
    const telemetry = machines.map((m) => ({ ...m, load: 2.5, memory: { used: 4096 }, last_seen: "2026-01-01T00:00:00Z" }));
    const after = selectSidebarMachines({ machines: telemetry });
    expect(after).toBe(before);
    expect(after[0]).toBe(before[0]);
    expect(after[0]).not.toHaveProperty("load");
    expect(after[0]).not.toHaveProperty("memory");
    expect(after[0].last_seen).toBe(machines[0].last_seen);
    expect(selectSidebarMachines({ machines: telemetry.map((m) => ({ ...m, load: 3, memory: { used: 8192 } })) })).toBe(before);
  });

  it.each<Partial<Machine>>([
    { id: "m3" },
    { name: "Renamed laptop" },
    { online: false },
    { hub_id: "h3" },
    { hub_name: "Renamed hub" },
    { error_code: "never_connected" },
    { trust: "pending" },
  ])("updates a changed sidebar field: %j", (patch) => {
    const machines = computers();
    const before = selectSidebarMachines({ machines });
    const after = selectSidebarMachines({ machines: [{ ...machines[0], ...patch }, { ...machines[1] }] });
    expect(after).not.toBe(before);
    expect(after[0]).not.toBe(before[0]);
    expect(after[0]).toMatchObject(patch);
    expect(after[1]).toBe(before[1]);
  });

  it("preserves order, additions and removals without replacing unchanged rows", () => {
    const machines = computers();
    const before = selectSidebarMachines({ machines });
    const reversed = selectSidebarMachines({ machines: [...machines].reverse() });
    expect(reversed).not.toBe(before);
    expect(reversed[0]).toBe(before[1]);
    expect(reversed[1]).toBe(before[0]);

    const removed = selectSidebarMachines({ machines: [machines[0]] });
    expect(removed).toHaveLength(1);
    expect(removed[0]).toBe(before[0]);
    const added = selectSidebarMachines({ machines });
    expect(added).toHaveLength(2);
    expect(added[0]).toBe(before[0]);
    expect(added[1]).toMatchObject(machines[1]);
    expect(selectSidebarMachines({ machines: [] })).toEqual([]);
  });

  it("clears optional fields when the hub omits them", () => {
    const before = selectSidebarMachines({ machines: [{ ...computers()[0], error_code: "never_connected" }] });
    const after = selectSidebarMachines({ machines: [{ id: "m1", name: "Laptop", online: true }] });
    expect(after).not.toBe(before);
    expect(after[0].hub_id).toBeUndefined();
    expect(after[0].hub_name).toBeUndefined();
    expect(after[0].error_code).toBeUndefined();
  });

  it("preserves never-connected labels when pending computers are first seen or confirmed", () => {
    const machine: Machine = { id: "m1", name: "Laptop", online: false, trust: "pending" };
    const pending = selectSidebarMachines({ machines: [machine] });
    expect(neverConnected(pending[0])).toBe(true);
    const seen = selectSidebarMachines({ machines: [{ ...machine, last_seen: "2026-01-01T00:00:00Z" }] });
    expect(seen).not.toBe(pending);
    expect(neverConnected(seen[0])).toBe(false);
    expect(selectSidebarMachines({ machines: [{ ...machine, last_seen: "2026-01-02T00:00:00Z" }] })).toBe(seen);

    const unseen = selectSidebarMachines({ machines: [machine] });
    expect(neverConnected(unseen[0])).toBe(true);
    const confirmed = selectSidebarMachines({ machines: [{ ...machine, trust: undefined }] });
    expect(confirmed).not.toBe(unseen);
    expect(neverConnected(confirmed[0])).toBe(false);
  });
});
