// A Feishu chat, opened at the item's first new message (docs/assistant/inbox.md: "Opening it: the Feishu chat stops at the item's first new message").
// Supports native Feishu messaging interactions: direct replies, quoting messages, copying, asking Persona for summary,
// and reporting seen messages to Feishu (docs/assistant/inbox.md "Seen Feishu messages are reported read when visible", lark-read.ts). The chat's Pins
// (lark-pins.ts) open over it; a Pin found in the chat replaces the window around it, a reply opens its thread.
// Like the phone's chat (lib/lark-chat.ts, LarkExtras.tsx): pictures open in the viewer, files download, voice and video
// play here, a card's buttons press, my messages show who read them, reactions can be put on, an avatar opens the
// person's card, a group's box can @ people, and what is typed stays as the chat's draft.
import { chooseManager, ItemManager, noteConversation, otherManager, ownerHeaders, useItemManager, withManager } from "../../lib/manager-choice";
import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, enc, errorText, getToken } from "../../lib/api";
import { startConversation } from "../../lib/assistant-start";
import { clock } from "../../lib/format";
import {
  cardText, comparePosition, emojiLabel, isDone, mergeMessages, partRuns, withToken, type InboxItem, type LarkMessage,
  type LarkPage, type LarkPart,
} from "../../lib/inbox";
import { LARK_ATTACHMENT_REPLY_FEATURE, LARK_ATTACHMENTS_FEATURE, routeAllowed, type LarkAttachment } from "../../lib/lark-attachments";
import { larkPath, olAccount } from "../../lib/lark-accounts";
import { larkAccount } from "../../lib/lark-chats";
import {
  cardButtons, chatImages, draftKey, isVoice, loadDraft, mergeAround, reactionsOf, readMark, ReadReceipts, readStates, react,
  statusOf, toggledReactions, type ReadState,
} from "../../lib/lark-chat";
import { quoteSummary, showsBody, showsHistory, useLarkDisplay, useLarkDisplayState } from "../../lib/lark-display";
import type { LarkPerson } from "../../lib/lark-people";
import { canAddPin, changeChatPin, needsRecheck, pinProblemText, type ChatPinTarget } from "../../lib/lark-pins";
import { historyProblem, type HistoryProblem, latestOf, nearTop, OlderPaging, screensFromEnd, showsJumpToLatest } from "../../lib/lark-history";
import { initialCursor, LarkReadTracker } from "../../lib/lark-read";
import { clickLink, linkHref, prefetchLink } from "../../lib/links";
import { isResumed } from "../../lib/reconnect";
import { navigate } from "../../lib/router";
import { getState, onHubEvent, useStore } from "../../lib/store";
import { isForeignCached, requestTranslation, translatesChat, translationOf, useTranslatePrefs, useTranslations } from "../../lib/translate";
import { t } from "../../i18n";
import { Button, cx, Dialog, Loading, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { scrollBehavior } from "../../ui/stream/motion";
import { ImageViewer } from "../chat/ImageViewer";
import { Monogram } from "./InboxParts";
import { LarkEditHistoryDialog } from "./LarkEditHistory";
import { LarkComposer, type LarkComposerHandle, type LarkReplyTarget } from "./LarkComposer";
import { CardButtons, FilePart, ProfileDialog, ReactionPicker, Reactions, ReadMarkView, VideoPart, VoicePart } from "./LarkExtras";
import { newClientID, useLarkOutbox, type LarkOutgoing } from "./LarkOutbox";
import { LarkPinsDialog } from "./LarkPins";
import { isSummaryCard, LarkSummaryCard } from "./LarkSummaryCard";
import { LarkTopicCard, LarkTopicDialog, useTopicReplies } from "./LarkTopicCard";

const PAGE = 30;

export type { LarkReplyTarget };

/** What the hub's Feishu extension lets the composer send: pictures and files, and those quoting or in a thread. */
function useAttachmentSupport() {
  const files = useStore((s) => s.features.includes(LARK_ATTACHMENTS_FEATURE));
  const routed = useStore((s) => s.features.includes(LARK_ATTACHMENT_REPLY_FEATURE));
  return { files, routed };
}

/** Text, then each attachment, as the messages to send. False when the extension cannot route the attachments as
 * asked (a quote): nothing goes and it all stays in the box (docs/extensions/lark-attachments.md, #1035). */
export function outgoingOf(text: string, attachments: LarkAttachment[], route: LarkOutgoing["route"], quote?: LarkOutgoing["quote"],
  features: string[] = getState().features, mentions?: LarkOutgoing["mentions"]): LarkOutgoing[] | false {
  if (attachments.length && !routeAllowed(route, features)) return false;
  const out: LarkOutgoing[] = [];
  if (text) out.push({ clientID: newClientID(), text, route: { replyTo: route.replyTo, thread: route.thread }, quote, ...(mentions?.length ? { mentions } : {}) });
  for (const a of attachments) out.push({ clientID: newClientID(), attachment: a, route, quote });
  return out;
}

/** A batch the extension could not route: it says why. */
function refuseAttachments() {
  toast(t("inbox-larkcomposer.quote_unsupported"), "error");
}

/** The chat a LarkThread shows: from an inbox item, or a row of "Messages". */
export interface LarkChatRef {
  /** The manager of the inbox item it opened from (a merged list's stamp): its Persona summarizes it. */
  manager?: string;
  feed: string;
  title: string;
  topic?: boolean;
  /** Open at this position (the first new message) instead of the newest. */
  focus?: string;
  /** The Feishu account the chat is in (`ol_account`, docs/extensions/feishu.md "Multiple accounts"); undefined: the hub's own. */
  account?: number;
  /** A group: its box can @ people, my messages show how many read them. */
  group?: boolean;
}

export function chatOfItem(item: InboxItem): LarkChatRef {
  return { feed: item.lark!.feed_id, title: item.title, topic: item.lark?.is_topic, focus: !isDone(item) ? item.lark?.start_position : undefined,
    account: olAccount(item.lark?.ol_account), group: item.lark?.is_group, manager: item.manager };
}

/** The account of the chat a message is drawn in: its thread is read as that account. */
const ChatAccount = createContext<number | undefined>(undefined);
/** …and its feed, where a thread's pictures and files are sent. */
const ChatFeed = createContext("");

/** What a message drawn in the chat can reach: its pictures, the people, read receipts, a fresh read of the chat. */
interface ChatTools {
  feed: string;
  group: boolean;
  openImage: (src: string) => void;
  openProfile: (who: { id: string; name: string; avatar?: string; bot?: boolean }) => void;
  receipts: Map<string, ReadState>;
  /** Reads the window around a message again (a card button was pressed on it). */
  refresh: (messageID: string) => Promise<void>;
}
const Tools = createContext<ChatTools | null>(null);

type LarkThreadProps = { chat: LarkChatRef; pinsOpen?: boolean; onPinsOpen?: (open: boolean) => void };

export function LarkThread(props: LarkThreadProps) {
  // whose Lark the chat is (an inbox item's manager; a chat opened from it is the same one's): every request below names it
  const outer = useItemManager();
  const manager = props.chat.manager ?? outer;
  return (
    <ItemManager.Provider value={manager}>
      <ChatAccount.Provider value={props.chat.account}>
        <ChatFeed.Provider value={props.chat.feed}><LarkThreadBody {...props} /></ChatFeed.Provider>
      </ChatAccount.Provider>
    </ItemManager.Provider>
  );
}

function LarkThreadBody({ chat, pinsOpen = false, onPinsOpen }: LarkThreadProps) {
  const feed = chat.feed;
  const ol = chat.account;
  const manager = useItemManager();
  const focus = chat.focus;
  const [page, setPage] = useState<LarkPage | null>(null);
  const [error, setError] = useState("");
  const [newerBusy, setNewerBusy] = useState(false);
  // The draft's quote comes back with the chat (lib/lark-chat.ts drafts); set while drawing, so the box never keeps
  // another chat's quote for a frame.
  const dk = draftKey(feed, ol);
  const [replyTo, setReplyTo] = useState<LarkReplyTarget | null>(() => loadDraft(dk).replyTo ?? null);
  const [replyFor, setReplyFor] = useState(dk);
  if (replyFor !== dk) {
    setReplyFor(dk);
    setReplyTo(loadDraft(dk).replyTo ?? null);
  }
  const [viewing, setViewing] = useState<string | null>(null);
  const [reacting, setReacting] = useState<LarkMessage | null>(null);
  const [person, setPerson] = useState<{ id: string; name: string; avatar?: string; bot?: boolean } | null>(null);
  const [direct, setDirect] = useState<{ feed: string; name: string } | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const composer = useRef<LarkComposerHandle>(null);
  const support = useAttachmentSupport();
  const anchor = useRef<{ height: number; top: number; id?: string; offset?: number } | null>(null);
  const placed = useRef(false);
  const base = `/lark/conversations/${enc(feed)}/messages`;
  const topic = Boolean(chat.topic);
  const tracker = useRef<LarkReadTracker | null>(null);
  const [flash, setFlash] = useState<{ id: string; n: number } | null>(null);
  const jumpTo = useRef<string | null>(null);
  const [pinThread, setPinThread] = useState<ChatPinTarget | null>(null);
  const [adding, setAdding] = useState<LarkMessage | null>(null);
  const [changingPin, setChangingPin] = useState(false);
  const [pinRecheck, setPinRecheck] = useState(false);
  // Older pages as the list nears its top (lark-history.ts); "Back to latest" from the newest messages this window kept.
  const paging = useRef(new OlderPaging());
  const [, setPagingTick] = useState(0);
  const latest = useRef<LarkPage | null>(null);
  const toEnd = useRef(false);
  const windowGen = useRef(0);
  const [jumpShown, setJumpShown] = useState(false);
  const [jumping, setJumping] = useState(false);
  const prefs = useTranslatePrefs();
  const translateTo = translatesChat(prefs, feed) ? prefs.target : undefined;
  // A topic group: a card per topic, its newest replies on it, its thread in a dialog (LarkTopicCard.tsx).
  const topicReplies = useTopicReplies(feed, ol);
  const [openTopic, setOpenTopic] = useState<LarkMessage | null>(null);
  useLarkDisplayState(ol);

  useEffect(() => {
    let live = true;
    placed.current = false;
    tracker.current = null;
    setPage(null);
    setError("");
    setFlash(null);
    setPinThread(null);
    setOpenTopic(null);
    paging.current.reset();
    latest.current = null;
    toEnd.current = false;
    windowGen.current++;
    setJumpShown(false);
    setJumping(false);
    api<LarkPage>(larkPath(`${base}?limit=${PAGE}${focus ? `&around=${enc(focus)}` : ""}`, ol, manager))
      .then((r) => {
        if (!live) return;
        const messages = mergeMessages([], r.messages ?? []);
        tracker.current = new LarkReadTracker(initialCursor(focus, messages, topic || Boolean(r.topic)));
        setPage({ ...r, messages });
      })
      .catch((e) => live && setError(errorText(e)));
    return () => {
      live = false;
    };
  }, [base, focus, topic, ol, manager]);

  // New messages in this chat (the hub's `lark` push names the chats): read the newest page again when we show it.
  const pageRef = useRef(page);
  pageRef.current = page;
  // After a card button: the window around the pressed message, so its new state shows wherever the reader is (the
  // newest page merged into an older window would drop everything between with no gap shown).
  // `around` takes a position (docs/protocol/protocol.md); a card in an opened thread is not in the window and is not read again.
  const refreshAround = useCallback(async (mid: string) => {
    const gen = windowGen.current;
    const at = pageRef.current?.messages.find((m) => m.id === mid)?.position;
    if (!at) return;
    try {
      const r = await api<LarkPage>(larkPath(`${base}?limit=${PAGE}&around=${enc(at)}`, ol, manager));
      if (gen === windowGen.current) setPage((cur) => cur && mergeAround(cur, r));
    } catch {
      // the card keeps what it showed; the next push reads the chat again
    }
  }, [base, ol, manager]);
  useReadReports(feed, ol, scroller, tracker, pageRef, placed, page);
  const { states: receipts, changed: receiptsChanged } = useReadReceipts(feed, ol, scroller, pageRef, page);

  useEffect(() => {
    const changed = () => {
      const p = pageRef.current;
      if (!p) return;
      // something happened in the chat: the receipts of mine on screen may have moved too
      receiptsChanged();
      if (p.has_newer) return;
      const gen = windowGen.current;
      api<LarkPage>(larkPath(`${base}?limit=${PAGE}`, ol, manager))
        .then((r) => {
          if (gen !== windowGen.current) return;
          // A list showing its newest message keeps showing it as more come in (iOS larkFollowsEnd).
          const el = scroller.current;
          if (el && screensFromEnd(el) < 0.05) toEnd.current = true;
          setPage((cur) => cur && { ...cur, messages: mergeMessages(cur.messages, r.messages ?? []) });
        })
        .catch(() => {});
    };
    const offLark = onHubEvent("lark", (msg) => {
      const ids = Array.isArray(msg.feed_ids) ? (msg.feed_ids as string[]) : [];
      // another account's push names its own chats (the same group can be in two accounts)
      if (ids.includes(feed) && olAccount(msg.ol_account) === ol) changed();
    });
    // A reconnect the hub could not resume: the `lark` pushes missed meanwhile are not replayed.
    const offHello = onHubEvent("hello", (msg) => !isResumed(msg) && changed());
    return () => {
      offLark();
      offHello();
    };
  }, [feed, ol, manager, base, receiptsChanged]);

  // First paint: at the first new message (a little of what came before shows above it), else at the bottom.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el || !page) return;
    if (toEnd.current) {
      toEnd.current = false;
      el.scrollTop = el.scrollHeight;
      return;
    }
    if (jumpTo.current) {
      const target = el.querySelector<HTMLElement>(`[data-mid="${CSS.escape(jumpTo.current)}"]`);
      jumpTo.current = null;
      if (target) el.scrollTop = Math.max(0, target.offsetTop - el.clientHeight / 3);
      return;
    }
    if (anchor.current) {
      const a = anchor.current;
      anchor.current = null;
      const held = a.id ? el.querySelector<HTMLElement>(`[data-mid="${CSS.escape(a.id)}"]`) : null;
      if (held && a.offset !== undefined) el.scrollTop += held.getBoundingClientRect().top - el.getBoundingClientRect().top - a.offset;
      else el.scrollTop = a.top + (el.scrollHeight - a.height);
      return;
    }
    if (placed.current) return;
    placed.current = true;
    const target = focus ? el.querySelector<HTMLElement>(`[data-pos="${CSS.escape(focus)}"]`) : null;
    if (target) el.scrollTop = Math.max(0, target.offsetTop - 64);
    else el.scrollTop = el.scrollHeight;
  }, [page, focus]);

  const loadNewer = useCallback(async () => {
    const p = pageRef.current;
    if (!p) return;
    const gen = windowGen.current;
    setNewerBusy(true);
    try {
      const r = await api<LarkPage>(larkPath(`${base}?limit=${PAGE}&after=${enc(p.newer_cursor ?? "")}`, ol, manager));
      if (gen !== windowGen.current) return;
      setPage((cur) => cur && { ...cur, messages: mergeMessages(cur.messages, r.messages ?? []), has_newer: r.has_newer, newer_cursor: r.newer_cursor });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setNewerBusy(false);
    }
  }, [base, ol, manager]);

  // One older page at a time (iOS LarkHistoryPaging): the page goes in above with the message being read held where
  // it is; a failed or non-progressing page waits for a click on the header line; a reply for another window is dropped.
  const loadOlder = useCallback(async (retry = false) => {
    const p = pageRef.current;
    if (!p?.has_older) return;
    const req = paging.current.begin(p.older_cursor, retry);
    if (!req) return;
    setPagingTick((n) => n + 1);
    try {
      const r = await api<LarkPage>(larkPath(`${base}?limit=${PAGE}&before=${enc(req.cursor)}`, ol, manager));
      const got = paging.current.finish(req, r, pageRef.current?.messages ?? []);
      if (!got) return;
      const el = scroller.current;
      if (el) anchor.current = holdAnchor(el);
      setPage((cur) => cur && { ...cur, messages: mergeMessages(cur.messages, got.messages), has_older: got.has_older, older_cursor: got.older_cursor });
    } catch (e) {
      paging.current.fail(req, historyProblem(e instanceof ApiError ? e.code : undefined));
    } finally {
      setPagingTick((n) => n + 1);
    }
  }, [base, ol, manager]);

  const autoOlder = page !== null && !topic && !page.topic;
  // Near the top (also right after a page landed, when a short page does not fill the list) asks for the next one.
  const checkScroll = useCallback(() => {
    const el = scroller.current;
    const p = pageRef.current;
    if (!el || !p) return;
    if (autoOlder && placed.current && p.has_older && nearTop(el)) void loadOlder();
    setJumpShown(showsJumpToLatest(p.has_newer, el));
  }, [autoOlder, loadOlder]);
  useEffect(() => {
    checkScroll();
  }, [page, checkScroll]);

  // The newest messages, kept while the window reaches them, so "Back to latest" goes back without waiting for the hub.
  useEffect(() => {
    if (page && !page.has_newer && page.messages.length) latest.current = latestOf(page);
  }, [page]);

  // "Back to latest" (iOS #1034): a window that reaches the newest messages scrolls to them; one moved away (the first unread,
  // a Pin) goes back to the newest ones it kept at once, then reads the newest page quietly and joins it on when it
  // follows on; across a gap it takes the newest page only while the list is still at its end.
  const jumpToLatest = useCallback(async () => {
    const p = pageRef.current;
    const el = scroller.current;
    if (!p || !el) return;
    if (!p.has_newer) {
      el.scrollTo({ top: el.scrollHeight, behavior: scrollBehavior() });
      return;
    }
    const gen = ++windowGen.current;
    paging.current.reset();
    anchor.current = null;
    jumpTo.current = null;
    placed.current = true;
    const kept = latest.current;
    if (kept) {
      toEnd.current = true;
      setPage({ ...p, ...kept });
    } else {
      setJumping(true);
    }
    try {
      const r = await api<LarkPage>(larkPath(`${base}?limit=${PAGE}`, ol, manager));
      if (gen !== windowGen.current) return;
      const fresh = mergeMessages([], r.messages ?? []);
      const box = scroller.current;
      setPage((cur) => {
        if (!cur) return cur;
        if (!kept) return { ...cur, ...r, messages: fresh };
        const have = new Set(cur.messages.map((m) => m.id));
        if (fresh.some((m) => have.has(m.id))) return { ...cur, messages: mergeMessages(cur.messages, fresh) };
        return box && screensFromEnd(box) < 0.05 ? { ...cur, ...r, messages: fresh } : cur;
      });
      if (!kept || (box && screensFromEnd(box) < 0.05)) toEnd.current = true;
    } catch (e) {
      if (!kept) toast(errorText(e), "error");
    } finally {
      if (gen === windowGen.current) setJumping(false);
    }
  }, [base, ol, manager]);

  const handleReply = useCallback((m: LarkMessage) => {
    setReplyTo({
      id: m.id,
      sender_name: m.is_mine ? t("inbox-larkthread.me") : (m.sender_name || t("inbox-larkthread.unknown")),
      text: m.text || (m.parts ?? []).map((p) => p.text).join(""),
    });
    composer.current?.focus();
  }, []);

  // A Pin found by the hub (lark-pins.ts locate): a reply opens its thread; a message replaces the window around it.
  const openPin = useCallback((target: ChatPinTarget) => {
    onPinsOpen?.(false);
    if (target.root) {
      setPinThread(target);
      return;
    }
    placed.current = true;
    anchor.current = null;
    paging.current.reset();
    windowGen.current++;
    jumpTo.current = target.message.id;
    setPage((cur) => ({ ...(cur ?? {}), ...target.page, messages: mergeMessages([], target.page.messages ?? []) }));
    setFlash((f) => ({ id: target.message.id, n: (f?.n ?? 0) + 1 }));
  }, [onPinsOpen]);

  const addPin = useCallback(async (m: LarkMessage) => {
    setAdding(null);
    setChangingPin(true);
    try {
      await changeChatPin(feed, m.id, undefined, await larkAccount(ol, manager), ol, manager);
      toast(t("inbox-larkthread.pin_added"), "ok");
    } catch (e) {
      const recheck = needsRecheck(e);
      setPinRecheck(recheck);
      toast(pinProblemText(e), "error");
      if (recheck) onPinsOpen?.(true);
    } finally {
      setChangingPin(false);
    }
  }, [feed, ol, manager, onPinsOpen]);

  // Closing the list after it read cleanly settles an unknown add.
  const closePins = useCallback((checked: boolean) => {
    if (checked) setPinRecheck(false);
    onPinsOpen?.(false);
  }, [onPinsOpen]);

  // Starting a conversation takes seconds: while one is on its way the button is busy and another press starts nothing,
  // so a press that seemed lost does not open a second conversation (#5868). The ref holds before the state re-renders.
  const [asking, setAsking] = useState(false);
  const askingNow = useRef(false);
  const handleAskPersona = useCallback(async () => {
    if (askingNow.current) return;
    askingNow.current = true;
    setAsking(true);
    // i18n-ignore: the request to the assistant is a prompt, written in one language (docs/protocol/i18n.md "Prompts")
    const chatTitle = chat.title || "飞书会话"; // i18n-ignore
    const where = ol ? `，在飞书账号 ${ol}：lark_messages 传 account=${ol}` : ""; // i18n-ignore: prompt
    const prompt = `读取我飞书会话「${chatTitle}」（飞书网关 feedId ${feed}${where}）最近的消息，总结要点，列出需要我回复或处理的事，并给出建议的回复。`; // i18n-ignore: prompt
    toast(t("inbox-larkthread.summarizing"), "ok");
    try {
      // the chat's manager's Persona (the item's; a direct chat opened from a profile in it is the same one's)
      const r = await startConversation({ prompt }, ownerHeaders(manager));
      // its requests (feed, stop…) go to that manager whatever the page shows later
      noteConversation(r.session.session_id, otherManager(manager));
      if (manager) chooseManager(manager);
      navigate({ name: "persona", sid: r.session.session_id });
    } catch (e) {
      toast(t("inbox-larkthread.summary_failed", { error: errorText(e) }), "error");
    } finally {
      askingNow.current = false;
      setAsking(false);
    }
  }, [chat.title, manager, feed, ol]);

  const handleCopy = useCallback((m: LarkMessage) => {
    const text = m.text || (m.parts ?? []).map((p) => p.text ?? "").join("");
    if (text) {
      navigator.clipboard?.writeText(text).then(() => {
        toast(t("inbox-larkthread.copied"), "ok");
      }).catch(() => {
        toast(t("inbox-larkthread.copy_failed"), "error");
      });
    }
  }, []);

  const outbox = useLarkOutbox(
    feed, ol,
    (fn) => setPage((cur) => cur && { ...cur, messages: fn(cur.messages) }),
    () => pageRef.current?.messages ?? [],
    () => api<LarkPage>(larkPath(`${base}?limit=${PAGE}`, ol, manager)).then((r) => r.messages ?? []),
  );

  const handleSend = useCallback(async (content: string, attachments: LarkAttachment[], replyTarget?: LarkReplyTarget, mentions?: LarkPerson[]) => {
    const quote = replyTarget ? { id: replyTarget.id, sender_name: replyTarget.sender_name, text: replyTarget.text } : undefined;
    const outs = outgoingOf(content, attachments, { replyTo: replyTarget?.id }, quote, undefined, mentions);
    if (!outs) {
      refuseAttachments();
      return false;
    }
    setReplyTo(null);
    // the box is free again at once: the sends go on in the background (#1176)
    void outbox.send(outs);
    setTimeout(() => {
      const el = scroller.current;
      if (el) el.scrollTop = el.scrollHeight;
    }, 10);
    return true;
  }, [outbox]);

  // My reaction on or off: shown at once, put back when the hub says no.
  const toggleReaction = useCallback(async (m: LarkMessage, key: string) => {
    const before = reactionsOf(m);
    const { reactions, remove } = toggledReactions(before, key, t("inbox-larkthread.me"));
    const set = (rs: typeof reactions) => setPage((cur) => cur && { ...cur, messages: cur.messages.map((x) => (x.id === m.id ? { ...x, reactions: rs } : x)) });
    set(reactions);
    try {
      await react(m.id, key, remove, ol, manager);
    } catch (e) {
      set(before);
      toast(t("inbox-larkthread.react_failed", { error: errorText(e) }), "error");
    }
  }, [ol, manager]);

  // the gallery's addresses as the messages draw theirs (with the chat's manager), so the one opened is found in it
  const images = useMemo(() => chatImages(page?.messages ?? [], (u) => safeURL(u, manager)), [page, manager]);
  const tools = useMemo<ChatTools>(() => ({
    feed,
    group: Boolean(chat.group),
    openImage: setViewing,
    openProfile: setPerson,
    receipts,
    refresh: refreshAround,
  }), [feed, chat.group, receipts, refreshAround]);

  if (!page) {
    return <div className="p-5">{error ? <p className="text-[12.5px] text-red">{error}</p> : <Loading label={t("inbox-larkthread.loading")} />}</div>;
  }
  const firstNew = focus ? page.messages.find((m) => comparePosition(m.position, focus) >= 0)?.id : undefined;
  const shown = viewing ? images.findIndex((i) => i.src === viewing) : -1;
  const topicGroup = topic || Boolean(page.topic);
  return (
    <Tools.Provider value={tools}>
    <div
      className="flex h-full min-h-0 flex-col"
      onDragOver={(e) => {
        if (support.files && e.dataTransfer.types.includes("Files")) e.preventDefault();
      }}
      onDrop={(e) => {
        if (!support.files || !e.dataTransfer.files.length) return;
        e.preventDefault();
        composer.current?.addFiles(e.dataTransfer.files);
      }}
    >
      <div className="relative flex min-h-0 flex-1 flex-col">
      <div ref={scroller} onScroll={checkScroll} className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-[760px] px-2 py-3 sm:px-4">
          {page.has_older && (
            <OlderHeader auto={autoOlder} loading={paging.current.loading} problem={paging.current.problem} onLoad={() => void loadOlder(true)} />
          )}
          {!page.messages.length && <p className="py-10 text-center text-[12.5px] text-ink-3">{t("inbox-larkthread.empty")}</p>}
          {page.messages.map((m, i) => (
            <div key={m.id}>
              {m.id === firstNew && (
                <div className="my-2 flex items-center gap-2 px-2 text-[11px] font-medium text-accent-ink">
                  <span className="h-px flex-1 bg-accent/40" />
                  {t("inbox-larkthread.new_below")}
                  <span className="h-px flex-1 bg-accent/40" />
                </div>
              )}
              {topicGroup && !isSystem(m) ? (
                <LarkTopicCard
                  topic={m}
                  replies={topicReplies}
                  onOpen={setOpenTopic}
                  onReply={handleReply}
                  onCopy={handleCopy}
                  onPin={onPinsOpen && !changingPin && !pinRecheck && canAddPin(m) ? setAdding : undefined}
                  flash={flash?.id === m.id ? flash.n : undefined}
                >
                  <MessageBody m={m} translateTo={translateTo} />
                </LarkTopicCard>
              ) : <Message
                m={m}
                prev={m.id === firstNew ? undefined : page.messages[i - 1]}
                translateTo={translateTo}
                onReply={handleReply}
                onAskPersona={handleAskPersona}
                askingPersona={asking}
                onCopy={handleCopy}
                onRetry={outbox.retry}
                onCheck={outbox.check}
                onDiscard={outbox.discard}
                threadAttachments={support.files && support.routed}
                onPin={onPinsOpen && !changingPin && !pinRecheck ? setAdding : undefined}
                onReact={setReacting}
                onToggleReaction={toggleReaction}
                flash={flash?.id === m.id ? flash.n : undefined}
              />}
            </div>
          ))}
          {page.has_newer && (
            <div className="flex justify-center pt-2">
              <Button tone="quiet" busy={newerBusy} onClick={() => void loadNewer()}>{t("inbox-larkthread.newer")}</Button>
            </div>
          )}
          {error && <p className="px-2 pt-2 text-[12px] text-red">{error}</p>}
        </div>
      </div>
      {jumpShown && (
        <div className="pointer-events-none absolute inset-x-0 bottom-3 z-10 flex justify-center">
          <button
            type="button"
            onClick={() => void jumpToLatest()}
            disabled={jumping}
            className="pointer-events-auto inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded-full bg-surface px-3 text-[12px] font-medium text-ink shadow-raised transition-transform duration-150 active:scale-[0.96]"
            style={{ animation: "fade-up 180ms ease-out both" }}
          >
            {jumping ? <Spinner size={12} /> : <Icon name="arrowDown" size={13} />}
            {t("inbox-larkthread.jump_latest")}
          </button>
        </div>
      )}
      </div>
      <LarkComposer
        key={dk}
        ref={composer}
        draft={dk}
        feed={feed}
        account={ol}
        group={Boolean(chat.group) && !topicGroup}
        replyTo={replyTo}
        onClearReply={() => setReplyTo(null)}
        onSend={handleSend}
        attachments={support.files}
      />
      {viewing && (
        <ImageViewer
          images={(shown >= 0 ? images : [{ id: viewing, src: viewing }]).map((i) => ({ id: i.id, src: i.src, label: t("inbox-larkthread.image_name") }))}
          start={Math.max(0, shown)}
          onClose={() => setViewing(null)}
        />
      )}
      {reacting && (
        <ReactionPicker
          mine={new Set(reactionsOf(reacting).filter((r) => r.mine).map((r) => r.key.toLowerCase()))}
          onPick={(key) => {
            const cur = pageRef.current?.messages.find((x) => x.id === reacting.id) ?? reacting;
            void toggleReaction(cur, key);
          }}
          onClose={() => setReacting(null)}
        />
      )}
      {person && (
        <ProfileDialog
          who={person}
          feed={feed}
          account={ol}
          onClose={() => setPerson(null)}
          onChat={(f, name) => {
            setPerson(null);
            setDirect({ feed: f, name });
          }}
        />
      )}
      {direct && (
        <Dialog open onClose={() => setDirect(null)} title={direct.name} width={680}>
          <div className="-mx-4 -mb-4 h-[min(70vh,640px)] max-sm:h-[70vh]">
            <LarkThread chat={{ feed: direct.feed, title: direct.name, account: ol }} />
          </div>
        </Dialog>
      )}
      {pinsOpen && <LarkPinsDialog feed={feed} topic={topic} account={ol} onClose={closePins} onOpen={openPin} />}
      {openTopic && <LarkTopicDialog topic={openTopic} account={ol} onClose={() => setOpenTopic(null)} renderMessage={(m) => <Message m={m} nested translateTo={translateTo} />} />}
      {pinThread && <PinThreadDialog target={pinThread} translateTo={translateTo} onClose={() => setPinThread(null)} />}
      <Dialog
        open={adding !== null}
        onClose={() => setAdding(null)}
        title={t("inbox-larkthread.pin_confirm")}
        footer={(
          <>
            <Button tone="quiet" onClick={() => setAdding(null)}>{t("common.cancel")}</Button>
            <Button tone="accent" icon="pin" onClick={() => adding && void addPin(adding)}>{t("inbox-larkthread.add_pin")}</Button>
          </>
        )}
      >
        <p className="text-[12.5px] leading-relaxed text-ink-2">{t("inbox-larkthread.pin_hint")}</p>
        {adding && <p className="mt-2 line-clamp-3 rounded-[8px] bg-inset px-3 py-2 text-[12.5px] text-ink shadow-hairline">{adding.text || (adding.parts ?? []).map((p) => p.text ?? "").join("") || t("inbox-larkthread.message")}</p>}
      </Dialog>
    </div>
    </Tools.Provider>
  );
}

