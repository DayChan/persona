// The voice call's bar (docs/assistant/voice.md 6.2, docs/web/web.md "Voice"): the composer turned into it in place, in the
// composer's own shape. Its top row: Mute, the orb (a click while the voice speaks interrupts it: never the agent),
// what the voice does, ⋯ and End. Under it the line about the bound conversation (lib/voice/bar-line.ts) with its buttons,
// Stop <agent> among them. Every button is one line; the line's buttons wrap under its words when they do not fit.
import { useEffect, useRef, useState } from "react";
import { t } from "../../../i18n";
import { BUSY_MOTION, spinStyle } from "../../../lib/busy-motion";
import { announced, barLine, barStatus, sinceText, type BarAction, type BarLine, type OrbPhase } from "../../../lib/voice/bar-line";
import { voiceCalls, useVoiceSnapshot } from "../../../lib/voice/calls";
import type { VoiceConversation } from "../../../lib/voice/conversation";
import { chooseVoice, listVoices } from "../../../lib/voice/support";
import type { Interaction, Session } from "../../../lib/types";
import { Button, cx, Dialog, EASE, Menu, useMenu, useNow, type MenuItem } from "../../../ui/atoms";
import { Icon } from "../../../ui/icons";
import { StatusMark } from "../../../ui/StatusMark";
import { resolve } from "../Interactions";

