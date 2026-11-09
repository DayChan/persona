// The extensions' places among the web's own pages (docs/extensions/extensions.md "UI slots"): a nav_page's full page (/extension/<id>/<ui>,
// its entry is on the sidebar), the cards on "Overview" (overview_card) and the sections on a machine's page
// (machine_section). Each draws the extension's declarative View with ExtensionUI; the list comes from lib/extension-slots.ts.
import { Suspense, useMemo, useState } from "react";
import { uiIcon, type UIIcon, type UIPlace, type UITone } from "../../lib/extension-ui";
import { useExtensionPlace, useExtensionSlot } from "../../lib/extension-slots";
import { href, navigate, onLinkClick, type Route } from "../../lib/router";
import { useKnownDefault } from "../../lib/manager-choice";
import { useStore } from "../../lib/store";
import { t } from "../../i18n";
import { cx, EASE, IconButton, Loading } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { lazyPage } from "../../lib/lazy-page";
import { NEW_ICON_GLYPH } from "./icon-map";

// The extension page is its own chunk (#1794): App.tsx reaches this file for the sidebar's and the nav page's icons and
// titles, and the first screen draws no extension UI. A page, a card and a section each wait under their own Suspense
// (the page keeps its top bar) with the Loading that ExtensionUI shows while it asks for its view.
const ExtensionUI = lazyPage(() => import("../settings/ExtensionView").then((m) => m.ExtensionUI));

/** A nav_page's icon from the fixed vocabulary, as one of the web's icons (unknown: the extension's). */
const NAV_ICON: Record<UIIcon, IconName> = { extension: "extension", plugin: "extension", chart: "chart", gauge: "gauge", list: "list", bell: "bell", doc: "read", calendar: "calendar", money: "money",
  branch: "branch", code: "code", pull_request: "pullRequest", merge: "merge", ...NEW_ICON_GLYPH };
export const navIcon = (icon?: string): IconName => NAV_ICON[uiIcon(icon)];

/** A badge's tone as the sidebar's badge colors. */
export const BADGE_TONE: Record<UITone, "red" | "orange" | "green" | "accent"> = { danger: "red", caution: "orange", positive: "green", neutral: "accent" };

/** Where an extension's page opens: its own route (a nav_page, or a plugin_page a card opens). */
export const pageRoute = (extension: string, ui: string): Route => ({ name: "extension", extension, ui });

/** An extension's page, full size (a nav_page opened from the sidebar, or what a card opens). */
export function ExtensionNavPage({ extension, ui, onToggleSidebar }: { extension: string; ui: string; onToggleSidebar: () => void }) {
  const place = useExtensionPlace(extension, ui);
  const [n, setN] = useState(0);
  // a page a card or a section pushed opens here with the state it was pushed with (?state=)
  const state = useMemo(() => new URLSearchParams(location.search).get("state") ?? undefined, [extension, ui]);
  const title = place?.title || ui;
  // the extension's name after the title, unless it only says the title again ("Example Example", #3115)
  const name = place?.extension_name;
  const showName = !!name && name.trim().toLowerCase() !== title.trim().toLowerCase();
  // a code review or a file browser (UI version 4) takes the page's whole width; everything else stays a column
  const [wide, setWide] = useState(false);
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("extensions-extensionslots.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <span className="flex size-7 shrink-0 items-center justify-center rounded-[8px] bg-field text-ink-2">
          <Icon name={navIcon(place?.icon)} size={14} />
        </span>
        <h1 className="min-w-0 truncate text-[14px] font-semibold text-ink">{title}</h1>
        {showName && <span className="hidden min-w-0 truncate text-[12px] text-ink-3 sm:inline">{name}</span>}
        <span className="flex-1" />
        <IconButton icon="retry" label={t("extensions-extensionslots.redraw")} onClick={() => setN((x) => x + 1)} />
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className={cx("mx-auto flex flex-col px-4 py-6 sm:px-8", wide ? "max-w-[1680px]" : "max-w-[880px]")}>
          <Suspense fallback={<Loading />}>
            <ExtensionUI key={`${extension}/${ui}/${n}`} extension={extension} ui={ui} title={title} bare state={state} onWide={setWide} />
          </Suspense>
        </div>
      </div>
    </div>
  );
}

