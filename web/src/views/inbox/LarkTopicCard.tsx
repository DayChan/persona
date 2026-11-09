// A Feishu topic group drawn the way the Lark gateway draws one (#2903, iOS LarkTopicCard.swift): a card per topic with the
// poster's avatar, name and time on top and the topic under them, folded behind "Expand" past 320 px, then its newest
// replies (lib/lark-topic-replies.ts). A tap on the replies opens the topic's thread in a dialog. Topics are never run
// together like a chat's messages: where a bot posts a topic a minute, each one still has its own card, name and time.
// Reactions (emoji replies) are not drawn (not a second Lark).
import { useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { api, enc, errorText } from "../../lib/api";
import { clock } from "../../lib/format";
import { cardText, emojiLabel, mergeMessages, type LarkMessage, type LarkPage, type LarkPart } from "../../lib/inbox";
import { larkPath } from "../../lib/lark-accounts";
import { useItemManager } from "../../lib/manager-choice";
import { LarkTopicReplies, SHOWN, topicReplyCount, type TopicPreview } from "../../lib/lark-topic-replies";
import { t } from "../../i18n";
import { Button, cx, Dialog, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { MessageBar, Monogram } from "./InboxParts";

/** The fold: a topic taller than this shows its top under a fade (the Lark gateway folds at 320 px). */
const FOLD = 320;
/** A card on screen this long has its replies read (not one flung past). */
const SETTLE_MS = 200;
const THREAD_PAGE = 60;

/** One queue of reply reads for the chat shown, on its account; closed when the chat changes or the page goes. */
export function useTopicReplies(feed: string, account: number | undefined): LarkTopicReplies {
  const manager = useItemManager();
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const queue = useMemo(() => newQueue(account, manager), [feed, account, manager]);
  useEffect(() => {
    queue.open();
    return () => queue.close();
  }, [queue]);
  return queue;
}

function newQueue(account: number | undefined, manager: string | undefined) {
  return new LarkTopicReplies(async (id, limit) => {
    const r = await api<LarkPage>(larkPath(`/lark/messages/${enc(id)}/thread?limit=${limit}`, account, manager));
    return mergeMessages([], r.messages ?? []);
  });
}

function usePreview(replies: LarkTopicReplies, id: string): TopicPreview | undefined {
  const read = () => replies.preview(id);
  return useSyncExternalStore((fn) => replies.subscribe(fn), read, read);
}

/** A reply's line on its topic's card, shown and read out alike: one line, a picture or a file by what it is. */
export function replyLine(m: LarkMessage): string {
  if (m.recalled) return t("inbox-larktopiccard.recalled");
  const text = (m.text?.trim() || (m.parts ?? []).map(partSummary).join("")).replace(/\s+/g, " ").trim();
  return text || t("inbox-larktopiccard.message");
}

function partSummary(p: LarkPart): string {
  switch (p.kind) {
    case "emotion":
      return emojiLabel(p.refId ?? p.text ?? "", p.text);
    case "image":
      return t("inbox-larktopiccard.image");
    case "file":
    case "media": {
      const name = p.text || (typeof p.metadata?.name === "string" ? p.metadata.name : "");
      return name ? `${t("inbox-larktopiccard.file")} ${name}` : t("inbox-larktopiccard.file");
    }
    case "card":
      return cardText(p.metadata).title || p.text || t("inbox-larktopiccard.card");
    default:
      return p.text ?? "";
  }
}

export interface LarkTopicCardProps {
  topic: LarkMessage;
  replies: LarkTopicReplies;
  /** The topic's own content (what a chat row holds, without its header). */
  children: React.ReactNode;
  /** Opens the topic's thread; undefined when there is none to open (recalled, not in Feishu yet). */
  onOpen?: (topic: LarkMessage) => void;
  onReply?: (m: LarkMessage) => void;
  onCopy?: (m: LarkMessage) => void;
  onPin?: (m: LarkMessage) => void;
  /** Lit for a moment (a Pin jumped here); a new number lights it again. */
  flash?: number;
}

export function LarkTopicCard({ topic, replies, children, onOpen, onReply, onCopy, onPin, flash }: LarkTopicCardProps) {
  const box = useRef<HTMLDivElement>(null);
  const [onScreen, setOnScreen] = useState(false);
  const count = topicReplyCount(topic);
  const name = topic.is_mine ? t("inbox-larktopiccard.me") : topic.sender_name || t("inbox-larktopiccard.unknown");
  const open = onOpen && !topic.recalled && !topic.sending && !topic.id.startsWith("temp-") ? () => onOpen(topic) : undefined;

  useEffect(() => {
    const el = box.current;
    if (!el || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver((entries) => setOnScreen(entries.some((e) => e.isIntersecting)), { threshold: 0.01 });
    io.observe(el);
    return () => io.disconnect();
  }, []);

  // Read the newest replies once the card has been on screen a moment, and again when its reply count changes.
  useEffect(() => {
    if (!onScreen) {
      replies.drop(topic.id);
      return;
    }
    if (!open || count <= 0) return;
    const timer = setTimeout(() => replies.want(topic), SETTLE_MS);
    return () => clearTimeout(timer);
    // `open` only matters as "can it be opened"; topic identity changes on every page merge
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [onScreen, count, topic.id, Boolean(open), replies]);

  return (
    <div
      ref={box}
      data-pos={topic.position}
      data-mid={topic.id}
      data-seen-pos={topic.position}
      data-testid="lark-topic"
      className={cx("group/msg relative isolate mx-1 my-2 rounded-[14px] bg-surface px-4 py-3 shadow-card", topic.at_me && "ring-1 ring-accent/40")}
    >
      {flash !== undefined && <span key={flash} aria-hidden className="pointer-events-none absolute inset-0 -z-10 rounded-[14px] bg-accent-tint" style={{ animation: "pin-flash 1.8s ease-out both" }} />}
      <div className="flex items-center gap-2.5">
        <Monogram name={name} size={36} src={topic.sender_avatar} />
        <div className="min-w-0">
          <p className="flex items-center gap-1.5">
            <span className={cx("truncate text-[13px] font-medium", topic.is_mine ? "text-accent-ink" : "text-ink")}>{name}</span>
            {topic.sender_type === "bot" && <span className="shrink-0 rounded-[4px] bg-field px-1 text-[10px] whitespace-nowrap text-ink-3">{t("inbox-larktopiccard.bot")}</span>}
          </p>
          <p className="text-[11px] text-ink-3 tabular-nums">
            {clock(topic.created_at)}
            {topic.edited && <span className="ml-1.5">{t("inbox-larktopiccard.edited")}</span>}
          </p>
        </div>
      </div>
      <Fold>{children}</Fold>
      {!topic.recalled && count > 0 && <ReplyPreview topic={topic} count={count} replies={replies} open={open} />}
      {!topic.recalled && !topic.sending && (onReply || onCopy || onPin) && (
        <MessageBar className="top-2 right-2">
          {onReply && <HoverButton icon="quote" label={t("inbox-larktopiccard.reply")} onClick={() => onReply(topic)} />}
          {onPin && <HoverButton icon="pin" label="Pin" title={t("inbox-larktopiccard.add_pin")} onClick={() => onPin(topic)} />}
          {onCopy && <HoverButton icon="copy" title={t("inbox-larktopiccard.copy")} onClick={() => onCopy(topic)} />}
        </MessageBar>
      )}
    </div>
  );
}

function HoverButton({ icon, label, title, onClick }: { icon: "quote" | "pin" | "copy"; label?: string; title?: string; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title ?? label}
      aria-label={title ?? label}
      className={cx("flex items-center gap-1 rounded text-[11px] whitespace-nowrap text-ink-2 hover:bg-surface-hover hover:text-ink", label ? "px-1.5 py-0.5" : "p-1 text-ink-3")}
    >
      <Icon name={icon} size={11} />
      {label && <span>{label}</span>}
    </button>
  );
}

/** A topic taller than the fold shows its top under a fade with "Expand" over it; open, "Show less" under it. It is laid out no
 * taller than the fold from the start, so a long topic never shows whole first and then jumps shut. */
function Fold({ children }: { children: React.ReactNode }) {
  const inner = useRef<HTMLDivElement>(null);
  const [height, setHeight] = useState(0);
  const [open, setOpen] = useState(false);
  useLayoutEffect(() => {
    const el = inner.current;
    if (!el) return;
    setHeight(el.scrollHeight);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => setHeight(el.scrollHeight));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const tall = height > FOLD;
  const folded = tall && !open;
  return (
    <div className="mt-2">
      <div
        className="relative overflow-hidden"
        style={open ? undefined : { maxHeight: FOLD, ...(folded ? { maskImage: "linear-gradient(to bottom, #000 80%, transparent)", WebkitMaskImage: "linear-gradient(to bottom, #000 80%, transparent)" } : {}) }}
      >
        <div ref={inner}>{children}</div>
      </div>
      {tall && (
        <div className={cx("flex justify-center", folded ? "-mt-7 relative" : "mt-1")}>
          <button
            type="button"
            onClick={() => setOpen((o) => !o)}
            aria-expanded={open}
            className="rounded-full bg-surface px-4 py-1 text-[11.5px] font-medium whitespace-nowrap text-ink-2 shadow-btn hover:text-ink"
          >
            {open ? t("inbox-larktopiccard.collapse") : t("inbox-larktopiccard.expand")}
          </button>
        </div>
      )}
    </div>
  );
}

/** The newest two replies, a line each, under "View N earlier replies" when there are more. Until they are read, the lines
 * they will take are there already as placeholders: a card does not grow when its replies come in. */
function ReplyPreview({ topic, count, replies, open }: { topic: LarkMessage; count: number; replies: LarkTopicReplies; open?: () => void }) {
  const preview = usePreview(replies, topic.id);
  const shown = preview?.replies ?? [];
  const pending = shown.length ? 0 : Math.min(count, SHOWN);
  const earlier = Math.max(0, count - Math.max(shown.length, pending));
  const spoken = shown.length
    ? [...(earlier ? [t("inbox-larktopiccard.earlier", { count: earlier })] : []), ...shown.map((r) => t("inbox-larktopiccard.line", { name: senderOf(r), text: replyLine(r) }))].join(t("inbox-larktopiccard.separator"))
    : t("inbox-larktopiccard.reading", { count });
  return (
    <>
      <button
        type="button"
        onClick={open}
        disabled={!open}
        aria-label={spoken}
        data-testid="lark-topic-replies"
        className="mt-2.5 flex w-full flex-col gap-1 rounded-[10px] bg-inset px-3 py-2 text-left hover:bg-surface-hover disabled:cursor-default"
      >
        {earlier > 0 && <span className="text-[11.5px] font-medium whitespace-nowrap text-accent-ink">{t("inbox-larktopiccard.earlier", { count: earlier })}</span>}
        {shown.map((r) => (
          <span key={r.id} className="truncate text-[12.5px] text-ink-2">
            <span className="font-medium text-ink">{senderOf(r)}</span>{t("inbox-larktopiccard.colon")}{replyLine(r)}
          </span>
        ))}
        {Array.from({ length: pending }, (_, i) => (
          <span key={i} aria-hidden className="flex h-[19px] items-center gap-1.5">
            <span className="h-2.5 w-10 animate-pulse busy-motion rounded-full bg-field" />
            <span className="h-2.5 w-40 animate-pulse busy-motion rounded-full bg-field" />
          </span>
        ))}
      </button>
      {preview?.failed && !shown.length && (
        <p className="mt-1 flex items-center gap-2 text-[11.5px] text-ink-3">
          <span className="whitespace-nowrap">{t("inbox-larktopiccard.read_failed")}</span>
          <button type="button" onClick={() => replies.retry(topic)} className="font-medium whitespace-nowrap text-accent-ink hover:underline">{t("inbox-larktopiccard.retry")}</button>
        </p>
      )}
    </>
  );
}

function senderOf(m: LarkMessage) {
  return m.is_mine ? t("inbox-larktopiccard.me") : m.sender_name || t("inbox-larktopiccard.unknown");
}

/** A topic's thread: the topic, then its replies, oldest first, older ones a page at a time. */
export function LarkTopicDialog({
  topic,
  account,
  renderMessage,
  onClose,
}: {
  topic: LarkMessage;
  account: number | undefined;
  renderMessage: (m: LarkMessage) => React.ReactNode;
  onClose: () => void;
}) {
  const [page, setPage] = useState<LarkPage | null>(null);
  const [error, setError] = useState("");
  const [more, setMore] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const base = `/lark/messages/${enc(topic.id)}/thread?limit=${THREAD_PAGE}`;
  const manager = useItemManager();

  useEffect(() => {
    let live = true;
    api<LarkPage>(larkPath(base, account, manager))
      .then((r) => live && setPage({ ...r, messages: mergeMessages([], r.messages ?? []) }))
      .catch((e) => live && setError(errorText(e)));
    return () => {
      live = false;
    };
  }, [base, account, manager]);

  // Opened at the newest reply.
  const placed = useRef(false);
  useLayoutEffect(() => {
    if (!page || placed.current || !box.current) return;
    placed.current = true;
    box.current.scrollTop = box.current.scrollHeight;
  }, [page]);

  const older = async () => {
    if (!page?.older_cursor) return;
    setMore(true);
    try {
      const r = await api<LarkPage>(larkPath(`${base}&before=${enc(page.older_cursor)}`, account, manager));
      setPage((cur) => cur && { ...cur, messages: mergeMessages(cur.messages, r.messages ?? []), has_older: r.has_older, older_cursor: r.older_cursor });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setMore(false);
    }
  };

  const replies = page ? page.messages.filter((m) => m.id !== topic.id) : [];
  const count = Math.max(topicReplyCount(topic), replies.length);
  return (
    <Dialog open onClose={onClose} title={t("inbox-larktopiccard.thread")} width={620}>
      <div ref={box} className="-mx-2 max-h-[60vh] overflow-y-auto" data-testid="lark-topic-thread">
        {renderMessage(topic)}
        <p className="mx-2 my-2 border-t border-line pt-2 text-[11.5px] font-medium text-ink-3">{t("inbox-larktopiccard.replies", { count })}</p>
        {page?.has_older && (
          <div className="flex justify-center pb-1">
            <Button tone="quiet" busy={more} onClick={() => void older()}>{t("inbox-larktopiccard.older")}</Button>
          </div>
        )}
        {!page && !error && <div className="px-2 py-3"><Loading label={t("inbox-larktopiccard.loading")} /></div>}
        {replies.map((m) => <div key={m.id}>{renderMessage(m)}</div>)}
        {page && !replies.length && !error && <p className="px-2 py-2 text-[12px] text-ink-3">{t("inbox-larktopiccard.no_replies")}</p>}
        {error && <p className="flex items-center gap-1.5 px-2 py-2 text-[12px] text-red"><Icon name="x" size={12} />{error}</p>}
      </div>
    </Dialog>
  );
}