/** Where the list stands before a page goes in above: the message in the middle of the list and how far it is from
 * the list's top (iOS LarkHistoryHold). The oldest row is not used: its own header can change once older rows join it. */
function holdAnchor(el: HTMLElement): { height: number; top: number; id?: string; offset?: number } {
  const box = el.getBoundingClientRect();
  const mid = box.top + box.height / 2;
  const rows = [...el.querySelectorAll<HTMLElement>(":scope > div > div > [data-mid]")];
  const pick = rows.slice(1).find((r) => r.getBoundingClientRect().bottom >= mid) ?? rows[1];
  return { height: el.scrollHeight, top: el.scrollTop, id: pick?.dataset.mid, offset: pick ? pick.getBoundingClientRect().top - box.top : undefined };
}

function olderProblemText(p: HistoryProblem): string {
  switch (p) {
    case "stopped": return t("inbox-larkthread.older_stopped");
    case "account_changed": return t("inbox-larkthread.older_account_changed");
    case "signed_out": return t("inbox-larkthread.older_signed_out");
    default: return t("inbox-larkthread.older_failed");
  }
}

/** Above the oldest message loaded: a small spinner while a page loads by itself (a topic group keeps the button,
 * iOS #333), and one clickable line when paging stopped. */
function OlderHeader({ auto, loading, problem, onLoad }: { auto: boolean; loading: boolean; problem: HistoryProblem | null; onLoad: () => void }) {
  if (problem) {
    return (
      <div className="flex justify-center pb-2">
        <button type="button" onClick={onLoad} className="truncate whitespace-nowrap px-2 py-1 text-[12px] font-medium text-accent-ink hover:underline">
          {olderProblemText(problem)}
        </button>
      </div>
    );
  }
  if (!auto) {
    return (
      <div className="flex justify-center pb-2">
        <Button tone="quiet" busy={loading} onClick={onLoad}>{t("inbox-larkthread.older")}</Button>
      </div>
    );
  }
  return (
    <div className="flex h-7 justify-center pb-2" aria-live="polite">
      {loading && (
        <span className="inline-flex items-center">
          <Spinner size={14} />
          <span className="sr-only">{t("inbox-larkthread.older_loading")}</span>
        </span>
      )}
    </div>
  );
}

