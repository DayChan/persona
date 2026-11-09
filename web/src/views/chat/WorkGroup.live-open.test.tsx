// @vitest-environment jsdom
// #1614: in a Persona conversation the running exchange's work line is open, each tool call its own row as it comes;
// once the exchange ends it folds back to its summary, and a tap opens it again with every call. A fold the reader
// made during the run holds for the run and is dropped when it ends. A coding agent's chat keeps its folded default.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyTaskIndex } from "../../lib/tasks";
import type { Item } from "../../lib/types";
import type { Segment, Step } from "../../lib/turns";
import type { WorkLineStyle } from "../../lib/work-groups";
import { ChatContext, type ChatContextValue } from "./chat-context";
import { forgetExpanded } from "./expanded";
import { SegmentView } from "./SegmentView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const T0 = Date.parse("2026-11-04T10:00:00Z");
const at = (s: number) => new Date(T0 + s * 1000).toISOString();
const tool = (n: number, done = true): Step => ({
  kind: "tool",
  id: `t${n}#0`,
  use: { id: `t${n}#0`, seq: n, role: "assistant", kind: "tool_use", tool_name: "Bash", tool_use_id: `c-${n}`, tool_input: { command: `echo ${n}` }, ts: at(n) },
  result: done ? { id: `t${n}-r#0`, seq: n, role: "user", kind: "tool_result", tool_use_id: `c-${n}`, text: "ok", ts: at(n + 1) } : undefined,
  images: [],
});
const words = (n: number, text: string): Step => {
  const item = { id: `a${n}#0`, seq: n, role: "assistant", kind: "text", text, ts: at(n) } as Item;
  return { kind: "text", id: item.id, item };
};
const seg = (steps: Step[]): Segment => ({ id: "seg", steps, tools: steps.length, thoughts: 0, replies: 0, folds: [], start: at(0) });
const expandFold = async () => {};

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  forgetExpanded();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

function draw(style: WorkLineStyle, s: Segment, live: boolean, expand: (f: Item) => Promise<void> = expandFold) {
  const ctx: ChatContextValue = { tasks: emptyTaskIndex(), subagents: [], scope: "x:", lineStyle: style };
  act(() =>
    root.render(
      <ChatContext.Provider value={ctx}>
        <SegmentView seg={s} live={live} mid="m" sid="s" expandFold={expand} />
      </ChatContext.Provider>,
    ),
  );
}

const header = () => host.querySelector<HTMLButtonElement>("[data-work-group] > button")!;
const detail = () => host.querySelector("[data-work-detail]");
const detailText = () => detail()?.textContent ?? "";
const tap = () => act(() => header().click());

// seven calls (more than the five a live group peeks), the last still running
const running = [1, 2, 3, 4, 5, 6].map((n) => tool(n)).concat(tool(7, false));
const finished = [1, 2, 3, 4, 5, 6, 7].map((n) => tool(n)).concat(words(9, "Done."));

describe("assistant work line (#1614)", () => {
  it("is open while the exchange runs, every call its own row, the running one too", () => {
    draw("assistant", seg(running), true);
    expect(header().getAttribute("aria-expanded")).toBe("true");
    for (let n = 1; n <= 7; n++) expect(detailText()).toContain(`echo ${n}`);
    expect(host.querySelector("[data-live-preview]")).toBeNull();
  });

  it("lists a call as it arrives, and opens even with one call", () => {
    draw("assistant", seg([tool(1, false)]), true);
    expect(header().disabled).toBe(false);
    expect(detailText()).toContain("echo 1");
    draw("assistant", seg([tool(1), tool(2, false)]), true);
    expect(detailText()).toContain("echo 2");
  });

  it("folds to its summary when the exchange ends, and a tap opens it with every call", () => {
    draw("assistant", seg(running), true);
    draw("assistant", seg(finished), false);
    expect(header().getAttribute("aria-expanded")).toBe("false");
    tap();
    expect(header().getAttribute("aria-expanded")).toBe("true");
    for (let n = 1; n <= 7; n++) expect(detailText()).toContain(`echo ${n}`);
    tap();
    expect(header().getAttribute("aria-expanded")).toBe("false");
  });

  it("a fold during the run holds while it runs, and is dropped when it ends", () => {
    draw("assistant", seg(running), true);
    tap();
    expect(header().getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector("[data-live-preview]")).toBeNull(); // just the summary: no steps peek out
    draw("assistant", seg([...running.slice(0, 6), tool(7), tool(8, false)]), true);
    expect(header().getAttribute("aria-expanded")).toBe("false");
    // opened again during the run: that is dropped too, the ended line folds
    tap();
    expect(header().getAttribute("aria-expanded")).toBe("true");
    draw("assistant", seg(finished), false);
    expect(header().getAttribute("aria-expanded")).toBe("false");
  });

  it("opening the summary of an exchange with words between its calls lists every call (inner lines open)", () => {
    const steps = [tool(1), tool(2), words(3, "Now the tests."), tool(4), tool(5), words(9, "Done.")];
    draw("assistant", seg(steps), false);
    tap();
    for (const n of [1, 2, 4, 5]) expect(detailText()).toContain(`echo ${n}`);
  });

  it("a reloaded finished exchange starts folded and opens with every call", () => {
    draw("assistant", seg(finished), false);
    expect(header().getAttribute("aria-expanded")).toBe("false");
    tap();
    for (let n = 1; n <= 7; n++) expect(detailText()).toContain(`echo ${n}`);
  });
});