export function VoiceBar({ call, session, cards, label, persona, onStopAgent }: {
  call: VoiceConversation;
  session: Session | undefined;
  /** The bound conversation's pending cards. */
  cards: readonly Interaction[];
  /** Who works in the conversation: "Claude", "Codex", the persona's name for its own. */
  label: string;
  persona: string;
  onStopAgent: () => void;
}) {
  const snap = useVoiceSnapshot(call);
  const [typing, setTyping] = useState(false);
  const [captions, setCaptions] = useState(false);
  const more = useMenu();
  // a new card asks for the user: a cue, as the phone plays (docs/assistant/voice.md 1.2 I)
  const seen = useRef<Set<string> | null>(null);
  const ids = cards.map((c) => c.id).join(",");
  useEffect(() => {
    const now = new Set(ids ? ids.split(",") : []);
    if (seen.current && [...now].some((id) => !seen.current!.has(id))) call.cue("attention");
    seen.current = now;
  }, [ids, call]);
  if (!snap) return null;
  const line = barLine({
    phase: snap.phase, link: snap.link, runs: snap.runs, session, cards, muted: snap.muted, asking: snap.askingID, notice: snap.notice, label,
    dismissed: snap.dismissed, resendable: snap.resendable,
  });
  const working = line.kind === "working" || line.kind === "paused" || line.kind === "waiting";
  const status = barStatus({ phase: snap.phase, link: snap.link, muted: snap.muted, working, tool: snap.tools.at(-1)?.label });
  const perform = (a: BarAction) => {
    switch (a.kind) {
      case "stopAgent":
        return onStopAgent();
      case "resend":
        return void call.resend(a.run);
      case "clearAndSend":
        return void call.resend(a.run, true);
      case "returnPlan": {
        // the page's own answer to the card (decision 5: the user clicked it), with the refused words as the feedback
        call.dismiss(a.run);
        const plan = cards.find((c) => c.id === a.card);
        if (plan) void resolve(plan, { approve: false, feedback: a.feedback });
        return;
      }
      case "dismiss":
        return call.dismiss(a.run);
    }
  };
  const voiceItems = async (): Promise<MenuItem[]> => {
    const list = await listVoices(call.extensionID, snap.model);
    if (!list.voices.length) return [{ key: "none", label: t("voice.voice_none"), note: true, onSelect: () => {} }];
    const picked = snap.voice ?? list.default;
    return list.voices.map((v) => ({
      key: v.id, label: v.name || v.id, radio: true, checked: v.id === picked,
      onSelect: () => {
        chooseVoice(call.extensionID, v.id);
        call.changeVoice(v.id);
      },
    }));
  };
  const menu: MenuItem[] = [
    { key: "type", label: t("voice.type_line"), icon: "keyboard", onSelect: () => setTyping(true) },
    { key: "captions", label: t("voice.all_captions"), icon: "list", onSelect: () => setCaptions(true) },
    { key: "voice", label: t("voice.voice_menu"), icon: "volume", onSelect: () => {}, children: voiceItems },
  ];
  return (
    <div
      data-testid="voice-bar"
      data-voice-phase={snap.phase.kind === "live" ? snap.phase.state : snap.phase.kind}
      className="relative flex flex-col gap-1 rounded-[14px] border border-line bg-surface p-1.5 shadow-card"
      style={{ animation: `pop-in 200ms ${EASE} both` }}
    >
      <div className="flex min-h-10 items-center gap-1.5">
        <button
          type="button"
          data-testid="voice-mute"
          aria-pressed={snap.muted}
          aria-label={snap.muted ? t("voice.unmute") : t("voice.mute")}
          title={snap.muted ? t("voice.unmute") : t("voice.mute")}
          onClick={() => call.setMuted(!snap.muted)}
          className={cx(
            "touch-hit flex size-9 shrink-0 items-center justify-center rounded-full transition-[background-color,color,transform] duration-150 active:scale-[0.94]",
            snap.muted ? "bg-accent-tint text-accent-ink" : "text-ink-2 hover:bg-hover hover:text-ink",
          )}
        >
          <Icon name={snap.muted ? "micOff" : "mic"} size={17} stroke={2} />
        </button>
        <VoiceOrb call={call} phase={status.orb} onInterrupt={() => call.interruptSpeech()} />
        <span data-testid="voice-status" className="min-w-0 flex-1 truncate pl-0.5 text-[13.5px] font-medium text-ink">{status.text}</span>
        <button
          type="button"
          data-testid="voice-more"
          aria-label={t("voice.more")}
          title={t("voice.more")}
          aria-haspopup="menu"
          aria-expanded={Boolean(more.anchor)}
          onClick={more.open}
          className="touch-hit flex size-8 shrink-0 items-center justify-center rounded-[8px] text-ink-3 transition-colors hover:bg-hover hover:text-ink"
        >
          <Icon name="more" size={16} />
        </button>
        {more.anchor && <Menu anchor={more.anchor} onClose={more.close} side="top" align="right" width={200} items={menu} />}
        <button
          type="button"
          data-testid="voice-end"
          aria-label={t("voice.end")}
          title={t("voice.end_hint")}
          onClick={() => voiceCalls.end()}
          className="touch-hit flex size-9 shrink-0 items-center justify-center rounded-full bg-danger-fill text-white transition-[filter,transform] duration-150 hover:brightness-105 active:scale-[0.94]"
        >
          <Icon name="hangUp" size={17} stroke={1.9} />
        </button>
      </div>
      <SecondLine line={line} label={label} perform={perform} />
      <Announce line={line} ended={snap.phase.kind === "ended"} />
      <TypeLine open={typing} onClose={() => setTyping(false)} onSend={(text) => call.say(text)} />
      <CaptionsDialog open={captions} onClose={() => setCaptions(false)} call={call} persona={persona} />
    </div>
  );
}

