// The bodies of an opened item besides a Feishu chat: a mail (its HTML in a sandboxed frame), a Feishu document's
// rows about you (status menus write into the document), a Moments post with its comments.
import { useEffect, useRef, useState } from "react";
import { api, enc, errorText } from "../../lib/api";
import { bytes, clock } from "../../lib/format";
import { docTypeName, isDone, itemKey, momentText, withToken, type InboxItem, type MomentComment, type MomentPost } from "../../lib/inbox";
import { defaultManager, itemHeaders, useItemManager, withManager } from "../../lib/manager-choice";
import { openLink } from "../../lib/links";
import { addressText, markMailSeen, replyAllDraft, replyDraft, useMailAccounts, type ComposeDraft, type MailMessage } from "../../lib/mail";
import { hubTranslation, isForeignCached, languageName, strippedHTML, translateMail, translatesMail, useTranslatePrefs, type MailTranslation } from "../../lib/translate";
import { t } from "../../i18n";
import { Button, cx, Loading, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ComposeDialog } from "./ComposeDialog";
import { Glyph, Monogram, StatusMenu, Tag } from "./InboxParts";
import { openMedia } from "./LarkThread";

// ── mail ─────────────────────────────────────────────────

/** A mail: who, the body (its HTML in a sandboxed frame), attachments. Opening it marks it read in the mailbox; with
 * Settings › Translation on for mail, a foreign one is shown translated ("Original" one click away). Replies are written by hand. */
