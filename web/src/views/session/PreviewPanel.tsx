// The dev server preview panel (docs/web/preview.md, #150): a session's dev server as the headless browser on its computer
// draws it, beside the chat. Pick a server (the project's .claude/launch.json, the ports the session's agents serve,
// another port), use the page (pointer, wheel, keys, the address bar, back / forward / reload, desktop or phone size),
// take a screenshot into the message box, or pick an element and comment on it for the agent. Logic in lib/preview.ts.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, enc, errorText } from "../../lib/api";
import { addCodeComment, commentKey } from "../../lib/comment-target";
import { sendDraft } from "../../lib/drafts";
import {
  addressOf, buttonName, commandLine, editCommand, elementQuote, keyMessage, LAUNCH_EXAMPLE, modsOf, parseBinary, PHONE_SIZE, previewOptions,
  previewProblems, previewSocketURL, safeExternal, toPage, type PickedElement, type PreviewMessage, type PreviewOption, type PreviewServers,
} from "../../lib/preview";
import { navigate } from "../../lib/router";
import { useStore } from "../../lib/store";
import { Button, cx, Dialog, IconButton, Menu, Spinner, toast, useMenu } from "../../ui/atoms";
import type { MenuItem } from "../../ui/menu-model";
import { Icon } from "../../ui/icons";
import { hasKey, t } from "../../i18n";
import { PanelHeader } from "../../ui/PanelHeader";
import { Empty } from "./files/parts";
import { LineBox } from "./files/LineComment";

const KEEP = "persona.preview.";
const MAX_DPR = 2;

/** The server last shown for a session, so the panel opens on it again. */
function remembered(mid: string, sid: string): { port: number; path: string } | null {
  try {
    const v = JSON.parse(sessionStorage.getItem(KEEP + `${mid}/${sid}`) ?? "null");
    return v && typeof v.port === "number" ? { port: v.port, path: typeof v.path === "string" ? v.path : "/" } : null;
  } catch {
    return null;
  }
}

function remember(mid: string, sid: string, port: number, path: string) {
  try {
    sessionStorage.setItem(KEEP + `${mid}/${sid}`, JSON.stringify({ port, path }));
  } catch {
    // private mode: it opens on the first server next time
  }
}

type Phase =
  | { kind: "loading" }
  | { kind: "old" }
  | { kind: "failed"; error: string }
  | { kind: "choose" }
  | { kind: "starting"; option: PreviewOption; terminal?: string; since: number }
  | { kind: "live"; port: number; path: string; name: string };