describe("agents work line (unchanged)", () => {
  it("is folded while running: the newest five calls peek out", () => {
    draw("agents", seg(running), true);
    expect(detail()).toBeNull();
    const peek = host.querySelector("[data-live-preview]")?.textContent ?? "";
    expect(peek).not.toContain("echo 2");
    expect(peek).toContain("echo 7");
  });

  it("a short live line does not open; a finished one stays folded; inner lines stay folded", () => {
    draw("agents", seg([tool(1, false)]), true);
    expect(header().disabled).toBe(true);
    draw("agents", seg(finished), false);
    expect(header().getAttribute("aria-expanded")).toBe("false");
    const steps = [tool(1), tool(2), words(3, "Now the tests."), tool(4), tool(5), words(9, "Done.")];
    draw("agents", seg(steps), false);
    tap();
    expect(detailText()).not.toContain("echo 1");
  });
});

// The hub folds earlier work (fold_work): a run of calls comes as one fold step, fetched by from_seq / through_seq when
// its group opens. Seqs: calls n at n; a fold of calls a..b stands at a, through b.
const fold = (a: number, b: number): Step => {
  const item = { id: `fold:t${a}#0`, seq: a, role: "assistant", kind: "fold", ts: at(a), fold: { items: (b - a + 1) * 2, tools: b - a + 1, thoughts: 0, until: at(b + 1), through_seq: b } } as Item;
  return { kind: "fold", id: item.id, item };
};
const calls = (a: number, b: number) => Array.from({ length: b - a + 1 }, (_, i) => tool(a + i));

/** A chat whose folds come in: expandFold swaps a fold for its calls and draws again, as useTranscript does. */
function foldingChat(style: WorkLineStyle, start: Step[], live: boolean) {
  let steps = start;
  let isLive = live;
  const fetched: number[] = [];
  const expand = vi.fn(async (f: Item) => {
    fetched.push(f.seq);
    await new Promise((r) => setTimeout(r, 3)); // the request
    steps = steps.flatMap((s) => (s.kind === "fold" && s.item.id === f.id ? calls(f.seq, f.fold!.through_seq) : [s]));
    // as useTranscript's apply: the steps are set and drawn a moment later, the promise settles at once, so the
    // fetch goes on to the next fold before the chat draws this one's steps
    setTimeout(() => draw(style, seg(steps), isLive, expand));
  });
  return {
    fetched,
    draw: () => draw(style, seg(steps), isLive, expand),
    set: (next: Step[], nextLive: boolean) => {
      steps = next;
      isLive = nextLive;
      draw(style, seg(steps), isLive, expand);
    },
  };
}
/** Lets the fetches and draws run their course: in short acts, as one long act would hold every draw to its end. */
const settle = async () => {
  for (let i = 0; i < 30; i++) await act(() => new Promise<void>((r) => setTimeout(r, 2)));
};
const groups = () => [...host.querySelectorAll<HTMLElement>("[data-work-group]")];
const allText = () => host.textContent ?? "";
/** The headers' "loading the folded steps" spinners (Spinner size 11). */
const loadingSpinners = () => host.querySelectorAll('[data-work-group] > button span[style*="width: 11px"]').length;

describe("assistant work line with the hub's folds and words between calls (#1614)", () => {
  // calls 1-3 (folded by the hub), words, calls 5-7 (folded), words, 11-12 running
  const runningSteps = () => [fold(1, 3), words(4, "First notes read."), fold(5, 7), words(8, "Checking the rest."), tool(11), tool(12, false)];

  it("while it runs every call of the turn is listed, those before the words too: their folds are fetched", async () => {
    const chat = foldingChat("assistant", runningSteps(), true);
    chat.draw();
    await settle();
    expect(chat.fetched.sort((a, b) => a - b)).toEqual([1, 5]);
    for (const g of groups()) expect(g.querySelector(":scope > button")!.getAttribute("aria-expanded")).toBe("true");
    for (const n of [1, 2, 3, 5, 6, 7, 11, 12]) expect(allText()).toContain(`echo ${n}`);
    expect(loadingSpinners()).toBe(0);
  });

  it("ended, it folds; reopened, every call is listed (inner lines open, folds fetched), and no spinner stays", async () => {
    const ended = [fold(1, 3), words(4, "First notes read."), fold(5, 7), words(8, "Checking the rest."), ...calls(11, 13), words(20, "Done.")];
    const chat = foldingChat("assistant", ended, false);
    chat.draw();
    await settle();
    expect(groups()).toHaveLength(1);
    expect(header().getAttribute("aria-expanded")).toBe("false");
    expect(chat.fetched).toEqual([]);
    tap();
    await settle();
    expect(new Set(chat.fetched)).toEqual(new Set([1, 5]));
    for (const n of [1, 2, 3, 5, 6, 7, 11, 12, 13]) expect(detailText()).toContain(`echo ${n}`);
    for (const g of groups()) expect(g.querySelector(":scope > button")!.getAttribute("aria-expanded")).toBe("true");
    // every fold came in: the header's loading spinner is gone (it stayed when the last fold ended a superseded fetch)
    expect(loadingSpinners()).toBe(0);
  });

  it("from running to ended: all listed, then the one summary, folded", async () => {
    const chat = foldingChat("assistant", runningSteps(), true);
    chat.draw();
    await settle();
    chat.set([...calls(1, 3), words(4, "First notes read."), ...calls(5, 7), words(8, "Checking the rest."), ...calls(11, 12), words(20, "Done.")], false);
    expect(groups()).toHaveLength(1);
    expect(header().getAttribute("aria-expanded")).toBe("false");
    tap();
    for (const n of [1, 2, 3, 5, 6, 7, 11, 12]) expect(detailText()).toContain(`echo ${n}`);
  });

  it("agents: earlier lines stay folded while it runs and nothing is fetched", async () => {
    const chat = foldingChat("agents", runningSteps(), true);
    chat.draw();
    await settle();
    expect(chat.fetched).toEqual([]);
    expect(allText()).not.toContain("echo 2");
  });
});
