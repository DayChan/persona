// A connector's inbox item opened (iOS InboxConnectorViews / ConnectorThread): the thread as the hub reads it
// (GET /api/v1/connectors/{id}/thread) — a chat app's conversation as bubbles (Telegram), a GitHub pull request or
// issue as GitHub's own timeline — with a box to answer in when the thread names its reply action (POST …/thread/reply).
// A thread that cannot be read (a CI run, a rate limit, a connector without threads) shows what the hub collected
// about the item instead, with "Open in X". Read-only otherwise: no pages for browsing the source (CLAUDE.md).
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { api, ApiError, errorText } from "../../lib/api";
import {
  chatHeadline, checkLabel, githubStateLabel, isChatThread, isGitHubThread, labelColor, loadThread, mediaLine, mediaURL, mergeNewest, mergeOlder,
  reachMessage, replyInThread, reviewLabel, startsGroup, type ThreadChat, type ThreadMedia, type ThreadMessage, type ThreadPage,
} from "../../lib/connectors";
import { connectorReference, itemPath, itemRequest, shortTime, tightCJK, withToken, type InboxItem } from "../../lib/inbox";
import { defaultManager, useItemManager, withManager } from "../../lib/manager-choice";
import { openLink } from "../../lib/links";
import { t } from "../../i18n";
import { Button, cx, Loading, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { ImageViewer } from "../chat/ImageViewer";
import { scrollBehavior } from "../../ui/stream/motion";
import { Monogram } from "./InboxParts";

/** A link the system handles (sms:, tg://) goes to it; a web page opens in a tab. */
export function openSystem(url: string) {
  if (/^https?:/i.test(url)) window.open(url, "_blank", "noopener,noreferrer");
  else location.href = url;
}

const clock = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
};

export function ConnectorBody({ item }: { item: InboxItem }) {
  // The whole page, including in-flight reads and history, belongs to this manager's item and thread.
  const shown = useItemManager();
  const manager = item.manager ?? shown;
  const ownerKey = JSON.stringify([manager || defaultManager(), item.id, item.source, item.connector?.thread ?? ""]);
  return <ConnectorThreadBody key={ownerKey} item={item} manager={manager} />;
}