export function PreviewPanel({ mid, sid, cwd, port, path, onClose }: { mid: string; sid: string; cwd: string; port?: number; path?: string; onClose: () => void }) {
  const session = useStore((s) => (s.sessions[mid] ?? []).find((x) => x.session_id === sid));
  const machine = useStore((s) => s.machines.find((m) => m.id === mid));
  const machineName = machine?.name || mid;
  const ports = session?.ports;
  const [servers, setServers] = useState<PreviewServers | null>(null);
  const [phase, setPhase] = useState<Phase>({ kind: "loading" });
  const [confirm, setConfirm] = useState<PreviewOption | null>(null);
  const [starting, setStarting] = useState(false);
  const [other, setOther] = useState(false);
  const options = useMemo(() => previewOptions(servers, ports ?? []), [servers, ports]);
  const menu = useMenu();

  // `current` says whether the caller still wants the reply: one that comes back after the panel moved on changes nothing.
  const load = useCallback(async (current: () => boolean = () => true) => {
    try {
      const res = await api<PreviewServers>(`/machines/${enc(mid)}/preview/servers?cwd=${enc(cwd)}`);
      if (current()) setServers(res);
      return res;
    } catch (e) {
      if (!current()) return null;
      if (e instanceof ApiError && e.code === "node_outdated") setPhase({ kind: "old" });
      else setPhase({ kind: "failed", error: errorText(e) });
      return null;
    }
  }, [mid, cwd]);

  const show = useCallback(
    (o: { port: number; path: string; name: string }) => {
      remember(mid, sid, o.port, o.path);
      setPhase({ kind: "live", ...o });
    },
    [mid, sid],
  );

  // first: the servers, then the one asked for (a link), the one shown last time, or the first running one
  useEffect(() => {
    let gone = false;
    void load().then((res) => {
      if (gone || !res) return;
      const opts = previewOptions(res, ports ?? []);
      const want = port ? { port, path: path ?? "/" } : remembered(mid, sid);
      const pick = want ? opts.find((o) => o.port === want.port) : opts.find((o) => o.listening);
      if (want && (pick?.listening ?? true)) show({ port: want.port, path: want.path, name: pick?.name ?? `:${want.port}` });
      else if (pick?.listening) show({ port: pick.port, path: pick.path, name: pick.name });
      else setPhase({ kind: "choose" });
    });
    return () => {
      gone = true;
    };
    // the session's ports change as it runs: only the first look picks
  }, [load, mid, sid, port, path, show]);

  // waiting for a started server: its port, every 1.5 s, for 90 s. One read at a time and none while the tab is hidden;
  // a reply that comes back after Cancel (or any other phase) is dropped, so it can't open the page or fail the panel.
  useEffect(() => {
    if (phase.kind !== "starting") return;
    const o = phase.option;
    let stopped = false;
    let reading = false;
    const timer = setInterval(async () => {
      if (reading || document.visibilityState === "hidden") return;
      reading = true;
      const res = await load(() => !stopped);
      reading = false;
      // no reply: load already moved the panel to failed or old
      if (stopped || !res) return;
      const now = previewOptions(res, ports ?? []).find((x) => x.port === o.port);
      if (now?.listening) show({ port: o.port, path: o.path, name: o.name });
      else if (Date.now() - phase.since > 90_000) {
        toast(t("preview.start_slow", { name: o.name, port: o.port }), "error");
        setPhase({ kind: "choose" });
      }
    }, 1500);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
  }, [phase, load, ports, show]);

  const start = async (o: PreviewOption) => {
    if (!session?.workspace_id) return;
    setStarting(true);
    try {
      const res = await api<{ pane: string; terminal: string; port: number }>(`/machines/${enc(mid)}/preview/start`, {
        method: "POST",
        json: { cwd, name: o.name, workspace: session.workspace_id, kind: session.space_kind || session.host || "" },
      });
      setConfirm(null);
      setPhase({ kind: "starting", option: o, terminal: res.terminal, since: Date.now() });
    } catch (e) {
      toast(t("preview.start_failed", { name: o.name, error: errorText(e) }), "error");
    } finally {
      setStarting(false);
    }
  };

  const items: MenuItem[] = [
    ...options.map((o) => ({
      key: o.key,
      label: o.name,
      hint: `:${o.port}`,
      detail: o.listening ? o.title && o.title !== o.name ? o.title : undefined : t("preview.not_running"),
      icon: "web" as const,
      checked: phase.kind === "live" && phase.port === o.port,
      radio: true,
      onSelect: () => (o.listening ? show({ port: o.port, path: o.path, name: o.name }) : o.command ? setConfirm(o) : undefined),
    })),
    { key: "other", label: t("preview.other_port"), icon: "plus", divider: options.length > 0, onSelect: () => setOther(true) },
  ];
  const live = phase.kind === "live" ? phase : null;
  const title = live ? live.name : t("preview.title");
  const subtitle = live ? `${machineName} · localhost:${live.port}` : machineName;

  return (
    <div className="flex min-h-0 flex-1 flex-col" data-preview-panel>
      <PanelHeader
        icon="web"
        title={title}
        subtitle={subtitle}
        onClose={onClose}
        actions={
          (options.length > 0 || live) && (
            <button
              type="button"
              onClick={menu.open}
              aria-haspopup="menu"
              className="flex h-7 shrink-0 items-center gap-1 rounded-[8px] px-2 text-[12px] font-medium whitespace-nowrap text-ink-2 transition-colors hover:bg-hover hover:text-ink"
            >
              {t("preview.servers")}
              <Icon name="chevron" size={12} />
            </button>
          )
        }
      />
      {menu.anchor && <Menu items={items} anchor={menu.anchor} onClose={menu.close} align="right" width={260} header={t("preview.servers_header")} />}
      {live ? (
        <PreviewView
          key={`${live.port}`}
          mid={mid}
          sid={sid}
          cwd={cwd}
          port={live.port}
          path={live.path}
          machineName={machineName}
          onPath={(p) => remember(mid, sid, live.port, p)}
          onGone={() => {
            void load();
            setPhase({ kind: "choose" });
          }}
        />
      ) : phase.kind === "loading" ? (
        <div className="flex flex-1 items-center justify-center gap-2 text-[12.5px] text-ink-3">
          <Spinner />
          {t("preview.loading")}
        </div>
      ) : phase.kind === "old" ? (
        <Empty icon="info" title={t("preview.old_title")} detail={<Words>{t("preview.old_detail", { machine: machineName })}</Words>} />
      ) : phase.kind === "failed" ? (
        <Empty icon="info" title={t("preview.load_failed")} detail={phase.error} action={<Button icon="retry" onClick={() => void load().then((r) => r && setPhase({ kind: "choose" }))}>{t("preview.retry")}</Button>} />
      ) : phase.kind === "starting" ? (
        <Empty
          icon="play"
          title={t("preview.starting", { name: phase.option.name })}
          detail={<Words>{t("preview.starting_detail", { port: phase.option.port })}</Words>}
          action={
            <div className="flex items-center gap-2">
              <Spinner />
              {phase.terminal && (
                <Button tone="quiet" icon="terminal" onClick={() => navigate({ name: "terminal", mid, id: phase.terminal })}>
                  {t("preview.show_terminal")}
                </Button>
              )}
              <Button tone="quiet" onClick={() => setPhase({ kind: "choose" })}>
                {t("preview.cancel")}
              </Button>
            </div>
          }
        />
      ) : (
        <Choose servers={servers} options={options} canStart={Boolean(session?.workspace_id)} onShow={show} onStart={setConfirm} onOther={() => setOther(true)} />
      )}
      <Dialog
        open={Boolean(confirm)}
        onClose={() => setConfirm(null)}
        title={confirm ? t("preview.start_title", { name: confirm.name }) : ""}
        footer={
          <>
            <Button tone="quiet" onClick={() => setConfirm(null)}>
              {t("preview.cancel")}
            </Button>
            <Button tone="accent" icon="play" busy={starting} onClick={() => confirm && void start(confirm)}>
              {t("preview.start")}
            </Button>
          </>
        }
      >
        {confirm && (
          <div className="flex flex-col gap-3 text-[13px] leading-relaxed text-ink-2">
            <p>{t("preview.start_explain", { machine: machineName })}</p>
            <pre className="overflow-x-auto rounded-[8px] bg-inset px-3 py-2 font-mono text-[12px] whitespace-pre-wrap text-ink shadow-hairline [overflow-wrap:anywhere]" data-preview-command>
              {commandLine(confirm)}
            </pre>
            {confirm.cwd && <p className="text-[12px] text-ink-3">{t("preview.start_in", { cwd: confirm.cwd })}</p>}
          </div>
        )}
      </Dialog>
      <OtherPort open={other} onClose={() => setOther(false)} onOpen={(p) => show({ port: p, path: "/", name: `:${p}` })} />
    </div>
  );
}