/** A pinned reply in its thread (iOS opens LarkThreadView): the thread's first message, then the replies the hub read
 * up to the pinned one, which is brought into view and lit for a moment. */
function PinThreadDialog({ target, translateTo, onClose }: { target: ChatPinTarget; translateTo?: string; onClose: () => void }) {
  const box = useRef<HTMLDivElement>(null);
  const root = target.root!;
  const replies = mergeMessages([], (target.page.messages ?? []).filter((m) => m.id !== root.id));
  useLayoutEffect(() => {
    box.current?.querySelector<HTMLElement>(`[data-mid="${CSS.escape(target.message.id)}"]`)?.scrollIntoView({ block: "center" });
  }, [target]);
  return (
    <Dialog open onClose={onClose} title={t("inbox-larkthread.thread")} width={620}>
      <div ref={box} className="-mx-2 max-h-[60vh] overflow-y-auto">
        <Message m={root} nested translateTo={translateTo} flash={root.id === target.message.id ? 1 : undefined} />
        <p className="mx-2 my-2 border-t border-line pt-2 text-[11.5px] font-medium text-ink-3">{t("inbox-larkthread.replies", { count: replies.length })}</p>
        {replies.map((m) => <Message key={m.id} m={m} nested translateTo={translateTo} flash={m.id === target.message.id ? 1 : undefined} />)}
      </div>
    </Dialog>
  );
}