function ConnectorThreadBody({ item, manager }: { item: InboxItem; manager?: string }) {
  const id = item.source;
  const thread = item.connector?.thread ?? "";
  const [page, setPage] = useState<ThreadPage | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [older, setOlder] = useState(false);
  // Apply responses synchronously so a history continuation sees a replaced window even in one React batch.
  const currentPage = useRef<ThreadPage | null>(null);
  const owner = useRef<object | null>(null);
  const newestRead = useRef(0);
  const historyPending = useRef<object | null>(null);
  useLayoutEffect(() => {
    owner.current = {};
    return () => {
      owner.current = null;
      historyPending.current = null;
    };
  }, []);

  const load = useCallback(async () => {
    const scope = owner.current;
    if (!scope) return;
    const request = ++newestRead.current;
    const current = () => owner.current === scope && newestRead.current === request;
    try {
      const p = await loadThread(id, thread, undefined, manager);
      if (!current()) return;
      const next = mergeNewest(currentPage.current, p);
      // mergeNewest returns the incoming page when it discards the previous window: its history cursor retires too.
      if (next === p) {
        historyPending.current = null;
        setOlder(false);
      }
      currentPage.current = next;
      setPage(next);
      setFailure(null);
    } catch (e) {
      if (!current()) return;
      // Said over the text only when it is worth saying (a rate limit, the connector unavailable); a thread that
      // simply has no page of its own (a CI run) falls back quietly. "" is failed, null still loading.
      setFailure(e instanceof ApiError && (e.status === 429 || e.status === 409) ? errorText(e) : "");
    }
  }, [id, thread, manager]);
  // Again when the item moves (a new message came in).
  useEffect(() => void load(), [load, item.updated_at]);

  const loadOlder = async () => {
    const scope = owner.current;
    const start = currentPage.current;
    if (!scope || !start?.before || historyPending.current) return;
    const pending = {};
    historyPending.current = pending;
    const current = () => owner.current === scope && historyPending.current === pending;
    setOlder(true);
    try {
      const p = await loadThread(id, thread, start.before, manager);
      const have = currentPage.current;
      if (!current() || !have) return;
      const next = mergeOlder(have, p);
      currentPage.current = next;
      setPage(next);
    } catch (e) {
      if (current()) toast(t("inbox-connectorthread.older_failed", { error: errorText(e) }), "error");
    } finally {
      if (current()) {
        historyPending.current = null;
        setOlder(false);
      }
    }
  };

  // A quote's message: the pages before it read until it is on screen (a few at most, as iOS); whether it is.
  const reach = async (msg: string) => {
    const scope = owner.current;
    const start = currentPage.current;
    if (!scope || !start) return;
    if (start.messages.some((m) => m.id === msg)) return true;
    if (historyPending.current) return;
    const pending = {};
    historyPending.current = pending;
    const current = () => owner.current === scope && historyPending.current === pending;
    setOlder(true);
    try {
      const r = await reachMessage(start, msg, async (before) => {
        if (!current()) throw new Error("Thread history retired");
        const p = await loadThread(id, thread, before, manager);
        const have = currentPage.current;
        if (!current() || !have) throw new Error("Thread history retired");
        // Merge only the fetched history, not the jump's older snapshot of the newest messages.
        const next = mergeOlder(have, p);
        currentPage.current = next;
        setPage(next);
        return p;
      });
      if (!current()) return;
      return r.found;
    } catch (e) {
      if (current()) toast(t("inbox-connectorthread.older_failed", { error: errorText(e) }), "error");
    } finally {
      if (current()) {
        historyPending.current = null;
        setOlder(false);
      }
    }
  };

  const send = async (text: string, replyTo?: ThreadMessage) => {
    const scope = owner.current;
    if (!scope || !page?.chat.reply) return false;
    try {
      await replyInThread(id, { thread, action: page.chat.reply, text, reply_to: replyTo?.id }, manager);
    } catch (e) {
      if (owner.current === scope) toast(t("inbox-connectorthread.send_failed", { error: errorText(e) }), "error");
      return false;
    }
    // Provider delivery succeeded: a failed refresh must not offer the same message for another send.
    if (owner.current === scope) await load();
    return true;
  };

  if (!page && failure === null) return <div className="p-5"><Loading label={t("inbox-connectorthread.loading", { name: item.account_name || id })} /></div>;
  if (!page || (!isChatThread(page.chat) && !isGitHubThread(page.chat))) return <ConnectorText item={item} problem={page ? undefined : failure || undefined} />;
  // Drafts, quotes and pending-send state belong to this manager's item and thread.
  const composerKey = JSON.stringify([manager || defaultManager(), item.id, id, thread]);
  return isGitHubThread(page.chat)
    ? <GitHubThread key={composerKey} id={id} item={item} page={page} older={older} onOlder={() => void loadOlder()} onSend={send} />
    : <ChatThread key={composerKey} id={id} page={page} older={older} onOlder={() => void loadOlder()} onReach={reach} onSend={send} />;
}

// ── what the hub collected (no thread page) ──────────────