/** Text in an empty state that breaks between words (the state's own breaks anywhere, for paths). */
function Words({ children }: { children: React.ReactNode }) {
  return <span className="break-normal">{children}</span>;
}

/** No server shown: the ones there are (Start for those not running), or how to add some. */
function Choose({ servers, options, canStart, onShow, onStart, onOther }: {
  servers: PreviewServers | null;
  options: PreviewOption[];
  canStart: boolean;
  onShow: (o: { port: number; path: string; name: string }) => void;
  onStart: (o: PreviewOption) => void;
  onOther: () => void;
}) {
  const problems = previewProblems(servers);
  if (!options.length) {
    return (
      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
        <Empty
          icon="web"
          title={servers?.error ? t("preview.file_error") : t("preview.empty_title")}
          detail={servers?.error ?? <Words>{t("preview.empty_detail")}</Words>}
          action={
            <div className="flex flex-col items-center gap-3">
              {!servers?.error && (
                <pre className="max-w-80 rounded-[8px] bg-inset px-3 py-2 text-left font-mono text-[11.5px] leading-[1.5] text-ink-2 shadow-hairline">{LAUNCH_EXAMPLE}</pre>
              )}
              <div className="flex items-center gap-2">
                {!servers?.error && (
                  <Button icon="copy" onClick={() => void navigator.clipboard?.writeText(LAUNCH_EXAMPLE).then(() => toast(t("preview.copied_example"), "ok"))}>
                    {t("preview.copy_example")}
                  </Button>
                )}
                <Button tone="quiet" onClick={onOther}>
                  {t("preview.other_port")}
                </Button>
              </div>
            </div>
          }
        />
        <Problems problems={problems} />
      </div>
    );
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-3 pt-3 pb-6" style={{ animation: "fade-in 160ms ease-out both" }}>
      <div className="px-1 pb-2 text-[12px] font-medium text-ink-3">{t("preview.choose")}</div>
      <div className="flex flex-col overflow-hidden rounded-[12px] bg-surface shadow-hairline">
        {options.map((o, i) => (
          <div key={o.key} className={cx("flex min-w-0 items-center gap-3 px-3 py-2.5", i > 0 && "border-t border-line")} data-preview-server={o.name}>
            <span className={cx("size-2 shrink-0 rounded-full", o.listening ? "bg-green" : "bg-ink-3/40")} aria-hidden />
            <div className="min-w-0 flex-1">
              <div className="truncate text-[13px] font-medium text-ink">
                {o.name} <span className="font-normal text-ink-3">:{o.port}</span>
              </div>
              <div className="truncate font-mono text-[11.5px] text-ink-3">
                {o.listening ? o.title || t("preview.running") : o.command ? commandLine(o) : t("preview.not_running")}
              </div>
            </div>
            {o.listening ? (
              <Button onClick={() => onShow(o)}>{t("preview.open")}</Button>
            ) : o.command && canStart ? (
              <Button icon="play" onClick={() => onStart(o)}>
                {t("preview.start_short")}
              </Button>
            ) : null}
          </div>
        ))}
      </div>
      <button type="button" onClick={onOther} className="mt-2 self-start rounded-[8px] px-2 py-1.5 text-[12.5px] font-medium whitespace-nowrap text-accent-ink transition-colors hover:bg-hover">
        {t("preview.other_port")}
      </button>
      <Problems problems={problems} className="" />
    </div>
  );
}

function Problems({ problems, className = "mx-4" }: { problems: ReturnType<typeof previewProblems>; className?: string }) {
  if (!problems.length) return null;
  return (
    <div className={cx(className, "mt-3 flex flex-col gap-1 rounded-[10px] bg-inset px-3 py-2 text-[12px] text-ink-3 shadow-hairline")} data-preview-problems>
      <div className="font-medium text-ink-2">{t("preview.problems")}</div>
      {problems.map((p, i) => (
        <div key={i} className="truncate">
          {p.name || t("preview.unnamed")}: {hasKey(`preview.problem_${p.problem}`) ? t(`preview.problem_${p.problem}`) : p.problem}
        </div>
      ))}
    </div>
  );
}

