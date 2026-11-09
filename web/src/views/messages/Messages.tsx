// "Messages" (docs/extensions/lark-migration.md "Entry", #392's narrow exception): every Feishu private and group chat, for a browser
// that turned on Settings › Connectors › Lark › Show messages on home, on a hub that reads Feishu (feature `lark`). Not a second Feishu: one list and the inbox's own chat view
// (LarkThread: reading, sending, Pins), no contacts or workbench. Wide: the list (380px) and the chat side by side;
// narrow: the chat takes the list's place, with a way back. The pinned chats sit on top as a row of faces.
import { memo, useEffect, useMemo, useRef, useState } from "react";
import { shortTime } from "../../lib/inbox";
import { accountTitle, larkAccounts, useLarkAccounts, type LarkAccountsState } from "../../lib/lark-accounts";
import { HIDDEN_DISPLAY, summaryText, useLarkDisplayState, type LarkMessageDisplay } from "../../lib/lark-display";
import {
  arrangeConversations, conversationPreview, filterConversations, larkChatsFor, unreadBadge, useLarkChats, useLarkHome, type LarkChatsStore,
  type LarkConversation,
} from "../../lib/lark-chats";
import { isResumed } from "../../lib/reconnect";
import { href, navigate, onLinkClick } from "../../lib/router";
import { onHubEvent, useLarkOn } from "../../lib/store";
import { t } from "../../i18n";
import { Button, cx, IconButton, Loading, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Chip, Count, Monogram } from "../inbox/InboxParts";
import { LarkThread } from "../inbox/LarkThread";

const SETTINGS = "/settings?tab=connectors";

