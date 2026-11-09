// The questions the assistant answered for the user (#544; lib/delegate.ts): what it answered and why, and "Change answer",
// in the chat (DelegatedCard, with the cards just answered) and in the session's info (DelegatedHistory).
import { useEffect, useState } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { changedAnswers, formAnswers, initialPicks, latestAnswers, loadDelegated, reanswer, reanswerChanged, sameAnswer, type DelegatedAnswer } from "../../lib/delegate";
import { ago, sessionCan } from "../../lib/format";
import { composing } from "../../lib/send-history";
import { dropDelegated, getSession, noteReanswer, useStore } from "../../lib/store";
import type { Question, Session } from "../../lib/types";
import { Button, EASE, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Choice } from "./Choice";

/** A question the assistant just answered, in the chat over the composer, until put away. */
export function DelegatedCard({ d }: { d: DelegatedAnswer }) {
  return (
    <div data-delegated className="shrink-0 overflow-hidden rounded-card bg-surface px-3.5 py-2.5 shadow-card" style={{ animation: `fade-up 300ms ${EASE} both` }}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="mb-1 flex items-center gap-1.5 text-[11px] font-medium text-accent-ink">
            <Icon name="sparkles" size={11} stroke={2.4} />
            {t("chat-delegate.answered_title")}
          </div>
          <DelegatedBody d={d} manager={d.ix.manager} />
        </div>
        <button
          type="button"
          aria-label={t("chat-interactions.collapse")}
          title={t("chat-interactions.collapse")}
          onClick={() => dropDelegated(d.ix.id)}
          className="touch-hit flex size-6 shrink-0 items-center justify-center rounded-[6px] text-ink-3 transition-colors hover:bg-hover hover:text-ink"
        >
          <Icon name="x" size={14} stroke={2.2} />
        </button>
      </div>
    </div>
  );
}

/** The questions the assistant answered in this session, newest first, each with "Change answer"; nothing while
 *  loading or when it answered none. */
