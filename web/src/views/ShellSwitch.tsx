// "Assistant | Agents" in the middle of a phone's top bar (#1996), like the iOS app's new shell (docs/apple/ios.md "New shell"): the
// main conversation and the Agents page, side by side. Agents carries how many agents are waiting for you (quiet ones
// are told about in the main conversation, not counted here). Narrow screens only; wide ones have the sidebar.
import { href, onLinkClick, type Route } from "../lib/router";
import { othersWaiting, useCounts } from "../lib/sessions";
import type { Session } from "../lib/types";
import { cx, IconButton } from "../ui/atoms";
import { HEADER_SLOT, HEADER_SLOT_END, HEADER_SLOT_START } from "./chat/header";
import { t } from "../i18n";

type ShellPage = "assistant" | "agents";

const SHELL_PAGES: { page: ShellPage; label: string; to: Route }[] = [
  { page: "assistant", label: t("shellswitch.assistant"), to: { name: "persona" } },
  { page: "agents", label: t("shellswitch.agents"), to: { name: "agent-list" } },
];

export function ShellSwitch({ page }: { page: ShellPage }) {
  return <ShellSwitchView page={page} waiting={useCounts().badge} />;
}

function WaitingBadge({ count, className }: { count: number; className?: string }) {
  return <span aria-hidden className={cx("flex h-4 min-w-4 items-center justify-center rounded-full bg-field px-1 text-[10px] font-semibold text-attention tabular-nums", className)}>{count}</span>;
}

/** A chat header's way to the sidebar on a narrow screen, carrying how many other agents are waiting for you. `counted` is
 *  off where the header's switch already shows that number. */
export function ChatSidebarButton({ session, counted, onClick }: { session?: Session; counted: boolean; onClick: () => void }) {
  const { badge, quietCounted } = useCounts();
  const waiting = counted ? othersWaiting(badge, session, quietCounted) : 0;
  const label = t("chat-chatview.sidebar");
  return (
    <span className="relative flex shrink-0 lg:hidden">
      <IconButton icon="sidebar" label={waiting ? t("shellswitch.waiting_label", { label, count: waiting }) : label} onClick={onClick} />
      {waiting > 0 && <WaitingBadge count={waiting} className="pointer-events-none absolute -top-1 -right-1" />}
    </span>
  );
}

/** A phone's top bar around the switch, in ChatView's header slots (chat/header.ts): sidebar on the left, `end` on the
 *  right, so both pages have their buttons in the same places. Narrow only, unless `endWide` keeps `end` on a wide screen. */
export function ShellHeaderSlots({ page, onToggleSidebar, end, endWide }: { page: ShellPage; onToggleSidebar: () => void; end: React.ReactNode; endWide?: boolean }) {
  return (
    <>
      <div className={cx(HEADER_SLOT, HEADER_SLOT_START, "lg:hidden")}>
        <IconButton icon="sidebar" label={t("shellswitch.sidebar")} onClick={onToggleSidebar} />
      </div>
      <div className="flex min-w-0 flex-1 justify-center lg:hidden">
        <ShellSwitch page={page} />
      </div>
      <div className={cx(HEADER_SLOT, HEADER_SLOT_END, !endWide && "lg:hidden")}>{end}</div>
    </>
  );
}

/** The switch itself, with the number of agents waiting for you on its Agents side (0: no badge). */
export function ShellSwitchView({ page, waiting }: { page: ShellPage; waiting: number }) {
  return (
    <nav aria-label={t("shellswitch.switch_page")} className="flex h-8 shrink-0 items-center gap-0.5 rounded-full bg-inset p-0.5 shadow-hairline">
      {SHELL_PAGES.map((p) => {
        const on = p.page === page;
        const badge = p.page === "agents" && waiting > 0 ? waiting : 0;
        return (
          <a
            key={p.page}
            href={href(p.to)}
            onClick={(e) => (on ? e.preventDefault() : onLinkClick(e, p.to))}
            aria-current={on ? "page" : undefined}
            aria-label={badge ? t("shellswitch.waiting_label", { label: p.label, count: badge }) : p.label}
            className={cx(
              "touch-hit flex h-7 items-center gap-1.5 whitespace-nowrap rounded-full px-3.5 text-[13px] transition-[background-color,color,box-shadow] duration-150",
              on ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-2 hover:text-ink",
            )}
          >
            {p.label}
            {badge > 0 && <WaitingBadge count={badge} />}
          </a>
        );
      })}
    </nav>
  );
}
