// @vitest-environment jsdom
// Every chat (an agent's too, with the gate off) and the Persona page hold the readiness gate. A machines push, a
// refresh() and applyMachine each replace the machine list; the gate reads that list only for "Confirm computer", on
// the click, so a change to some computer does not draw the whole chat or page again.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { applyMachine, getState, reset } from "../../lib/store";
import type { Machine } from "../../lib/types";
import { useReadinessGate, type ReadinessGate } from "./Readiness";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
// Add computer is its own chunk (views/LazyOverlay.tsx): in before it is drawn
beforeAll(() => loadPages(), 60_000);

const mac: Machine = { id: "mac", name: "Mac", online: true };
let renders = 0;
let gate: ReadinessGate | null = null;
// stands in for ChatView and PersonaAssistant: the component that holds the gate, counted each time it draws
function Holder({ on }: { on: boolean }) {
  renders++;
  gate = useReadinessGate(on);
  return <>{gate.dialog}</>;
}

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(json({ machines: getState().machines }))));
  renders = 0;
  gate = null;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

const mount = (on: boolean) => act(() => root.render(<Holder on={on} />));

describe("the readiness gate and the machine list", () => {
  it.each([false, true])("does not draw its holder again when a computer changes (gate on: %s)", (on) => {
    Object.assign(getState(), { machines: [mac] });
    mount(on);
    const before = renders;
    for (let i = 1; i <= 10; i++) act(() => applyMachine({ ...mac, name: `Mac ${i}` }));
    expect(getState().machines[0]?.name).toBe("Mac 10");
    expect(renders - before).toBe(0);
  });

  it("confirms the computer waiting as the list has it at the click", () => {
    Object.assign(getState(), { features: ["machine_trust"], machines: [{ ...mac, trust: "pending" }] });
    mount(true);
    // renamed after the holder last drew: the click still finds it, under its new name
    act(() => applyMachine({ ...mac, name: "小林的 MacBook", trust: "pending" }));
    act(() => gate!.confirm());
    expect(document.body.textContent).toContain("有一台电脑想加入：小林的 MacBook");
  });

  it("opens the machines page when no computer is waiting", () => {
    Object.assign(getState(), { features: ["machine_trust"], machines: [mac] });
    mount(true);
    act(() => gate!.confirm());
    expect(location.pathname).toMatch(/\/machines$/);
    expect(document.body.textContent).not.toContain("有一台电脑想加入");
  });
});