/** The cards on "Overview" (overview_card), one row under the agents' summary: one card full width, more two a row. */
export function OverviewCards() {
  const cards = useExtensionSlot("overview_card");
  if (!cards.length) return null;
  return (
    <section aria-label={t("extensions-extensionslots.extensions")} className={cx("grid grid-cols-1 gap-3", cards.length > 1 && "md:grid-cols-2")}>
      {cards.map((c, i) => (
        <OverviewCard key={`${c.extension}/${c.id}`} place={c} i={i} />
      ))}
    </section>
  );
}

const INTERACTIVE = "a,button,input,select,textarea,label,[role=radio],[role=switch],[tabindex]";

function OverviewCard({ place, i }: { place: UIPlace; i: number }) {
  const [gone, setGone] = useState(false);
  const context = useMemo(() => ({ size: "compact" as const }), []);
  const to = place.opens ? pageRoute(place.extension, place.opens) : null;
  if (gone) return null;
  // the whole card opens the page; a button, a row or the chart inside it keeps its own click
  const open = (e: React.MouseEvent) => {
    if (!to || (e.target as HTMLElement).closest(INTERACTIVE)) return;
    if (window.getSelection()?.toString()) return;
    navigate(to);
  };
  return (
    <div
      data-extension-card={`${place.extension}/${place.id}`}
      onClick={open}
      className={cx("flex min-w-0 flex-col rounded-card bg-surface pb-1.5 shadow-card", to && "cursor-pointer transition-colors hover:bg-inset")}
      style={{ animation: `fade-up 420ms ${EASE} ${Math.min(i, 6) * 50}ms both` }}
    >
      <div className="flex min-w-0 items-center gap-2 px-4 pt-3 pb-0.5">
        {to ? (
          <a href={href(to)} onClick={(e) => onLinkClick(e, to)} className="group flex min-w-0 flex-1 items-center gap-1 outline-none focus-visible:underline">
            <span className="truncate text-[13px] font-medium text-ink">{place.title}</span>
            <Icon name="chevronRight" size={12} stroke={2.2} className="shrink-0 text-ink-3 transition-transform duration-150 group-hover:translate-x-0.5" />
          </a>
        ) : (
          <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-ink">{place.title}</span>
        )}
        {place.extension_name && <span className="max-w-[45%] shrink-0 truncate text-[11px] text-ink-3">{place.extension_name}</span>}
      </div>
      <Suspense fallback={<Loading />}>
        <ExtensionUI extension={place.extension} ui={place.id} context={context} compact onMissing={() => setGone(true)} />
      </Suspense>
    </div>
  );
}

/** The extensions' sections on a machine's page (machine_section), after its own; the extension is told which machine.
 *  A computer of another manager than the default gets that manager's extensions' sections, asked with its name
 *  (docs/assistant/managers.md "Clients"). */
export function MachineExtensionSections({ mid, heading }: { mid: string; heading: (title: string, right?: React.ReactNode) => React.ReactNode }) {
  const manager = useStore((s) => s.machines.find((m) => m.id === mid)?.manager);
  const known = useKnownDefault();
  const sections = useExtensionSlot("machine_section", manager);
  // whether the computer is another manager's goes by the roster's default: nothing is drawn before it is known
  if (manager && !known) return null;
  return (
    <>
      {sections.map((s) => (
        <MachineSection key={`${manager ?? ""}/${s.extension}/${s.id}`} place={s} mid={mid} manager={manager} heading={heading} />
      ))}
    </>
  );
}

function MachineSection({ place, mid, manager, heading }: { place: UIPlace; mid: string; manager?: string; heading: (title: string, right?: React.ReactNode) => React.ReactNode }) {
  const [gone, setGone] = useState(false);
  const context = useMemo(() => ({ machine: mid }), [mid]);
  if (gone) return null;
  return (
    <section data-extension-section={`${place.extension}/${place.id}`}>
      {heading(
        place.title,
        place.extension_name && (
          <span className="flex min-w-0 items-center gap-1 text-[11.5px] text-ink-3">
            <Icon name="extension" size={11} className="shrink-0" />
            <span className="truncate">{place.extension_name}</span>
          </span>
        ),
      )}
      <Suspense fallback={<Loading />}>
        <ExtensionUI extension={place.extension} ui={place.id} context={context} manager={manager} bare title={place.title} pages="open" onMissing={() => setGone(true)} />
      </Suspense>
    </section>
  );
}