/** Another port of that computer, typed in. */
function OtherPort({ open, onClose, onOpen }: { open: boolean; onClose: () => void; onOpen: (port: number) => void }) {
  const [value, setValue] = useState("");
  const n = Number(value);
  const ok = Number.isInteger(n) && n > 0 && n < 65536;
  const submit = () => {
    if (!ok) return;
    onClose();
    setValue("");
    onOpen(n);
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("preview.other_title")}
      width={360}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("preview.cancel")}
          </Button>
          <Button tone="accent" disabled={!ok} onClick={submit}>
            {t("preview.open")}
          </Button>
        </>
      }
    >
      <label className="flex items-center gap-2 text-[13px] text-ink-2">
        <span className="font-mono text-ink-3">localhost:</span>
        <input
          autoFocus
          inputMode="numeric"
          value={value}
          onChange={(e) => setValue(e.target.value.replace(/\D/g, "").slice(0, 5))}
          onKeyDown={(e) => e.key === "Enter" && submit()}
          aria-label={t("preview.other_label")}
          placeholder="3000"
          className="h-9 w-28 rounded-[8px] bg-field px-2.5 font-mono text-[13px] text-ink outline-none placeholder:text-ink-3 focus:shadow-[0_0_0_1.5px_var(--accent)]"
        />
      </label>
    </Dialog>
  );
}

// ── the page ──────────────────────────────────────────

type Bar =
  | { kind: "external"; url: string }
  | { kind: "failed"; url: string; error: string }
  | { kind: "crashed" }
  | { kind: "notice"; code: string }
  | { kind: "dialog"; dialog: Extract<PreviewMessage, { t: "dialog" }> };

type Conn = { kind: "connecting" } | { kind: "open" } | { kind: "lost"; tries: number } | { kind: "error"; code: string; error: string } | { kind: "closed" };