export function MailBody({ item }: { item: InboxItem }) {
  const m = item.mail!;
  const uid = m.uids[m.uids.length - 1];
  const mailbox = m.mailbox || "INBOX";
  const [msg, setMsg] = useState<MailMessage | null>(null);
  const [error, setError] = useState("");
  // "Reply" open: its template as it opened, so the dialog stays over a new mail of the thread read under it (#4563)
  const [replying, setReplying] = useState<{ item: string; mode: "reply" | "reply-all"; initial: ComposeDraft } | null>(null);
  const accounts = useMailAccounts();
  const prefs = useTranslatePrefs();
  const [translation, setTranslation] = useState<MailTranslation | null>(null);
  const [translating, setTranslating] = useState(false);
  const [translateError, setTranslateError] = useState("");
  const [showOriginal, setShowOriginal] = useState(false);
  const path = `/mail/messages/${enc(m.account_id)}/${uid}`;
  const unread = !isDone(item) && item.read !== true;
  const manager = useItemManager();

  useEffect(() => {
    let live = true;
    setMsg(null);
    setError("");
    api<{ message: MailMessage }>(`${path}?mailbox=${enc(mailbox)}`, { headers: itemHeaders(manager) })
      .then((r) => {
        if (!live) return;
        setMsg(r.message);
        if (unread && r.message?.seen !== true) void markMailSeen(m.account_id, uid, mailbox, manager);
      })
      .catch((e) => live && setError(errorText(e)));
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, mailbox, manager]);

  // Settings › Translation: a mail in another language than the target is asked of the hub once it is here.
  const wants = translatesMail(prefs);
  useEffect(() => {
    setTranslation(null);
    setTranslateError("");
    setShowOriginal(false);
    if (!msg || !wants) return;
    const body = msg.text?.trim() ? msg.text : strippedHTML(msg.html ?? "");
    if (!isForeignCached(`${msg.subject ?? ""}\n${body}`, prefs.target)) return;
    let live = true;
    setTranslating(true);
    translateMail(m.account_id, uid, mailbox, prefs.target, manager)
      .then((tr) => live && setTranslation(tr))
      .catch((e) => live && setTranslateError(errorText(e)))
      .finally(() => live && setTranslating(false));
    return () => {
      live = false;
      setTranslating(false);
    };
  }, [msg, wants, prefs.target, m.account_id, uid, mailbox, manager]);

  // kept by the item (the thread): a new mail of it moves `uid` on and reads the body again under the dialog
  // another item opened in this body: that reply's dialog goes (and keeps what was typed for its own item)
  const replyItem = itemKey({ id: item.id, manager: item.manager || defaultManager() });
  const replyKey = replying && `${replying.mode}:${replying.item}`;
  const dialog = replying?.item === replyItem && <ComposeDialog key={replyKey} draftKey={replyKey!} initial={replying.initial} accounts={accounts} onClose={() => setReplying((current) => current === replying ? null : current)} />;
  if (!msg) return <div className="p-5">{error ? <p className="text-[12.5px] text-red">{error}</p> : <Loading label={t("inbox-inboxbodies.reading_mail")} />}{dialog}</div>;
  const shown = showOriginal ? null : translation;
  const lang = languageName(prefs.target);
  const allDraft = replyAllDraft(m.account_id, msg, accounts.find((a) => a.id === m.account_id)?.email);
  return (
    <div className="mx-auto max-w-[760px] px-4 py-4 sm:px-6">
      <div className="flex flex-col items-start gap-3 sm:flex-row">
        <dl className="grid min-w-0 flex-1 grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[12.5px]">
          <dt className="text-ink-3">{t("inbox-inboxbodies.from")}</dt>
          <dd className="min-w-0 truncate text-ink">{addressText(msg.from)}</dd>
          {!!msg.to?.length && (<><dt className="text-ink-3">{t("inbox-inboxbodies.to")}</dt><dd className="min-w-0 truncate text-ink-2">{msg.to.map(addressText).join(t("inbox-inboxbodies.address_sep"))}</dd></>)}
          {!!msg.cc?.length && (<><dt className="text-ink-3">{t("inbox-inboxbodies.cc")}</dt><dd className="min-w-0 truncate text-ink-2">{msg.cc.map(addressText).join(t("inbox-inboxbodies.address_sep"))}</dd></>)}
          {msg.date && (<><dt className="text-ink-3">{t("inbox-inboxbodies.date")}</dt><dd className="text-ink-2">{clock(msg.date)}</dd></>)}
        </dl>
        <div className="flex shrink-0 gap-2">
          <Button tone="secondary" onClick={() => setReplying({ item: replyItem, mode: "reply", initial: replyDraft(m.account_id, msg) })} title={t("inbox-inboxbodies.reply_title")}>
            <Glyph name="reply" size={14} stroke={2.1} />
            {t("inbox-inboxbodies.reply")}
          </Button>
          <Button tone="secondary" disabled={!allDraft} onClick={() => allDraft && setReplying({ item: replyItem, mode: "reply-all", initial: allDraft })} title={t(allDraft ? "inbox-inboxbodies.reply_all_title" : "inbox-inboxbodies.reply_all_unavailable")}>
            {t("inbox-inboxbodies.reply_all")}
          </Button>
        </div>
      </div>
      {(translating || translation || translateError) && (
        <div className="mt-3 flex min-h-8 items-center gap-2 rounded-control bg-inset px-3 py-1.5 text-[12px] text-ink-3 shadow-hairline">
          {translating ? (
            <>
              <Spinner />
              <span className="truncate">{t("inbox-inboxbodies.translating", { language: lang })}</span>
            </>
          ) : translation ? (
            <>
              <Icon name="translate" size={13} className="shrink-0" />
              <span className="min-w-0 flex-1 truncate">{showOriginal ? t("inbox-inboxbodies.original") : t("inbox-inboxbodies.translated_to", { language: lang })}</span>
              <button type="button" onClick={() => setShowOriginal((v) => !v)} className="shrink-0 rounded-[6px] px-1.5 py-0.5 font-medium whitespace-nowrap text-accent-ink hover:bg-hover">
                {showOriginal ? t("inbox-inboxbodies.show_translation") : t("inbox-inboxbodies.show_original")}
              </button>
            </>
          ) : (
            <>
              <Icon name="info" size={13} className="shrink-0" />
              <span className="min-w-0 flex-1 truncate" title={translateError}>{t("inbox-inboxbodies.translate_failed", { error: translateError })}</span>
            </>
          )}
        </div>
      )}
      <div className="mt-4">
        {shown ? (
          <div className="rounded-card bg-surface px-[18px] py-4 shadow-card">
            {shown.subject && shown.subject !== (hubTranslation(item, prefs)?.subject || item.title) && <p className="mb-2 text-[14px] font-semibold text-ink">{shown.subject}</p>}
            <div className="text-[13.5px] leading-relaxed break-words whitespace-pre-wrap text-ink">{shown.text}</div>
            {shown.clipped && <p className="mt-3 text-[12px] text-ink-3">{t("inbox-inboxbodies.clipped")}</p>}
          </div>
        ) : msg.html ? <MailFrame html={msg.html} /> : <div className="text-[13.5px] leading-relaxed break-words whitespace-pre-wrap text-ink">{msg.text || t("inbox-inboxbodies.no_body")}</div>}
      </div>
      {!!msg.attachments?.length && (
        <div className="mt-5">
          <p className="mb-1.5 text-[12px] font-medium text-ink-3">{t("inbox-inboxbodies.attachments", { count: msg.attachments.length })}</p>
          <div className="flex flex-wrap gap-2">
            {msg.attachments.map((a, i) => {
              const url = a.id ? withToken(withManager(`/api/v1${path}/attachments/${enc(a.id)}?mailbox=${enc(mailbox)}`, manager)) : undefined;
              const body = (
                <>
                  <Icon name="clip" size={13} className="shrink-0 text-ink-3" />
                  <span className="truncate">{a.filename || t("inbox-inboxbodies.attachment")}</span>
                  {a.size !== undefined && <span className="shrink-0 text-ink-3">{bytes(a.size)}</span>}
                </>
              );
              const cls = "flex max-w-full items-center gap-2 rounded-[8px] bg-surface px-2.5 py-1.5 text-[12.5px] text-ink shadow-card";
              return url ? <a key={i} href={url} download={a.filename} className={cx(cls, "hover:bg-hover")}>{body}</a> : <span key={i} className={cls}>{body}</span>;
            })}
          </div>
        </div>
      )}
      {dialog}
    </div>
  );
}