function ConnectorText({ item, problem }: { item: InboxItem; problem?: string }) {
  const [context, setContext] = useState<string | null>(null);
  const [failure, setFailure] = useState("");
  const c = item.connector;
  useEffect(() => {
    let live = true;
    api<InboxItem>(itemPath(item.id), { headers: itemRequest(item) })
      .then((r) => live && (setContext(r.context ?? ""), setFailure("")))
      .catch((e) => live && setFailure(errorText(e)));
    return () => {
      live = false;
    };
  }, [item.id, item.manager]);
  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex max-w-[760px] flex-col gap-3 px-4 py-5 sm:px-6">
        {(c?.url || (c?.reply_to && c.can_reply)) && (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
            {c?.url && (
              <Button tone="secondary" icon="external" onClick={() => openLink(c.url!, item.title)}>
                {tightCJK(t("inbox-connectorthread.open_in", { name: item.account_name || item.source }))}
              </Button>
            )}
            {c?.reply_to && c.can_reply && <p className="min-w-0 text-[12px] text-ink-3">{t("inbox-connectorthread.reply_goes", { how: c.reply_to })}</p>}
          </div>
        )}
        {problem && (
          <p className="flex items-start gap-2 rounded-[10px] bg-inset px-3 py-2 text-[12px] text-ink-2 shadow-hairline">
            <Icon name="info" size={13} className="mt-0.5 shrink-0 text-ink-3" />
            <span className="min-w-0 break-words">{t("inbox-connectorthread.thread_failed", { error: problem })}</span>
          </p>
        )}
        <p className="text-[12px] font-medium text-ink-3">{t("inbox-connectorthread.content")}</p>
        {context === null && !failure ? <Loading /> : failure ? (
          <p className="text-[12.5px] text-red">{t("inbox-connectorthread.content_failed", { error: failure })}</p>
        ) : (
          <div className="text-[13px] leading-relaxed break-words whitespace-pre-wrap text-ink select-text">{context || item.preview}</div>
        )}
      </div>
    </div>
  );
}

// ── the answer box ───────────────────────────────────────

function ThreadComposer({ placeholder, replyTo, onClearReply, onSend, note }: {
  placeholder: string;
  replyTo?: ThreadMessage | null;
  onClearReply?: () => void;
  onSend: (text: string, replyTo?: ThreadMessage) => Promise<boolean>;
  note?: string;
}) {
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const sendPending = useRef(false);
  const draftRevision = useRef(0);
  const ref = useRef<HTMLTextAreaElement>(null);
  useLayoutEffect(() => {
    draftRevision.current++;
  }, [replyTo]);
  useEffect(() => {
    if (replyTo) ref.current?.focus();
  }, [replyTo]);
  const submit = async () => {
    const body = text.trim();
    if (!body || sendPending.current) return;
    sendPending.current = true;
    const submittedRevision = draftRevision.current;
    setSending(true);
    try {
      const ok = await onSend(body, replyTo ?? undefined);
      // Typing or changing a quote starts a new draft, even if its words later match the sent message.
      if (ok && draftRevision.current === submittedRevision) {
        setText("");
        onClearReply?.();
        if (ref.current) ref.current.style.height = "auto";
      }
    } finally {
      sendPending.current = false;
      setSending(false);
    }
  };
  return (
    <div className="shrink-0 border-t border-line bg-surface px-3 py-2 sm:px-4">
      <div className="mx-auto max-w-[760px]">
        {replyTo && (
          <div className="mb-2 flex items-center gap-1.5 rounded-[8px] bg-inset px-3 py-1.5 text-[12px] text-ink-2">
            <Icon name="quote" size={12} className="shrink-0 text-accent-ink" />
            <span className="shrink-0 font-medium text-ink">{t("inbox-connectorthread.quoted_sender", { name: replyTo.mine ? t("inbox-connectorthread.me") : replyTo.sender.name })}</span>
            <span className="min-w-0 flex-1 truncate text-ink-3">{replyTo.text || (replyTo.media?.length ? mediaLine(replyTo.media[0]) : "")}</span>
            <button type="button" onClick={onClearReply} title={t("inbox-connectorthread.cancel_quote")} aria-label={t("inbox-connectorthread.cancel_quote")} className="shrink-0 rounded p-0.5 text-ink-3 hover:bg-hover hover:text-ink">
              <Icon name="x" size={12} />
            </button>
          </div>
        )}
        <div className="flex items-end gap-2">
          <textarea
            ref={ref}
            value={text}
            rows={1}
            aria-label={placeholder}
            placeholder={placeholder}
            onChange={(e) => {
              draftRevision.current++;
              setText(e.target.value);
              e.target.style.height = "auto";
              e.target.style.height = `${Math.min(e.target.scrollHeight, 140)}px`;
            }}
            onKeyDown={(e) => {
              if (e.nativeEvent.isComposing || e.keyCode === 229) return;
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                void submit();
              }
            }}
            className="max-h-[140px] min-h-[36px] flex-1 resize-none rounded-[10px] bg-field px-3 py-1.5 text-[13px] leading-relaxed text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)]"
          />
          <Button tone="accent" icon="send" busy={sending} disabled={!text.trim()} onClick={() => void submit()} className="h-9">
            {t("inbox-connectorthread.send")}
          </Button>
        </div>
        {note && <p className="mt-1 truncate text-[11px] text-ink-3">{note}</p>}
      </div>
    </div>
  );
}

