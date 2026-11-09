// The inbox list (docs/assistant/inbox.md"App"): filters by source and kind, search, then Needs you (agents waiting on you first),
// FYI, Collapsed and Done, each a section that opens / closes from its title.
import { Fragment, memo, useEffect, useMemo, useState } from "react";
import {
  agentDraft, agentWorking, conversationRoute, fromLine, interactionAgentTitle, interactionAsk, interactionTitle, isDone, isLark,
  itemKey, itemRoute, listInboxMutes, muteItem, muteLine, muteShortName, muteTitle, policyMenuTitle, sameItem, unmuteInbox,
  BULK_CONFIRM_AT, bulkScope, bulkScopeName, connectorSources, KIND_FILTERS, kindLabel, labelIsUrgent, mailAccounts, mailShort, reloadInbox, sections, sendReply, setAllDone, setDone, shortTime, TIERS,
  useInbox, waitingAgents, type InboxItem, type InboxMute, type SectionKey,
} from "../../lib/inbox";
import { useMailAccounts } from "../../lib/mail";
import { hubTranslation, useTranslatePrefs } from "../../lib/translate";
import { machineWithHub } from "../../lib/mesh";
import { href, navigate, onLinkClick } from "../../lib/router";
import { useInboxSources } from "../../lib/inbox-sources";
import { managerOrder, multiManager, ownerIn, useRoster, type Manager } from "../../lib/managers";
import { useStore } from "../../lib/store";
import { ManagerHeading } from "../ManagerParts";
import type { Interaction } from "../../lib/types";
import { t } from "../../i18n";
import { requestRoute } from "../../lib/card-focus";
import { Button, Collapse, cx, Dialog, IconButton, Loading, Menu, toast, useMenu, type MenuItem } from "../../ui/atoms";
import { errorText } from "../../lib/api";
import { contextMenuProps } from "../../ui/context-menu";
import type { MenuAt } from "../../ui/menu-model";
import { Icon } from "../../ui/icons";
import { openPolicyDialog, RulesDialog } from "./PolicyDialog";
import { isNoHover } from "../../ui/media-query";
import { DraftDialog } from "./InboxDialogs";
import { TriageBanner } from "./TriageBanner";
import { EventMark } from "./InboxEvent";
import { AgentWorking, Chip, Count, DraftCard, Glyph, ItemAvatar, Spark, StatusMenu, Tag, type GlyphName } from "./InboxParts";

function useStored(key: string): [string, (v: string) => void] {
  const [v, setV] = useState(() => localStorage.getItem(key) ?? "");
  return [v, (x: string) => {
    setV(x);
    if (x) localStorage.setItem(key, x);
    else localStorage.removeItem(key);
  }];
}

/** The sections open at first (in every manager's group): Needs you and FYI. */
const OPEN_AT_FIRST = new Set(["action", "fyi"]);

