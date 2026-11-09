// The hub's side of a spec/fixtures/sync case (spec/fixtures/sync/README.md), for one device: the hub's state as the
// case changes it, the reads answered from that state, the writes held until the case answers them, and the socket
// the page connects with, which delivers only what the case says.
import type { Interaction, Item } from "../../../lib/types";

export interface SyncCase {
  name: string;
  description: string;
  /** `new`: the chat a `user.start` starts from the new-conversation page (README "The story"). */
  chat: { machine_id: string; session_id: string; assistant?: boolean; new?: boolean };
  devices: string[];
  start: { hub: HubState };
  steps: Step[];
  end: Record<string, View>;
}

export interface HubState {
  history: string;
  lines: Item[];
  to_seq: number;
  interactions: Interaction[];
  session: { state: string; queued?: string[] };
  event_stream: string;
  event_seq: number;
  page_turns?: number;
}

export interface Step {
  hub?: {
    restart?: { resumable: boolean };
    rewrite?: { history: string; lines: Item[]; to_seq: number };
    append?: Item[];
    to_seq?: number;
    end?: { id: string; status: string; by: string };
    raise?: Interaction;
    session?: { state?: string; queued?: string[] };
  };
  frames?: Record<string, Record<string, unknown>[]>;
  socket?: { device: string; drop?: boolean; open?: { resumed: boolean } };
  user?: { device: string; send?: { id: string; text: string }; start?: { id: string; text: string }; answer?: { id: string; resolution: Record<string, unknown> }; retry?: string; discard?: string };
  reply?: { device: string; write: string; status?: number; body?: unknown; cut?: boolean; done?: boolean; receipt?: { status: number; body: unknown } };
  wait?: number;
  hold?: { device: string; get: string };
  release?: { device: string; get: string };
  expect?: Record<string, View>;
}

export interface Message {
  from: "user" | "agent";
  text: string;
  state?: "pending" | "failed" | "shown";
}

export interface View {
  messages?: Message[];
  cards?: Record<string, "open" | "answering" | "answered">;
  status?: string;
  live?: string | null;
  requests?: Request[];
}

export interface Request {
  get?: string;
  after_seq?: number;
  ws?: string;
  since_seq?: number;
  post?: "messages" | "resolve" | "conversations";
  id?: string;
  new_op?: boolean;
}

/** A request the page made, as the case's `requests` names them. */
export interface Seen {
  get?: string;
  after_seq?: number;
  ws?: string;
  since_seq?: number;
  post?: "messages" | "resolve" | "conversations";
  key?: string; // the case's id for the write: a message's id, a card's id, a start's id
  op?: string;
}

const STATUS: Record<string, string> = { working: "working", needs_you: "blocked", unread: "done", idle: "idle" };
const UPDATED = "2026-01-02T03:04:05Z";

class Truth {
  history: string;
  lines: Item[];
  toSeq: number;
  pending: Interaction[];
  state: string;
  stream: string;
  seq: number;
  updated = UPDATED;
  rev = 100;

  constructor(h: HubState) {
    this.history = h.history;
    this.lines = structuredClone(h.lines);
    this.toSeq = h.to_seq;
    this.pending = structuredClone(h.interactions);
    this.state = h.session.state;
    this.stream = h.event_stream;
    this.seq = h.event_seq;
  }

  apply(c: NonNullable<Step["hub"]>) {
    if (c.restart && !c.restart.resumable) this.stream += "+";
    if (c.rewrite) {
      this.history = c.rewrite.history;
      this.lines = structuredClone(c.rewrite.lines);
      this.toSeq = c.rewrite.to_seq;
    }
    if (c.append) {
      this.lines.push(...structuredClone(c.append));
      this.toSeq = Math.max(this.toSeq, ...c.append.map((i) => i.seq));
    }
    if (c.to_seq !== undefined) this.toSeq = c.to_seq;
    if (c.append?.length) {
      // the list moves with the transcript, as the hub's does
      const last = c.append[c.append.length - 1];
      if (last.ts && last.ts > this.updated) this.updated = last.ts;
      this.rev += 10;
    }
    if (c.end) {
      this.pending = this.pending.filter((i) => i.id !== c.end!.id);
      this.seq++;
    }
    if (c.raise) {
      this.pending.push(structuredClone(c.raise));
      this.seq++;
    }
    if (c.session?.state) this.state = c.session.state;
  }

