// "New email" (the app's ComposeView): from one of the hub's mail accounts, to / cc / bcc, subject and plain text; a reply comes
// with Re:, the thread ids and the start of the mail quoted (replyDraft). Sent by the hub (POST /mail/send).
// "Cancel" drops the mail; closing it any other way (Esc, a click outside, the page under it going away) keeps it for the
// next "New email" in this page.
import { useEffect, useRef, useState } from "react";
import { errorText } from "../../lib/api";
import type { MailAccount } from "../../lib/inbox";
import { badAddresses, parseAddresses, sendMail, type ComposeDraft } from "../../lib/mail";
import { defaultManager, useItemManager } from "../../lib/manager-choice";
import { t } from "../../i18n";
import { Button, Dialog, toast } from "../../ui/atoms";
import { composing } from "../../lib/send-history";

const kept = new Map<string, ComposeDraft>();
// A later opening owns this key's edits and Cancel, even if an older send has not answered yet.
const pendingRestores = new Map<string, object>();

/** The mail left open under `key` ("new", or a reply's), else `fresh`. */
function keptDraft(key: string, fresh: ComposeDraft): ComposeDraft {
  return kept.get(key) ?? fresh;
}

const fieldClass = "h-8 min-w-0 flex-1 rounded-control bg-field px-2.5 text-[12.5px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)]";

type ComposeDialogProps = { draftKey: string; initial: ComposeDraft; accounts: MailAccount[]; onClose: () => void };

export function ComposeDialog(props: ComposeDialogProps) {
  // Both retained drafts and live form state belong to the owner, including an explicitly stamped default manager.
  // Keep the item context unchanged for sends; resolve the default only for draft identity.
  const manager = useItemManager();
  const key = JSON.stringify([manager || defaultManager(), props.draftKey]);
  return <OwnedComposeDialog key={key} {...props} draftKey={key} manager={manager} />;
}