/** One server shown: the socket, the picture, the input and the bars around them. */
function PreviewView({ mid, sid, cwd, port, path, machineName, onPath, onGone }: {
  mid: string;
  sid: string;
  cwd: string;
  port: number;
  path: string;
  machineName: string;
  onPath: (path: string) => void;
  onGone: () => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const keys = useRef<HTMLTextAreaElement>(null);
  const sock = useRef<WebSocket | null>(null);
  const [conn, setConn] = useState<Conn>({ kind: "connecting" });
  const [page, setPage] = useState({ url: "", title: "", loading: true, back: false, forward: false });
  const [address, setAddress] = useState(path);
  const [editing, setEditing] = useState(false);
  const [phone, setPhone] = useState(false);
  const [picking, setPicking] = useState(false);
  const [picked, setPicked] = useState<PickedElement | null>(null);
  const [bar, setBar] = useState<Bar | null>(null);
  const [area, setArea] = useState<{ w: number; h: number } | null>(null);
  const [drawn, setDrawn] = useState(false);
  const pageSize = useRef({ w: 1, h: 1 });
  const at = useRef(path);
  const shots = useRef(new Map<number, boolean>());
  const shotSeq = useRef(0);
  const [reconnect, setReconnect] = useState(0);
  const key = commentKey(mid, sid);

  const send = useCallback((m: Record<string, unknown>) => {
    const s = sock.current;
    if (s?.readyState === WebSocket.OPEN) s.send(JSON.stringify(m));
  }, []);

  // the page's size: the panel's (desktop) or a phone's, in CSS pixels
  const size = useMemo(() => {
    if (!area) return null;
    const dpr = Math.min(window.devicePixelRatio || 1, MAX_DPR);
    return phone ? { ...PHONE_SIZE, dpr, mobile: true } : { w: Math.max(200, Math.floor(area.w)), h: Math.max(200, Math.floor(area.h)), dpr, mobile: false };
  }, [area, phone]);
  const sizeNow = useRef(size);
  sizeNow.current = size;

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setArea({ w: e.contentRect.width, h: e.contentRect.height }));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // the socket: opened once the panel's size is known, opened again after it drops (1, 2, 4, 8, 15 s)
  const ready = size !== null;
  useEffect(() => {
    if (!ready) return;
    let gone = false;
    let tries = 0;
    let timer: ReturnType<typeof setTimeout>;
    const open = async () => {
      // the hub lets a port be shown once the user opened it (an agent's always): this is that open
      try {
        await api(`/machines/${enc(mid)}/ports/${port}/open`, { method: "POST" });
      } catch (e) {
        if (gone) return;
        if (e instanceof ApiError && e.status === 404) {
          setConn({ kind: "error", code: "port_gone", error: errorText(e) });
          return;
        }
        if (e instanceof ApiError && e.status === 503) {
          setConn({ kind: "error", code: "offline", error: errorText(e) });
          return;
        }
      }
      if (gone) return;
      const s = new WebSocket(previewSocketURL(location, mid, port, at.current, sizeNow.current!));
      s.binaryType = "arraybuffer";
      sock.current = s;
      let refused = false;
      s.onopen = () => setConn((c) => (c.kind === "error" ? c : { kind: "open" }));
      s.onmessage = (ev) => {
        if (typeof ev.data !== "string") {
          const b = parseBinary(ev.data as ArrayBuffer);
          if (b?.kind === "frame") void draw(b.data, b.w, b.h).then(() => s.readyState === WebSocket.OPEN && s.send(JSON.stringify({ t: "ack", seq: b.seq })));
          else if (b?.kind === "shot" && shots.current.delete(b.id)) addShot(b.data);
          return;
        }
        let m: PreviewMessage;
        try {
          m = JSON.parse(ev.data);
        } catch {
          return;
        }
        if (m.t === "ready") tries = 0;
        if (m.t === "error") refused = true;
        onMessage(m);
      };
      s.onclose = () => {
        if (sock.current === s) sock.current = null;
        if (gone || refused) return;
        tries++;
        setConn({ kind: "lost", tries });
        timer = setTimeout(open, Math.min(15_000, 1000 * 2 ** Math.min(tries - 1, 4)));
      };
    };
    void open();
    return () => {
      gone = true;
      clearTimeout(timer);
      sock.current?.close();
      sock.current = null;
    };
    // a new socket for another server, or after "Try again"; a new size goes as a message
  }, [ready, mid, port, reconnect]);

  // a new size (the panel resized, Desktop ⇄ Phone) goes to the page, settled first
  useEffect(() => {
    if (!size) return;
    const id = setTimeout(() => send({ t: "size", ...size }), 150);
    return () => clearTimeout(id);
  }, [size, send]);

  const draw = async (data: Uint8Array, w: number, h: number) => {
    const c = canvas.current;
    if (!c) return;
    const bmp = await createImageBitmap(new Blob([data as BlobPart], { type: "image/jpeg" }));
    if (c.width !== bmp.width || c.height !== bmp.height) {
      c.width = bmp.width;
      c.height = bmp.height;
    }
    c.getContext("2d")?.drawImage(bmp, 0, 0);
    bmp.close();
    pageSize.current = { w, h };
    setDrawn(true);
  };

  const addShot = (png: Uint8Array) => {
    let bin = "";
    for (let i = 0; i < png.length; i += 0x8000) bin += String.fromCharCode(...png.subarray(i, i + 0x8000));
    const name = `preview-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-")}.png`;
    if (key && sendDraft(key, { type: "image", image: { name, mime: "image/png", data: btoa(bin) } })) {
      toast(t("preview.shot_added"), "ok");
      return;
    }
    // no message box to take it: saved instead
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([png as BlobPart], { type: "image/png" }));
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 10_000);
    toast(t("preview.shot_saved"), "ok");
  };

  const onMessage = (m: PreviewMessage) => {
    switch (m.t) {
      case "page":
        setPage(m);
        if (m.url) {
          const a = addressOf(m.url, port);
          at.current = a.startsWith("/") ? a : at.current;
          if (!editingNow.current) setAddress(a);
          if (a.startsWith("/")) onPath(a);
        }
        setBar((b) => (b?.kind === "failed" || b?.kind === "crashed" ? null : b));
        break;
      case "external":
        setBar({ kind: "external", url: m.url });
        break;
      case "failed":
        setBar({ kind: "failed", url: m.url, error: m.error });
        break;
      case "crashed":
        setBar({ kind: "crashed" });
        break;
      case "notice":
        setBar({ kind: "notice", code: m.code });
        break;
      case "dialog":
        setBar({ kind: "dialog", dialog: m });
        break;
      case "dialog_closed":
        setBar((b) => (b?.kind === "dialog" ? null : b));
        break;
      case "pick":
        setPicking(m.on);
        break;
      case "picked":
        setPicking(false);
        setPicked(m);
        break;
      case "pick_failed":
        setPicking(false);
        toast(t("preview.pick_failed"), "error");
        break;
      case "shot_failed":
        shots.current.delete(m.id);
        toast(t("preview.shot_failed", { error: m.error }), "error");
        break;
      case "copied":
        void navigator.clipboard?.writeText(m.text).catch(() => undefined);
        break;
      case "error":
        setConn({ kind: "error", code: m.code, error: m.error });
        break;
      case "closed":
        setConn({ kind: "closed" });
        break;
    }
  };
  const editingNow = useRef(editing);
  editingNow.current = editing;

  // ── input ──
  const pointer = useRef<{ id: number; x: number; y: number; scrolled: boolean; touch: boolean } | null>(null);
  const moved = useRef<{ x: number; y: number; buttons: number; mods: number } | null>(null);
  const frame = useRef(0);
  const where = (e: { clientX: number; clientY: number }) => toPage(e.clientX, e.clientY, canvas.current!.getBoundingClientRect(), pageSize.current);
  const flushMove = () => {
    frame.current = 0;
    const m = moved.current;
    moved.current = null;
    if (m) send({ t: "mouse", type: "move", x: m.x, y: m.y, buttons: m.buttons, mods: m.mods, button: m.buttons & 1 ? "left" : "none" });
  };
  const focusKeys = () => keys.current?.focus({ preventScroll: true });

  useEffect(() => {
    const el = canvas.current;
    if (!el) return;
    const wheel = (e: WheelEvent) => {
      e.preventDefault();
      const scale = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? el.clientHeight : 1;
      const p = where(e);
      send({ t: "wheel", ...p, dx: e.deltaX * scale, dy: e.deltaY * scale, mods: modsOf(e) });
    };
    el.addEventListener("wheel", wheel, { passive: false });
    return () => el.removeEventListener("wheel", wheel);
  });

  const onPointerDown = (e: React.PointerEvent<HTMLCanvasElement>) => {
    if (conn.kind !== "open") return;
    e.preventDefault();
    focusKeys();
    e.currentTarget.setPointerCapture(e.pointerId);
    const touch = e.pointerType === "touch";
    pointer.current = { id: e.pointerId, x: e.clientX, y: e.clientY, scrolled: false, touch };
    // a finger may be starting to scroll: its press is sent when it lifts without having moved
    if (!touch) send({ t: "mouse", type: "down", ...where(e), button: buttonName(e.button), buttons: e.buttons, clicks: e.detail || 1, mods: modsOf(e) });
  };
  const onPointerMove = (e: React.PointerEvent<HTMLCanvasElement>) => {
    if (conn.kind !== "open") return;
    const p = pointer.current;
    if (p?.touch && p.id === e.pointerId) {
      const dx = p.x - e.clientX;
      const dy = p.y - e.clientY;
      if (p.scrolled || Math.hypot(dx, dy) > 8) {
        p.scrolled = true;
        p.x = e.clientX;
        p.y = e.clientY;
        const r = canvas.current!.getBoundingClientRect();
        send({ t: "wheel", ...where(e), dx: (dx * pageSize.current.w) / r.width, dy: (dy * pageSize.current.h) / r.height, mods: 0 });
      }
      return;
    }
    moved.current = { ...where(e), buttons: e.buttons, mods: modsOf(e) };
    if (!frame.current) frame.current = requestAnimationFrame(flushMove);
  };
  const onPointerUp = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const p = pointer.current;
    pointer.current = null;
    if (conn.kind !== "open") return;
    if (frame.current) {
      cancelAnimationFrame(frame.current);
      flushMove();
    }
    const at = where(e);
    if (p?.touch) {
      if (!p.scrolled) {
        send({ t: "mouse", type: "move", ...at });
        send({ t: "mouse", type: "down", ...at, button: "left", buttons: 1, clicks: 1, mods: 0 });
        send({ t: "mouse", type: "up", ...at, button: "left", clicks: 1, mods: 0 });
      }
      return;
    }
    send({ t: "mouse", type: "up", ...at, button: buttonName(e.button), buttons: e.buttons, clicks: e.detail || 1, mods: modsOf(e) });
  };

  const onKey = (e: React.KeyboardEvent<HTMLTextAreaElement>, type: "down" | "up") => {
    if (conn.kind !== "open") return;
    if (type === "down" && e.key === "Escape" && picking) {
      e.preventDefault();
      e.stopPropagation();
      togglePick(false);
      return;
    }
    if (type === "down") {
      const cmd = editCommand(e);
      if (cmd) {
        e.preventDefault();
        send({ t: "cmd", name: cmd });
        return;
      }
      // ⌘V / Ctrl+V: the paste event brings the text
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "v") return;
    }
    const m = keyMessage(e.nativeEvent, type);
    if (!m) return;
    // the page has the keys, Esc and Tab too (not the side panel's Esc, nor the page's focus order)
    e.preventDefault();
    e.stopPropagation();
    send(m);
  };

  const togglePick = (on: boolean) => {
    setPicking(on);
    send({ t: "pick", on });
    if (on) focusKeys();
  };
  const shoot = (full: boolean) => {
    const id = ++shotSeq.current;
    shots.current.set(id, true);
    send({ t: "shot", id, full });
  };
  const go = (s: string) => {
    const v = s.trim();
    if (!v) return;
    setEditing(false);
    send({ t: "nav", url: v });
    focusKeys();
  };
  const shotMenu = useMenu();

  const lost = conn.kind === "lost";
  const stopped = conn.kind === "error" || conn.kind === "closed";
  // the picture, letterboxed into the panel at the page's proportions (a phone's is narrower than the panel)
  const fit = useMemo(() => {
    if (!area || !size) return null;
    const s = Math.min(area.w / size.w, area.h / size.h, 1);
    return { w: Math.floor(size.w * s), h: Math.floor(size.h * s) };
  }, [area, size]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-11 shrink-0 items-center gap-1 border-b border-line px-2">
        <IconButton icon="arrowLeft" label={t("preview.back")} onClick={() => send({ t: "back" })} disabled={!page.back || conn.kind !== "open"} />
        <IconButton icon="arrowRight" label={t("preview.forward")} onClick={() => send({ t: "forward" })} disabled={!page.forward || conn.kind !== "open"} />
        <IconButton icon="retry" label={t("preview.reload")} onClick={() => send({ t: "reload" })} disabled={conn.kind !== "open"} />
        <form
          className="min-w-0 flex-1"
          onSubmit={(e) => {
            e.preventDefault();
            go(address);
          }}
        >
          <input
            value={address}
            onFocus={(e) => {
              setEditing(true);
              e.currentTarget.select();
            }}
            onBlur={() => {
              setEditing(false);
              setAddress(addressOf(page.url, port) || at.current);
            }}
            onChange={(e) => setAddress(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                e.stopPropagation();
                e.currentTarget.blur();
              }
            }}
            aria-label={t("preview.address")}
            spellCheck={false}
            className="h-8 w-full min-w-0 rounded-[8px] bg-field px-2.5 font-mono text-[12px] text-ink outline-none placeholder:text-ink-3 focus:shadow-[0_0_0_1.5px_var(--accent)]"
            data-preview-address
          />
        </form>
        {page.loading && conn.kind === "open" && <Spinner />}
        <IconButton icon={phone ? "phone" : "monitor"} label={phone ? t("preview.size_phone") : t("preview.size_desktop")} active={phone} onClick={() => setPhone((p) => !p)} />
        <IconButton icon="target" label={t("preview.pick")} active={picking} onClick={() => togglePick(!picking)} disabled={conn.kind !== "open"} />
        <IconButton icon="image" label={t("preview.screenshot")} onClick={shotMenu.open} disabled={conn.kind !== "open"} />
        {shotMenu.anchor && (
          <Menu
            items={[
              { key: "view", label: t("preview.shot_view"), icon: "image", onSelect: () => shoot(false) },
              { key: "full", label: t("preview.shot_full"), icon: "expand", onSelect: () => shoot(true) },
            ]}
            anchor={shotMenu.anchor}
            onClose={shotMenu.close}
            align="right"
            header={key ? t("preview.shot_header") : t("preview.shot_header_save")}
          />
        )}
      </div>
      {picking && (
        <div className="flex shrink-0 items-center gap-2 border-b border-line bg-accent/[0.08] px-3 py-1.5 text-[12px] text-ink-2" data-preview-picking>
          <Icon name="target" size={13} className="shrink-0 text-accent-ink" />
          <span className="min-w-0 flex-1 truncate">{t("preview.picking")}</span>
          <button type="button" onClick={() => togglePick(false)} className="shrink-0 rounded-[6px] px-1.5 py-0.5 font-medium whitespace-nowrap text-accent-ink hover:bg-hover">
            {t("preview.cancel")}
          </button>
        </div>
      )}
      {bar && <PreviewBar bar={bar} onClose={() => setBar(null)} send={send} />}
      <div ref={box} className={cx("relative flex min-h-0 flex-1 items-center justify-center overflow-hidden", phone ? "bg-inset p-4" : "bg-page")}>
        <canvas
          ref={canvas}
          tabIndex={-1}
          aria-label={t("preview.page_label", { title: page.title || `localhost:${port}` })}
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onPointerCancel={() => (pointer.current = null)}
          onContextMenu={(e) => e.preventDefault()}
          className={cx(
            "block touch-none bg-white transition-opacity duration-200 select-none",
            phone && "rounded-[18px] shadow-overlay",
            picking ? "cursor-crosshair" : "cursor-default",
            (lost || stopped) && drawn && "opacity-40",
            !drawn && "opacity-0",
          )}
          style={fit ? { width: fit.w, height: fit.h } : undefined}
          data-preview-canvas
        />
        {/* the keyboard's target: typed keys, an input method's text and a paste go to the page from here */}
        <textarea
          ref={keys}
          aria-label={t("preview.keyboard_label")}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          className="pointer-events-none absolute top-0 left-0 size-px resize-none opacity-0"
          onKeyDown={(e) => onKey(e, "down")}
          onKeyUp={(e) => onKey(e, "up")}
          onPaste={(e) => {
            const text = e.clipboardData.getData("text/plain");
            e.preventDefault();
            if (text) send({ t: "text", text });
          }}
          onCompositionEnd={(e) => {
            if (e.data) send({ t: "text", text: e.data });
            e.currentTarget.value = "";
          }}
          onInput={(e) => {
            // a phone's keyboard types without key codes: what landed here goes as text
            const el = e.currentTarget;
            if ((e.nativeEvent as InputEvent).isComposing || !el.value) return;
            send({ t: "text", text: el.value });
            el.value = "";
          }}
        />
        {!drawn && !stopped && (
          <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 text-[12.5px] text-ink-3">
            <Spinner />
            {t("preview.connecting", { machine: machineName })}
          </div>
        )}
        {lost && (
          <div className="absolute top-3 left-1/2 flex -translate-x-1/2 items-center gap-2 rounded-full bg-surface px-3 py-1.5 text-[12px] text-ink-2 shadow-overlay" data-preview-lost>
            <Spinner />
            {t("preview.reconnecting")}
          </div>
        )}
        {stopped && (
          <div className="absolute inset-0 flex bg-page/80">
            <Stopped conn={conn} port={port} machineName={machineName} onRetry={() => {
              setConn({ kind: "connecting" });
              setReconnect((n) => n + 1);
            }} onGone={onGone} />
          </div>
        )}
      </div>
      {picked && key && (
        <LineBox
          where={`${picked.selector} · ${addressOf(picked.url, port)}`}
          onCancel={() => setPicked(null)}
          onAdd={(body) => {
            addCodeComment(key, elementQuote(picked, { mid, sid, cwd, port }), body);
            setPicked(null);
          }}
        />
      )}
      {picked && !key && (
        <div className="flex shrink-0 items-center gap-2 border-t border-line px-3 py-2 text-[12px] text-ink-3">
          <span className="min-w-0 flex-1 truncate">{t("preview.no_box")}</span>
          <Button tone="quiet" onClick={() => setPicked(null)}>
            {t("preview.cancel")}
          </Button>
        </div>
      )}
    </div>
  );
}