export function DelegatedHistory({ session }: { session: Session }) {
  const { machine_id: mid, session_id: sid, manager } = session;
  const [rows, setRows] = useState<DelegatedAnswer[] | null>(null);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    let live = true;
    setRows(null);
    setError("");
    loadDelegated(mid, sid, manager)
      .then((r) => live && setRows(r))
      .catch((e) => live && setError(errorText(e)));
    return () => {
      live = false;
    };
  }, [mid, sid, manager, revision]);
  if (!error && !rows?.length) return null;
  return (
    <section aria-label={t("chat-delegate.history_title")}>
      <h3 className="mb-1 text-[11.5px] font-medium text-ink-3">{t("chat-delegate.history_title")}</h3>
      {error ? (
        <div className="flex items-center justify-between gap-3">
          <p role="alert" className="min-w-0 text-[12px] text-red">{error}</p>
          <Button tone="secondary" onClick={() => setRevision((n) => n + 1)}>
            {t("chat-delegate.history_retry")}
          </Button>
        </div>
      ) : (
        <div className="divide-y divide-line overflow-hidden rounded-[10px] bg-inset shadow-hairline">
          {rows!.map((d) => (
            <div key={d.ix.id} data-delegated-row className="px-3 py-2.5">
              {d.at > 0 && <span className="mb-1 block text-[11px] text-ink-3">{ago(new Date(d.at).toISOString())}</span>}
              <DelegatedBody d={d} manager={d.ix.manager ?? manager} />
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

/** Each question with the answer as it is now (the user's change over the assistant's), why the assistant answered
 *  so, and "Change answer" (`manager`: the session's, when not the default). */
function DelegatedBody({ d, manager }: { d: DelegatedAnswer; manager?: string }) {
  const id = d.ix.id;
  const re = useStore((s) => s.reanswered[id]);
  // the new answer reaches the agent as a message (POST …/reanswer), so it takes what sending does, not answering a card
  const canChange = useStore(() => sessionCan(getSession(d.ix.machine_id, d.ix.session_id), "send"));
  const [changing, setChanging] = useState(false);
  const now = latestAnswers(d, re);
  const qs = d.ix.questions ?? [];
  return (
    <>
      <div data-delegated-answer className="flex flex-col gap-1">
        {qs.map((q) => {
          const answer = now[q.question] ?? "";
          const before = (d.answers[q.question] ?? "").trim();
          const changed = Boolean(before) && !sameAnswer(q, answer, before);
          return (
            <div key={q.question} className="min-w-0 text-[13px] leading-snug">
              <span className="block text-[12px] text-ink-3">{q.question}</span>
              <span className="block whitespace-pre-wrap text-ink">{answer || t("lib-answered.unanswered")}</span>
              {changed && <span className="block text-[12px] text-ink-3">{t("chat-delegate.was", { answer: before })}</span>}
            </div>
          );
        })}
      </div>
      {d.reason && <p className="mt-1 text-[12px] leading-snug text-ink-3">{t("chat-delegate.reason", { reason: d.reason })}</p>}
      {changing ? (
        <ReanswerForm id={id} questions={qs} before={now} manager={manager} onClose={() => setChanging(false)} />
      ) : (
        <div className="mt-2 flex items-center justify-between gap-2">
          {re ? (
            <span className="flex min-w-0 items-center gap-1 text-[11.5px] font-medium text-green">
              <Icon name="check" size={11} stroke={3} className="shrink-0" />
              <span className="truncate">{t("chat-delegate.reanswered")}</span>
            </span>
          ) : (
            <span />
          )}
          <Button tone="secondary" icon="pencil" disabled={!canChange} title={canChange ? undefined : t("agents-agentsview.cannot_send")} onClick={() => setChanging(true)}>
            {t("chat-delegate.change")}
          </Button>
        </div>
      )}
    </>
  );
}

/** "Change answer": the answers so far picked or typed out; only the ones changed go to the agent, as a message. */
function ReanswerForm({ id, questions, before, manager, onClose }: { id: string; questions: Question[]; before: Record<string, string>; manager?: string; onClose: () => void }) {
  const [start] = useState(() => initialPicks(questions, before));
  const [picked, setPicked] = useState(start.picked);
  const [custom, setCustom] = useState(start.custom);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const after = formAnswers(questions, picked, custom);
  const ready = reanswerChanged(before, after, questions) && !busy;

  const toggle = (i: number, q: Question, label: string) => {
    setCustom((c) => ({ ...c, [i]: "" }));
    setPicked((cur) => {
      const have = cur[i] ?? [];
      return { ...cur, [i]: q.multi_select ? (have.includes(label) ? have.filter((x) => x !== label) : [...have, label]) : [label] };
    });
  };

  const send = async () => {
    const changed = changedAnswers(before, after, questions);
    setBusy(true);
    setError("");
    const r = await reanswer(id, changed, manager);
    setBusy(false);
    if (!r.ok) {
      setError(r.error);
      return;
    }
    noteReanswer(id, changed);
    toast(t("chat-delegate.sent"), "ok");
    onClose();
  };

  return (
    <div className="mt-2 flex flex-col gap-2 border-t border-line pt-2">
      <p className="text-[12px] leading-snug text-ink-3">{t("chat-delegate.change_hint")}</p>
      {questions.map((q, i) => (
        <div key={q.question} role="group" aria-label={q.question} className="flex flex-col gap-0.5">
          {questions.length > 1 && <span className="text-[12px] font-medium text-ink-2">{q.question}</span>}
          {(q.options ?? []).map((o) => (
            <Choice key={o.label} on={(picked[i] ?? []).includes(o.label)} multi={q.multi_select} label={o.label} desc={o.description} onClick={() => toggle(i, q, o.label)} />
          ))}
          <label className="-mx-1.5 flex items-center gap-2 rounded-control px-1.5 py-1.5 transition-colors focus-within:bg-hover hover:bg-hover">
            <span aria-hidden className="size-4 shrink-0" />
            <input
              value={custom[i] ?? ""}
              onChange={(e) => {
                const v = e.target.value;
                setCustom((c) => ({ ...c, [i]: v }));
                if (v) setPicked((p) => ({ ...p, [i]: [] }));
              }}
              onKeyDown={(e) => e.key === "Enter" && !composing(e.nativeEvent) && ready && void send()}
              placeholder={t("chat-interactions.custom_placeholder")}
              aria-label={t("chat-interactions.custom_answer")}
              className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3"
            />
          </label>
        </div>
      ))}
      {error && (
        <p role="alert" className="text-[12px] leading-snug text-red">
          {error}
        </p>
      )}
      <div className="flex items-center justify-end gap-2">
        <Button tone="quiet" onClick={onClose}>
          {t("common.cancel")}
        </Button>
        <Button tone="primary" busy={busy} disabled={!ready} onClick={() => void send()}>
          {t("chat-delegate.send")}
        </Button>
      </div>
    </div>
  );
}
