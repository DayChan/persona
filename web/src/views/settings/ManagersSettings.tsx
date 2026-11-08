// Settings › Managers (docs/assistant/managers.md, docs/web/web.md"Settings"): the hub's managers (each a whole assistant) — where each runs,
// online or when it last synced, the default; one's details with Rename / Set as default / Install elsewhere / Delete; New manager gives a
// one-time install code (and the install commands when the hub has them). The list follows the hub's `managers` pushes.
import { useCallback, useEffect, useState } from "react";
import { formatList, t } from "../../i18n";
import { isAbort } from "../../lib/api";
import { clock, secondsLeft } from "../../lib/connect-device";
import { ago } from "../../lib/format";
import {
  createManager, deleteBlocked, deleteManager, listManagers, makeDefault, managerError, NAME_MAX, renameManager, renewCode, statusLine,
  unsupported, validName, whereText, type BotConflict, type InstallCode, type Manager, type Tone,
} from "../../lib/managers";
import { loadNotifyPrefs, managerMuted, saveManagerPush, useNotifyPrefs, useNotifyPrefsGeneration } from "../../lib/notify";
import { onHubEvent, useStore } from "../../lib/store";
import { Button, copyText, cx, Dialog, IconButton, Loading, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { inputClass } from "../assistant/parts";
import { Group, Pill, Row, Switch } from "./SettingsParts";

const DOT: Record<Tone, string> = { green: "bg-green", grey: "bg-line-strong", accent: "bg-accent" };

/** Re-renders every `ms`, for the countdowns. */
function useNow(ms: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const tick = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(tick);
  }, [ms]);
  return now;
}

/** A code on screen: the manager it installs, and whether it replaces one already installed somewhere. */
interface Shown {
  id: string;
  name: string;
  code: InstallCode;
  /** Never installed: once its code runs out the hub may have dropped it, and a new code makes it again (renewCode). */
  fresh?: boolean;
}

export function ManagersSettings() {
  const [list, setList] = useState<Manager[] | null>(null);
  const [conflicts, setConflicts] = useState<BotConflict[]>([]);
  const [error, setError] = useState("");
  const [old, setOld] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [shown, setShown] = useState<Shown | null>(null);
  const now = useNow(15_000);

  const load = useCallback(
    () =>
      listManagers()
        .then((r) => (setList(r.managers ?? []), setConflicts(r.bot_conflicts ?? []), setError("")))
        .catch((e) => (unsupported(e) ? setOld(true) : setError(managerError(e)))),
    [],
  );
  useEffect(() => {
    void load();
    return onHubEvent("managers", () => void load());
  }, [load]);

  if (old) {
    return (
      <Group>
        <p className="px-4 py-3 text-[12.5px] leading-relaxed text-ink-3">{t("settings-managers.unsupported")}</p>
      </Group>
    );
  }
  if (!list) return error ? <p role="alert" className="px-1 text-[13px] text-red">{error}</p> : <Loading />;

  const current = list.find((m) => m.id === open) ?? null;
  return (
    <>
      {!!conflicts.length && (
        <div role="alert" className="mb-6 flex items-start gap-3 rounded-card bg-orange-tint p-4 text-sm leading-relaxed">
          <Icon name="warning" className="mt-1 size-4 shrink-0 text-orange" />
          <div className="min-w-0 flex-1 break-words">
            <h3 className="font-medium text-orange">{t("settings-managers.bot_conflicts_title")}</h3>
            <ul className="mt-2 space-y-1 text-ink">
              {conflicts.map((conflict, i) => (
                <li key={i}>
                  {t(`settings-managers.bot_conflict_${conflict.platform}`, {
                    names: formatList(conflict.managers.map((id) => list.find((m) => m.id === id)?.name ?? id)),
                  })}
                </li>
              ))}
            </ul>
            <p className="mt-2 text-ink-2">{t("settings-managers.bot_conflicts_hint")}</p>
          </div>
        </div>
      )}
      <Group
        footer={t("settings-managers.hint")}
        right={
          <Button tone="quiet" icon="plus" onClick={() => setCreating(true)}>
            {t("settings-managers.new")}
          </Button>
        }
      >
        {list.map((m) => (
          <ManagerRow key={m.id} m={m} now={now} onOpen={() => setOpen(m.id)} />
        ))}
      </Group>
      {error && <p role="alert" className="px-1 text-[12.5px] text-red">{error}</p>}
      <ManagerDetail
        m={current}
        onClose={() => setOpen(null)}
        onChanged={() => void load()}
        onCode={(code, id) => current && (setOpen(null), setShown({ id: id ?? current.id, name: current.name, code, fresh: !!current.installing }), void load())}
      />
      <NewManager
        open={creating}
        onClose={() => setCreating(false)}
        onMade={(m, code) => {
          setCreating(false);
          setShown({ id: m.id, name: m.name, code, fresh: true });
          void load();
        }}
      />
      <CodeDialog shown={shown} onClose={() => setShown(null)} onCode={(code, id) => (setShown((s) => (s ? { ...s, code, id } : s)), void load())} />
    </>
  );
}