/** `selected`: the item opened (its id and manager). */
export function InboxList({ selected }: { selected?: { id: string; manager?: string } }) {
  const items = useInbox((s) => s.items);
  const loaded = useInbox((s) => s.loaded);
  const loading = useInbox((s) => s.loading);
  const error = useInbox((s) => s.error);
  const triage = useInbox((s) => s.triage);
  const storeLoaded = useStore((s) => s.loaded);
  // The waiting agents come with the store, often after the items: painting the items first and then the agents over
  // them pushed every row down (a layout shift of 600px on a phone), so the list waits for both and paints once.
  const ready = loaded && storeLoaded;
  const [source, setSource] = useStored("inbox.source");
  const [kind, setKind] = useStored("inbox.kind");
  const [query, setQuery] = useState("");
  // The agents waiting, picked in the store: the list holds the store's own interaction objects, so a sessions or
  // machines push that leaves them as they were keeps the same array (useStore compares it item by item) and the list,
  // with every row in it, does not render again.
  const agents = useStore((st) => waitingAgents(st.interactions, st.sessions, st.mutes, { source, kind, query }));
  // the sections opened or closed by hand, by "<manager group>:<section>" (each manager's group keeps its own; one
  // manager: the group ""), the others as they are at first
  const [toggled, setToggled] = useState<Set<string>>(() => new Set());
  const isOpen = (group: string, k: string) => OPEN_AT_FIRST.has(k) !== toggled.has(`${group}:${k}`);
  const [editing, setEditing] = useState<InboxItem | null>(null);
  const accounts = useMailAccounts();
  const more = useMenu();
  const [mutesOpen, setMutesOpen] = useState(false);
  const [rulesOpen, setRulesOpen] = useState(false);
  // Clean up all over many items asks first; the items are the ones shown when it was asked.
  const [confirm, setConfirm] = useState<{ items: InboxItem[]; scope: string } | null>(null);

  // The opened item stays in sight in the list (a link straight to it, a click in the detail).
  const selectedKey = selected ? itemKey(selected) : "";
  useEffect(() => {
    if (!selected) return;
    const rows = [...document.querySelectorAll<HTMLElement>(`[data-item="${CSS.escape(selected.id)}"]`)];
    (rows.find((el) => sameItem({ id: selected.id, manager: el.dataset.manager }, selected)) ?? rows[0])?.scrollIntoView({ block: "nearest" });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedKey, loaded, storeLoaded]);

  const s = useMemo(() => sections(items, { source, kind, query }), [items, source, kind, query]);
  // with several managers, each one's items and waiting agents under its name, sectioned as before (docs/assistant/managers.md
  // "Clients"); one: the list as it was
  const roster = useRoster();
  const byManager = useMemo(() => {
    if (!multiManager(roster)) return null;
    return managerOrder(roster)
      .map((m) => ({
        manager: m,
        sec: sections(items.filter((it) => ownerIn(roster, it.manager) === m.id), { source, kind, query }),
        ags: agents.filter((ix) => ownerIn(roster, ix.manager) === m.id),
      }))
      .filter((g) => g.ags.length || g.sec.done.length || TIERS.some((tier) => g.sec.tiers[tier.key].length));
  }, [roster, items, agents, source, kind, query]);
  const chips = mailAccounts(accounts, items);
  const connectors = useMemo(() => connectorSources(items), [items]);
  const failed = Boolean(error) && !items.length;
  const empty = !s.tiers.action.length && !s.tiers.working.length && !s.tiers.fyi.length && !s.tiers.noise.length && !agents.length;
  // nothing ever came in, nothing filtered: is anything connected at all? (still unknown: the plain "All caught up")
  const sources = useInboxSources(ready && empty && !failed && !items.length && !query && !source && !kind);
  const unconnected = sources && !sources.connected ? sources : null;
  // Clean up all: the open items the list shows (its filters and search); the agents waiting are not items.
  const openInView = kind === "agent" ? [] : [...s.tiers.action, ...s.tiers.working, ...s.tiers.fyi, ...s.tiers.noise];
  const filterNames = [
    source === "feishu" ? t("inbox-inboxlist.source_lark") : connectors.find((c) => c.id === source)?.name ?? mailShort(chips.find((a) => a.id === source)?.email ?? ""),
    KIND_FILTERS.find((k) => k.key === kind)?.label ?? "",
  ];
  const scope = bulkScope(filterNames, query, openInView.length);
  // Clean up all files them as Dismiss (dismiss): the user set them aside, they did not handle them.
  const runBulk = () => {
    if (!openInView.length) return;
    if (openInView.length >= BULK_CONFIRM_AT) setConfirm({ items: openInView, scope: bulkScopeName(filterNames, query) });
    else void setAllDone(openInView, "dismiss");
  };
  const toggle = (group: string, k: string) => setToggled((o) => {
    const n = new Set(o);
    const at = `${group}:${k}`;
    if (n.has(at)) n.delete(at);
    else n.add(at);
    return n;
  });
  // Agents have no source: picking one leaves the Agent filter, and the Agent filter leaves the source.
  const pickSource = (v: string) => {
    setSource(source === v ? "" : v);
    if (kind === "agent") setKind("");
  };
  const pickKind = (v: string) => {
    setKind(kind === v ? "" : v);
    if (v === "agent") setSource("");
  };

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto max-w-[760px] px-3 pt-3 pb-16 sm:px-4">
        <label className="flex h-8 items-center gap-2 rounded-control bg-surface px-2.5 shadow-btn focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
          <Icon name="search" size={13} stroke={2} className="shrink-0 text-ink-3" />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("inbox-inboxlist.search_placeholder")} aria-label={t("inbox-inboxlist.search_label")} className="min-w-0 flex-1 bg-transparent text-[12.5px] text-ink outline-none placeholder:text-ink-3" />
          {query && <IconButton icon="x" label={t("inbox-inboxlist.clear")} size={20} iconSize={11} onClick={() => setQuery("")} />}
        </label>
        {/* A phone: the chips in one row that scrolls sideways, Clean up all in the ⋯ menu at its end. */}
        <div className="mt-2.5 flex items-center gap-1.5">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5 max-sm:-my-1 max-sm:flex-nowrap max-sm:overflow-x-auto max-sm:py-1 max-sm:scrollbar-none max-sm:[&>*]:shrink-0" role="toolbar" aria-label={t("inbox-inboxlist.filters")}>
          <Chip on={!source && !kind} onClick={() => { setSource(""); setKind(""); }}>{t("inbox-inboxlist.all")}</Chip>
          <Chip on={source === "feishu"} onClick={() => pickSource("feishu")}>{t("inbox-inboxlist.source_lark")}</Chip>
          {chips.map((a) => (
            <Chip key={a.id} on={source === a.id} onClick={() => pickSource(a.id)} title={a.email}>{mailShort(a.email)}</Chip>
          ))}
          {connectors.map((c) => (
            <Chip key={`c:${c.id}`} on={source === c.id} onClick={() => pickSource(c.id)}>{c.name}</Chip>
          ))}
          <span aria-hidden className="mx-0.5 h-4 w-px bg-line-strong" />
          {KIND_FILTERS.map((k) => (
            <Chip key={k.key} on={kind === k.key} onClick={() => pickKind(k.key)}>{k.label}</Chip>
          ))}
          <button
            type="button"
            disabled={!openInView.length}
            title={t("inbox-inboxlist.clean_all_scope", { scope })}
            onClick={runBulk}
            className="ml-auto inline-flex h-7 items-center gap-1.5 rounded-control bg-surface px-2.5 text-[12px] whitespace-nowrap text-ink-2 shadow-btn transition-colors duration-150 hover:bg-hover hover:text-ink disabled:pointer-events-none disabled:opacity-45 max-sm:hidden"
          >
            <Icon name="check" size={12} stroke={2.6} />
            {t("inbox-inboxlist.clean_all")}
            <span className="text-ink-3">{scope}</span>
          </button>
        </div>
        <IconButton icon="more" label={t("inbox-inboxlist.more")} onClick={more.open} active={Boolean(more.anchor)} />
        </div>
        {more.anchor && (
          <Menu anchor={more.anchor} onClose={more.close} align="right" width={240}
            items={[
              { key: "clean-all", label: t("inbox-inboxlist.clean_all"), hint: scope, icon: "check", disabled: !openInView.length, onSelect: runBulk },
              { key: "muted", label: t("inbox-inboxlist.muted_list"), icon: "bellOff", divider: true, onSelect: () => setMutesOpen(true) },
              { key: "rules", label: t("inbox-inboxlist.rules_list"), icon: "target", onSelect: () => setRulesOpen(true) },
            ]} />
        )}
        {mutesOpen && <MutesDialog onClose={() => setMutesOpen(false)} />}
        {rulesOpen && <RulesDialog onClose={() => setRulesOpen(false)} />}
        <BulkConfirm confirm={confirm} onClose={() => setConfirm(null)} />

        <TriageBanner triage={triage} />
        {/* never loaded (an error and no items): its own empty state with "Retry", not "All caught up" under the error (#2970);
            with agents waiting the list is not empty, so the reason stays a line over it */}
        {ready && failed && !empty && <p className="mt-4 text-[12.5px] text-red">{error}</p>}
        {!ready && <div className="mt-6 px-1"><Loading /></div>}
        {ready && failed && empty && (
          <div className="mt-10 flex flex-col items-center gap-2 text-center" role="status" data-inbox-failed>
            <span className="flex size-10 items-center justify-center rounded-full bg-field text-ink-3">
              <Icon name="inbox" size={17} />
            </span>
            <p className="text-[13.5px] font-medium text-ink">{t("inbox-inboxlist.failed_title")}</p>
            <p className="max-w-80 text-[12.5px] text-ink-3">{t("inbox-inboxlist.failed_hint")}</p>
            <p className="max-w-80 text-[11.5px] break-words text-ink-3">{error}</p>
            <Button tone="secondary" icon="retry" busy={loading} onClick={() => void reloadInbox()} className="mt-2">
              {loading ? t("inbox-inboxlist.retrying") : t("inbox-inboxlist.retry")}
            </Button>
          </div>
        )}
        {ready && empty && !failed && unconnected && (
          <div className="mt-10 flex flex-col items-center gap-2 text-center" data-inbox-no-sources>
            <span className="flex size-10 items-center justify-center rounded-full bg-field text-ink-3">
              <Icon name="inbox" size={17} />
            </span>
            <p className="text-[13.5px] font-medium text-ink">{t("inbox-inboxlist.no_sources_title")}</p>
            <p className="max-w-80 text-[12.5px] leading-relaxed text-ink-3">{t("inbox-inboxlist.no_sources_hint")}</p>
            <div className="mt-2 flex flex-wrap justify-center gap-1.5">
              {unconnected.offers.map((o, i) => (
                <Button key={o.id} tone={i === 0 ? "primary" : "secondary"} icon={i === 0 ? "plus" : undefined} onClick={() => navigate(o.href)}>
                  {t("inbox-inboxlist.no_sources_connect", { name: o.name })}
                </Button>
              ))}
            </div>
          </div>
        )}
        {ready && empty && !failed && !unconnected && (
          <div className="mt-10 flex flex-col items-center gap-2 text-center">
            <span className="flex size-10 items-center justify-center rounded-full bg-green-tint text-green">
              <Icon name="check" size={17} stroke={2.6} />
            </span>
            <p className="text-[13.5px] font-medium text-ink">{query ? t("inbox-inboxlist.empty_no_match") : t("inbox-inboxlist.empty_all_done")}</p>
            <p className="text-[12.5px] text-ink-3">{query ? t("inbox-inboxlist.empty_no_match_hint") : t("inbox-inboxlist.empty_all_done_hint")}</p>
          </div>
        )}

        {(byManager ?? [{ manager: null, sec: s, ags: agents }]).map(({ manager, sec, ags }) => {
          const group = manager?.id ?? "";
          const isSelected = (it: InboxItem) => !!selected && sameItem(it, selected);
          return (
            <Fragment key={group}>
              {manager && <ManagerHeading manager={manager} className="mt-5 px-1" />}
              {ready && TIERS.map((tier) => {
                const list = sec.tiers[tier.key];
                const waiting = tier.key === "action" ? ags : [];
                if (!list.length && !waiting.length) return null;
                const clear = tier.key === "fyi" ? t("inbox-inboxlist.clear_fyi") : tier.key === "noise" ? t("inbox-inboxlist.clear_noise") : undefined;
                return (
                  <Section key={tier.key} k={tier.key} title={tier.title} count={sectionCount(list.length, waiting.length)} open={tier.key === "working" || isOpen(group, tier.key)} onToggle={tier.key === "working" ? undefined : () => toggle(group, tier.key)}
                    action={clear && list.length ? { label: clear, run: () => setAllDone(list, true) } : undefined}>
                    {waiting.map((ix) => <AgentRow key={ix.id} ix={ix} />)}
                    {list.map((it) => <ItemRow key={itemKey(it)} it={it} selected={isSelected(it)} showAccount={chips.length > 1} onEdit={setEditing} />)}
                  </Section>
                );
              })}
              {ready && sec.done.length > 0 && (
                <Section k="done" title={t("inbox-inboxlist.done")} count={sectionCount(sec.done.length, 0)} open={isOpen(group, "done")} onToggle={() => toggle(group, "done")}>
                  {sec.done.map((it) => <ItemRow key={itemKey(it)} it={it} selected={isSelected(it)} showAccount={chips.length > 1} onEdit={setEditing} />)}
                </Section>
              )}
            </Fragment>
          );
        })}
        {loading && loaded && <span className="sr-only">{t("inbox-inboxlist.refreshing")}</span>}
      </div>
      {editing && <DraftDialog item={editing} onClose={() => setEditing(null)} />}
    </div>
  );
}

