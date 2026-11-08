// @vitest-environment jsdom
// The shared chat sync scenarios (spec/fixtures/sync, #1106) against the web app's own state code: the real store,
// useTranscript, ChatView, the composer and the cards. Only the socket, fetch and the clock are the case's
// (sync/fake-hub.ts). Each step: deliver it, let the page do what is due, project the page (sync/project.ts) and check
// the step's expect and the invariants; at the end, settle and compare with the case's end.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FakeSocket, SyncHub, type Message, type Request, type Seen, type Step, type SyncCase, type View } from "./fake-hub";
import { project } from "./project";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  // a desktop browser: a precise pointer (Enter sends), a wide window
  g.matchMedia = (q: string) => ({ matches: /pointer: *fine|hover: *hover/.test(q), addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});

// What sits around a chat and is not about its sync: the avatar, the assistant's menus and task cards, the side panel.
vi.mock("../../../lib/panel", async (importOriginal) => ({ ...await importOriginal<typeof import("../../../lib/panel")>(), usePanel: () => null }));
vi.mock("../../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));
vi.mock("../../assistant/AssistantChat", () => ({
  AssistantFace: () => null,
  AssistantLauncherChip: () => null,
  CompactionStrip: () => null,
  useAssistantMenu: () => ({ items: [], dialogs: null }),
}));
vi.mock("../../assistant/Tasks", () => ({
  DayLine: () => null,
  FeedLineView: () => null,
  QuickResultCard: () => null,
  RouteChip: () => null,
  TaskDock: () => null,
  TaskQuestions: () => null,
}));

const store = await import("../../../lib/store");
const { setToken } = await import("../../../lib/api");
const { t } = await import("../../../i18n");
const { ChatView } = await import("../ChatView");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const files = import.meta.glob<{ cases: SyncCase[] }>("../../../../../spec/fixtures/sync/*.json", { eager: true, import: "default" });

/** Cases this page does not pass yet: the bug, and what every failure of the case must say. A known case that fails
 *  for another reason, or passes, fails the suite: the fixed bug comes off this list, and a new one is not hidden. */
const CARD_STAYS_LIVE = { issue: "#1120", because: /card ix\d is answerable again|open cards \["ix\d"\], want \[\]|open cards \["ix1"\], want \["ix2"\]/ };
const KNOWN: Record<string, { issue: string; because: RegExp }> = {
  card_answer_then_resolved: CARD_STAYS_LIVE,
  card_tap_then_reconnect_snapshot: CARD_STAYS_LIVE,
  card_old_list_after_answer: CARD_STAYS_LIVE,
  card_answer_fails_card_back: CARD_STAYS_LIVE,
  card_prompt_changed_only_new_card: CARD_STAYS_LIVE,
  "card_answered_on_other_device as b": CARD_STAYS_LIVE,
  "both_devices_answer_once as a": CARD_STAYS_LIVE,
  "both_devices_answer_once as b": CARD_STAYS_LIVE,
  // Only while the answer is on its way (step 1); after the resumed snapshot the card stays answered (#1541).
  card_answered_then_resumed_snapshot: { issue: "#1120", because: /^step 1 \(user\): (card ix1 is answerable again|open cards \["ix1"\], want \[\])$/ },
  catch_up_over_cap_asks_rest: { issue: "#1118", because: /after_seq":12\}|want \[.*Question 6/ },
  send_accepted_transcript_late: { issue: "#1119", because: /m1 shows 0 times|want \[.*Add a picnic on Saturday/ },
  send_unknown_then_transcript_has_it: { issue: "#1121", because: /m1 shows 2 times|failed \["Reserve two bikes."\], want \[\]/ },
  send_failed_stays_above_later: { issue: "#1122", because: /m1 shows 0 times|failed \[\], want \["Find a dinner place."\]/ },
};
// a new conversation's first message (chat.new, user.start): the runner does not play it yet, so every such case is
// this one known failure
const NO_START = "the web runner does not play a new conversation's start yet";
const START_KNOWN = { issue: "#1382", because: new RegExp(NO_START) };

let host: HTMLDivElement;
let root: Root;
let hub: SyncHub;

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date", "requestAnimationFrame", "cancelAnimationFrame"] });
  vi.setSystemTime(new Date("2026-01-02T03:05:00Z"));
  vi.stubGlobal("WebSocket", FakeSocket);
  setToken("tok");
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  store.disconnect();
  store.reset();
  FakeSocket.hub = null;
  vi.unstubAllGlobals();
  vi.useRealTimers();
  localStorage.clear();
});

/** Lets the page do what is due: `ms` of its clock, then a few frames (the store batches its notices per frame). */
async function settle(ms = 0) {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(i === 0 ? ms : 17);
    });
  }
}