/** Feishu read state from what reached the screen: the list's messages (not the opened threads') are watched;
 * seen ones are reported 450 ms after the last one came into view, and when the page goes to the background or closes. */
function useReadReports(
  feed: string,
  ol: number | undefined,
  scroller: React.RefObject<HTMLDivElement | null>,
  tracker: React.RefObject<LarkReadTracker | null>,
  pageRef: React.RefObject<LarkPage | null>,
  placed: React.RefObject<boolean>,
  page: LarkPage | null,
) {
  const inView = useRef(new Set<string>());
  const observer = useRef<IntersectionObserver | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const ready = page !== null;
  // bound here: a report flushed as the chat closes still goes to this chat's manager
  const manager = useItemManager();

  useEffect(() => {
    const root = scroller.current;
    if (!ready || !root) return;
    const flush = () => {
      clearTimeout(timer.current);
      const t = tracker.current;
      const p = pageRef.current;
      if (!t || !p) return;
      const previous = t.cursor;
      const report = t.report(p.messages);
      if (!report) return;
      api(larkPath(`/lark/conversations/${enc(feed)}/read`, ol, manager), { method: "POST", json: report, token: getToken() }).catch(() => t.undo(report, previous));
    };
    // The gate (docs/archive/redesign/ux.md 3.5): in front, placed, nothing covering the list.
    const look = () => {
      const t = tracker.current;
      if (!t || !placed.current || document.visibilityState !== "visible" || document.querySelector('[aria-modal="true"]')) return;
      let fresh = false;
      for (const pos of inView.current) if (t.see(pos)) fresh = true;
      if (!fresh) return;
      clearTimeout(timer.current);
      timer.current = setTimeout(flush, 450);
    };
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          const pos = (e.target as HTMLElement).dataset.seenPos;
          if (!pos) continue;
          const enough = e.isIntersecting && (e.intersectionRatio >= 0.5 || (e.rootBounds !== null && e.intersectionRect.height >= e.rootBounds.height * 0.5));
          if (enough) inView.current.add(pos);
          else inView.current.delete(pos);
        }
        look();
      },
      // Every tenth: a message taller than the list still passes "half the list" as it scrolls through.
      { root, threshold: Array.from({ length: 11 }, (_, i) => i / 10) },
    );
    observer.current = io;
    root.querySelectorAll<HTMLElement>("[data-seen-pos]").forEach((el) => io.observe(el));
    const onVisibility = () => (document.visibilityState === "visible" ? look() : flush());
    document.addEventListener("visibilitychange", onVisibility);
    // A dialog over the page ("AI draft reply", "Hand to AI") is portaled into <body>: look again once it is gone.
    const dialogs = new MutationObserver(look);
    dialogs.observe(document.body, { childList: true });
    const seen = inView.current;
    return () => {
      io.disconnect();
      observer.current = null;
      dialogs.disconnect();
      document.removeEventListener("visibilitychange", onVisibility);
      flush();
      seen.clear();
    };
  }, [feed, ol, manager, ready, scroller, tracker, pageRef, placed]);

  // Messages added later (older / newer pages, pushes) are watched too.
  useEffect(() => {
    const io = observer.current;
    scroller.current?.querySelectorAll<HTMLElement>("[data-seen-pos]").forEach((el) => io?.observe(el));
  }, [page, scroller]);
}

