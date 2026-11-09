// What can be done with one item besides reading it (docs/assistant/inbox.md "What you can do with one item"): "AI draft reply" (a reply in the
// user's voice, edited and sent from here) and "Hand to AI" (the item as <persona-context> to Persona or a running agent;
// Persona's main conversation may then write back to the item).
import { chooseManager, noteConversation, otherManager, ownerHeaders } from "../../lib/manager-choice";
import { useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, errorText } from "../../lib/api";
import { startConversation } from "../../lib/assistant-start";
import { adoptConversation, askPersonaOf, noteSent, sendToSession } from "../../lib/outbox";
import { sessionTitle, stateOf, uuid } from "../../lib/format";
import {
  agentDraft, connectorReference, conversationRoute, docChangeLine, HANDOFF_PRESETS, handOffMessage, handOffToMain, isLark, itemKey, itemPath, itemRequest, sendReply,
  setDone, unsentReplies, type DocChange, type InboxItem,
} from "../../lib/inbox";
import { isTransient } from "../../lib/ops";
import { machineWithHub } from "../../lib/mesh";
import { navigate } from "../../lib/router";
import { refreshAssistant, useStore } from "../../lib/store";
import type { Session } from "../../lib/types";
import { t } from "../../i18n";
import { Button, cx, Dialog, Loading, toast } from "../../ui/atoms";
import { StatusMark } from "../../ui/StatusMark";
import { Icon } from "../../ui/icons";
import { Spark } from "./InboxParts";
import { byTime } from "../../lib/times";

const changeKey = (c: DocChange) => `${c.ref}|${c.field}`;

export function draftTitle(it: InboxItem): string {
  if (it.moment) return t("inbox-inboxdialogs.title_moment");
  if (it.doc) return t("inbox-inboxdialogs.title_comment", { title: it.title });
  if (it.connector) {
    // A pull request / issue is commented on ("Comment #12"); a chat is answered.
    const ref = connectorReference(it);
    return ref ? t("inbox-inboxdialogs.title_comment", { title: ref.split("/").pop() ?? "" }) : t("inbox-inboxdialogs.title_reply_to", { title: it.title });
  }
  return isLark(it) ? t("inbox-inboxdialogs.title_reply_to", { title: it.title }) : t("inbox-inboxdialogs.title_reply_mail");
}

/** The drafted reply, editable; "Rewrite" asks again with an instruction; "Send" replies and files the item as done. */
export function DraftDialog(props: { item: InboxItem; onClose: () => void }) {
  return <DraftEditor key={itemKey(props.item)} {...props} />;
}