// Mail HTML is somebody else's page: no scripts, no forms (sandbox without allow-scripts / allow-forms). Same origin
// only so the page can size the frame to its content and open its links like every other link (openLink).
const FRAME_HEAD =
  '<!doctype html><html><head><meta charset="utf-8"><base target="_blank"><style>' +
  ":root{color-scheme:light}html{background:Canvas;color:CanvasText}" +
  'body{margin:0;padding:16px 18px;font:14px/1.6 -apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}' +
  "img{max-width:100%;height:auto}table{max-width:100%}pre{white-space:pre-wrap}</style></head><body>";

function MailFrame({ html }: { html: string }) {
  const ref = useRef<HTMLIFrameElement>(null);
  const [height, setHeight] = useState(240);
  // undoes what the last load hooked onto the frame's document. The observer lives in this window: left connected, it
  // keeps that document and this frame's state alive, one more on each load and on each frame that goes (a new mail of
  // the thread, Show translation, another item).
  const release = useRef<(() => void) | null>(null);
  useEffect(() => () => release.current?.(), []);

  const onLoad = () => {
    release.current?.();
    release.current = null;
    const doc = ref.current?.contentDocument;
    if (!doc) return;
    const fit = () => setHeight(Math.max(80, Math.ceil(doc.body.getBoundingClientRect().height)));
    fit();
    const sizes = new ResizeObserver(fit);
    sizes.observe(doc.body);
    const click = (e: MouseEvent) => {
      const a = (e.target as Element | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
      if (!a) return;
      e.preventDefault();
      if (/^(https?:|mailto:)/i.test(a.href)) openLink(a.href);
    };
    doc.addEventListener("click", click);
    release.current = () => {
      sizes.disconnect();
      doc.removeEventListener("click", click);
    };
  };

  return (
    <iframe
      ref={ref}
      title={t("inbox-inboxbodies.mail_frame")}
      sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
      srcDoc={FRAME_HEAD + html + "</body></html>"}
      onLoad={onLoad}
      className="block w-full overflow-hidden rounded-card shadow-card"
      style={{ height, colorScheme: "light" }}
    />
  );
}

// ── Feishu document ──────────────────────────────────────

export function DocBody({ item }: { item: InboxItem }) {
  const doc = item.doc!;
  const rows = doc.rows ?? [];
  const tables = new Set(rows.map((r) => r.table).filter(Boolean));
  const kind = docTypeName(doc.type);
  return (
    <div className="mx-auto max-w-[760px] px-4 py-4 sm:px-6">
      <div className="flex flex-wrap items-center gap-2">
        <Button tone="secondary" icon="external" onClick={() => openLink(doc.url, item.title)}>{t("inbox-inboxbodies.open_doc", { kind })}</Button>
        {doc.reply && (
          <span className="flex items-center gap-1.5 text-[12px] text-ink-3">
            <Glyph name="reply" size={12} />
            {t("inbox-inboxbodies.reply_goes", { how: doc.reply.label })}
          </span>
        )}
      </div>
      {doc.error && <p className="mt-3 text-[12.5px] text-red">{t("inbox-inboxbodies.doc_error", { error: doc.error })}</p>}
      {rows.length > 0 && (
        <section className="mt-5">
          <h3 className="mb-1.5 px-1 text-[12px] font-medium text-ink-3">{t("inbox-inboxbodies.rows_mentioning_you")}</h3>
          <div className="flex flex-col gap-2">
            {rows.map((row) => (
              <div key={row.ref} className="rounded-card bg-surface px-4 py-3 shadow-card">
                <div className="flex items-start gap-2">
                  <p className="min-w-0 flex-1 text-[13.5px] font-semibold text-ink">{row.title}</p>
                  {row.table && (tables.size > 1 || doc.type === "base") && <Tag>{row.table}</Tag>}
                </div>
                {!!row.fields?.length && (
                  <dl className="mt-1.5 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-[12.5px]">
                    {row.fields.map((f) => (
                      <div key={f.name} className="contents">
                        <dt className="text-ink-3">{f.name}</dt>
                        <dd className="min-w-0 break-words text-ink">{f.value}</dd>
                      </div>
                    ))}
                  </dl>
                )}
                {!!row.status?.length && (
                  <div className="mt-2.5 flex flex-wrap gap-1.5">
                    {row.status.map((st) => <StatusMenu key={st.field} it={item} row={row} status={st} showName />)}
                  </div>
                )}
              </div>
            ))}
          </div>
          <p className="mt-1.5 px-1 text-[11.5px] text-ink-3">{isDone(item) ? t("inbox-inboxbodies.done_no_status") : t("inbox-inboxbodies.status_writes", { kind })}</p>
        </section>
      )}
      {!rows.length && !doc.error && <p className="mt-5 text-[12.5px] text-ink-3">{item.summary || item.preview}</p>}
    </div>
  );
}

// ── Moments ─────────────────────────────────────────────

export function MomentBody({ item }: { item: InboxItem }) {
  const id = item.moment!.post_id;
  const replyTo = item.moment!.reply_to;
  const [post, setPost] = useState<MomentPost | null>(null);
  const [comments, setComments] = useState<MomentComment[]>([]);
  const [error, setError] = useState("");
  const manager = useItemManager();

  useEffect(() => {
    let live = true;
    setPost(null);
    setError("");
    const base = `/lark/moments/posts/${enc(id)}`;
    const headers = itemHeaders(manager);
    api<MomentPost>(base, { headers }).then((p) => live && setPost(p)).catch((e) => live && setError(errorText(e)));
    api<{ comments?: MomentComment[] }>(`${base}/comments`, { headers }).then((r) => live && setComments(r.comments ?? [])).catch(() => {});
    return () => {
      live = false;
    };
  }, [id, manager]);

  if (!post) return <div className="p-5">{error ? <p className="text-[12.5px] text-red">{error}</p> : <Loading label={t("inbox-inboxbodies.reading_post")} />}</div>;
  const images = (post.images ?? []).map((i) => withToken(i.url && withManager(i.url, manager))).filter(Boolean) as string[];
  return (
    <div className="mx-auto max-w-[760px] px-4 py-4 sm:px-6">
      <article className="rounded-card bg-surface p-4 shadow-card">
        <Author name={post.userName ?? ""} avatar={post.userAvatarUrl} time={post.createTime} />
        <p className="mt-3 text-[14px] leading-relaxed break-words whitespace-pre-wrap text-ink">{momentText(post.content)}</p>
        {images.length > 0 && (
          <div className={cx("mt-3 grid gap-1", images.length === 1 ? "max-w-[300px] grid-cols-1" : "max-w-[420px] grid-cols-3")}>
            {images.map((src, i) => (
              <button key={i} type="button" onClick={() => openMedia(src)} className={cx("overflow-hidden rounded-[8px] shadow-hairline", images.length > 1 && "aspect-square")}>
                <img src={src} alt="" loading="lazy" className="size-full object-cover" />
              </button>
            ))}
          </div>
        )}
        <p className="mt-3 flex flex-wrap gap-x-3 text-[11.5px] text-ink-3">
          {!!post.categoryNames?.length && <span>{post.categoryNames.join(" · ")}</span>}
          {(post.reactionCount ?? 0) > 0 && <span>{t("inbox-inboxbodies.reactions", { count: post.reactionCount ?? 0 })}</span>}
          <span>{t("inbox-inboxbodies.comments_public")}</span>
        </p>
      </article>
      <h3 className="mt-5 mb-1.5 px-1 text-[12px] font-medium text-ink-3">{t("inbox-inboxbodies.comments", { count: post.commentCount ?? comments.length })}</h3>
      <div className="flex flex-col gap-1">
        {comments.map((c) => (
          <div key={c.id} className={cx("rounded-[10px] px-3 py-2.5", c.id === replyTo ? "bg-accent-tint" : "bg-surface shadow-card")}>
            <Author name={c.userName ?? ""} avatar={c.userAvatarUrl} time={c.createTime} small extra={c.id === replyTo ? t("inbox-inboxbodies.send_replies_here") : undefined} />
            <p className="mt-1 pl-[38px] text-[13px] leading-relaxed break-words whitespace-pre-wrap text-ink">
              {c.replyUserName && <span className="text-ink-3">{t("inbox-inboxbodies.reply_to_user", { name: c.replyUserName })}</span>}
              {momentText(c.content)}
            </p>
          </div>
        ))}
      </div>
    </div>
  );
}

function Author({ name, avatar, time, small, extra }: { name: string; avatar?: string; time?: number; small?: boolean; extra?: string }) {
  return (
    <div className="flex items-center gap-2.5">
      <Monogram name={name || "?"} size={small ? 28 : 36} src={avatar} />
      <div className="min-w-0 flex-1">
        <p className={cx("truncate font-medium text-ink", small ? "text-[12.5px]" : "text-[13.5px]")}>{name}</p>
        {time ? <p className="text-[11px] text-ink-3">{clock(new Date(time).toISOString())}</p> : null}
      </div>
      {extra && <span className="shrink-0 text-[11px] font-medium text-accent-ink">{extra}</span>}
    </div>
  );
}