/** Why the page is not shown any more, and what to do. */
function Stopped({ conn, port, machineName, onRetry, onGone }: { conn: Conn; port: number; machineName: string; onRetry: () => void; onGone: () => void }) {
  if (conn.kind === "closed") return <Empty icon="web" title={t("preview.closed")} action={<Button icon="retry" onClick={onRetry}>{t("preview.reopen")}</Button>} />;
  if (conn.kind !== "error") return null;
  switch (conn.code) {
    case "no_browser":
      return <Empty icon="web" title={t("preview.no_browser_title", { machine: machineName })} detail={<Words>{t("preview.no_browser_detail")}</Words>} action={<Button icon="retry" onClick={onRetry}>{t("preview.retry")}</Button>} />;
    case "port_gone":
      return (
        <Empty
          icon="web"
          title={t("preview.port_gone", { port })}
          detail={<Words>{t("preview.port_gone_detail")}</Words>}
          action={
            <div className="flex gap-2">
              <Button icon="retry" onClick={onRetry}>{t("preview.retry")}</Button>
              <Button tone="quiet" onClick={onGone}>{t("preview.servers")}</Button>
            </div>
          }
        />
      );
    case "offline":
      return <Empty icon="machine" title={t("preview.offline", { machine: machineName })} action={<Button icon="retry" onClick={onRetry}>{t("preview.retry")}</Button>} />;
    case "node_outdated":
      return <Empty icon="info" title={t("preview.old_title")} detail={<Words>{t("preview.old_detail", { machine: machineName })}</Words>} />;
    default:
      return <Empty icon="info" title={t("preview.open_failed")} detail={conn.error} action={<Button icon="retry" onClick={onRetry}>{t("preview.retry")}</Button>} />;
  }
}