function typeAndSend(words: string) {
  const box = host.querySelector<HTMLTextAreaElement>("[data-chat-dock] textarea");
  if (!box) throw new Error("no composer");
  const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  set.call(box, words);
  box.dispatchEvent(new Event("input", { bubbles: true }));
  box.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
}

function answer(id: string, resolution: Record<string, unknown>) {
  const card = [...host.querySelectorAll<HTMLElement>("[data-card]")].find((el) => el.dataset.card === id);
  if (!card) throw new Error(`no card ${id} on screen`);
  const label = resolution.allow === false ? t("chat-interactions.deny") : t("chat-interactions.allow");
  const button = [...card.querySelectorAll<HTMLButtonElement>("button")].find((b) => (b.textContent ?? "").trim() === label);
  if (!button) throw new Error(`card ${id} has no "${label}"`);
  button.click();
}

/** The requests since the last check, matched in order against what the case names (others may come between). */
function matchRequests(want: Request[], seen: Seen[], ops: (key: string) => Set<string>, before: Set<string>): string | null {
  let at = 0;
  for (const w of want) {
    const k = seen.findIndex((s, i) => {
      if (i < at) return false;
      if (w.get) return s.get === w.get && (w.after_seq === undefined || s.after_seq === w.after_seq);
      if (w.ws) return s.ws === w.ws && (w.since_seq === undefined || s.since_seq === w.since_seq);
      if (w.post !== s.post) return false;
      if (w.new_op) return s.op !== undefined && !before.has(s.op);
      return s.key === w.id;
    });
    if (k < 0) return `no ${JSON.stringify(w)} in ${JSON.stringify(seen.slice(at))}`;
    at = k + 1;
  }
  void ops;
  return null;
}

/** Compares the page with a view: failed messages back in the composer have no place in the list (README "Allowed
 *  differences"), so they are compared apart from the order. */
function differences(got: ReturnType<typeof project> & { status?: string }, want: View): string[] {
  const out: string[] = [];
  if (want.messages) {
    const placed = (ms: Message[]) => ms.filter((m) => m.state !== "failed").map((m) => `${m.from}:${m.state ?? ""}:${m.text}`);
    const failed = (ms: Message[]) => ms.filter((m) => m.state === "failed").map((m) => m.text).sort();
    if (JSON.stringify(placed(got.messages)) !== JSON.stringify(placed(want.messages))) out.push(`messages ${JSON.stringify(placed(got.messages))}, want ${JSON.stringify(placed(want.messages))}`);
    if (JSON.stringify(failed(got.messages)) !== JSON.stringify(failed(want.messages))) out.push(`failed ${JSON.stringify(failed(got.messages))}, want ${JSON.stringify(failed(want.messages))}`);
  }
  if (want.cards) {
    const open = (cs: Record<string, string>) => Object.keys(cs).filter((id) => cs[id] === "open").sort();
    if (JSON.stringify(open(got.cards)) !== JSON.stringify(open(want.cards))) out.push(`open cards ${JSON.stringify(open(got.cards))}, want ${JSON.stringify(open(want.cards))}`);
  }
  if (want.live !== undefined && (got.live ?? null) !== want.live) out.push(`live ${JSON.stringify(got.live)}, want ${JSON.stringify(want.live)}`);
  if (want.status !== undefined && got.status !== want.status) out.push(`status ${got.status}, want ${want.status}`);
  return out;
}

