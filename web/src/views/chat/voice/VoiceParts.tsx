// What a voice call puts into the conversation's page besides the bar (docs/assistant/voice.md 6.2, docs/web/web.md
// "Voice"): its captions as the thread's last rows, the composer's entry and why it cannot start here, and the row that
// stays after a call ended by itself. Each reads the call itself, so ChatView (the hot path) only learns whether a call
// is on.
import { useMemo, useRef, useState } from "react";
import { t } from "../../../i18n";
import { BASE } from "../../../lib/base";
import { navigate } from "../../../lib/router";
import { browserVoice, entryMode, evaluate, httpsAddress } from "../../../lib/voice/availability";
import { CAPTIONS_SHOWN } from "../../../lib/voice/captions";
import { useVoiceCall, useVoiceSnapshot, voiceCalls, type VoiceOwner } from "../../../lib/voice/calls";
import { endNote, unavailableNote, type VoiceUnavailableNote } from "../../../lib/voice/notes";
import { helloAddresses, useVoiceProbe } from "../../../lib/voice/support";
import { recentLines } from "../../../lib/voice/wire";
import type { Item, Session } from "../../../lib/types";
import { Button, Dialog } from "../../../ui/atoms";
import { Icon } from "../../../ui/icons";
import { CaptionRow } from "./VoiceBar";

/** What the composer offers: start a call, or say why it cannot start here. */
export interface VoiceOffer {
  mode: "start" | "explain";
  label: string;
  onClick: () => void;
}

/**
 * The composer's voice entry for this page, and the dialog that explains why it cannot start (docs/assistant/voice.md 6.1).
 * `items`: the transcript as the page has it, read when the call starts (`start.recent`).
 */
export function useVoiceEntry(o: VoiceOwner & { session: Session | undefined; items: () => readonly Item[]; label: string; off?: boolean }): { offer: VoiceOffer | undefined; dialog: React.ReactNode } {
  const probe = useVoiceProbe();
  const [note, setNote] = useState<VoiceUnavailableNote | null>(null);
  const items = useRef(o.items);
  items.current = o.items;
  // whether it can send is all that matters of the session here: the object changes on every push
  const sendable = Boolean(o.session) && !o.off;
  const sessionRef = useRef(o.session);
  sessionRef.current = o.session;
  const availability = useMemo(
    () => evaluate({ session: sendable ? sessionRef.current : undefined, extensions: probe.extensions, info: probe.info, browser: browserVoice() }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [sendable, probe, o.session?.source_kind, o.session?.capabilities?.send],
  );
  const mode = entryMode(availability);
  const extensionID = availability.kind === "available" ? availability.extensionID : "";
  const { mid, sid, owner, label } = o;
  const offer = useMemo<VoiceOffer | undefined>(() => {
    if (!mode) return undefined;
    if (mode === "start") {
      return {
        mode,
        label: t("voice.voice_chat_hint", { name: label }),
        // inside the click: the call makes its audio before anything is awaited
        onClick: () => void voiceCalls.start({ mid, sid, owner, extensionID, recent: recentLines(items.current()) }),
      };
    }
    return {
      mode,
      label: t("voice.voice_unavailable_hint"),
      onClick: () => setNote(unavailableNote(availability, httpsAddress(helloAddresses(), location, BASE))),
    };
  }, [mode, extensionID, mid, sid, owner, label, availability]);
  const dialog = <VoiceUnavailableDialog note={note} onClose={() => setNote(null)} />;
  return { offer, dialog };
}

function VoiceUnavailableDialog({ note, onClose }: { note: VoiceUnavailableNote | null; onClose: () => void }) {
  return (
    <Dialog
      open={Boolean(note)}
      onClose={onClose}
      title={note?.title}
      width={420}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>{t("voice.close")}</Button>
          {note?.settings && (
            <Button
              tone="primary"
              onClick={() => {
                onClose();
                navigate(note.settings!);
              }}
            >
              {t("voice.open_settings")}
            </Button>
          )}
          {note?.https && (
            <Button tone="primary" icon="external" href={note.https}>
              {t("voice.open_https")}
            </Button>
          )}
        </>
      }
    >
      <p data-testid="voice-unavailable" className="text-[13px] leading-relaxed whitespace-pre-line text-ink-2">{note?.message}</p>
      {note?.https && <p className="mt-2 rounded-control bg-inset px-2.5 py-1.5 font-mono text-[12px] break-all text-ink shadow-hairline">{new URL(note.https).origin}</p>}
    </Dialog>
  );
}