function DraftEditor({ item, onClose }: { item: InboxItem; onClose: () => void }) {
  const sessions = useStore((s) => s.sessions);
  const assistant = useStore((s) => s.assistant);
  const key = itemKey(item);
  const agentText = agentDraft(item);
  const [text, setText] = useState(() => unsentReplies.get(key) ?? agentText ?? "");
  const [instruction, setInstruction] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [dropped, setDropped] = useState<Set<string>>(new Set());
  const proposed = agentText ? item.agent?.changes ?? [] : [];
  const kept = proposed.filter((c) => !dropped.has(changeKey(c)));
  const generation = useRef(0);
  const agent = item.agent;

  // Save as text changes: navigation can unmount without onClose, and sendReply owns clearing/restoring on send.
  const keepText = (value: string) => {
    unsentReplies.set(key, value);
    setText(value);
  };

  const load = async (ask: string) => {
    const request = ++generation.current;
    setLoading(true);
    try {
      const r = await api<{ text?: string }>(`${itemPath(item.id)}/draft`, { method: "POST", json: { instruction: ask }, headers: itemRequest(item) });
      if (request !== generation.current) return;
      keepText(r.text ?? "");
      setInstruction("");
      setError("");
    } catch (e) {
      if (request === generation.current) setError(errorText(e));
    } finally {
      if (request === generation.current) setLoading(false);
    }
  };

  useEffect(() => {
    if (!unsentReplies.has(key) && !agentText) void load("");
    return () => {
      generation.current++;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  const cancel = () => {
    // Like manual mail, Cancel discards; incidental dismissal keeps the reply.
    unsentReplies.delete(key);
    onClose();
  };

  const send = () => {
    void sendReply(item, text.trim(), kept);
    onClose();
  };
  const canSend = !loading && (text.trim().length > 0 || kept.length > 0);

  return (
    <Dialog
      open
      onClose={onClose}
      title={<span className="block max-w-[420px] truncate">{draftTitle(item)}</span>}
      width={580}
      footer={
        <>
          <Button tone="quiet" onClick={cancel}>{t("common.cancel")}</Button>
          <Button tone="accent" icon="send" disabled={!canSend} onClick={send}>{t("inbox-inboxdialogs.send")}</Button>
        </>
      }
    >
      {agentText && agent && (agent.note || agent.session_id) && (
        <div className="mb-3 rounded-[10px] bg-accent-tint px-3 py-2.5">
          {agent.note && <p className="text-[12px] leading-relaxed text-ink-2"><Spark />{agent.note}</p>}
          {agent.machine_id && agent.session_id && (
            <button
              type="button"
              onClick={() => {
                onClose();
                // The AI twin runs as the assistant (docs/assistant/inbox.md): its conversation is one of Persona's.
                navigate(agent.machine_id === assistant?.machine_id ? { name: "persona", sid: agent.session_id! } : conversationRoute(agent.machine_id!, agent.session_id!, sessions, assistant));
              }}
              className="mt-1.5 flex items-center gap-1 text-[12px] font-medium whitespace-nowrap text-accent-ink hover:underline"
            >
              <Icon name="list" size={12} stroke={2.2} />
              {t("inbox-inboxdialogs.view_process")}
            </button>
          )}
        </div>
      )}
      {proposed.length > 0 && (
        <div className="mb-3">
          <p className="mb-1 text-[11.5px] font-medium text-ink-3">{t("inbox-inboxdialogs.changes_on_send")}</p>
          {proposed.map((c) => {
            const on = !dropped.has(changeKey(c));
            return (
              <label key={changeKey(c)} className="flex cursor-pointer items-center gap-2 py-1 text-[12.5px] text-ink">
                <input
                  type="checkbox"
                  checked={on}
                  onChange={() => setDropped((d) => {
                    const n = new Set(d);
                    if (on) n.add(changeKey(c));
                    else n.delete(changeKey(c));
                    return n;
                  })}
                  className="size-3.5 accent-[var(--accent)]"
                />
                <span className={cx("truncate", !on && "text-ink-3 line-through")}>{docChangeLine(c, item.doc)}</span>
              </label>
            );
          })}
        </div>
      )}
      {item.connector?.reply_to && <p className="mb-2 text-[11.5px] leading-relaxed text-ink-3">{t("inbox-inboxdialogs.reply_goes", { how: item.connector.reply_to })}</p>}
      <div className="relative">
        <textarea
          value={text}
          onChange={(e) => keepText(e.target.value)}
          disabled={loading}
          rows={8}
          aria-label={t("inbox-inboxdialogs.reply_label")}
          placeholder={loading ? "" : t("inbox-inboxdialogs.reply_placeholder")}
          className="block w-full resize-y rounded-control bg-field px-3 py-2.5 text-[13.5px] leading-relaxed text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-50"
        />
        {loading && (
          <div className="pointer-events-none absolute top-3 left-3">
            <Loading label={t("inbox-inboxdialogs.drafting")} />
          </div>
        )}
      </div>
      {error && <p className="mt-2 text-[12px] text-red">{error}</p>}
      <form
        className="mt-3 flex items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (!loading) void load(instruction);
        }}
      >
        <input
          value={instruction}
          onChange={(e) => setInstruction(e.target.value)}
          placeholder={t("inbox-inboxdialogs.instruction_placeholder")}
          aria-label={t("inbox-inboxdialogs.instruction_label")}
          className="h-8 min-w-0 flex-1 rounded-control bg-field px-3 text-[12.5px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)]"
        />
        <Button type="submit" tone="secondary" icon="retry" busy={loading}>{t("inbox-inboxdialogs.rewrite")}</Button>
      </form>
    </Dialog>
  );
}

/** The hub may have taken a hand-off it gave no answer for: the same one asked again is sent once. Any other answer is
 *  kept under its client_id, so a retry after it needs a new one. */
const mayHaveGone = (e: unknown) => isTransient(e) || (e instanceof ApiError && (e.code === "assistant_in_doubt" || e.code === "assistant_offline"));

/** Hands the item to Persona (its manager's main conversation by default, which may write back to the item, or a new
 *  one) or to a running agent, as a <persona-context> plus the ask. */
export function HandOffDialog({ item, onClose }: { item: InboxItem; onClose: () => void }) {
  const sessions = useStore((s) => s.sessions);
  const machines = useStore((s) => s.machines);
  const [ask, setAsk] = useState("");
  const [target, setTarget] = useState<"main" | "new" | Session>("main");
  // Off for the main conversation, which may still write back to the item; on for the others. The user's own choice stays.
  const [markDone, setMarkDone] = useState(false);
  const doneChosen = useRef(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // The main conversation's hand-off being tried: the same text again after no answer is the same hand-off.
  const attempt = useRef<{ text: string; id: string } | null>(null);

  const choose = (next: "main" | "new" | Session) => {
    setTarget(next);
    if (!doneChosen.current) setMarkDone(next !== "main");
  };

  // The 8 agents worked on most recently, on computers that are online now.
  const agents = useMemo(() => {
    const online = new Set(machines.filter((m) => m.online).map((m) => m.id));
    return Object.values(sessions).flat()
      .filter((s) => !s.assistant && online.has(s.machine_id) && stateOf(s) !== "offline")
      .sort((a, b) => byTime(b.updated_at, a.updated_at))
      .slice(0, 8);
  }, [sessions, machines]);

  const send = async () => {
    setBusy(true);
    setError("");
    try {
      const full = await api<InboxItem>(itemPath(item.id), { headers: itemRequest(item) });
      const text = handOffMessage({ ...item, ...full }, ask);
      if (target === "main") {
        const id = attempt.current?.text === text ? attempt.current.id : uuid();
        attempt.current = { text, id };
        let to;
        try {
          to = await handOffToMain(item, text, id);
        } catch (e) {
          if (!(e instanceof ApiError && (e.status === 404 || e.status === 405))) {
            if (!mayHaveGone(e)) attempt.current = null;
            throw e;
          }
          // An older hub or manager: the text goes to the main conversation as before, nothing to write back to.
          // A failure after it opens shows there with "Retry".
          const { sent } = await askPersonaOf(item.manager, text);
          onClose();
          void sent.then((ok) => {
            if (ok && markDone) void setDone(item, true);
          });
          return;
        }
        // its requests (feed, stop…) go to the item's manager, whose page shows it
        adoptConversation(item.manager, to.session_id);
        noteSent(to.machine_id, to.session_id, { text });
        void refreshAssistant().catch(() => {});
        toast(t("inbox-inboxdialogs.handed_to_persona"), "ok");
        onClose();
        navigate({ name: "persona", sid: to.session_id });
        if (markDone) void setDone(item, true);
        return;
      }
      if (target !== "new") {
        // the conversation opens at once with the message on its way; a failure shows there with "Retry"
        onClose();
        navigate({ name: "session", mid: target.machine_id, sid: target.session_id });
        void sendToSession(target.machine_id, target.session_id, { text }).then((ok) => {
          if (ok && markDone) void setDone(item, true);
        });
        return;
      }
      // Persona here is the item's own manager's (docs/assistant/managers.md "Clients"), whatever the assistant page shows now;
      // the page then shows that manager, where the conversation is
      const r = await startConversation({ prompt: text }, ownerHeaders(item.manager));
      // its requests (feed, stop…) go to that manager whatever the page shows later
      noteConversation(r.session.session_id, otherManager(item.manager));
      if (item.manager) chooseManager(item.manager);
      noteSent(r.session.machine_id, r.session.session_id, { text });
      void refreshAssistant().catch(() => {});
      if (markDone) void setDone(item, true);
      toast(t("inbox-inboxdialogs.handed_to_persona"), "ok");
      onClose();
      navigate({ name: "persona", sid: r.session.session_id });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("inbox-inboxdialogs.handoff_title")}
      width={520}
      footer={
        <>
          <label className="mr-auto flex cursor-pointer items-center gap-2 text-[12.5px] whitespace-nowrap text-ink-2">
            <input
              type="checkbox"
              checked={markDone}
              onChange={(e) => {
                doneChosen.current = true;
                setMarkDone(e.target.checked);
              }}
              className="size-3.5 accent-[var(--accent)]"
            />
            {t("inbox-inboxdialogs.also_done")}
          </label>
          <Button tone="quiet" onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="accent" icon="send" busy={busy} onClick={() => void send()}>{t("inbox-inboxdialogs.send")}</Button>
        </>
      }
    >
      <p className="mb-1.5 text-[12px] font-medium text-ink-2">{t("inbox-inboxdialogs.ask_label")}</p>
      <textarea
        value={ask}
        onChange={(e) => setAsk(e.target.value)}
        onKeyDown={(e) => {
          if (e.nativeEvent.isComposing || e.keyCode === 229) return;
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            if (!busy) void send();
          }
        }}
        rows={3}
        placeholder={HANDOFF_PRESETS[0]}
        aria-label={t("inbox-inboxdialogs.ask_label")}
        className="block w-full resize-none rounded-control bg-field px-3 py-2 text-[13px] leading-relaxed text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)]"
      />
      <div className="mt-2 flex flex-wrap gap-1.5">
        {HANDOFF_PRESETS.map((p) => (
          <button key={p} type="button" onClick={() => setAsk(p)} className={cx("h-6.5 max-w-full truncate rounded-full px-2.5 text-[11.5px] whitespace-nowrap transition-colors", ask === p ? "bg-ink text-surface" : "bg-field text-ink-2 hover:bg-hover-2 hover:text-ink")}>
            {p}
          </button>
        ))}
      </div>

      <p className="mt-4 mb-1.5 text-[12px] font-medium text-ink-2">{t("inbox-inboxdialogs.target_label")}</p>
      <div role="radiogroup" aria-label={t("inbox-inboxdialogs.target_label")} className="max-h-[300px] divide-y divide-line overflow-y-auto rounded-card bg-surface shadow-card">
        <Target on={target === "main"} onPick={() => choose("main")} title={t("inbox-inboxdialogs.target_main")} sub={t("inbox-inboxdialogs.target_main_sub")} icon={<span className="flex size-6 items-center justify-center rounded-[7px] bg-accent-tint text-accent-ink"><Icon name="spark" size={12} /></span>} />
        <Target on={target === "new"} onPick={() => choose("new")} title={t("inbox-inboxdialogs.target_persona")} sub={t("inbox-inboxdialogs.target_persona_sub")} icon={<span className="flex size-6 items-center justify-center rounded-[7px] bg-field text-ink-2"><Icon name="compose" size={12} /></span>} />
        {agents.map((s) => (
          <Target
            key={`${s.machine_id}/${s.session_id}`}
            on={typeof target === "object" && target.machine_id === s.machine_id && target.session_id === s.session_id}
            onPick={() => choose(s)}
            title={sessionTitle(s)}
            sub={[machineWithHub(s.machine_id, machines), s.space_name].filter(Boolean).join(" › ")}
            icon={<span className="flex size-6 items-center justify-center"><StatusMark state={stateOf(s)} /></span>}
          />
        ))}
      </div>
      {error && <p className="mt-2 text-[12px] text-red">{error}</p>}
    </Dialog>
  );
}

function Target({ on, onPick, title, sub, icon }: { on: boolean; onPick: () => void; title: string; sub?: string; icon: React.ReactNode }) {
  return (
    <button type="button" role="radio" aria-checked={on} onClick={onPick} className={cx("flex w-full items-center gap-2.5 px-3 py-2 text-left transition-colors", on ? "bg-accent-tint" : "hover:bg-hover")}>
      {icon}
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13px] text-ink">{title}</span>
        {sub && <span className="block truncate text-[11.5px] text-ink-3">{sub}</span>}
      </span>
      <Icon name="check" size={13} stroke={2.6} className={cx("shrink-0 text-accent-ink", !on && "invisible")} />
    </button>
  );
}