/** Read receipts of my messages on screen (lib/lark-chat.ts ReadReceipts, iOS LarkReadReceipts): the list's rows are
 * watched as they come into view and leave it; asked in batches, at most every 3 s, again when messages come or go or a
 * push names the chat (not when a reaction or a send state flips), not at all on a hub without the route. */
function useReadReceipts(
  feed: string,
  ol: number | undefined,
  scroller: React.RefObject<HTMLDivElement | null>,
  pageRef: React.RefObject<LarkPage | null>,
  page: LarkPage | null,
): { states: Map<string, ReadState>; changed: () => void } {
  const rr = useRef<ReadReceipts | null>(null);
  const [states, setStates] = useState<Map<string, ReadState>>(() => new Map());
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const busy = useRef(false);
  const manager = useItemManager();
  const live = useRef({ feed, ol, manager });
  live.current = { feed, ol, manager };
  const observer = useRef<IntersectionObserver | null>(null);
  const ids = useRef("");

  const schedule = useCallback(() => {
    const r = rr.current;
    if (!r || busy.current) return;
    const wait = r.wait(Date.now());
    if (wait === null) return;
    clearTimeout(timer.current);
    timer.current = setTimeout(async () => {
      if (rr.current !== r) return;
      const ids = r.take(Date.now());
      if (!ids.length) return;
      busy.current = true;
      const at = live.current;
      try {
        r.record(await readStates(at.feed, ids, at.ol, at.manager));
      } catch (e) {
        r.failed(statusOf(e));
      } finally {
        busy.current = false;
      }
      if (rr.current !== r) return;
      setStates(new Map(r.states));
      schedule();
    }, wait);
  }, []);

  const changed = useCallback(() => {
    rr.current?.changed();
    schedule();
  }, [schedule]);

  const ready = page !== null;
  useEffect(() => {
    const r = new ReadReceipts();
    rr.current = r;
    ids.current = "";
    setStates(new Map());
    const root = scroller.current;
    if (ready && root && typeof IntersectionObserver !== "undefined") {
      observer.current = new IntersectionObserver(
        (entries) => {
          if (rr.current !== r) return;
          const byId = new Map((pageRef.current?.messages ?? []).map((m) => [m.id, m]));
          for (const e of entries) {
            const id = (e.target as HTMLElement).dataset.mid;
            if (!id) continue;
            const m = byId.get(id);
            if (e.isIntersecting && m) r.appeared(m);
            else r.disappeared(id);
          }
          schedule();
        },
        { root },
      );
    }
    return () => {
      observer.current?.disconnect();
      observer.current = null;
      clearTimeout(timer.current);
      rr.current = null;
      busy.current = false;
    };
  }, [feed, ol, manager, ready, scroller, pageRef, schedule]);

  // Rows drawn later (older / newer pages, pushes, a sent message getting its id) are watched too; observing a row
  // twice is a no-op. Only a change in which messages there are asks again.
  useEffect(() => {
    const r = rr.current;
    if (!r || !page) return;
    scroller.current?.querySelectorAll<HTMLElement>("[data-seen-pos][data-mid]").forEach((el) => observer.current?.observe(el));
    const now = page.messages.filter(ReadReceipts.tracks).map((m) => m.id).join(",");
    if (now === ids.current) return;
    ids.current = now;
    changed();
  }, [page, scroller, changed]);

  return { states, changed };
}