export function Messages({ id, onToggleSidebar }: { id?: string; onToggleSidebar: () => void }) {
  const lark = useLarkOn();
  const larkSwitch = useLarkHome();
  const on = lark && larkSwitch;
  const accounts = useLarkAccounts();
  // The chosen Feishu account's list (docs/extensions/feishu.md "Multiple accounts"): each account keeps its own.
  const larkChats = larkChatsFor(accounts.request);
  const s = useLarkChats(larkChats);
  const [pins, setPins] = useState(false);
  const chat = useMemo(() => (id ? [...s.pinned, ...s.rows].find((c) => c.id === id) : undefined), [id, s.pinned, s.rows]);

  useEffect(() => setPins(false), [id]);
  useEffect(() => {
    if (!on) return;
    void larkChats.refresh();
    void larkAccounts.refresh();
    // A chat changed (the hub's coalesced `lark` push): read the loaded window again (and the accounts' unread counts),
    // once things settle.
    let timer: ReturnType<typeof setTimeout> | undefined;
    const settle = () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        void larkChats.refresh();
        void larkAccounts.refresh();
      }, 800);
    };
    const off = onHubEvent("lark", (msg) => {
      if (Array.isArray(msg.feed_ids) && !msg.feed_ids.length) return;
      settle();
    });
    // A reconnect the hub could not resume: the `lark` pushes missed meanwhile are not replayed.
    const offHello = onHubEvent("hello", (msg) => !isResumed(msg) && settle());
    return () => {
      off();
      offHello();
      clearTimeout(timer);
    };
  }, [on, larkChats]);

  // Another account picked: an open chat of the account left behind closes (not when the list first arrives and puts
  // the page on the account chosen last time).
  const shownAccount = useRef(accounts.request);
  const listed = useRef(false);
  useEffect(() => {
    const switched = listed.current && shownAccount.current !== accounts.request;
    shownAccount.current = accounts.request;
    listed.current = accounts.accounts.length > 0;
    if (switched && id) navigate({ name: "messages" });
  }, [accounts.request, accounts.accounts.length, id]);

  if (!on) {
    return (
      <div className="flex h-full flex-col">
        <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
          <IconButton icon="sidebar" label={t("messages-messages.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
          <h1 className="min-w-0 flex-1 truncate text-[14px] font-semibold text-ink">{t("messages-messages.title")}</h1>
        </header>
        <div className="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center">
          <span className="flex size-10 items-center justify-center rounded-full bg-field text-ink-3"><Icon name="chat" size={18} /></span>
          {lark ? (
            <>
              <p className="text-[13.5px] font-medium text-ink">{t("messages-messages.off_title")}</p>
              <p className="max-w-[340px] text-[12.5px] text-ink-3">{t("messages-messages.off_hint")}</p>
              <Button tone="secondary" icon="gear" className="mt-2" onClick={() => navigate(SETTINGS)}>{t("messages-messages.goto_lark_settings")}</Button>
            </>
          ) : (
            <>
              <p className="text-[13.5px] font-medium text-ink">{t("messages-messages.no_lark_title")}</p>
              <p className="max-w-[340px] text-[12.5px] text-ink-3">{t("messages-messages.no_lark_hint")}</p>
            </>
          )}
        </div>
      </div>
    );
  }

  return (
    <div className="@container flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("messages-messages.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        {id && <IconButton icon="chevronLeft" label={t("messages-messages.back")} onClick={() => navigate({ name: "messages" })} className="@4xl:hidden" />}
        <h1 className="min-w-0 flex-1 truncate text-[14px] font-semibold text-ink">
          {id ? <><span className="@4xl:hidden">{chat?.name || t("messages-messages.lark_chat")}</span><span className="hidden @4xl:inline">{t("messages-messages.title")}</span></> : t("messages-messages.title")}
        </h1>
        {s.loading && s.loaded && <Spinner />}
        {id && <IconButton icon="pin" label={t("messages-messages.pin_messages")} onClick={() => setPins(true)} />}
        <IconButton icon="retry" label={t("messages-messages.refresh")} onClick={() => void larkChats.refresh()} disabled={s.loading} />
        <IconButton icon="gear" label={t("messages-messages.lark_settings")} onClick={() => navigate(SETTINGS)} />
      </header>
      <div className="flex min-h-0 flex-1">
        <section aria-label={t("messages-messages.chat_list")} className={cx("min-h-0 w-full flex-col @4xl:flex @4xl:w-[380px] @4xl:shrink-0 @4xl:border-r @4xl:border-line", id ? "hidden" : "flex")}>
          <ChatList selected={id} store={larkChats} accounts={accounts} />
        </section>
        <section aria-label={t("messages-messages.chat")} className={cx("min-h-0 min-w-0 flex-1 flex-col @4xl:flex", id ? "flex" : "hidden")}>
          {id ? (
            <div className="flex h-full min-h-0 flex-col">
              <ChatHeader chat={chat} account={accounts.selectedName} />
              <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
                <LarkThread key={`${accounts.request ?? ""}:${id}`} chat={{ feed: id, title: chat?.name || t("messages-messages.lark_chat"), topic: chat?.is_topic, account: accounts.request, group: chat?.is_group }}
                  pinsOpen={pins} onPinsOpen={setPins} />
              </div>
            </div>
          ) : (
            <div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center">
              <span className="flex size-10 items-center justify-center rounded-full bg-field text-ink-3"><Icon name="chat" size={18} /></span>
              <p className="text-[13.5px] font-medium text-ink">{t("messages-messages.pick_title")}</p>
              <p className="text-[12.5px] text-ink-3">{t("messages-messages.pick_hint")}</p>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}

/** The opened chat's name over it (wide only: narrow puts it in the page's title row); whose account it is while
 * more than one is signed in. */
function ChatHeader({ chat, account }: { chat?: LarkConversation; account?: string }) {
  if (!chat) return null;
  return (
    <div className="hidden shrink-0 items-center gap-3 border-b border-line px-4 py-2.5 @4xl:flex sm:px-6">
      <Monogram name={chat.name} size={28} src={chat.avatar} />
      <div className="min-w-0 flex-1">
        <p className="truncate text-[13.5px] font-semibold text-ink">{chat.name}</p>
        <p className="truncate text-[11.5px] text-ink-3">{[chat.is_topic ? t("messages-messages.kind_topic") : chat.is_group ? t("messages-messages.kind_group") : t("messages-messages.kind_dm"), account].filter(Boolean).join(" · ")}</p>
      </div>
    </div>
  );
}

function ChatList({ selected, store: larkChats, accounts }: { selected?: string; store: LarkChatsStore; accounts: LarkAccountsState }) {
  const display = useLarkDisplayState(accounts.request).snapshot ?? HIDDEN_DISPLAY;
  const s = useLarkChats(larkChats);
  const [query, setQuery] = useState("");
  const end = useRef<HTMLDivElement>(null);
  const { pinned, rest } = arrangeConversations(s.pinned, s.rows);
  const shown = filterConversations(query ? [...pinned, ...rest] : rest, query);

  // The next page as the list nears its end (not while searching: the search covers what is loaded).
  useEffect(() => {
    const el = end.current;
    if (!el || !s.cursor || query || s.moreProblem) return;
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && void larkChats.more(), { rootMargin: "240px" });
    io.observe(el);
    return () => io.disconnect();
  }, [s.cursor, query, s.moreProblem, s.loading, s.rows.length]);

  useEffect(() => {
    if (!selected) return;
    document.querySelector(`[data-chat="${CSS.escape(selected)}"]`)?.scrollIntoView({ block: "nearest" });
  }, [selected, s.loaded]);

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto max-w-[760px] px-3 pt-3 pb-16 sm:px-4">
        <label className="flex h-8 items-center gap-2 rounded-control bg-surface px-2.5 shadow-btn focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
          <Icon name="search" size={13} stroke={2} className="shrink-0 text-ink-3" />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("messages-messages.search_placeholder")} aria-label={t("messages-messages.search_label")} className="min-w-0 flex-1 bg-transparent text-[12.5px] text-ink outline-none placeholder:text-ink-3" />
          {query && <IconButton icon="x" label={t("messages-messages.clear")} size={20} iconSize={11} onClick={() => setQuery("")} />}
        </label>

        {accounts.accounts.length > 1 && (
          <div role="group" aria-label={t("messages-messages.accounts")} className="mt-2.5 flex gap-1.5 overflow-x-auto pb-0.5">
            {accounts.accounts.map((a) => (
              <Chip key={a.id} on={a.id === accounts.selected} onClick={() => larkAccounts.select(a.id)} title={a.tenant}>
                <span className="inline-flex items-center gap-1.5">
                  {accountTitle(a)}
                  {a.unread > 0 && <span className="text-[11px] tabular-nums opacity-70">{a.unread > 99 ? "99+" : a.unread}</span>}
                </span>
              </Chip>
            ))}
          </div>
        )}

        {s.problem && (
          <div className="mt-3 flex items-start gap-3 rounded-[14px] bg-surface px-3 py-3 shadow-card">
            <Icon name="info" size={14} className="mt-0.5 shrink-0 text-orange" />
            <p className="min-w-0 flex-1 text-[12.5px] leading-relaxed text-ink-2">{s.problem.text}</p>
            {s.problem.kind === "other" ? (
              <Button tone="secondary" icon="retry" busy={s.loading} onClick={() => void larkChats.refresh()}>{t("messages-messages.retry")}</Button>
            ) : (
              <Button tone="secondary" icon="gear" onClick={() => navigate(SETTINGS)}>{t("messages-messages.lark_settings")}</Button>
            )}
          </div>
        )}
        {!s.loaded && !s.rows.length && <div className="mt-6 px-1"><Loading label={t("messages-messages.loading")} /></div>}

        {!query && pinned.length > 0 && <PinnedShelf chats={pinned} selected={selected} />}

        {s.loaded && !shown.length && !s.problem && (
          <div className="mt-10 flex flex-col items-center gap-2 text-center">
            <span className="flex size-10 items-center justify-center rounded-full bg-field text-ink-3"><Icon name={query ? "search" : "chat"} size={17} /></span>
            <p className="text-[13.5px] font-medium text-ink">{query ? t("messages-messages.empty_no_match") : t("messages-messages.empty_none")}</p>
            <p className="text-[12.5px] text-ink-3">{query ? t("messages-messages.empty_no_match_hint") : t("messages-messages.empty_none_hint")}</p>
          </div>
        )}

        {shown.length > 0 && (
          <div className="mt-3 flex flex-col gap-1.5">
            {shown.map((c) => <ChatRow key={c.id} c={c} selected={c.id === selected} display={display} time={shortTime(c.updated_at)} />)}
          </div>
        )}
        {!query && s.moreProblem && (
          <div className="mt-3 flex items-center gap-3 px-1">
            <p className="min-w-0 flex-1 truncate text-[12px] text-red">{s.moreProblem}</p>
            <Button tone="secondary" icon="retry" onClick={() => void larkChats.more()}>{t("messages-messages.retry")}</Button>
          </div>
        )}
        {!query && s.cursor && !s.moreProblem && <div ref={end} className="flex justify-center py-4"><Spinner /></div>}
      </div>
    </div>
  );
}

/** "Pinned": the pinned chats, a row of faces with their names and unread counts (the Lark gateway's order). */
function PinnedShelf({ chats, selected }: { chats: LarkConversation[]; selected?: string }) {
  return (
    <section aria-label={t("messages-messages.pinned_label")} className="mt-3">
      <p className="px-1 pb-1.5 text-[12px] font-medium text-ink-3">{t("messages-messages.pinned")} <span className="text-[11.5px] tabular-nums">{chats.length}</span></p>
      <div className="flex gap-1 overflow-x-auto pb-1">
        {chats.map((c) => {
          const to = { name: "messages", id: c.id } as const;
          const b = unreadBadge(c);
          return (
            <a
              key={c.id}
              data-chat={c.id}
              href={href(to)}
              onClick={(e) => onLinkClick(e, to)}
              aria-current={c.id === selected ? "page" : undefined}
              title={c.name}
              className={cx("flex w-[64px] shrink-0 flex-col items-center gap-1 rounded-[12px] px-1 py-2 transition-colors hover:bg-hover", c.id === selected && "bg-inset shadow-[0_0_0_1.5px_var(--accent)]")}
            >
              <span className="relative">
                <Monogram name={c.name} size={40} src={c.avatar} />
                {(b.count || b.dot) && (
                  <span className="absolute -top-1 -right-1">
                    {b.count ? <Count n={b.count} accent={!b.muted} /> : <span className={cx("block size-2.5 rounded-full ring-2 ring-page", b.muted ? "bg-ink-3" : "bg-accent")} />}
                  </span>
                )}
              </span>
              <span className="w-full truncate text-center text-[11.5px] text-ink-2">{c.name}</span>
            </a>
          );
        })}
      </div>
    </section>
  );
}

/** One chat of the list. It draws again only when its props change: a refresh keeps the object of a chat that did not
 * change (LarkChatsStore), and the list works out the time label each time it draws, so a new day still moves it on. */
const ChatRow = memo(function ChatRow({ c, selected, display, time }: { c: LarkConversation; selected: boolean; display: LarkMessageDisplay; time: string }) {
  const to = { name: "messages", id: c.id } as const;
  const b = unreadBadge(c);
  const preview = conversationPreview({ ...c, preview: c.preview && summaryText(display, c.preview) });
  return (
    <a
      data-chat={c.id}
      href={href(to)}
      onClick={(e) => onLinkClick(e, to)}
      aria-current={selected ? "page" : undefined}
      className={cx("flex scroll-my-3 gap-3 rounded-[14px] p-3 outline-offset-[-2px] transition-[background-color,box-shadow] duration-150 hover:bg-inset", selected ? "bg-inset shadow-[0_0_0_1.5px_var(--accent)]" : "bg-surface shadow-card")}
    >
      <Monogram name={c.name} size={40} src={c.avatar} />
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-center gap-1.5">
          <span className={cx("truncate text-[13.5px]", b.count || b.dot ? "font-semibold text-ink" : "font-medium text-ink")}>{c.name}</span>
          {c.is_group && <Icon name="layers" size={11} className="shrink-0 text-ink-3" />}
          {c.muted && <Icon name="bellOff" size={11} className="shrink-0 text-ink-3" />}
          <span className="flex-1" />
          <span className="shrink-0 text-[11.5px] text-ink-3 tabular-nums">{time}</span>
        </span>
        <span className="mt-0.5 flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink-2">
            {c.at_me && <span className="mr-1 font-medium text-attention">{c.at_all ? t("messages-messages.at_all") : t("messages-messages.at_me")}</span>}
            {preview}
          </span>
          {b.count ? <Count n={b.count} accent={!b.muted} /> : b.dot ? <span className={cx("size-2 shrink-0 rounded-full", b.muted ? "bg-ink-3" : "bg-accent")} /> : null}
        </span>
      </span>
    </a>
  );
});