function ManagerRow({ m, now, onOpen }: { m: Manager; now: number; onOpen: () => void }) {
  const status = statusLine(m, now);
  const where = whereText(m);
  return (
    <button type="button" onClick={onOpen} className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-hover pointer-coarse:min-h-11">
      <span aria-hidden className={cx("size-2 shrink-0 rounded-full", DOT[status.tone])} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className={cx("truncate text-[13px]", m.online || m.installing ? "text-ink" : "text-ink-2")}>{m.name}</span>
          {m.default && <Pill tone="accent">{t("settings-managers.default")}</Pill>}
          {m.needs_update && <Pill tone="orange">{t("settings-managers.needs_update")}</Pill>}
        </div>
        <div className="mt-0.5 truncate text-[11.5px] text-ink-3">{[status.text, where].filter(Boolean).join(" · ")}</div>
      </div>
      <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
    </button>
  );
}

/** This device's pushes from one manager (NotifyPrefs.muted_managers); not shown by a hub from before them. */
function PushSwitch({ id }: { id: string }) {
  const prefs = useNotifyPrefs();
  const generation = useNotifyPrefsGeneration();
  useEffect(() => void loadNotifyPrefs().catch(() => {}), [generation]);
  if (!prefs.muted_managers) return null;
  const on = !managerMuted(prefs, id, id);
  return (
    <div className="flex flex-col gap-1.5">
      <div className="overflow-hidden rounded-card bg-inset shadow-hairline">
        <Row label={t("settings-managers.push")}>
          <Switch
            on={on}
            label={t("settings-managers.push")}
            onChange={(next) => void saveManagerPush(id, next).catch((e) => {
              if (!isAbort(e)) toast(t("settings-managers.push_failed", { error: managerError(e) }), "error");
            })}
          />
        </Row>
      </div>
      <p className="px-1 text-[11.5px] leading-snug text-ink-3">{t("settings-managers.push_note")}</p>
    </div>
  );
}

type Step = "view" | "rename" | "move" | "delete";