/** The line about the bound conversation: a mark, its words and how long it has been so, then its buttons. */
function SecondLine({ line, label, perform }: { line: BarLine; label: string; perform: (a: BarAction) => void }) {
  const now = useNow(60_000, line.since !== undefined);
  const strong = line.kind === "needsTap" || line.kind === "asking" || line.kind === "notSent" || line.kind === "offline";
  return (
    <div data-testid="voice-line" data-kind={line.kind} className="flex flex-wrap items-center gap-x-2 gap-y-1.5 px-2 pb-0.5">
      <span className="flex min-w-[12rem] flex-1 items-start gap-1.5">
        <span className="flex h-[18px] shrink-0 items-center">
          <LineMark kind={line.kind} />
        </span>
        <span className={cx("line-clamp-3 min-w-0 text-[12.5px] leading-[18px] [overflow-wrap:anywhere]", strong ? "text-ink" : "text-ink-2")}>
          {sinceText(line.text, line.since, now)}
        </span>
      </span>
      {line.actions.length > 0 && (
        <span className="ml-auto flex flex-wrap items-center justify-end gap-1.5">
          {line.actions.map((a) => (
            <Button
              key={`${a.kind}:${"run" in a ? a.run : ""}`}
              tone={a.kind === "stopAgent" ? "danger" : a.kind === "dismiss" ? "quiet" : "secondary"}
              icon={a.kind === "stopAgent" ? "stop" : undefined}
              onClick={() => perform(a)}
              className="h-7 px-2.5 text-[12px]"
            >
              <span data-voice-action={a.kind}>{actionLabel(a, label)}</span>
            </Button>
          ))}
        </span>
      )}
    </div>
  );
}

function actionLabel(a: BarAction, label: string): string {
  switch (a.kind) {
    case "stopAgent":
      return t("voice.action_stop", { name: label });
    case "resend":
      return t("voice.action_resend");
    case "clearAndSend":
      return t("voice.action_clear_send");
    case "returnPlan":
      return t("voice.action_return_plan");
    case "dismiss":
      return t("voice.action_dismiss");
  }
}

function LineMark({ kind }: { kind: BarLine["kind"] }) {
  switch (kind) {
    case "needsTap":
    case "asking":
      return <StatusMark state="needs_you" size="sm" label={false} />;
    case "notSent":
    case "offline":
      return <Icon name="warning" size={13} stroke={2.2} className="text-orange" />;
    case "muted":
      return <Icon name="micOff" size={13} stroke={2.2} className="text-ink-3" />;
    case "working":
    case "handing":
    case "waiting":
    case "paused":
      return <StatusMark state="working" size="sm" label={false} />;
    default:
      return <Icon name="mic" size={13} stroke={2.2} className="text-ink-3" />;
  }
}

/** A screen reader hears what asks for the user or stops the voice, never the captions (they would talk over the voice
 *  and leak into the microphone), and the call ending. */
function Announce({ line, ended }: { line: BarLine; ended: boolean }) {
  const [said, setSaid] = useState("");
  const kind = line.kind;
  const text = line.text;
  useEffect(() => {
    if (announced(kind)) setSaid(text);
    // only a change of what the line is about: its words change as time passes
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind]);
  useEffect(() => {
    if (ended) setSaid(t("voice.status_ended"));
  }, [ended]);
  return <span role="status" className="sr-only">{said}</span>;
}

/**
 * The orb: the voice's or the microphone's level as a halo, a turning arc while connecting or while the conversation
 * works. While the voice speaks it is the "Interrupt the voice" button (it stops the voice, never the agent); otherwise
 * it is only a picture. The level is read about 20 times a second straight into the halo's style: no render per frame.
 */