  row(chat: SyncCase["chat"]) {
    return {
      machine_id: chat.machine_id, session_id: chat.session_id, title: "Weekend trip", cwd: "/home/user/project",
      status: STATUS[this.state] ?? "idle", state: this.state, assistant: Boolean(chat.assistant), updated_at: this.updated, rev: this.rev,
    };
  }

  /** The latest page: the last `turns` turns (a turn starts at a user's text line), or everything. */
  latest(turns?: number) {
    let start = 0;
    if (turns) {
      const starts = this.lines.flatMap((it, i) => (it.role === "user" && it.kind === "text" ? [i] : []));
      start = starts.length > turns ? starts[starts.length - turns] : 0;
    }
    return { items: structuredClone(this.lines.slice(start)), history: this.history, to_seq: this.toSeq, has_more: start > 0 };
  }

  after(seq: number) {
    return { items: structuredClone(this.lines.filter((i) => i.seq > seq)), history: this.history, from_seq: seq, to_seq: this.toSeq };
  }

  before(seq: number, turns?: number) {
    const older = this.lines.filter((i) => i.seq < seq);
    let start = 0;
    if (turns) {
      const starts = older.flatMap((it, i) => (it.role === "user" && it.kind === "text" ? [i] : []));
      start = starts.length > turns ? starts[starts.length - turns] : 0;
    }
    return { items: structuredClone(older.slice(start)), history: this.history, has_more: start > 0 };
  }
}

type Deferred = { resolve: (r: Response) => void; reject: (e: unknown) => void };

type WriteKind = "messages" | "resolve" | "conversations";

interface HeldWrite extends Deferred {
  kind: WriteKind;
  key: string;
  op: string;
}

export class FakeSocket {
  static hub: SyncHub | null = null;
  static OPEN = 1;
  readyState = 0;
  url: string;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number; reason?: string }) => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
    FakeSocket.hub?.socketMade(this);
  }
  send(s: string) {
    FakeSocket.hub?.fromPage(this, s);
  }
  close() {
    if (this.readyState === 3) return;
    this.readyState = 3;
    FakeSocket.hub?.socketGone(this);
  }
  addEventListener() {}
  removeEventListener() {}
}

function json(v: unknown, status = 200) {
  return new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
}

export class SyncHub {
  readonly c: SyncCase;
  readonly device: string;
  truth: Truth;
  seen: Seen[] = [];
  unknown: string[] = [];
  /** The open socket, and sockets the page opened while the case keeps it disconnected. */
  socket: FakeSocket | null = null;
  waiting: FakeSocket[] = [];
  accepting = true;
  private writes: HeldWrite[] = [];
  private receipts = new Map<string, { status: number; body: unknown }>(); // by op id
  private keyOf = new Map<string, string>(); // the page's client_msg_id → the case's message id
  private holds = new Map<string, Deferred[] | true>();
  private lastCmid = new Map<string, string>(); // the case's message id → the client_msg_id its latest write had
  private texts = new Map<string, string>(); // the case's message id → its words
  private ops = new Map<string, Set<string>>(); // the case's id → the op ids its writes had
  /** A `new` chat: whether the hub has carried out its start (until then it has no session, README "The story"). */
  started: boolean;
  private startId = ""; // the case's id for the start

  constructor(c: SyncCase, device: string) {
    this.c = c;
    this.device = device;
    this.truth = new Truth(c.start.hub);
    this.started = !c.chat.new;
  }

  /** The session list: the chat's row once it exists. */
  private rows() {
    return this.started ? [this.truth.row(this.c.chat)] : [];
  }

  /** The start the case is about to make from the new-conversation page. */
  expectStart(id: string) {
    this.startId = id;
  }

  private get base() {
    return `/api/v1/sessions/${encodeURIComponent(this.c.chat.machine_id)}/${encodeURIComponent(this.c.chat.session_id)}`;
  }

  /** A send the case is about to make: its words find the write the page makes for it. */
  expectSend(id: string, text: string) {
    this.texts.set(id, text);
  }

  opsOf(key: string) {
    return this.ops.get(key) ?? new Set<string>();
  }

  inFlight(kind: WriteKind, key: string) {
    return this.writes.some((w) => w.kind === kind && w.key === key);
  }

  // ── the socket ──

  socketMade(s: FakeSocket) {
    if (this.accepting && !this.socket) {
      queueMicrotask(() => this.open(s, false, true));
      return;
    }
    this.waiting.push(s);
  }