function isSystem(m: LarkMessage) {
  return m.type === "SYSTEM" || (m.parts ?? []).some((p) => p.kind === "text" && p.metadata?.system);
}

/** Settings › Translation for this chat: the translation of someone else's message in another language, asked for when it shows. */
function TranslationLine({ m, target }: { m: LarkMessage; target: string }) {
  const text = m.text?.trim() ?? "";
  const applies = !m.is_mine && !m.recalled && text !== "" && isForeignCached(text, target);
  useTranslations();
  useEffect(() => {
    if (applies) requestTranslation(text, target);
  }, [applies, text, target]);
  const tr = applies ? translationOf(text, target) : undefined;
  if (!tr) return null;
  return (
    <p className="mt-1 flex items-start gap-1.5 text-[12.5px] leading-relaxed text-ink-2" aria-label={t("inbox-larkthread.translation_label", { text: tr })}>
      <Icon name="translate" size={12} className="mt-[3px] shrink-0 text-ink-3" />
      <span className="min-w-0 break-words whitespace-pre-wrap">{tr}</span>
    </p>
  );
}

/** A topic's own content on its card: what a chat row holds under its header. */
function MessageBody({ m, translateTo }: { m: LarkMessage; translateTo?: string }) {
  const display = useLarkDisplay(useContext(ChatAccount));
  const shown = showsBody(display, m);
  return (
    <>
      {m.reply_to && (
        <p className="mb-1 truncate border-l-2 border-line-strong pl-2 text-[12px] text-ink-3">
          {m.reply_to.sender_name ? t("inbox-larkthread.quoted_sender", { name: m.reply_to.sender_name }) : ""}{quoteSummary(display, m.reply_to)}
        </p>
      )}
      {shown ? <Parts parts={m.parts?.length ? m.parts : [{ kind: "text", text: m.text ?? "" }]} /> : <p className="text-[13px] text-ink-3 italic">{t("inbox-larkthread.recalled")}</p>}
      {shown && m.recalled && <p className="mt-0.5 text-[11px] text-ink-3">{t("inbox-larkthread.recalled_mark")}</p>}
      {translateTo && <TranslationLine m={m} target={translateTo} />}
    </>
  );
}