function OlderButton({ busy, onClick }: { busy: boolean; onClick: () => void }) {
  return (
    <div className="flex justify-center pb-2">
      <Button tone="quiet" busy={busy} onClick={onClick}>{t("inbox-connectorthread.older")}</Button>
    </div>
  );
}

// ── a chat app's conversation (Telegram) ─────────────────

function ChatThread({ id, page, older, onOlder, onReach, onSend }: {
  id: string;
  page: ThreadPage;
  older: boolean;
  onOlder: () => void;
  /** Whether the message was reached; undefined when busy, retired or the read already reported its failure. */
  onReach: (id: string) => Promise<boolean | undefined>;
  onSend: (text: string, replyTo?: ThreadMessage) => Promise<boolean>;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const [replyTo, setReplyTo] = useState<ThreadMessage | null>(null);
  // The message a quote led back to, lit for a moment (n tells a second jump to the same one apart).
  const [flash, setFlash] = useState<{ id: string; n: number } | null>(null);
  const last = page.messages[page.messages.length - 1]?.id;
  const group = page.chat.kind !== "dm";
  const headline = chatHeadline(page.chat);
  // At the newest message on open and when one comes in (an older page leaves the scroll where it was).
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [last]);
  // To the quoted message once it is drawn: centred, without the smooth scroll when the user asks for less motion.
  useEffect(() => {
    if (!flash) return;
    const row = [...(scroller.current?.querySelectorAll<HTMLElement>("[data-mid]") ?? [])].find((el) => el.dataset.mid === flash.id);
    row?.scrollIntoView?.({ block: "center", behavior: scrollBehavior() });
    const off = setTimeout(() => setFlash(null), 1600);
    return () => clearTimeout(off);
  }, [flash]);
  const jump = async (to: string) => {
    const found = await onReach(to);
    if (found) setFlash({ id: to, n: Date.now() });
    else if (found === false) toast(t("inbox-connectorthread.quote_not_found"));
  };
  return (
    <div className="flex h-full min-h-0 flex-col">
      {headline && <p className="shrink-0 truncate border-b border-line px-4 py-1.5 text-center text-[12px] text-ink-3 tabular-nums">{headline}</p>}
      <div ref={scroller} className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-[760px] px-3 py-3 sm:px-5">
          {page.before && <OlderButton busy={older} onClick={onOlder} />}
          {!page.messages.length && <p className="py-10 text-center text-[12.5px] text-ink-3">{t("inbox-connectorthread.empty")}</p>}
          {page.messages.map((m, i) => (
            <Bubble key={m.id} id={id} m={m} head={startsGroup(m, page.messages[i - 1])} group={group} lit={flash?.id === m.id}
              onQuote={m.reply_to?.id ? () => void jump(m.reply_to!.id) : undefined} onReply={page.chat.reply ? () => setReplyTo(m) : undefined} />
          ))}
        </div>
      </div>
      {page.chat.reply ? (
        <ThreadComposer placeholder={t("inbox-connectorthread.send_to", { name: page.chat.name })} replyTo={replyTo} onClearReply={() => setReplyTo(null)} onSend={onSend} />
      ) : null}
    </div>
  );
}