  socketGone(s: FakeSocket) {
    if (this.socket === s) this.socket = null;
    this.waiting = this.waiting.filter((w) => w !== s);
  }

  /** Opens `s`: the hello, then the hub's connection snapshot (machines, the chat's session list). The pending cards
   *  follow only on the first connection; later ones get the case's frames for them. */
  open(s: FakeSocket, resumed: boolean, first = false) {
    this.waiting = this.waiting.filter((w) => w !== s);
    this.socket = s;
    s.readyState = 1;
    s.onopen?.();
    this.push({
      type: "hello", server_version: "test", client_id: "c-" + this.device, hub_id: "hub1", hub_name: "Hub",
      features: ["client_msg_id", "client_ops", "send_refusals", "sessions_delta", "error_codes", "client_hello", "app_ping", "resumable_interactions", "assistant_start_ops"],
      event_stream: this.truth.stream, event_seq: this.truth.seq, resumed,
    });
    this.push({ type: "machines", machines: [{ id: this.c.chat.machine_id, name: "Hub machine", online: true }] });
    this.push({ type: "sessions", machine_id: this.c.chat.machine_id, sessions: this.rows() });
    if (first) for (const ix of this.truth.pending) this.push({ type: "interaction", interaction: ix });
  }

  drop() {
    const s = this.socket;
    this.socket = null;
    this.accepting = false;
    if (!s) return;
    s.readyState = 3;
    s.onclose?.({ code: 1006 });
  }

  push(frame: Record<string, unknown>) {
    if (!this.socket) throw new Error(`a frame for a closed socket: ${String(frame.type)}`);
    this.socket.onmessage?.({ data: JSON.stringify(this.tie(frame)) });
  }

  /** The case ties a line to its own message id (`m1`); the page sent that message with an id of its own. The node
   *  tags the line with the id the message went with, so the hub here does too. */
  tie<T>(v: T): T {
    const walk = (x: unknown): unknown => {
      if (Array.isArray(x)) return x.map(walk);
      if (!x || typeof x !== "object") return x;
      const o = { ...(x as Record<string, unknown>) };
      for (const [k, val] of Object.entries(o)) o[k] = walk(val);
      if (typeof o.client_msg_id === "string") o.client_msg_id = this.lastCmid.get(o.client_msg_id) ?? o.client_msg_id;
      return o;
    };
    return walk(v) as T;
  }

  fromPage(s: FakeSocket, raw: string) {
    if (s !== this.socket) return;
    const m = JSON.parse(raw) as Record<string, unknown>;
    if (m.type === "subscribe" && m.session_id === this.c.chat.session_id) {
      this.seen.push({ ws: "subscribe", since_seq: typeof m.since_seq === "number" ? m.since_seq : undefined });
      // the first subscription's replay (the last turns) is how the chat loads; later ones come from the case
      if (this.accepting && this.seen.filter((r) => r.ws === "subscribe").length === 1) {
        queueMicrotask(() => this.socket && this.push({ type: "items", machine_id: this.c.chat.machine_id, session_id: this.c.chat.session_id, backlog: true, ...this.truth.latest(2) }));
      }
    }
  }

  // ── HTTP ──