function ManagerDetail({ m, onClose, onChanged, onCode }: { m: Manager | null; onClose: () => void; onChanged: () => void; onCode: (c: InstallCode, id?: string) => void }) {
  const [step, setStep] = useState<Step>("view");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const id = m?.id;
  useEffect(() => {
    setStep("view");
    setError("");
  }, [id]);
  const machines = useStore((s) => s.machines);
  // what the dialog shows while closing (m is null by then)
  const [last, setLast] = useState<Manager | null>(null);
  useEffect(() => void (m && setLast(m)), [m]);
  const shownM = m ?? last;
  if (!shownM) return null;

  const run = async (work: () => Promise<unknown>, after?: () => void) => {
    setBusy(true);
    setError("");
    try {
      await work();
      onChanged();
      (after ?? (() => setStep("view")))();
    } catch (e) {
      setError(managerError(e));
    } finally {
      setBusy(false);
    }
  };
  const installing = !!shownM.installing;
  const ownMachines = machines.filter((x) => x.manager === shownM.id).length;
  const blocked = deleteBlocked(shownM);
  const status = statusLine(shownM);
  const where = whereText(shownM);
  const code = () => run(() => renewCode(shownM.id, shownM.name, installing).then((r) => onCode(r.code, r.id)));

  let body: React.ReactNode;
  let footer: React.ReactNode;
  if (step === "rename") {
    const ok = validName(name);
    body = (
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (ok && !busy) void run(() => renameManager(shownM.id, name.trim()));
        }}
      >
        <input
          autoFocus
          value={name}
          maxLength={NAME_MAX * 2}
          onChange={(e) => setName(e.target.value)}
          placeholder={t("settings-managers.name_placeholder")}
          aria-label={t("settings-managers.name")}
          className={inputClass}
        />
      </form>
    );
    footer = (
      <>
        <Button tone="quiet" disabled={busy} onClick={() => setStep("view")}>{t("common.cancel")}</Button>
        <Button tone="primary" busy={busy} disabled={!ok} onClick={() => void run(() => renameManager(shownM.id, name.trim()))}>{t("settings-managers.save")}</Button>
      </>
    );
  } else if (step === "move") {
    body = <p className="text-[12.5px] leading-relaxed text-ink-2">{t("settings-managers.move_confirm")}</p>;
    footer = (
      <>
        <Button tone="quiet" disabled={busy} onClick={() => setStep("view")}>{t("common.cancel")}</Button>
        <Button tone="danger" busy={busy} onClick={() => void code()}>{t("settings-managers.move")}</Button>
      </>
    );
  } else if (step === "delete") {
    body = (
      <div className="flex flex-col gap-2 text-[12.5px] leading-relaxed text-ink-2">
        <p>{t(installing ? "settings-managers.cancel_install_confirm" : "settings-managers.delete_confirm", { name: shownM.name })}</p>
        {/* what goes with it (docs/assistant/managers.md "Moving a machine or a source"): its computers come back, its sources don't */}
        {!installing && ownMachines > 0 && <p>{t("settings-managers.delete_machines", { count: ownMachines })}</p>}
        {!installing && <p className="text-orange">{t("settings-managers.delete_sources")}</p>}
      </div>
    );
    footer = (
      <>
        <Button tone="quiet" disabled={busy} onClick={() => setStep("view")}>{t("settings-managers.keep")}</Button>
        <Button tone="danger" busy={busy} onClick={() => void run(() => deleteManager(shownM.id), onClose)}>
          {t(installing ? "settings-managers.cancel_install" : "settings-managers.delete")}
        </Button>
      </>
    );
  } else {
    body = (
      <div className="flex flex-col gap-4">
        <div className="divide-y divide-line overflow-hidden rounded-card bg-inset shadow-hairline">
          <Row label={t("settings-managers.row_status")}>
            {shownM.needs_update && <Pill tone="orange">{t("settings-managers.needs_update")}</Pill>}
            <span aria-hidden className={cx("size-2 shrink-0 rounded-full", DOT[status.tone])} />
            <span className="truncate">{status.text}</span>
          </Row>
          {where && <Row label={t("settings-managers.row_where")}><span className="max-w-[240px] truncate" title={where}>{where}</span></Row>}
          {shownM.version && <Row label={t("settings-managers.row_version")} mono><span className="max-w-[240px] truncate">{shownM.version}</span></Row>}
          {shownM.online && shownM.since && <Row label={t("settings-managers.row_since")}>{ago(shownM.since)}</Row>}
          {!shownM.online && shownM.last_seen && <Row label={t("settings-managers.row_last_seen")}>{ago(shownM.last_seen)}</Row>}
        </div>
        {shownM.needs_update && <p className="-mt-2 px-1 text-[11.5px] leading-snug text-ink-3">{t("settings-managers.needs_update_note")}</p>}
        {!installing && <PushSwitch id={shownM.id} />}
        <div className="flex flex-wrap gap-2">
          {!installing && (
            <Button icon="pencil" disabled={busy} onClick={() => (setName(shownM.name), setStep("rename"))}>{t("settings-managers.rename")}</Button>
          )}
          {!installing && !shownM.default && (
            <Button icon="star" busy={busy} onClick={() => void run(() => makeDefault(shownM.id))}>{t("settings-managers.make_default")}</Button>
          )}
          {!shownM.builtin && (
            <Button icon="retry" disabled={busy} onClick={() => (installing ? void code() : setStep("move"))}>
              {t(installing ? "settings-managers.new_code" : "settings-managers.move")}
            </Button>
          )}
          <Button
            icon="trash"
            disabled={busy || !!blocked}
            title={blocked || undefined}
            onClick={() => setStep("delete")}
            className="text-red enabled:hover:text-red"
          >
            {t(installing ? "settings-managers.cancel_install" : "settings-managers.delete")}
          </Button>
        </div>
        {blocked && <p className="-mt-2 text-[11.5px] leading-snug text-ink-3">{blocked}</p>}
      </div>
    );
    footer = <Button tone="primary" onClick={onClose}>{t("settings-managers.done")}</Button>;
  }

  return (
    <Dialog
      open={!!m}
      onClose={onClose}
      width={440}
      title={
        <span className="flex min-w-0 items-center gap-2">
          <span className="truncate">{step === "rename" ? t("settings-managers.rename") : shownM.name}</span>
          {step !== "rename" && shownM.default && <Pill tone="accent">{t("settings-managers.default")}</Pill>}
        </span>
      }
      footer={footer}
    >
      {body}
      {error && <p role="alert" className="mt-3 text-[12.5px] text-red">{error}</p>}
    </Dialog>
  );
}