function Bubble({ id, m, head, group, lit, onQuote, onReply }: {
  id: string;
  m: ThreadMessage;
  head: boolean;
  group: boolean;
  /** A quote just led back here. */
  lit?: boolean;
  /** Back to the message its quote answers. */
  onQuote?: () => void;
  onReply?: () => void;
}) {
  if (m.event) return <p data-mid={m.id} className="px-2 py-2 text-center text-[11.5px] text-ink-3">{m.text}</p>;
  const mine = !!m.mine;
  const pictures = (m.media ?? []).filter((x) => (x.kind === "image" || x.kind === "sticker" || x.kind === "video") && (x.ref || x.thumb));
  const others = (m.media ?? []).filter((x) => !pictures.includes(x));
  const bare = !m.text && !m.reply_to && !others.length; // only pictures: no bubble around them
  return (
    <div data-mid={m.id} className={cx("group/msg flex items-start gap-2 rounded-[16px] motion-safe:transition-colors motion-safe:duration-300", head ? "mt-2.5" : "mt-0.5", mine && "flex-row-reverse", lit && "bg-accent-tint")}>
      {!mine && <span className="w-8 shrink-0">{head && <Monogram name={m.sender.name} size={32} src={m.sender.avatar ? mediaURL(id, m.sender.avatar) : undefined} />}</span>}
      <div className={cx("flex min-w-0 max-w-[78%] flex-col", mine ? "items-end" : "items-start")}>
        {head && group && !mine && <span className="mb-0.5 px-1 text-[11.5px] font-medium text-ink-2">{m.sender.name}</span>}
        {pictures.map((x, i) => <Picture key={i} id={id} media={x} />)}
        {!bare && (
          <div className={cx("rounded-[16px] px-3 py-1.5 text-[13px] leading-relaxed break-words", mine ? "bg-user-bubble text-ink" : "bg-surface text-ink shadow-card", pictures.length > 0 && "mt-1")}>
            {m.reply_to && (() => {
              const quote = `${m.reply_to.from ? t("inbox-connectorthread.quoted_sender", { name: m.reply_to.from }) : ""}${m.reply_to.text || t("inbox-connectorthread.a_message")}`;
              const look = "mb-1 block w-full truncate border-l-2 border-line-strong pl-2 text-left text-[12px] text-ink-3";
              return onQuote ? (
                <button type="button" onClick={onQuote} title={t("inbox-connectorthread.quote_jump", { text: quote })} aria-label={t("inbox-connectorthread.quote_jump", { text: quote })}
                  className={cx(look, "rounded-r-[4px] hover:text-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent")}>
                  {quote}
                </button>
              ) : <p className={look}>{quote}</p>;
            })()}
            {others.map((x, i) => (
              <p key={i} className="flex items-center gap-1.5 text-[12.5px] text-ink-2">
                <Icon name={x.kind === "voice" ? "play" : "clip"} size={12} className="shrink-0" />
                <span className="truncate">{mediaLine(x)}</span>
              </p>
            ))}
            {m.text && <span className="whitespace-pre-wrap select-text">{m.text}</span>}
          </div>
        )}
        <span className="mt-0.5 flex items-center gap-1.5 px-1 text-[10.5px] text-ink-3 tabular-nums">
          {clock(m.at)}
          {m.edited && <span>{t("inbox-connectorthread.edited")}</span>}
          {onReply && (
            <button type="button" onClick={onReply} className="touch-hit flex items-center gap-0.5 whitespace-nowrap rounded text-ink-3 opacity-0 hover:text-ink focus-visible:opacity-100 focus-visible:outline-2 focus-visible:outline-accent group-hover/msg:opacity-100 group-focus-within/msg:opacity-100 [@media(hover:none)]:opacity-100">
              <Icon name="quote" size={10} />
              {t("inbox-connectorthread.reply")}
            </button>
          )}
        </span>
      </div>
    </div>
  );
}