/** A line over the page: a link it wanted to open elsewhere, a load that failed, a dialog it is waiting on. */
function PreviewBar({ bar, onClose, send }: { bar: Bar; onClose: () => void; send: (m: Record<string, unknown>) => void }) {
  const [answer, setAnswer] = useState(bar.kind === "dialog" ? (bar.dialog.default ?? "") : "");
  const row = "flex shrink-0 items-center gap-2 border-b border-line bg-surface px-3 py-2 text-[12.5px] text-ink-2";
  if (bar.kind === "external") {
    const url = safeExternal(bar.url);
    return (
      <div className={row} data-preview-bar="external">
        <Icon name="external" size={13} className="shrink-0 text-ink-3" />
        <span className="line-clamp-2 min-w-0 flex-1 [overflow-wrap:anywhere]" title={bar.url}>
          {t("preview.external", { url: bar.url })}
        </span>
        {url && (
          <a href={url} target="_blank" rel="noopener noreferrer" onClick={onClose} className="shrink-0 rounded-[6px] px-1.5 py-0.5 font-medium whitespace-nowrap text-accent-ink hover:bg-hover">
            {t("preview.open_tab")}
          </a>
        )}
        <IconButton icon="x" label={t("preview.dismiss")} size={24} iconSize={13} onClick={onClose} />
      </div>
    );
  }
  if (bar.kind === "dialog") {
    const d = bar.dialog;
    const answerIt = (accept: boolean) => {
      send({ t: "dialog", accept, text: accept ? answer : "" });
      onClose();
    };
    return (
      <div className={cx(row, "flex-wrap")} data-preview-bar="dialog">
        <Icon name="info" size={13} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">{d.kind === "beforeunload" ? t("preview.leave") : d.message}</span>
        {d.kind === "prompt" && (
          <input autoFocus value={answer} onChange={(e) => setAnswer(e.target.value)} onKeyDown={(e) => e.key === "Enter" && answerIt(true)} className="h-7 min-w-0 flex-1 rounded-[6px] bg-field px-2 text-[12.5px] text-ink outline-none" />
        )}
        {d.kind !== "alert" && (
          <Button tone="quiet" onClick={() => answerIt(false)}>
            {t("preview.cancel")}
          </Button>
        )}
        <Button tone="accent" onClick={() => answerIt(true)}>
          {t("preview.ok")}
        </Button>
      </div>
    );
  }
  const text = bar.kind === "failed" ? t("preview.failed", { error: bar.error }) : bar.kind === "crashed" ? t("preview.crashed") : hasKey(`preview.notice_${bar.code}`) ? t(`preview.notice_${bar.code}`) : bar.code;
  return (
    <div className={row} data-preview-bar={bar.kind}>
      <Icon name="warning" size={13} className="shrink-0 text-orange" />
      <span className="min-w-0 flex-1 truncate" title={text}>
        {text}
      </span>
      {bar.kind !== "notice" && (
        <button type="button" onClick={() => send({ t: "reload" })} className="shrink-0 rounded-[6px] px-1.5 py-0.5 font-medium whitespace-nowrap text-accent-ink hover:bg-hover">
          {t("preview.reload")}
        </button>
      )}
      <IconButton icon="x" label={t("preview.dismiss")} size={24} iconSize={13} onClick={onClose} />
    </div>
  );
}
