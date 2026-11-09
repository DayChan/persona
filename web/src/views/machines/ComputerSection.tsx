// "Computer control" on a Mac's page while the hub's extension cua is on (docs/extensions/computer-use.md, iOS ComputerSection): the switch that
// lets the assistant operate it through Cua Driver, and whether it can now. What is missing comes with what to do:
// "Install Cua Driver" (after a confirmation) when it is not there, "Grant access" when a permission is missing (macOS asks on the
// Mac's own screen), the node's progress while it works, and VNC (the web's live screen, /screen/<mid>) when
// Screen Sharing is on. The hub is asked again when the page opens, and every 2 s while a job runs and the tab is shown.
import { useEffect, useRef, useState } from "react";
import { api, enc, errorText } from "../../lib/api";
import { computerView, computerWatching, GRANT_HINT, NO_SCREEN_SHARING, screenPath, type ComputerAction, type MachineComputer } from "../../lib/computer";
import { documentPage, latestRequest, visiblePoll } from "../../lib/visible-poll";
import { Button, cx, Dialog, EASE, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Switch } from "../settings/SettingsParts";
import { t } from "../../i18n";
import { AllowedComputerApps } from "./AllowedComputerApps";

export function ComputerSection({ mid, computer, online, manager }: { mid: string; computer: MachineComputer; online: boolean; manager?: string }) {
  // What the hub said since the page opened (newer than the machine list's).
  const [fresh, setFresh] = useState<MachineComputer | null>(null);
  const [saving, setSaving] = useState(false);
  const [asking, setAsking] = useState(false);
  const [confirmInstall, setConfirmInstall] = useState(false);
  const shown = fresh ?? computer;
  const enabled = shown.enabled;
  const watching = computerWatching(shown, online);

  // One request line per Mac, like BrowserSection: only the latest read or write is shown, so a read sent before the
  // switch or an action (or before a newer read) never puts an older state back (#5455).
  const reads = useRef<ReturnType<typeof latestRequest<MachineComputer>> | null>(null);
  useEffect(() => {
    setFresh(null);
    // a failed read leaves the machine list's copy (or the last answer) on screen
    const line = latestRequest<MachineComputer>(setFresh, () => {});
    reads.current = line;
    return () => {
      line.dispose();
      if (reads.current === line) reads.current = null;
    };
  }, [mid]);

  const refresh = () => reads.current?.run(() => api<MachineComputer>(`/machines/${enc(mid)}/computer`)) ?? Promise.resolve();

  // Runs a write and shows its answer as the latest one (an older read still in flight is dropped). If another Mac is
  // shown by the time it comes back, both its answer and its failure belong to the Mac left behind and are dropped.
  const write = async (send: () => Promise<MachineComputer>) => {
    const line = reads.current;
    try {
      const value = await send();
      void line?.run(() => Promise.resolve(value));
    } catch (e) {
      if (line && line === reads.current) throw e;
    }
  };

  useEffect(() => {
    if (online && enabled) void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mid, online, enabled]);

  useEffect(() => {
    if (!watching) return;
    // Every 2 s while the tab is shown, and once on coming back. visiblePoll asks right away when it starts, but the
    // page just asked (on open or with the action that started this), so that first call is skipped.
    let started = false;
    const stop = visiblePoll(() => { if (started) void refresh(); }, 2000, documentPage);
    started = true;
    return stop;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mid, watching]);

  const set = async (on: boolean) => {
    setSaving(true);
    try {
      await write(() => api<MachineComputer>(`/machines/${enc(mid)}/computer`, { method: "PUT", json: { enabled: on } }));
    } catch (e) {
      toast(on ? t("machines-computersection.turn_on_failed", { error: errorText(e) }) : t("machines-computersection.turn_off_failed", { error: errorText(e) }), "error");
    } finally {
      setSaving(false);
    }
  };

  const run = async (what: ComputerAction) => {
    setAsking(true);
    try {
      await write(() => api<MachineComputer>(`/machines/${enc(mid)}/computer/${what}`, { method: "POST" }));
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      setAsking(false);
    }
  };

  const v = computerView(shown, online);
  const look = (prominent: boolean) => (
    <Button tone={prominent ? "primary" : "secondary"} icon="monitor" onClick={() => window.open(screenPath(mid), "_blank", "noopener")}>
      VNC
    </Button>
  );
  const action = (title: string, what: ComputerAction, prominent = false) => (
    <Button tone={prominent ? "primary" : "secondary"} busy={asking} onClick={() => (what === "install" ? setConfirmInstall(true) : void run(what))}>
      {title}
    </Button>
  );

  return (
    <section data-testid="computer-section">
      <div className="mb-2.5 flex min-h-7 items-center gap-2 px-1">
        <h2 className="text-[13px] font-medium text-ink">{t("machines-computersection.title")}</h2>
      </div>
      <div className="overflow-hidden rounded-card bg-surface shadow-card" style={{ animation: `fade-up 420ms ${EASE} both` }}>
        <div className="flex items-center gap-3 px-4 py-3">
          <div className="min-w-0 flex-1">
            <div className="truncate text-[13px] text-ink">{t("machines-computersection.allow")}</div>
            <div className="mt-0.5 truncate text-[11.5px] text-ink-3">
              {t(shown.backend === "lcu" ? "machines-computersection.allow_detail_lcu" : "machines-computersection.allow_detail")}
            </div>
          </div>
          {saving && <Spinner />}
          <Switch on={enabled} onChange={(on) => void set(on)} disabled={saving || !online} label={t("machines-computersection.allow")} />
        </div>
        {v.kind !== "off" && (
          <div className="flex flex-col items-start gap-2.5 border-t border-line px-4 py-3">
            {v.kind === "offline" && <Line text={t("machines-computersection.offline")} />}
            {v.kind === "running" && (
              <>
                <p className="flex items-center gap-2 text-[12.5px] text-ink-2" role="status">
                  <Spinner />
                  {v.text}
                </p>
                {(v.cancel || v.screen) && (
                  <div className="flex flex-wrap gap-2">
                    {v.cancel && action(t("common.cancel"), "cancel")}
                    {v.screen && look(true)}
                  </div>
                )}
              </>
            )}
            {v.kind === "install" && (
              <>
                <Line text={v.text} />
                <p className="text-[11.5px] leading-relaxed text-ink-3">
                  {t(shown.backend === "lcu" ? "machines-computersection.install_detail_lcu" : "machines-computersection.install_detail")}
                </p>
                {action(
                  v.retry
                    ? t("machines-computersection.reinstall")
                    : t(shown.backend === "lcu" ? "machines-computersection.install_title_lcu" : "machines-computersection.install_title"),
                  "install",
                  true,
                )}
              </>
            )}
            {v.kind === "granted" && (
              <>
                <Line text={GRANT_HINT} />
                <div className="flex flex-wrap gap-2">
                  {v.screen && look(true)}
                  {action(t("machines-computersection.ask_again"), "grant")}
                </div>
                {!v.screen && <p className="text-[11.5px] leading-relaxed text-ink-3">{NO_SCREEN_SHARING}</p>}
              </>
            )}
            {v.kind === "grant" && (
              <>
                <Line text={v.text} />
                {action(t("machines-computersection.grant"), "grant", true)}
              </>
            )}
            {v.kind === "ready" && (
              <>
                <Line text={v.text} ok />
                {v.screen ? look(false) : <p className="text-[11.5px] leading-relaxed text-ink-3">{NO_SCREEN_SHARING}</p>}
              </>
            )}
            {v.kind === "unknown" && <Line text={v.text} />}
          </div>
        )}
        <AllowedComputerApps mid={mid} manager={manager} />
      </div>
      <Dialog
        open={confirmInstall}
        onClose={() => setConfirmInstall(false)}
        title={t(shown.backend === "lcu" ? "machines-computersection.install_title_lcu" : "machines-computersection.install_title")}
        footer={
          <>
            <Button tone="quiet" onClick={() => setConfirmInstall(false)}>
              {t("common.cancel")}
            </Button>
            <Button
              tone="primary"
              onClick={() => {
                setConfirmInstall(false);
                void run("install");
              }}
            >
              {t("machines-computersection.install")}
            </Button>
          </>
        }
      >
        <p className="text-[12.5px] leading-relaxed text-ink-2">
          {t(shown.backend === "lcu" ? "machines-computersection.install_confirm_lcu" : "machines-computersection.install_confirm")}
        </p>
      </Dialog>
    </section>
  );
}

function Line({ text, ok }: { text: string; ok?: boolean }) {
  return (
    <p className="flex items-start gap-2 text-[12.5px]" role="status">
      <span className={cx("mt-[3px] flex size-3.5 shrink-0 items-center justify-center rounded-full", ok ? "bg-green-tint text-green" : "bg-orange-tint text-orange")}>
        <Icon name={ok ? "check" : "info"} size={9} stroke={2.8} />
      </span>
      <span className={cx("min-w-0 break-words", ok ? "text-ink" : "text-ink-2")}>{text}</span>
    </p>
  );
}