function Picture({ id, media }: { id: string; media: ThreadMedia }) {
  const ref = media.ref || media.thumb!;
  const src = withToken(withManager(mediaURL(id, ref), useItemManager()));
  const sticker = media.kind === "sticker";
  const [viewing, setViewing] = useState(false);
  const w = media.width && media.height ? Math.min(sticker ? 140 : 260, media.width) : sticker ? 140 : 260;
  const ratio = media.width && media.height ? media.height / media.width : 0.75;
  const size = { width: w, height: Math.round(w * ratio) };
  const img = <img src={src} alt={media.emoji || media.name || ""} loading="lazy" className={cx("size-full", sticker ? "object-contain" : "object-cover")} />;
  // a picture opens full screen (iOS: tap → LarkImageGallery); a sticker or a video stays as it is
  if (media.kind === "image") {
    return (
      <>
        <button
          type="button"
          onClick={() => setViewing(true)}
          aria-label={t("inbox-connectorthread.view_image")}
          className="relative block overflow-hidden rounded-[14px] outline-none transition-transform duration-150 focus-visible:ring-2 focus-visible:ring-accent active:scale-[0.98] motion-reduce:transition-none"
          style={size}
          data-thread-picture
        >
          {img}
        </button>
        {viewing && src && <ImageViewer images={[{ id: src, src, mime: media.mime }]} start={0} onClose={() => setViewing(false)} />}
      </>
    );
  }
  return (
    <span className="relative block overflow-hidden rounded-[14px]" style={size}>
      {img}
      {media.kind === "video" && (
        <span className="absolute inset-0 flex items-center justify-center">
          <span className="flex size-10 items-center justify-center rounded-full bg-black/50 text-white"><Icon name="play" size={16} /></span>
          {media.duration ? <span className="absolute right-1.5 bottom-1.5 rounded-[5px] bg-black/55 px-1 text-[10.5px] text-white tabular-nums">{/* i18n-ignore: strips the prefix lib/connectors mediaLine writes */ mediaLine(media).replace(/^视频 /, "")}</span> : null}
        </span>
      )}
    </span>
  );
}

// ── a GitHub pull request or issue ───────────────────────

const STATE_TONE: Record<string, string> = {
  open: "bg-green-tint text-green",
  merged: "bg-accent-tint text-accent-ink",
  closed: "bg-field text-ink-2",
  draft: "bg-field text-ink-2",
};

function GitHubThread({ id, item, page, older, onOlder, onSend }: {
  id: string;
  item: InboxItem;
  page: ThreadPage;
  older: boolean;
  onOlder: () => void;
  onSend: (text: string) => Promise<boolean>;
}) {
  const chat = page.chat;
  const ref = connectorReference(item) ?? chat.id;
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-[760px] px-4 py-4 sm:px-6">
          <GitHubHeader chat={chat} reference={ref} />
          {chat.body && (
            <Card id={id} who={chat.author} at={undefined} title={t("inbox-connectorthread.description_by", { name: chat.author?.name ?? "" })}>
              <Markdown text={chat.body} small />
            </Card>
          )}
          {page.before && <div className="mt-3"><OlderButton busy={older} onClick={onOlder} /></div>}
          <div className="mt-3 flex flex-col gap-2.5">
            {page.messages.map((m) => <TimelineEntry key={m.id} id={id} m={m} />)}
          </div>
        </div>
      </div>
      {chat.reply && <ThreadComposer placeholder={t("inbox-connectorthread.comment_on", { ref })} onSend={(body) => onSend(body)} note={item.connector?.reply_to ? t("inbox-connectorthread.reply_goes", { how: item.connector.reply_to }) : undefined} />}
    </div>
  );
}