function OwnedComposeDialog({ draftKey, initial, accounts, onClose, manager }: ComposeDialogProps & { manager?: string }) {
  const [d, setD] = useState(() => keptDraft(draftKey, initial));
  const [sending, setSending] = useState(false);
  const sendLock = useRef(false);
  const [error, setError] = useState("");
  const body = useRef<HTMLTextAreaElement>(null);
  const to = useRef<HTMLInputElement>(null);
  const recipients = [d.to, d.cc, d.bcc];
  const hasRecipients = recipients.some((field) => parseAddresses(field).length > 0);
  const dirty = d.to !== initial.to || d.cc !== initial.cc || d.bcc !== initial.bcc || d.subject !== initial.subject || d.body !== initial.body || d.accountID !== initial.accountID;
  // Taken down without being closed (the item or the page left under it): kept as a close keeps it. "Cancel", a close
  // and a send mark it done (a send that fails puts it back); a send still out when it goes away keeps it on failure.
  const now = useRef({ d, dirty, done: false, mounted: true });
  Object.assign(now.current, { d, dirty });
  useEffect(() => {
    const at = now.current;
    pendingRestores.delete(draftKey);
    at.mounted = true;
    return () => {
      at.mounted = false;
      if (at.done) return;
      if (at.dirty) kept.set(draftKey, at.d);
      else kept.delete(draftKey);
    };
  }, [draftKey]);

  // A reply starts in its body, above the quote; a new mail at its recipients.
  useEffect(() => {
    if (initial.to && body.current) {
      body.current.focus();
      body.current.setSelectionRange(0, 0);
    } else to.current?.focus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const set = (patch: Partial<ComposeDraft>) => {
    setD((x) => ({ ...x, ...patch }));
    setError("");
  };
  const keepOrDrop = () => {
    now.current.done = true;
    if (dirty) kept.set(draftKey, d);
    else kept.delete(draftKey);
    onClose();
  };
  const cancel = () => {
    now.current.done = true;
    kept.delete(draftKey);
    onClose();
  };
  const send = async () => {
    if (sendLock.current) return;
    const bad = recipients.flatMap(badAddresses);
    if (!hasRecipients) return setError(t("inbox-composedialog.need_recipient"));
    if (bad.length) return setError(t("inbox-composedialog.bad_addresses", { addresses: bad.join(t("inbox-composedialog.list_sep")) }));
    // All entry points share this lock before the busy button rerenders; only failure allows a retry.
    sendLock.current = true;
    setSending(true);
    // On its way: not kept (a box opened for this key meanwhile starts fresh, nothing to send twice).
    const at = now.current;
    const attempt = {};
    pendingRestores.set(draftKey, attempt);
    at.done = true;
    kept.delete(draftKey);
    try {
      await sendMail(d, manager);
      toast(t("inbox-composedialog.sent"), "ok");
      onClose();
    } catch (e) {
      sendLock.current = false;
      // still open: the mail stays in it to retry; gone: kept for the next "Reply"
      if (at.mounted) at.done = false;
      else if (pendingRestores.get(draftKey) === attempt) kept.set(draftKey, d);
      setError(t("inbox-composedialog.send_failed", { error: errorText(e) }));
    } finally {
      if (pendingRestores.get(draftKey) === attempt) pendingRestores.delete(draftKey);
      setSending(false);
    }
  };

  return (
    <Dialog
      open
      onClose={keepOrDrop}
      title={initial.inReplyTo || /^re:/i.test(initial.subject) ? t("inbox-composedialog.title_reply") : t("inbox-composedialog.title_new")}
      width={620}
      footer={
        <>
          <Button tone="quiet" onClick={cancel}>{t("common.cancel")}</Button>
          <Button tone="accent" icon="send" busy={sending} disabled={!hasRecipients || !d.accountID} onClick={() => void send()}>{t("inbox-composedialog.send")}</Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          void send();
        }}
        onKeyDown={(e) => {
          // ⌘↩ / Ctrl+↩ sends, as in a mail app.
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !composing(e.nativeEvent)) {
            e.preventDefault();
            void send();
          }
        }}
      >
        <Field label={t("inbox-composedialog.from")}>
          <select value={d.accountID} onChange={(e) => set({ accountID: e.target.value })} className={fieldClass} aria-label={t("inbox-composedialog.from")}>
            {!accounts.some((a) => a.id === d.accountID) && <option value={d.accountID}>{d.accountID || t("inbox-composedialog.no_account")}</option>}
            {accounts.map((a) => <option key={a.id} value={a.id}>{a.name && a.name !== a.email ? `${a.name} · ${a.email}` : a.email}</option>)}
          </select>
        </Field>
        <Field label={t("inbox-composedialog.to")}>
          <input ref={to} value={d.to} onChange={(e) => set({ to: e.target.value })} placeholder={t("inbox-composedialog.to_placeholder")} aria-label={t("inbox-composedialog.to")} type="text" inputMode="email" autoComplete="off" spellCheck={false} className={fieldClass} />
        </Field>
        <Field label={t("inbox-composedialog.cc")}>
          <input value={d.cc} onChange={(e) => set({ cc: e.target.value })} aria-label={t("inbox-composedialog.cc")} type="text" inputMode="email" autoComplete="off" spellCheck={false} className={fieldClass} />
        </Field>
        <Field label={t("inbox-composedialog.bcc")}>
          <input value={d.bcc} onChange={(e) => set({ bcc: e.target.value })} aria-label={t("inbox-composedialog.bcc")} type="text" inputMode="email" autoComplete="off" spellCheck={false} className={fieldClass} />
        </Field>
        <Field label={t("inbox-composedialog.subject")}>
          <input value={d.subject} onChange={(e) => set({ subject: e.target.value })} aria-label={t("inbox-composedialog.subject")} className={fieldClass} />
        </Field>
        <textarea
          ref={body}
          value={d.body}
          onChange={(e) => set({ body: e.target.value })}
          rows={12}
          aria-label={t("inbox-composedialog.body")}
          placeholder={t("inbox-composedialog.body")}
          className="mt-1 block w-full resize-y rounded-control bg-field px-3 py-2.5 text-[13.5px] leading-relaxed text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)]"
        />
        {error && <p role="alert" className="text-[12px] text-red">{error}</p>}
      </form>
    </Dialog>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex items-center gap-3">
      <span className="w-12 shrink-0 text-[12px] whitespace-nowrap text-ink-3">{label}</span>
      {children}
    </label>
  );
}
