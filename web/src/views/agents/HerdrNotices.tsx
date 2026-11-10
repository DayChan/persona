// "herdr is not running on <computer>" with "Start", at the top of the Agents list (#1916, lib/herdr-notice.ts): one row per online
// machine on classic herdr whose server does not answer. Asked while the page is visible; a failed start says why in
// words, and the hub's own text with its request id is behind "Copy details" (right click / the copy button), not in the row.

import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { HERDR_POLL_MS, HerdrWatch, herdrWatched, type HerdrFailure } from "../../lib/herdr-notice";
import { useStore } from "../../lib/store";
import type { Machine } from "../../lib/types";
import { contextMenuProps } from "../../ui/context-menu";
import { Icon } from "../../ui/icons";
import { Menu, Spinner, copyText } from "../../ui/atoms";
import type { MenuAt } from "../../ui/menu-model";
import { t } from "../../i18n";

/** The machines the rows can be about, as a key: their ids and engines, not each heartbeat of theirs. */
function watchKey(machines: Machine[]): string {
  return machines.filter(herdrWatched).map((m) => `${m.id} ${m.engine ?? ""}`).sort().join("|");
}

export function HerdrNotices({ watch: given }: { watch?: HerdrWatch } = {}) {
  const machines = useStore((s) => s.machines);
  const [watch] = useState(() => given ?? new HerdrWatch());
  const snap = useSyncExternalStore(watch.subscribe, watch.snapshot, watch.snapshot);
  const key = watchKey(machines);
  const watched = useMemo(() => machines.filter(herdrWatched), [key]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    let dead = false;
    let busy = false;
    const read = () => {
      if (dead || busy || document.visibilityState !== "visible") return;
      busy = true;
      void watch.refresh(watched).finally(() => (busy = false));
    };
    read();
    const timer = setInterval(read, HERDR_POLL_MS);
    document.addEventListener("visibilitychange", read);
    return () => {
      dead = true;
      clearInterval(timer);
      document.removeEventListener("visibilitychange", read);
    };
  }, [watch, watched]);

  // Polling depends on ids and engines; labels must follow a rename even when that polling set stays the same.
  const rows = machines.filter((m) => herdrWatched(m) && snap.stopped.has(m.id));
  if (!rows.length) return null;
  return (
    <div className="mt-1.5 flex flex-col gap-0.5">
      {rows.map((m) => (
        <HerdrNoticeRow key={m.id} machine={m} starting={snap.starting.has(m.id)} failure={snap.failures.get(m.id)} onStart={() => void watch.start(m.id)} />
      ))}
    </div>
  );
}

function HerdrNoticeRow({ machine, starting, failure, onStart }: { machine: Machine; starting: boolean; failure?: HerdrFailure; onStart: () => void }) {
  const [menu, setMenu] = useState<MenuAt | null>(null);
  const name = machine.name || machine.id;
  return (
    <div data-testid="herdr-notice" className="relative z-10 flex items-start gap-2 rounded-[7px] px-2 py-1.5 [@media(pointer:coarse)]:min-h-11">
      <span className="mt-[3px] flex size-3.5 shrink-0 items-center justify-center text-orange">
        {starting ? <Spinner size={11} /> : <Icon name="warning" size={13} stroke={2.2} />}
      </span>
      <div className="min-w-0 flex-1" aria-live="polite">
        <div className="text-[13px] leading-[1.45] text-ink-2">{starting ? t("herdr-notice.starting", { machine: name }) : t("herdr-notice.not_running", { machine: name })}</div>
        {!starting && failure && (
          <div className="mt-0.5 flex items-start gap-1">
            <span data-testid="herdr-notice-failure" tabIndex={0} {...contextMenuProps((at) => setMenu(at))} className="line-clamp-3 min-w-0 flex-1 text-[12px] leading-[1.45] text-red">
              {t("herdr-notice.failed", { reason: failure.text })}
            </span>
            <button type="button" aria-label={t("herdr-notice.copy_detail")} title={t("herdr-notice.copy_detail")} onClick={() => void copyText(failure.detail)} className="touch-hit -mt-0.5 shrink-0 rounded-control p-1 text-ink-3 hover:bg-hover hover:text-ink-2">
              <Icon name="copy" size={12} stroke={2} />
            </button>
          </div>
        )}
      </div>
      {/* Kept while it starts, unseen: the row keeps its height. */}
      <button
        type="button"
        onClick={onStart}
        disabled={starting}
        aria-hidden={starting || undefined}
        tabIndex={starting ? -1 : undefined}
        className={`touch-hit shrink-0 whitespace-nowrap rounded-control px-2 py-0.5 text-[12px] font-medium text-accent-ink hover:bg-hover ${starting ? "invisible" : ""}`}
      >
        {t("herdr-notice.start")}
      </button>
      {menu && failure && (
        <Menu anchor={menu.anchor ?? null} point={menu.point} onClose={() => setMenu(null)} items={[{ key: "copy", label: t("herdr-notice.copy_detail"), icon: "copy", onSelect: () => void copyText(failure.detail) }]} />
      )}
    </div>
  );
}