function GitHubHeader({ chat, reference }: { chat: ThreadChat; reference: string }) {
  return (
    <div className="mb-3 flex flex-col gap-2">
      <div className="flex min-w-0 flex-wrap items-center gap-2 text-[12px] text-ink-2">
        {chat.state && (
          <span className={cx("inline-flex h-[22px] shrink-0 items-center rounded-full px-2 text-[11.5px] font-medium whitespace-nowrap", STATE_TONE[chat.state] ?? "bg-field text-ink-2")}>
            {githubStateLabel(chat.state, chat.kind)}
          </span>
        )}
        <span className="truncate font-mono text-[12px] text-ink-2">{reference}</span>
        {chat.author && <span className="shrink-0 text-ink-3">{t("inbox-connectorthread.opened_by", { name: chat.author.name })}</span>}
        <span className="flex-1" />
        {chat.url && (
          <Button tone="quiet" icon="external" onClick={() => openLink(chat.url!, chat.name)} className="h-7 px-2">
            {t("inbox-connectorthread.open_github")}
          </Button>
        )}
      </div>
      {chat.branch && (
        <p className="flex min-w-0 items-center gap-1.5 font-mono text-[11.5px] text-ink-2">
          <Icon name="git" size={12} className="shrink-0 text-ink-3" />
          <span className="truncate">{chat.branch.head}</span>
          <Icon name="arrowRight" size={11} className="shrink-0 text-ink-3" />
          <span className="truncate">{chat.branch.base}</span>
        </p>
      )}
      {!!chat.labels?.length && (
        <div className="flex flex-wrap gap-1.5">
          {chat.labels.map((l) => {
            // A label's color is the repository's own (data, not theme): a small dot on a neutral fill.
            const c = labelColor(l.color);
            return (
              <span key={l.name} className="inline-flex h-[20px] items-center gap-1 rounded-full px-2 text-[11px] font-medium whitespace-nowrap text-ink" style={{ background: "var(--field)" }}>
                <span className="size-1.5 rounded-full" style={{ background: c ? `color-mix(in srgb, ${c} 80%, var(--ink))` : "var(--ink-3)" }} />
                {l.name}
              </span>
            );
          })}
        </div>
      )}
    </div>
  );
}