/** One message: who and when, quoted reply, the content, hover action bar, and retry for failed sends. */
function Message({
  m,
  prev,
  nested,
  translateTo,
  onReply,
  onAskPersona,
  askingPersona,
  onCopy,
  onRetry,
  onCheck,
  onDiscard,
  onPin,
  onReact,
  onToggleReaction,
  flash,
  threadAttachments,
}: {
  m: LarkMessage;
  prev?: LarkMessage;
  nested?: boolean;
  translateTo?: string;
  onReply?: (m: LarkMessage) => void;
  onAskPersona?: () => void;
  /** A conversation asked for with onAskPersona is starting: its button is busy until it opens or fails. */
  askingPersona?: boolean;
  onCopy?: (m: LarkMessage) => void;
  onRetry?: (m: LarkMessage) => void;
  /** A file whose upload broke off: "Check result", or remove the local copy. */
  onCheck?: (m: LarkMessage) => void;
  onDiscard?: (m: LarkMessage) => void;
  /** The thread's reply box can send pictures and files too. */
  threadAttachments?: boolean;
  /** "Add Pin", offered when the hub says the message may be pinned. */
  onPin?: (m: LarkMessage) => void;
  /** "Reactions": the picker for this message. */
  onReact?: (m: LarkMessage) => void;
  /** A reaction under it clicked: mine on or off. */
  onToggleReaction?: (m: LarkMessage, key: string) => void;
  /** Lit for a moment (a Pin jumped here); a new number lights it again. */
  flash?: number;
}) {
  const ol = useContext(ChatAccount);
  const manager = useItemManager();
  const tools = useContext(Tools);
  const [thread, setThread] = useState<LarkMessage[] | null>(null);
  const [opening, setOpening] = useState(false);
  const display = useLarkDisplay(ol);
  const [history, setHistory] = useState(false);
  if (isSystem(m)) {
    return <p data-pos={m.position} data-seen-pos={nested ? undefined : m.position} className="px-2 py-1.5 text-center text-[11.5px] text-ink-3">{m.recalled ? t("inbox-larkthread.recalled") : m.text || (m.parts ?? []).map((p) => p.text).join("")}</p>;
  }
  const shown = showsBody(display, m);
  const edited = showsHistory(display, m) ? (
    <button type="button" onClick={() => setHistory(true)} title={t("inbox-larkthread.edit_history_hint")} className="shrink-0 text-[11px] whitespace-nowrap text-ink-3 underline decoration-dotted underline-offset-2 hover:text-ink">{t("inbox-larkthread.edited")}</button>
  ) : <span className="shrink-0 text-[11px] text-ink-3">{t("inbox-larkthread.edited")}</span>;
  const name = m.is_mine ? t("inbox-larkthread.me") : m.sender_name || t("inbox-larkthread.unknown");
  const compact = !nested && prev && !isSystem(prev) && prev.sender_id === m.sender_id && !m.reply_to
    && Date.parse(m.created_at ?? "") - Date.parse(prev.created_at ?? "") < 5 * 60_000;
  const replies = m.thread_reply_count || m.reply_count || 0;
  const mark = m.is_mine && !m.sending && !m.failed && tools ? readMark(tools.receipts.get(m.id), tools.group) : null;
  const reactions = m.recalled ? [] : reactionsOf(m);
  const openThread = async () => {
    if (thread) return setThread(null);
    setOpening(true);
    try {
      const r = await api<LarkPage>(larkPath(`/lark/messages/${enc(m.id)}/thread?limit=50`, ol, manager));
      setThread(mergeMessages([], r.messages ?? []));
    } catch {
      setThread([]);
    } finally {
      setOpening(false);
    }
  };
  return (
    <div
      data-pos={m.position}
      data-mid={m.id}
      data-seen-pos={nested ? undefined : m.position}
      className={cx(
        "group/msg relative flex gap-2.5 rounded-[10px] px-2",
        compact ? "py-0.5" : "pt-2 pb-0.5",
        m.at_me && "bg-accent-tint/60",
        flash !== undefined && "isolate",
      )}
    >
      {flash !== undefined && <span key={flash} aria-hidden className="pointer-events-none absolute inset-0 -z-10 rounded-[10px] bg-accent-tint" style={{ animation: "pin-flash 1.8s ease-out both" }} />}
      <span className="w-8 shrink-0">
        {!compact && (tools && m.sender_id && !m.is_mine ? (
          <button
            type="button"
            onClick={() => tools.openProfile({ id: m.sender_id!, name, avatar: m.sender_avatar, bot: m.sender_type === "bot" })}
            aria-label={t("inbox-larkthread.open_profile", { name })}
            title={t("inbox-larkthread.open_profile", { name })}
            className="block rounded-full transition-opacity hover:opacity-85"
          >
            <Monogram name={name} size={nested ? 24 : 32} src={m.sender_avatar} />
          </button>
        ) : <Monogram name={name} size={nested ? 24 : 32} src={m.sender_avatar} />)}
      </span>
      <div className="min-w-0 flex-1">
        {!compact && (
          <p className="flex items-baseline gap-2">
            <span className={cx("truncate text-[12.5px] font-medium", m.is_mine ? "text-accent-ink" : "text-ink")}>{name}</span>
            {m.sender_type === "bot" && <span className="shrink-0 rounded-[4px] bg-field px-1 text-[10px] text-ink-3">{t("inbox-larkthread.bot")}</span>}
            <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">{clock(m.created_at)}</span>
            {m.edited && edited}
            {mark && <ReadMarkView mark={mark} />}
            {m.sending && (
              <span className="flex items-center gap-1 text-[11px] text-ink-3">
                <Spinner size={10} />
                <span>{t("inbox-larkthread.sending")}</span>
              </span>
            )}
            {m.failed && m.local_state === "unknown" && (
              <span className="flex items-center gap-2 text-[11px]">
                <span className="text-red">{t("inbox-larkthread.unknown_result")}</span>
                {onCheck && <button type="button" onClick={() => onCheck(m)} className="font-medium whitespace-nowrap text-accent-ink hover:underline">{t("inbox-larkthread.check_result")}</button>}
                {onDiscard && <button type="button" onClick={() => onDiscard(m)} className="whitespace-nowrap text-ink-3 hover:text-ink hover:underline">{t("inbox-larkthread.discard")}</button>}
              </span>
            )}
            {m.failed && m.local_state !== "unknown" && (
              <button
                type="button"
                onClick={() => onRetry?.(m)}
                className="flex items-center gap-1 text-[11px] font-medium text-red hover:underline"
                title={t("inbox-larkthread.click_retry")}
              >
                <Icon name="retry" size={11} />
                <span>{t("inbox-larkthread.failed_retry")}</span>
              </button>
            )}
          </p>
        )}
        {m.reply_to && (
          <p className="mt-0.5 truncate border-l-2 border-line-strong pl-2 text-[12px] text-ink-3">
            {m.reply_to.sender_name ? t("inbox-larkthread.quoted_sender", { name: m.reply_to.sender_name }) : ""}{quoteSummary(display, m.reply_to)}
          </p>
        )}
        {shown ? <Parts mid={m.id} parts={m.parts?.length ? m.parts : [{ kind: "text", text: m.text ?? "" }]} /> : <p className="text-[13px] text-ink-3 italic">{t("inbox-larkthread.recalled")}</p>}
        {((shown && m.recalled) || (compact && m.edited)) && (
          <p className="mt-0.5 flex items-center gap-2 text-[11px] text-ink-3">
            {shown && m.recalled && <span>{t("inbox-larkthread.recalled_mark")}</span>}
            {compact && m.edited && edited}
          </p>
        )}
        {history && <LarkEditHistoryDialog message={m} display={display} renderParts={(parts) => <Parts mid={m.id} parts={parts} />} onClose={() => setHistory(false)} />}
        {translateTo && <TranslationLine m={m} target={translateTo} />}
        <Reactions reactions={reactions} onToggle={onToggleReaction && !m.sending ? (key) => onToggleReaction(m, key) : undefined} />

        {replies > 0 && !nested && (
          <button type="button" onClick={() => void openThread()} className="mt-1 flex items-center gap-1 text-[11.5px] font-medium whitespace-nowrap text-accent-ink hover:underline">
            {opening ? <Spinner size={10} /> : <Icon name="chat" size={11} stroke={2.2} />}
            {thread ? t("inbox-larkthread.hide_replies") : t("inbox-larkthread.replies", { count: replies })}
          </button>
        )}
        {thread && <ThreadPanel root={m} thread={thread} setThread={setThread} translateTo={translateTo} attachments={threadAttachments} />}
      </div>
      {compact && mark && <span className="flex shrink-0 self-end pb-1"><ReadMarkView mark={mark} /></span>}

      {/* Hover action bar */}
      {/* nested replies have no actions: no empty bar when the message they sit under is hovered */}
      {!isSystem(m) && !m.recalled && !m.sending && (onReply || onCopy || (!nested && (onAskPersona || onPin))) && (
        <div className="absolute right-2 top-1.5 z-10 hidden items-center gap-0.5 rounded-[6px] border border-line bg-surface/95 px-1 py-0.5 shadow-hairline backdrop-blur-sm group-hover/msg:flex">
          {onReply && (
            <button
              type="button"
              onClick={() => onReply(m)}
              className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-ink-2 hover:bg-surface-hover hover:text-ink"
              title={t("inbox-larkthread.reply")}
            >
              <Icon name="quote" size={11} />
              <span>{t("inbox-larkthread.reply")}</span>
            </button>
          )}
          {onAskPersona && !nested && (
            <button
              type="button"
              onClick={onAskPersona}
              disabled={askingPersona}
              aria-busy={askingPersona || undefined}
              className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-accent-ink hover:bg-accent-tint/40"
              title={t("inbox-larkthread.ask_persona")}
            >
              {askingPersona ? <Spinner size={11} /> : <Icon name="spark" size={11} />}
              <span>{t("inbox-larkthread.summarize")}</span>
            </button>
          )}
          {onReact && !nested && !m.failed && (
            <button
              type="button"
              onClick={() => onReact(m)}
              className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] whitespace-nowrap text-ink-2 hover:bg-hover hover:text-ink"
              title={t("inbox-larkthread.react")}
            >
              <Icon name="smile" size={11} />
              <span>{t("inbox-larkthread.react")}</span>
            </button>
          )}
          {onPin && !nested && canAddPin(m) && (
            <button
              type="button"
              onClick={() => onPin(m)}
              className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] whitespace-nowrap text-ink-2 hover:bg-surface-hover hover:text-ink"
              title={t("inbox-larkthread.add_pin")}
            >
              <Icon name="pin" size={11} />
              <span>Pin</span>
            </button>
          )}
          {onCopy && (
            <button
              type="button"
              onClick={() => onCopy(m)}
              className="flex items-center rounded p-1 text-ink-3 hover:bg-surface-hover hover:text-ink"
              title={t("inbox-larkthread.copy")}
            >
              <Icon name="copy" size={11} />
            </button>
          )}
        </div>
      )}
    </div>
  );
}