/**
 * A section title's count, saying what it counts (#3288): the sidebar's "Inbox" badge counts messages only, while "Needs you"
 * also lists the agents waiting on you (those are "Overview"'s count), so the two are written apart: "4 messages · 7 agents", each
 * with its unit, which a phone has no hover to show (#4146).
 */
export function sectionCount(items: number, agents: number): { text: string; label: string } {
  const parts = [items > 0 && t("inbox-inboxlist.count_items", { count: items }), agents > 0 && t("inbox-inboxlist.count_agents", { count: agents })].filter((x): x is string => Boolean(x));
  return {
    text: items > 0 && agents > 0 ? `${t("inbox-inboxlist.count_items_short", { count: items })} · ${t("inbox-inboxlist.count_agents", { count: agents })}` : agents > 0 ? t("inbox-inboxlist.count_agents", { count: agents }) : String(items),
    label: parts.join(t("inbox-inboxlist.count_separator")),
  };
}

/** A section of the list; without `onToggle` it is always open (In progress: what the AI is on never hides). */
function Section({ k, title, count, open, onToggle, action, children }: { k: SectionKey | "done"; title: string; count: { text: string; label: string }; open: boolean; onToggle?: () => void; action?: { label: string; run: () => void }; children: React.ReactNode }) {
  const head = (
    <>
      <span className={cx("text-[12px] font-medium whitespace-nowrap", k === "action" ? "text-ink" : "text-ink-3")} data-section-title>{title}</span>
      <span title={count.label} className="text-[11.5px] whitespace-nowrap text-ink-3 tabular-nums"><span aria-hidden>{count.text}</span><span className="sr-only">{count.label}</span></span>
    </>
  );
  return (
    <section className="mt-4" data-section={k}>
      <div className="flex items-center gap-2 px-1 pb-1.5">
        {onToggle ? (
          <button type="button" onClick={onToggle} aria-expanded={open} className="flex min-w-0 flex-1 items-center gap-1.5 text-left">
            {head}
            <Icon name="chevronRight" size={11} stroke={2.4} className="text-ink-3 transition-transform duration-200" style={{ transform: open ? "rotate(90deg)" : undefined }} />
          </button>
        ) : (
          <h3 className="flex min-w-0 flex-1 items-center gap-1.5">{head}</h3>
        )}
        {action && (
          <button type="button" onClick={action.run} className="h-6 rounded-[6px] px-1.5 text-[11.5px] whitespace-nowrap text-ink-3 transition-colors hover:bg-hover hover:text-ink">
            {action.label}
          </button>
        )}
      </div>
      <Collapse open={open}>
        <div inert={!open} className="flex flex-col gap-1.5 pb-0.5">{children}</div>
      </Collapse>
    </section>
  );
}