async function run(c: SyncCase, device: string): Promise<string[]> {
  // the new-conversation page and its start are not driven here yet (#1382)
  if (c.chat.new) return [NO_START];
  hub = new SyncHub(c, device);
  FakeSocket.hub = hub;
  vi.stubGlobal("fetch", hub.fetch);
  const { machine_id: mid, session_id: sid, assistant } = c.chat;
  store.connect();
  await settle();
  root = createRoot(host);
  await act(async () => root.render(<ChatView mid={mid} sid={sid} assistant={assistant} />));
  await settle(100);

  const sent: { id: string; text: string; discarded?: boolean }[] = [];
  const ended = new Set<string>();
  const answering = new Set<string>();
  let checked = 0;
  const failures: string[] = [];
  const look = () => ({ ...project(host, sent.filter((s) => !s.discarded).map((s) => s.text)), status: store.getSession(mid, sid)?.state });

  const invariants = (where: string) => {
    const v = look();
    for (const s of sent) {
      if (s.discarded) continue;
      const n = v.messages.filter((m) => m.from === "user" && m.text === s.text).length;
      if (n !== 1) failures.push(`${where}: sent message ${s.id} shows ${n} times`);
    }
    for (const [id, state] of Object.entries(v.cards)) {
      if (state === "open" && (ended.has(id) || answering.has(id))) failures.push(`${where}: card ${id} is answerable again`);
    }
  };

  for (const [i, st] of c.steps.entries()) {
    const where = `step ${i + 1} (${Object.keys(st).filter((k) => k !== "expect").join("+")})`;
    await step(st, device, { sent, ended, answering });
    await settle();
    invariants(where);
    const want = st.expect?.[device];
    if (want) {
      const got = look();
      for (const d of differences(got, want)) failures.push(`${where}: ${d}`);
      if (want.requests) {
        const before = new Set(hub.seen.slice(0, checked).flatMap((s) => (s.op ? [s.op] : [])));
        const miss = matchRequests(want.requests, hub.seen.slice(checked), (k) => hub.opsOf(k), before);
        if (miss) failures.push(`${where}: requests: ${miss}`);
        checked = hub.seen.length;
      }
    }
  }

  // the end: connected, every read answered, the page's timers run
  if (!hub.socket) {
    await settle(1000);
    const s = hub.waiting.at(-1);
    if (s) await act(async () => hub.open(s, true));
  }
  await settle(10_000);
  invariants("end");
  for (const d of differences(look(), c.end[device])) failures.push(`end: ${d}`);
  return failures;
}

async function step(st: Step, device: string, t: { sent: { id: string; text: string; discarded?: boolean }[]; ended: Set<string>; answering: Set<string> }) {
  if (st.hub) hub.apply(st.hub);
  if (st.socket && st.socket.device === device) {
    if (st.socket.drop) await act(async () => hub.drop());
    if (st.socket.open) {
      // the page reconnects on its own clock: wait for its next socket
      for (let i = 0; i < 120 && !hub.waiting.length; i++) await settle(500);
      const s = hub.waiting.at(-1);
      if (!s) throw new Error("the page never reconnected");
      await act(async () => hub.open(s, st.socket!.open!.resumed));
      if (!st.socket.open.resumed) for (const id of t.answering) if (!hub.truth.pending.some((p) => p.id === id)) t.ended.add(id);
    }
  }
  for (const f of st.frames?.[device] ?? []) {
    if (f.type === "interaction_resolved") t.ended.add(String(f.id));
    await act(async () => hub.push(f));
  }
  const u = st.user;
  if (u && u.device === device) {
    if (u.send) {
      hub.expectSend(u.send.id, u.send.text);
      t.sent.push({ id: u.send.id, text: u.send.text });
      await act(async () => typeAndSend(u.send!.text));
    } else if (u.answer) {
      t.answering.add(u.answer.id);
      await act(async () => answer(u.answer!.id, u.answer!.resolution));
    } else if (u.retry) {
      const words = t.sent.find((s) => s.id === u.retry)!.text;
      await act(async () => typeAndSend(words));
    } else if (u.discard) {
      t.sent.find((s) => s.id === u.discard)!.discarded = true;
    }
  }
  const r = st.reply;
  if (r && r.device === device) {
    await act(async () => hub.reply(r));
    if (t.answering.has(r.write) && !r.cut) {
      t.answering.delete(r.write);
      if ((r.status ?? 0) < 300 || r.status === 410) t.ended.add(r.write);
    }
  }
  if (st.wait) await settle(st.wait);
  if (st.hold && st.hold.device === device) hub.hold(st.hold.get);
  if (st.release && st.release.device === device) await act(async () => hub.release(st.release!.get));
}

for (const [path, file] of Object.entries(files)) {
  describe(`sync ${path.split("/").pop()}`, () => {
    for (const c of file.cases) {
      for (const device of c.devices) {
        const name = c.devices.length > 1 ? `${c.name} as ${device}` : c.name;
        const known = KNOWN[name] ?? (c.chat.new ? START_KNOWN : undefined);
        it(known ? `${name} (known: ${known.issue})` : name, async () => {
          const failures = await run(c, device);
          if (!known) return expect(failures, `${name}; requests this hub does not answer: ${hub.unknown.join(", ")}`).toEqual([]);
          expect(failures.length, `${name} passes now: take it off KNOWN (${known.issue})`).toBeGreaterThan(0);
          expect(failures.filter((f) => !known.because.test(f)), `${name} fails for another reason than ${known.issue}`).toEqual([]);
        }, 60_000);
      }
    }
  });
}