function VoiceOrb({ call, phase, onInterrupt }: { call: VoiceConversation; phase: OrbPhase; onInterrupt: () => void }) {
  const halo = useRef<HTMLSpanElement>(null);
  const live = phase === "listening" || phase === "hearing" || phase === "speaking" || phase === "working";
  useEffect(() => {
    const el = halo.current;
    if (!el) return;
    if (!live || matchMedia("(prefers-reduced-motion: reduce)").matches) {
      el.style.transform = "scale(1)";
      return;
    }
    const speaking = phase === "speaking";
    const id = setInterval(() => {
      const l = call.levels();
      const level = speaking ? l.output : l.input;
      el.style.transform = `scale(${(1 + level * 0.55).toFixed(3)})`;
    }, 50);
    return () => clearInterval(id);
  }, [call, phase, live]);
  const dim = phase === "muted" || phase === "paused" || phase === "ended";
  const body = (
    <span className="relative flex size-9 items-center justify-center" aria-hidden>
      <span ref={halo} className={cx("absolute size-7 rounded-full transition-transform duration-75 ease-out", dim ? "bg-line" : "bg-ai-tint")} />
      <span className={cx("relative size-[18px] rounded-full transition-colors duration-200", dim ? "bg-ink-3" : "bg-ai")} />
      {(phase === "connecting" || phase === "working") && (
        <svg className={cx("absolute size-9 text-ai", BUSY_MOTION)} viewBox="0 0 36 36" style={spinStyle(phase === "connecting" ? 900 : 1600)}>
          <circle cx="18" cy="18" r="16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" pathLength={100} strokeDasharray="22 78" />
        </svg>
      )}
    </span>
  );
  if (phase !== "speaking") return <span data-testid="voice-orb" data-orb={phase} className="shrink-0">{body}</span>;
  return (
    <button
      type="button"
      data-testid="voice-orb"
      data-orb={phase}
      aria-label={t("voice.interrupt")}
      title={t("voice.interrupt")}
      onClick={onInterrupt}
      className="touch-hit shrink-0 rounded-full transition-transform duration-150 hover:scale-[1.04] focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none active:scale-[0.94]"
    >
      {body}
    </button>
  );
}

/** ⋯ › Type a line: the same as saying it. */
function TypeLine({ open, onClose, onSend }: { open: boolean; onClose: () => void; onSend: (text: string) => void }) {
  const [text, setText] = useState("");
  const send = () => {
    if (!text.trim()) return;
    onSend(text);
    setText("");
    onClose();
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("voice.type_line")}
      width={400}
      footer={
        <Button tone="primary" disabled={!text.trim()} onClick={send}>
          {t("voice.send")}
        </Button>
      }
    >
      <p className="mb-2 text-[12.5px] text-ink-2">{t("voice.type_line_hint")}</p>
      <input
        autoFocus
        data-testid="voice-type-input"
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.nativeEvent.isComposing) {
            e.preventDefault();
            send();
          }
        }}
        placeholder={t("voice.type_line_placeholder")}
        aria-label={t("voice.type_line_placeholder")}
        className="h-9 w-full rounded-control border border-line bg-field px-3 text-[13.5px] text-ink outline-none placeholder:text-ink-3 focus:border-line-strong"
      />
    </Dialog>
  );
}

/** ⋯ › All captions: every line the call kept, in the order they were said. */
function CaptionsDialog({ open, onClose, call, persona }: { open: boolean; onClose: () => void; call: VoiceConversation; persona: string }) {
  const snap = useVoiceSnapshot(open ? call : null);
  const captions = snap?.captions ?? [];
  return (
    <Dialog open={open} onClose={onClose} title={t("voice.all_captions")} width={520}>
      {captions.length ? (
        <div className="flex flex-col gap-2">
          {captions.map((c) => (
            <CaptionRow key={c.id} who={c.role === "user" ? t("voice.you") : persona} user={c.role === "user"} text={c.text} final={c.final} />
          ))}
        </div>
      ) : (
        <p className="text-[13px] text-ink-3">{t("voice.captions_empty")}</p>
      )}
    </Dialog>
  );
}

export function CaptionRow({ who, user, text, final }: { who: string; user: boolean; text: string; final: boolean }) {
  return (
    <div className="flex items-baseline gap-2 text-[13.5px] leading-[20px]">
      <span className={cx("shrink-0 text-[12px] font-medium whitespace-nowrap", user ? "text-ink-3" : "text-ai")}>{who}</span>
      <span className={cx("min-w-0 [overflow-wrap:anywhere]", final ? "text-ink" : "text-ink-3")}>{text}</span>
    </div>
  );
}