/** Room for the row's "Done" check, always shown on a touch screen (#4141), right of the time. */
const TOUCH_DONE_ROOM = "[@media(hover:none)]:mr-8";

/** The row's "Done" / "Move back": on a touch screen marking done says so with "Undo", since a tap there can be a slip. */
export async function markFromRow(it: InboxItem, done: boolean, touch = isNoHover()): Promise<void> {
  // after the request: "Undo" cannot race it, and a failure shows its own error instead of this
  const ok = await setDone(it, done);
  if (ok && done && touch) toast(t("inbox-inboxlist.marked_done"), "ok", { label: t("lib-inbox.undo"), run: () => void setDone(it, false) });
}

/** Memoized: a push that leaves this item's object as it was (or one for sessions and machines) skips the row. */
const ItemRow = memo(function ItemRow({ it, selected, showAccount, onEdit }: { it: InboxItem; selected: boolean; showAccount: boolean; onEdit: (it: InboxItem) => void }) {
  const to = itemRoute(it);
  const done = isDone(it);
  const label = kindLabel(it);
  // a suggestion takes the AI twin's draft's place, and the event's handling the AI twin's "working on it"
  const draft = !done && !it.suggestion && agentDraft(it);
  const working = !done && !it.handling && agentWorking(it);
  const mark = done ? (it.done_by === "manager" ? <EventMark it={it} /> : null) : it.suggestion || it.handling ? <EventMark it={it} /> : null;
  const rows = !done ? it.doc?.rows ?? [] : [];
  const first = rows[0];
  const translated = hubTranslation(it, useTranslatePrefs());
  const from = fromLine(it, translated?.preview);
  // a right click: "Done" / "Move back", then what it can be muted by (iOS: the row's long press)
  // (the menu key / Shift+F10 open it next to the row, see ui/context-menu.ts)
  const [menu, setMenu] = useState<MenuAt | null>(null);
  return (
    <div data-item={it.id} data-manager={it.manager} {...contextMenuProps((at) => setMenu(at))} className={cx("group relative scroll-my-3 rounded-[14px] transition-[background-color,box-shadow] duration-150 hover:bg-inset", selected ? "bg-inset shadow-[0_0_0_1.5px_var(--accent)]" : "bg-surface shadow-card")}>
      <a href={href(to)} onClick={(e) => onLinkClick(e, to)} aria-current={selected ? "page" : undefined} className="flex gap-3 rounded-[14px] p-3 outline-offset-[-2px]">
        <ItemAvatar it={it} />
        <span className="min-w-0 flex-1">
          <span className="flex min-w-0 items-center gap-1.5">
            <span className={cx("truncate text-[13.5px]", done ? "text-ink-2" : "font-semibold text-ink")}>{translated?.subject || it.title}</span>
            {translated && <span role="img" aria-label={t("inbox-inboxlist.translated")} title={t("inbox-inboxlist.translated")} className="shrink-0 text-ink-3"><Icon name="translate" size={12} /></span>}
            {label && <Tag urgent={labelIsUrgent(it)}>{label}</Tag>}
            <span className="flex-1" />
            <span className={cx("shrink-0 text-[11.5px] text-ink-3 tabular-nums group-focus-within:invisible group-hover:invisible", TOUCH_DONE_ROOM)}>{shortTime(it.updated_at)}</span>
          </span>
          {/* on a touch screen the check's 44 pt tap area reaches this line: keep the summary and count clear of it */}
          <span className="mt-0.5 flex items-start gap-2 [@media(hover:none)]:pr-8">
            <span className="min-w-0 flex-1">
              {it.summary ? (
                <>
                  <span className={cx("line-clamp-2 text-[12.5px] leading-[1.5]", done ? "text-ink-2" : "text-ink")}><Spark />{it.summary}</span>
                  <span className="mt-0.5 block truncate text-[11.5px] text-ink-2">{from}</span>
                </>
              ) : (
                <span className="line-clamp-2 text-[12.5px] leading-[1.5] text-ink-2">{from}</span>
              )}
              {showAccount && !isLark(it) && !it.connector && <span className="mt-0.5 block truncate text-[11px] text-ink-3">{it.account_name}</span>}
            </span>
            {it.count > 1 && !done && <Count n={it.count} accent={it.kind === "mention"} />}
          </span>
        </span>
      </a>
      {(first || draft || working || mark) && (
        <div className="-mt-1 flex flex-col gap-2 pr-3 pb-3 pl-[64px]">
          {first && (
            <div className="flex min-w-0 items-center gap-1.5 text-[11.5px] text-ink-2">
              <Glyph name="reply" size={12} className="shrink-0 -scale-y-100 text-ink-3" />
              <span className="min-w-0 truncate">{first.title}</span>
              {first.status?.[0] && <StatusMenu it={it} row={first} status={first.status[0]} />}
              {rows.length > 1 && <span className="shrink-0 text-ink-3">{t("inbox-inboxlist.more_rows", { count: rows.length - 1 })}</span>}
            </div>
          )}
          {draft && <DraftCard it={it} onEdit={() => onEdit(it)} onSend={() => void sendReply(it, draft, it.agent?.changes ?? [])} />}
          {working && <AgentWorking it={it} />}
          {mark}
        </div>
      )}
      {/* Shown on hover with a mouse; on a touch screen (no hover, #4141) always there as a check beside the time, so an
          item is marked without opening it, with "Undo" in the toast in case the tap was a slip. */}
      <button
        type="button"
        onClick={() => void markFromRow(it, !done)}
        aria-label={done ? t("inbox-inboxlist.move_back_title") : t("inbox-inboxlist.mark_done_title")}
        className="touch-hit absolute top-2.5 right-2.5 flex h-6 items-center gap-1 rounded-[7px] bg-surface px-2 text-[11.5px] font-medium whitespace-nowrap text-ink-2 opacity-0 shadow-btn transition-opacity duration-150 group-focus-within:opacity-100 group-hover:opacity-100 hover:text-ink focus-visible:opacity-100 [@media(hover:none)]:w-6 [@media(hover:none)]:justify-center [@media(hover:none)]:px-0 [@media(hover:none)]:opacity-100"
        title={done ? t("inbox-inboxlist.move_back_title") : t("inbox-inboxlist.mark_done_title")}
      >
        {done ? <Glyph name="undo" size={12} stroke={2.2} /> : <Icon name="check" size={12} stroke={2.6} />}
        <span className="[@media(hover:none)]:hidden">{done ? t("inbox-inboxlist.move_back") : t("inbox-inboxlist.done")}</span>
      </button>
      {menu && <Menu anchor={menu.anchor ?? null} point={menu.point} onClose={() => setMenu(null)} width={224} items={rowMenu(it, done)} />}
    </div>
  );
});