function Card({ id, who, at, title, tone, url, children }: {
  id: string;
  who?: { name: string; avatar?: string };
  at?: string;
  title: React.ReactNode;
  tone?: string;
  url?: string;
  children?: React.ReactNode;
}) {
  return (
    <div className="flex gap-2.5">
      <Monogram name={who?.name ?? "?"} size={28} src={who?.avatar ? mediaURL(id, who.avatar) : undefined} />
      <div className="min-w-0 flex-1 overflow-hidden rounded-[12px] bg-surface shadow-card">
        <div className="flex min-w-0 items-center gap-2 border-b border-line bg-inset px-3 py-1.5 text-[12px]">
          <span className={cx("min-w-0 truncate font-medium", tone ?? "text-ink")}>{title}</span>
          <span className="flex-1" />
          {at && <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">{shortTime(at)}</span>}
          {url && (
            <button type="button" onClick={() => openLink(url)} title={t("inbox-connectorthread.open_github")} aria-label={t("inbox-connectorthread.open_github")} className="shrink-0 text-ink-3 hover:text-ink">
              <Icon name="external" size={12} />
            </button>
          )}
        </div>
        {children && <div className="px-3 py-2 text-[13px] text-ink">{children}</div>}
      </div>
    </div>
  );
}

const REVIEW_TONE: Record<string, string> = { approved: "text-green", changes_requested: "text-attention" };

function TimelineEntry({ id, m }: { id: string; m: ThreadMessage }) {
  if (m.event) {
    return (
      <p className="flex items-center gap-2 pl-[38px] text-[11.5px] text-ink-3">
        <span className="size-1.5 shrink-0 rounded-full bg-line-strong" />
        <span className="min-w-0 truncate">{m.text}</span>
        <span className="shrink-0 tabular-nums">{shortTime(m.at)}</span>
      </p>
    );
  }
  if (m.kind === "check" && m.check) return <CheckRow check={m.check} at={m.at} />;
  const text = m.text ? (m.format === "markdown" ? <Markdown text={m.text} small /> : <p className="whitespace-pre-wrap">{m.text}</p>) : null;
  if (m.kind === "review") {
    return (
      <Card id={id} who={m.sender} at={m.at} url={m.url} tone={REVIEW_TONE[m.review ?? ""]} title={`${m.sender.name} ${reviewLabel(m.review)}`}>
        {text}
      </Card>
    );
  }
  if (m.kind === "code" && m.code) {
    return (
      <Card id={id} who={m.sender} at={m.at} url={m.url} title={<span className="font-mono">{m.code.path}{m.code.line ? `:${m.code.line}` : ""}</span>}>
        {m.code.hunk && <Hunk hunk={m.code.hunk} />}
        {m.reply_to && <p className="mb-1 truncate border-l-2 border-line-strong pl-2 text-[12px] text-ink-3">{m.reply_to.from ? t("inbox-connectorthread.quoted_sender", { name: m.reply_to.from }) : ""}{m.reply_to.text}</p>}
        <p className="mb-0.5 text-[11.5px] font-medium text-ink-2">{m.mine ? t("inbox-connectorthread.me") : m.sender.name}</p>
        {text}
      </Card>
    );
  }
  return (
    <Card id={id} who={m.sender} at={m.at} url={m.url} title={m.mine ? t("inbox-connectorthread.me") : m.sender.name}>
      {text}
    </Card>
  );
}

function Hunk({ hunk }: { hunk: string }) {
  return (
    <pre className="-mx-3 -mt-2 mb-2 overflow-x-auto border-b border-line bg-inset py-1 font-mono text-[11.5px] leading-[1.55]">
      {hunk.split("\n").map((l, i) => (
        <div key={i} className={cx("px-3 whitespace-pre", l.startsWith("+") ? "bg-diff-add-bg text-diff-add-fg" : l.startsWith("-") ? "bg-diff-del-bg text-diff-del-fg" : l.startsWith("@@") ? "text-ink-3" : "text-ink-2")}>
          {l || " "}
        </div>
      ))}
    </pre>
  );
}

function CheckRow({ check, at }: { check: NonNullable<ThreadMessage["check"]>; at: string }) {
  const [open, setOpen] = useState(false);
  const failed = check.state === "failure";
  const tone = check.state === "success" ? "text-green" : failed ? "text-red" : "text-ink-3";
  return (
    <div className="pl-[38px]">
      <div className="flex min-w-0 items-center gap-2 text-[12px]">
        <span className={cx("flex size-4 shrink-0 items-center justify-center", tone)}>
          {check.state === "success" ? <Icon name="check" size={13} stroke={2.6} /> : failed ? <Icon name="x" size={13} stroke={2.6} /> : check.state === "pending" ? <Spinner size={11} /> : <Icon name="clock" size={12} />}
        </span>
        <span className="min-w-0 truncate text-ink">{check.name}</span>
        <span className={cx("shrink-0", tone)}>{checkLabel(check.state)}</span>
        {check.log && (
          <button type="button" onClick={() => setOpen(!open)} aria-expanded={open} className="shrink-0 whitespace-nowrap text-[11.5px] text-accent-ink hover:underline">
            {open ? t("inbox-connectorthread.hide_log") : t("inbox-connectorthread.show_log")}
          </button>
        )}
        <span className="flex-1" />
        <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">{shortTime(at)}</span>
        {check.url && (
          <button type="button" onClick={() => openLink(check.url!)} title={t("inbox-connectorthread.open_github")} aria-label={t("inbox-connectorthread.open_github")} className="shrink-0 text-ink-3 hover:text-ink">
            <Icon name="external" size={12} />
          </button>
        )}
      </div>
      {open && check.log && (
        <pre className="mt-1.5 max-h-[280px] overflow-auto rounded-[8px] bg-inset px-3 py-2 font-mono text-[11px] leading-[1.5] whitespace-pre text-ink-2 shadow-hairline select-text">{check.log}</pre>
      )}
    </div>
  );
}