/** A thread opened under its first message: the replies, and a box to reply in the thread (iOS LarkThreadView). */
function ThreadPanel({ root, thread, setThread, translateTo, attachments }: {
  root: LarkMessage;
  thread: LarkMessage[];
  setThread: React.Dispatch<React.SetStateAction<LarkMessage[] | null>>;
  translateTo?: string;
  attachments?: boolean;
}) {
  const ol = useContext(ChatAccount);
  const manager = useItemManager();
  const feed = useContext(ChatFeed);
  const threadRef = useRef(thread);
  threadRef.current = thread;
  const outbox = useLarkOutbox(
    feed, ol,
    (fn) => setThread((cur) => fn(cur ?? [])),
    () => threadRef.current,
    () => api<LarkPage>(larkPath(`/lark/messages/${enc(root.id)}/thread?limit=50`, ol, manager)).then((r) => r.messages ?? []),
  );
  return (
    <div className="mt-1 border-l-2 border-line pl-1">
      {thread.length
        ? thread.map((r) => <Message key={r.id} m={r} nested translateTo={translateTo} onRetry={outbox.retry} onCheck={outbox.check} onDiscard={outbox.discard} />)
        : <p className="px-2 py-1 text-[11.5px] text-ink-3">{t("inbox-larkthread.replies_unavailable")}</p>}
      <LarkComposer
        compact
        attachments={attachments}
        placeholder={t("inbox-larkthread.thread_placeholder")}
        onSend={async (text, picked) => {
          const outs = outgoingOf(text, picked, { replyTo: root.id, thread: true, threadRoot: root.id });
          if (!outs) {
            refuseAttachments();
            return false;
          }
          void outbox.send(outs);
          return true;
        }}
      />
    </div>
  );
}

/** A picture of the hub's (a relative /api/ URL with the token) at full size in a tab of its own. */
export function openMedia(src: string) {
  window.open(src, "_blank", "noopener,noreferrer");
}

function safeURL(u?: string, manager?: string) {
  if (!u) return undefined;
  if (u.startsWith("/api/")) return withToken(withManager(u, manager));
  return /^(https?:|mailto:)/i.test(u) ? u : undefined;
}

function LinkText({ url, children }: { url?: string; children: React.ReactNode }) {
  const safe = safeURL(url, useItemManager());
  if (!safe) return <>{children}</>;
  return (
    <a
      href={linkHref(safe)}
      target="_blank"
      rel="noopener noreferrer"
      onClick={(e) => clickLink(e, safe, typeof children === "string" ? children : undefined)}
      onMouseEnter={() => prefetchLink(safe)}
      className="text-accent-ink underline decoration-accent-ink/35 underline-offset-[3px] hover:decoration-accent-ink"
    >
      {children}
    </a>
  );
}

function Parts({ parts, mid }: { parts: LarkPart[]; mid?: string }) {
  return (
    <div className="flex flex-col gap-1.5 text-[13.5px] leading-relaxed text-ink">
      {partRuns(parts).map((run, i) =>
        run.inline ? (
          <p key={i} className="break-words whitespace-pre-wrap">
            {run.parts.map((p, j) => <Inline key={j} p={p} />)}
          </p>
        ) : (
          <Block key={i} p={run.parts[0]} mid={mid} />
        ),
      )}
    </div>
  );
}

function Inline({ p }: { p: LarkPart }) {
  switch (p.kind) {
    case "at":
      return <span className="font-medium text-accent-ink">{p.text || "@"}</span>;
    case "link":
      return <LinkText url={p.url}>{p.text || p.url}</LinkText>;
    case "docs":
      return (
        <LinkText url={p.url}>
          <Icon name="read" size={12} className="mr-0.5 inline-block align-[-1px]" />
          {p.text || t("inbox-larkthread.doc")}
        </LinkText>
      );
    case "emotion":
      return <>{emojiLabel(p.refId ?? p.text ?? "", p.text)}</>;
    default:
      return <>{p.text}</>;
  }
}

function Block({ p, mid }: { p: LarkPart; mid?: string }) {
  const meta = p.metadata ?? {};
  const tools = useContext(Tools);
  const ol = useContext(ChatAccount);
  const manager = useItemManager();
  switch (p.kind) {
    case "image": {
      // a picture this page is sending shows its own preview
      const src = p.url?.startsWith("blob:") ? p.url : safeURL(p.url, manager);
      return src ? (
        <button type="button" onClick={() => (tools ? tools.openImage(src) : openMedia(src))} aria-label={t("inbox-larkthread.open_image")} className="w-fit">
          <img src={src} alt={t("inbox-larkthread.image")} loading="lazy" className="max-h-64 max-w-[min(280px,100%)] rounded-[8px] object-contain shadow-hairline" />
        </button>
      ) : <span className="text-ink-3">{t("inbox-larkthread.image_placeholder")}</span>;
    }
    case "emotion": {
      const src = safeURL(p.url, manager);
      return src ? <img src={src} alt={p.text || t("inbox-larkthread.sticker")} loading="lazy" className="size-20 object-contain" /> : <span>{emojiLabel(p.refId ?? "", p.text)}</span>;
    }
    case "file":
      return <FilePart p={p} />;
    case "media":
      return isVoice(p) ? <VoicePart p={p} /> : <VideoPart p={p} />;
    case "code":
      return <pre className="overflow-x-auto rounded-[8px] bg-inset px-3 py-2 font-mono text-[12px] leading-relaxed shadow-hairline">{p.text}</pre>;
    case "card": {
      // Cards the Lark gateway summarises itself (merge-forward, group / contact cards, meetings, votes…), by their variant.
      if (isSummaryCard(meta)) return <LarkSummaryCard meta={meta} fallbackTitle={p.text ?? ""} renderParts={(parts) => <Parts parts={parts} />} />;
      const c = cardText(meta);
      return (
        <div className="max-w-[460px] rounded-[10px] bg-surface px-3 py-2.5 shadow-card">
          {c.title && <p className="text-[13px] font-semibold text-ink">{c.title}</p>}
          {c.lines.map((l, i) => <p key={i} className="text-[12.5px] whitespace-pre-wrap text-ink-2">{l}</p>)}
          {!c.title && !c.lines.length && <p className="text-[12.5px] text-ink-3">{p.text || t("inbox-larkthread.card_placeholder")}</p>}
          <CardButtons buttons={cardButtons(meta)} messageID={mid && !mid.startsWith("temp-") ? mid : undefined} account={ol} onDone={tools?.refresh} />
        </div>
      );
    }
    case "interactive":
      return <hr className="border-line" />;
    default:
      return p.text ? <p className="break-words whitespace-pre-wrap">{p.text}</p> : null;
  }
}