  fetch = async (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
    const url = new URL(String(input instanceof Request ? input.url : input), "http://hub");
    const path = url.pathname.replace(/^.*?(\/api\/v1\/)/, "/api/v1/");
    const method = (init.method ?? (input instanceof Request ? input.method : "GET")).toUpperCase();
    const headers = new Headers(init.headers);
    const op = headers.get("X-Persona-Op-Id") ?? "";
    const q = url.searchParams;
    if (method === "GET") {
      if (path === "/api/v1/sessions") return this.read("/sessions", () => ({ sessions: this.rows() }));
      // the assistant's page reads where its conversations run; a started chat is the one it opens
      if (path === "/api/v1/assistant") return json({ machine_id: this.c.chat.machine_id, conversations: this.rows() });
      if (path === "/api/v1/interactions") return this.read("/interactions", () => ({ interactions: structuredClone(this.truth.pending) }));
      if (path === "/api/v1/machines") return json({ machines: [{ id: this.c.chat.machine_id, name: "Hub machine", online: true }] });
      if (path === `${this.base}/items`) {
        if (q.has("after_seq")) {
          const after = Number(q.get("after_seq"));
          this.seen.push({ get: "/items", after_seq: after });
          return this.read("/items", () => this.truth.after(after));
        }
        this.seen.push({ get: "/items" });
        const turns = q.has("turns") ? Number(q.get("turns")) : undefined;
        if (q.has("before_seq")) return this.read("/items", () => this.truth.before(Number(q.get("before_seq")), turns));
        return this.read("/items", () => this.truth.latest(this.c.start.hub.page_turns ?? turns));
      }
      const opPath = path.match(/^\/api\/v1\/ops\/([^/]+)$/);
      if (opPath) {
        const r = this.receipts.get(decodeURIComponent(opPath[1]).toLowerCase());
        if (!r) return json({ error: "no such operation" }, 404);
        const body = btoa(JSON.stringify(r.body));
        return json({ id: opPath[1], state: r.status >= 200 && r.status < 300 ? "done" : "failed", result: { status: r.status, body, content_type: "application/json" } });
      }
      this.unknown.push(`GET ${path}`);
      return json({});
    }
    if (method === "POST" && path === `${this.base}/messages`) {
      const body = JSON.parse(String(init.body ?? "{}")) as { text?: string; client_msg_id?: string };
      const cmid = body.client_msg_id ?? op;
      let key = this.keyOf.get(cmid);
      if (!key) {
        key = [...this.texts].find(([, text]) => text === body.text)?.[0] ?? cmid;
        this.keyOf.set(cmid, key);
      }
      this.lastCmid.set(key, cmid);
      return this.write("messages", key, (op || cmid).toLowerCase());
    }
    const resolve = path.match(/^\/api\/v1\/interactions\/([^/]+)\/resolve$/);
    if (method === "POST" && resolve) return this.write("resolve", decodeURIComponent(resolve[1]), op.toLowerCase());
    if (method === "POST" && path === "/api/v1/assistant/conversations" && this.c.chat.new) return this.write("conversations", this.startId, op.toLowerCase());
    if (method === "POST" && path.endsWith("/seen")) return json({ ok: true });
    this.unknown.push(`${method} ${path}`);
    return json({});
  };

  private read(path: string, answer: () => unknown): Promise<Response> {
    const held = this.holds.get(path);
    if (held === true) {
      const body = this.tie(answer()); // what the hub held when the read came: a stale answer at the release
      return new Promise<Response>((resolve, reject) => {
        this.holds.set(path, [{ resolve: () => resolve(json(body)), reject }]);
      });
    }
    return Promise.resolve(json(this.tie(answer())));
  }

  private write(kind: WriteKind, key: string, op: string): Promise<Response> {
    this.seen.push({ post: kind, key, op });
    const ops = this.ops.get(key) ?? new Set<string>();
    ops.add(op);
    this.ops.set(key, ops);
    const receipt = op ? this.receipts.get(op) : undefined;
    if (receipt) return Promise.resolve(json(receipt.body, receipt.status));
    return new Promise<Response>((resolve, reject) => this.writes.push({ kind, key, op, resolve, reject }));
  }

  hold(path: string) {
    this.holds.set(path, true);
  }

  release(path: string) {
    const held = this.holds.get(path);
    this.holds.delete(path);
    if (Array.isArray(held)) held.forEach((d) => d.resolve(new Response()));
    else throw new Error(`release of ${path}: the page never read it`);
  }

  reply(r: NonNullable<Step["reply"]>) {
    const answering = this.writes.filter((w) => w.key === r.write);
    if (!answering.length) throw new Error(`reply to ${r.write}: the page has no write for it in flight`);
    this.writes = this.writes.filter((w) => w.key !== r.write);
    const done = r.done ?? (r.status !== undefined && r.status >= 200 && r.status < 300);
    if (done && r.receipt) for (const w of answering) this.receipts.set(w.op, r.receipt);
    else if (done && !r.cut && r.status) for (const w of answering) this.receipts.set(w.op, { status: r.status, body: r.body ?? {} });
    // a start carried out: from now on the chat exists (one op id starts one conversation, README "Steps")
    if (done && answering.some((w) => w.kind === "conversations")) {
      this.started = true;
      if (!this.truth.state) this.truth.state = "working";
    }
    for (const w of answering) {
      if (r.cut) w.reject(new TypeError("Failed to fetch"));
      else w.resolve(json(r.body ?? {}, r.status ?? 200));
    }
  }

  apply(c: NonNullable<Step["hub"]>) {
    this.truth.apply(c);
  }
}