/** A row's menu: "Done" (or "Move back"), then "Mute this repository / chat / sender" with the name under it (iOS InboxView.rowMenu). */
export function rowMenu(it: InboxItem, done: boolean): MenuItem[] {
  const items: MenuItem[] = [
    done
      ? { key: "undone", label: t("inbox-inboxlist.move_back"), icon: "inbox", onSelect: () => void setDone(it, false) }
      : { key: "done", label: t("inbox-inboxlist.done"), icon: "check", onSelect: () => void markFromRow(it, true, true) }, // a menu pick: "Undo" on any screen (#4162)
  ];
  (it.mutes ?? []).forEach((m, i) => items.push({ key: `mute:${m.tag}:${m.value}`, label: muteTitle(m), hint: muteShortName(m), icon: "bellOff", divider: i === 0, onSelect: () => void muteItem(it, m) }));
  // then a rule for each (docs/assistant/inbox.md "Policies"): the tier its items get, what the AI twin does with them
  (it.mutes ?? []).forEach((m, i) => items.push({ key: `policy:${m.tag}:${m.value}`, label: policyMenuTitle(m), hint: muteShortName(m), icon: "target", divider: i === 0, onSelect: () => openPolicyDialog({ item: it, option: m, source: it.source }) }));
  return items;
}