function NewManager({ open, onClose, onMade }: { open: boolean; onClose: () => void; onMade: (m: Manager, code: InstallCode) => void }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    setName("");
    setError("");
  }, [open]);
  const ok = validName(name);
  const make = async () => {
    if (!ok || busy) return;
    setBusy(true);
    setError("");
    try {
      const r = await createManager(name.trim());
      onMade(r.manager, r);
    } catch (e) {
      setError(managerError(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={400}
      title={t("settings-managers.new")}
      footer={
        <>
          <Button tone="quiet" disabled={busy} onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="primary" busy={busy} disabled={!ok} onClick={() => void make()}>{t("settings-managers.create")}</Button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void make();
        }}
      >
        <input
          autoFocus
          value={name}
          maxLength={NAME_MAX * 2}
          onChange={(e) => setName(e.target.value)}
          placeholder={t("settings-managers.name_placeholder")}
          aria-label={t("settings-managers.name")}
          className={inputClass}
        />
      </form>
      <p className="mt-2 text-[11.5px] leading-relaxed text-ink-3">{t("settings-managers.new_hint")}</p>
      {error && <p role="alert" className="mt-2 text-[12.5px] text-red">{error}</p>}
    </Dialog>
  );
}

/** The install code (and commands), its countdown, and once it ran out New installation code. */
function CodeDialog({ shown, onClose, onCode }: { shown: Shown | null; onClose: () => void; onCode: (c: InstallCode, id: string) => void }) {
  const now = useNow(1000);
  const [busy, setBusy] = useState(false);
  const [last, setLast] = useState<Shown | null>(null);
  useEffect(() => void (shown && setLast(shown)), [shown]);
  const s = shown ?? last;
  if (!s) return null;
  const left = secondsLeft(s.code.expires_at, now);
  const again = async () => {
    setBusy(true);
    try {
      const r = await renewCode(s.id, s.name, !!s.fresh);
      onCode(r.code, r.id);
    } catch (e) {
      toast(managerError(e), "error");
    } finally {
      setBusy(false);
    }
  };
  const commands = [
    { label: t("settings-managers.install_service"), text: s.code.commands?.service },
    { label: t("settings-managers.install_docker"), text: s.code.commands?.docker },
  ].filter((c): c is { label: string; text: string } => !!c.text);
  return (
    <Dialog open={!!shown} onClose={onClose} width={480} title={t("settings-managers.code_for", { name: s.name })} footer={<Button tone="primary" onClick={onClose}>{t("settings-managers.done")}</Button>}>
      {left === 0 ? (
        <div className="flex flex-col items-center gap-3 py-6 text-center">
          <p className="text-[13px] text-ink-2">{t("settings-managers.expired")}</p>
          <Button tone="primary" busy={busy} onClick={() => void again()}>{t("settings-managers.new_code")}</Button>
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          <div>
            <div className="mb-1 flex items-center gap-2 text-[12px] text-ink-3">
              <span className="flex-1">{t("settings-managers.code_title")}</span>
              <span className="tabular-nums">{t("settings-managers.expires_in", { time: clock(left) })}</span>
            </div>
            <CopyLine text={s.code.code} big />
          </div>
          {commands.map((c) => (
            <div key={c.label}>
              <div className="mb-1 text-[12px] text-ink-3">{c.label}</div>
              <CopyLine text={c.text} />
            </div>
          ))}
          <p className="text-[12px] leading-relaxed text-ink-3">{t(commands.length ? "settings-managers.commands_note" : "settings-managers.code_note")}</p>
        </div>
      )}
    </Dialog>
  );
}

function CopyLine({ text, big }: { text: string; big?: boolean }) {
  return (
    <div className="flex items-center gap-2 rounded-control bg-inset px-2.5 py-1.5 shadow-hairline">
      {/* a command wraps so all of it can be read before it is pasted; the code is one short line */}
      <code className={cx("min-w-0 flex-1 font-mono text-ink select-all", big ? "truncate text-[14px]" : "break-all text-[12px] leading-relaxed")} title={text}>{text}</code>
      <IconButton
        icon="copy"
        label={t("settings-managers.copy")}
        size={24}
        iconSize={12}
        onClick={() => void copyText(text)}
      />
    </div>
  );
}
