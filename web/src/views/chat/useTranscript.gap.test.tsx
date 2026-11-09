// @vitest-environment jsdom
// A lost push is filled even while the agent keeps working (#5355): each gapped push used to restart the 1.5 s fill
// timer, so in a busy turn (a push every second or so) the fill never ran.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemsEvent, listenSession } from "../../lib/store";
import type { Item, ItemsPage } from "../../lib/types";

const calls: string[] = [];
type Listener = Parameters<typeof listenSession>[2];
let listener: Listener | undefined;

vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: vi.fn(async (path: string): Promise<ItemsPage> => {
    calls.push(path);
    if (path.includes("after_seq=")) return { items: [item(101), item(102)], to_seq: 140 } as ItemsPage;
    return { items: [item(100)], to_seq: 100 } as ItemsPage;
  }),
}));
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  send: vi.fn(),
  listenSession: (_mid: string, _sid: string, l: Listener) => {
    listener = l;
    return () => (listener = undefined);
  },
}));

const { useTranscript } = await import("./useTranscript");

function item(seq: number): Item {
  return { id: `i${seq}`, seq, role: "assistant", kind: "text", text: `row ${seq}`, ts: "2026-11-06T00:00:00Z" } as Item;
}

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let seqs: number[] = [];
function Probe({ sid }: { sid: string }) {
  seqs = useTranscript("m1", sid).items.map((i) => i.seq);
  return null;
}

let root: Root;
beforeEach(() => {
  vi.useFakeTimers();
  calls.length = 0;
  root = createRoot(document.createElement("div"));
});
afterEach(() => {
  act(() => root.unmount());
  vi.useRealTimers();
});

const push = (e: ItemsEvent) => act(() => listener!.items!(e));
const fills = () => calls.filter((c) => c.includes("after_seq=")).length;

async function open(sid: string) {
  act(() => root.render(<Probe sid={sid} />));
  await act(async () => {}); // the first page
  expect(seqs).toEqual([100]);
}

describe("a gap in the pushed items", () => {
  it("fills from covered when a pushed batch says more_after", async () => {
    await open("s1");
    push({ from_seq: 100, to_seq: 110, items: [item(110)], more_after: true } as ItemsEvent);
    await act(async () => void vi.advanceTimersByTime(1600));
    expect(fills()).toBe(1);
    expect(calls.find((c) => c.includes("after_seq="))).toContain("after_seq=110");
  });

  it("is filled 1.5 s after it shows, while gapped pushes keep coming", async () => {
    await open("s1");
    // 101-110 were lost; the agent keeps pushing a row every 400 ms.
    for (let n = 0; n < 6; n++) {
      push({ from_seq: 111 + n, to_seq: 111 + n, items: [item(111 + n)] } as ItemsEvent);
      await act(async () => void vi.advanceTimersByTime(400));
    }
    expect(fills()).toBe(1);
    expect(calls.find((c) => c.includes("after_seq="))).toContain("after_seq=100");
    expect(seqs.slice(0, 3)).toEqual([100, 101, 102]);
  });

  it("starts a fresh timer for the next conversation's gap", async () => {
    await open("s1");
    push({ from_seq: 120, to_seq: 120, items: [item(120)] } as ItemsEvent);
    act(() => root.render(<Probe sid="s2" />)); // switched before the fill ran
    await act(async () => {});
    push({ from_seq: 130, to_seq: 130, items: [item(130)] } as ItemsEvent);
    await act(async () => void vi.advanceTimersByTime(1600));
    expect(fills()).toBe(1);
  });
});