/** The call's captions at the end of the thread: temporary rows, in this tab only, not saved (docs/assistant/voice.md 1.2
 *  C): the last few, in the order they were said; ⋯ › All captions has them all. Not a live region: a screen reader
 *  would read them over the voice itself. */
export function VoiceTail({ owner, persona }: { owner: VoiceOwner; persona: string }) {
  const call = useVoiceCall(owner);
  const snap = useVoiceSnapshot(call);
  const captions = snap?.captions.slice(-CAPTIONS_SHOWN) ?? [];
  if (!captions.length) return null;
  return (
    <div data-testid="voice-tail" aria-label={t("voice.tail_title")} className="flex flex-col gap-1.5 border-l-2 border-ai/40 pl-3" style={{ animation: "fade-up 200ms ease-out both" }}>
      <div className="flex items-center gap-1.5 text-[11.5px] font-medium text-ink-3">
        <Icon name="voice" size={12} stroke={2.2} />
        <span className="truncate">{t("voice.tail_title")}</span>
      </div>
      {captions.map((c) => (
        <div key={c.id} data-voice-caption={c.role} data-final={c.final || undefined}>
          <CaptionRow who={c.role === "user" ? t("voice.you") : persona} user={c.role === "user"} text={c.text} final={c.final} />
        </div>
      ))}
    </div>
  );
}

/** Over the composer after a call ended by itself, until the page leaves the conversation: "Voice ended · <why>", with
 *  Continue (an ending that may just pass) or Retry (a failure), Open settings and Details (the extension's own words). */
export function VoiceEnded({ owner }: { owner: VoiceOwner }) {
  const call = useVoiceCall(owner);
  const snap = useVoiceSnapshot(call);
  const [details, setDetails] = useState(false);
  if (!call || !snap || snap.phase.kind !== "ended") return null;
  const note = endNote(snap.phase.end, call.extensionID);
  if (!note) return null;
  return (
    <div role="status" data-testid="voice-ended" className="flex flex-wrap items-center gap-x-2 gap-y-1.5 rounded-[12px] bg-inset px-3 py-2 text-[12.5px] shadow-hairline" style={{ animation: "fade-up 200ms ease-out both" }}>
      <span className="flex min-w-[12rem] flex-1 items-center gap-2">
        <Icon name="voice" size={13} stroke={2.2} className="shrink-0 text-ink-3" />
        <span className="min-w-0 text-ink-2">{t("voice.ended", { why: note.text })}</span>
      </span>
      <span className="ml-auto flex flex-wrap items-center gap-1.5">
        {note.again && (
          <Button tone="secondary" className="h-7 px-2.5 text-[12px]" onClick={() => voiceCalls.resume(owner)}>
            {note.again === "resume" ? t("voice.continue") : t("voice.retry")}
          </Button>
        )}
        {note.settings && (
          <Button tone="quiet" className="h-7 px-2.5 text-[12px]" onClick={() => navigate(note.settings!)}>
            {t("voice.open_settings")}
          </Button>
        )}
        {note.details && (
          <Button tone="quiet" className="h-7 px-2.5 text-[12px]" onClick={() => setDetails(true)}>
            {t("voice.details")}
          </Button>
        )}
        <button
          type="button"
          aria-label={t("voice.close")}
          title={t("voice.close")}
          onClick={() => voiceCalls.leave(owner)}
          className="touch-hit flex size-7 shrink-0 items-center justify-center rounded-[8px] text-ink-3 transition-colors hover:bg-hover hover:text-ink"
        >
          <Icon name="x" size={13} stroke={2.2} />
        </button>
      </span>
      <Dialog open={details} onClose={() => setDetails(false)} title={t("voice.details")} width={460}>
        <pre className="max-h-60 overflow-auto rounded-[8px] bg-inset px-2.5 py-2 font-mono text-[12px] whitespace-pre-wrap text-ink shadow-hairline">{note.details}</pre>
      </Dialog>
    </div>
  );
}