/** One manager's mutes in the "Muted" dialog (several managers: each keeps its own, read with its name). */
type MuteGroup = { manager: Manager; mutes: InboxMute[] | null; error: string };

/** Each installed manager's mutes, the default first; one that can't answer (offline) says why under its name. */
async function mutesByManager(managers: Manager[]): Promise<MuteGroup[]> {
  const read = await Promise.allSettled(managers.map((m) => listInboxMutes(m.id)));
  return managers.map((m, i) => {
    const r = read[i];
    return r.status === "fulfilled" ? { manager: m, mutes: r.value, error: "" } : { manager: m, mutes: null, error: errorText(r.reason) };
  });
}

/** "Muted" (the ⋯ menu; iOS InboxMutesView): every source's muted repositories, chats and senders, newest first, each
 *  with "Unmute". Only what comes in later comes back. With several managers, each one's under its name. */
function MutesDialog({ onClose }: { onClose: () => void }) {
  const roster = useRoster();
  const managers = multiManager(roster) ? managerOrder(roster) : null;
  const [list, setList] = useState<InboxMute[] | null>(null);
  const [groups, setGroups] = useState<MuteGroup[] | null>(null);
  const [failed, setFailed] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const several = managers?.map((m) => m.id).join(",") ?? "";
  const load = () => {
    setFailed("");
    if (managers) void mutesByManager(managers).then(setGroups);
    else listInboxMutes().then(setList, (e) => setFailed(errorText(e)));
  };
  useEffect(load, [several]);
  const unmute = async (m: InboxMute) => {
    const key = `${m.manager ?? ""}:${m.id}`;
    setBusy(key);
    try {
      await unmuteInbox(m);
      setList((l) => (l ?? []).filter((x) => x.id !== m.id));
      setGroups((gs) => gs?.map((g) => (g.manager.id === m.manager ? { ...g, mutes: g.mutes?.filter((x) => x.id !== m.id) ?? null } : g)) ?? gs);
      toast(t("inbox-inboxlist.unmuted", { name: m.name }), "ok");
    } catch (e) {
      toast(t("inbox-inboxlist.unmute_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(null);
    }
  };
  const row = (m: InboxMute) => {
    const key = `${m.manager ?? ""}:${m.id}`;
    return (
      <li key={key} data-mute={m.id} className="flex items-center gap-3 rounded-control px-1 py-2 hover:bg-hover">
        <Icon name="bellOff" size={14} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[13px] text-ink" title={m.name}>{m.name}</span>
          <span className="mt-0.5 block truncate text-[11.5px] text-ink-3">{muteLine(m)}</span>
        </span>
        <Button tone="quiet" busy={busy === key} disabled={busy !== null && busy !== key} onClick={() => void unmute(m)}>
          {t("inbox-inboxlist.unmute")}
        </Button>
      </li>
    );
  };
  // several managers: the groups with something to say (mutes, or why they could not be read)
  const shownGroups = groups?.filter((g) => g.error || g.mutes?.length) ?? null;
  const empty = managers ? shownGroups?.length === 0 : list?.length === 0;
  return (
    <Dialog open onClose={onClose} title={t("inbox-inboxlist.muted_list")} width={420} footer={<Button tone="primary" onClick={onClose}>{t("inbox-inboxlist.close")}</Button>}>
      {failed ? (
        <div className="flex flex-col items-start gap-2">
          <p className="text-[12.5px] break-words text-red">{t("inbox-inboxlist.mutes_load_failed", { error: failed })}</p>
          <Button tone="secondary" icon="retry" onClick={load}>{t("inbox-inboxlist.retry")}</Button>
        </div>
      ) : (managers ? !groups : !list) ? (
        <div className="py-2"><Loading /></div>
      ) : empty ? (
        <div className="flex flex-col items-center gap-2 py-4 text-center">
          <span className="flex size-10 items-center justify-center rounded-full bg-field text-ink-3">
            <Icon name="bellOff" size={17} />
          </span>
          <p className="text-[13.5px] font-medium text-ink">{t("inbox-inboxlist.muted_empty_title")}</p>
          <p className="max-w-[300px] text-[12.5px] leading-relaxed text-ink-3">{t("inbox-inboxlist.muted_empty_hint")}</p>
        </div>
      ) : (
        <>
          {shownGroups ? (
            shownGroups.map((g) => (
              <section key={g.manager.id} data-mutes-manager={g.manager.id} className="mb-2">
                <ManagerHeading manager={g.manager} className="px-1 pt-1 pb-0.5" />
                {g.error ? (
                  <p className="px-1 py-1.5 text-[12px] break-words text-ink-3">{g.manager.online ? t("inbox-inboxlist.mutes_load_failed", { error: g.error }) : t("inbox-inboxlist.mutes_offline")}</p>
                ) : (
                  <ul className="-mx-1 flex flex-col">{g.mutes!.map(row)}</ul>
                )}
              </section>
            ))
          ) : (
            <ul className="-mx-1 flex flex-col">{list!.map(row)}</ul>
          )}
          <p className="mt-2 text-[11.5px] leading-relaxed text-ink-3">{t("inbox-inboxlist.muted_note")}</p>
        </>
      )}
    </Dialog>
  );
}

const IX_GLYPH: Record<Interaction["kind"], GlyphName | "list" | "terminal"> = { question: "question", plan: "list", permission: "hand", screen: "terminal", elicitation: "sheet" };

/**
 * An agent waiting on you: which agent, what it asks, then what it waits for (amber) and where. Memoized, and it reads
 * from the store only its route and its place, whose fields are strings: a sessions or machines push renders it again
 * only when one of those changed.
 */
const AgentRow = memo(function AgentRow({ ix }: { ix: Interaction }) {
  const to = useStore((s) => ix.session_id === "" ? requestRoute(ix) : conversationRoute(ix.machine_id, ix.session_id, s.sessions, s.assistant));
  const persona = to.name === "persona";
  const where = useStore((s) => (persona ? "Persona" : machineWithHub(ix.machine_id, s.machines)));
  const ask = interactionAsk(ix);
  const glyph = IX_GLYPH[ix.kind];
  return (
    <a href={href(to)} onClick={(e) => onLinkClick(e, to)} className="flex gap-3 rounded-[14px] bg-surface p-3 shadow-card transition-colors duration-150 hover:bg-inset">
      <span className="flex size-10 shrink-0 items-center justify-center rounded-[10px] bg-field text-attention">
        {glyph === "list" || glyph === "terminal" ? <Icon name={glyph} size={18} stroke={2} /> : <Glyph name={glyph} size={18} stroke={2} />}
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="truncate text-[13.5px] font-semibold text-ink">{interactionAgentTitle(ix)}</span>
          <span className="flex-1" />
          <span className="shrink-0 text-[11.5px] text-ink-3 tabular-nums">{shortTime(ix.created_at)}</span>
        </span>
        {ask ? (
          <>
            <span className="mt-0.5 line-clamp-2 text-[12.5px] leading-[1.5] text-ink">{ask}</span>
            <span className="mt-0.5 block truncate text-[11.5px]">
              <span className="font-medium text-attention">{interactionTitle(ix)}</span>
              <span className="text-ink-3"> · {where}</span>
            </span>
          </>
        ) : (
          <>
            <span className="mt-0.5 block truncate text-[12.5px] font-medium text-attention">{interactionTitle(ix)}</span>
            <span className="mt-0.5 block truncate text-[11.5px] text-ink-3">{where}</span>
          </>
        )}
      </span>
    </a>
  );
});

/** "Will clean up N items": asked before Clean up all over BULK_CONFIRM_AT items or more. */
function BulkConfirm({ confirm, onClose }: { confirm: { items: InboxItem[]; scope: string } | null; onClose: () => void }) {
  const n = confirm?.items.length ?? 0;
  return (
    <Dialog
      open={Boolean(confirm)}
      onClose={onClose}
      title={t("inbox-inboxlist.confirm_clean_title")}
      width={380}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>{t("common.cancel")}</Button>
          <Button
            tone="primary"
            icon="check"
            onClick={() => {
              if (confirm) void setAllDone(confirm.items, "dismiss");
              onClose();
            }}
          >
            {t("inbox-inboxlist.confirm_clean", { count: n })}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">{t("inbox-inboxlist.confirm_body", { count: n, scope: confirm?.scope ?? "" })}</p>
    </Dialog>
  );
}
