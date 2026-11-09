// Settings › Assistant (docs/assistant/assistant.md "Profile page and avatar", the iOS app's AssistantProfileView / AssistantNotesView /
// AssistantMemoryView, Android's assistant settings): what Persona does on its own (a line into "Scheduled tasks"), what it knows about you
// ("About me" = `notes`, saved on "Save" or when leaving; "Memory" = its memory files: read, edit the whole file, delete), and where
// it runs (read-only here). Its model is Settings › Models › Main chat, not here (#1291).
import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { BASE } from "../../lib/base";
import { settingsHref } from "./nav";
import { api, credentialOwner, enc, errorText, subscribeCredentialOwner } from "../../lib/api";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { PERMISSION_MODES } from "../../lib/session-actions";
import { ConfigurationManager } from "./ConfigurationManager";
import { aboutMeSaves } from "./about-me-saves";
import { ago } from "../../lib/format";
import { MEMORY_INDEX, memoryEntries, memoryTypeLabel, memoryTypeTone, parseMemory, type MemoryEntry, type MemoryFile } from "../../lib/memory";
import { navigate } from "../../lib/router";
import { momentLabel, upcoming, type Task } from "../../lib/routines";
import { refreshAssistant, useStore } from "../../lib/store";
import { Button, cx, Dialog, Loading, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { Group, Pill, Row, Switch } from "../settings/SettingsParts";
import { SkillsGroup } from "./Skills";
import { Tag, textareaClass } from "../assistant/parts";
import { PersonaAvatar, usePersona } from "../assistant/Persona";

interface AssistantState {
  manager?: string;
  default_permission_mode?: string;
  machine_id?: string;
  launcher?: string;
  cwd?: string;
  timezone?: string;
  /** Only once saved here (or in the app); `notes_effective` is what Persona is told (hub.json's otherwise). */
  notes?: string;
  notes_effective?: string;
  /** "Notify main chat tasks in the main chat only" (#1731): on by default; tasks opened from now on follow it. */
  main_tasks_quiet?: boolean;
  /** "Start what I asked for in the chat right away" (#2559): on by default; tasks the user asked for start without the plan card's "Start". */
  start_requested_tasks?: boolean;
}

export function AssistantSettings({ bare }: { /** Without its header (the profile page has its own). */ bare?: boolean } = {}) {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner);
  return <ConfigurationManager>{(manager) => <AssistantSettingsForManager key={JSON.stringify([manager, owner])} manager={manager} owner={owner} bare={bare} />}</ConfigurationManager>;
}

function AssistantSettingsForManager({ manager, owner, bare }: { manager: string; owner: string; bare?: boolean }) {
  const [notesSaves] = useState(() => aboutMeSaves(manager, owner));
  const scopedApi: typeof api = (path, init) => api(path, { ...init, headers: { ...Object.fromEntries(new Headers(init?.headers)), [MANAGER_HEADER]: manager } });
  const persona = usePersona();
  const machines = useStore((s) => s.machines);
  const [info, setInfo] = useState<AssistantState | null>(null);
  const [error, setError] = useState("");
  const [tasks, setTasks] = useState<Task[] | null>(null);
  const [files, setFiles] = useState<MemoryFile[] | null>(null);
  const [memoryError, setMemoryError] = useState("");
  const [open, setOpen] = useState<{ file: MemoryFile; index?: boolean } | null>(null);
  const [quietBusy, setQuietBusy] = useState(false);
  const [startBusy, setStartBusy] = useState(false);
  const [permissionBusy, setPermissionBusy] = useState(false);
  const setPermission = async (mode: string) => {
    setPermissionBusy(true);
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { default_permission_mode: mode } });
      if (r.manager !== manager || r.default_permission_mode !== mode) throw new Error(t("settings-assistantsettings.default_permissions_unconfirmed"));
      setInfo((i) => i ? { ...i, default_permission_mode: r.default_permission_mode } : i);
    } catch (e) {
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setPermissionBusy(false); }
  };

  const setQuiet = async (on: boolean) => {
    setQuietBusy(true);
    setInfo((i) => (i ? { ...i, main_tasks_quiet: on } : i));
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { main_tasks_quiet: on } });
      setInfo((i) => (i ? { ...i, main_tasks_quiet: r.main_tasks_quiet ?? on } : i));
    } catch (e) {
      setInfo((i) => (i ? { ...i, main_tasks_quiet: !on } : i));
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setQuietBusy(false);
    }
  };

  const setStartRequested = async (on: boolean) => {
    setStartBusy(true);
    setInfo((i) => (i ? { ...i, start_requested_tasks: on } : i));
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { start_requested_tasks: on } });
      setInfo((i) => (i ? { ...i, start_requested_tasks: r.start_requested_tasks ?? on } : i));
    } catch (e) {
      setInfo((i) => (i ? { ...i, start_requested_tasks: !on } : i));
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setStartBusy(false);
    }
  };

  const loadMemory = async () => {
    try {
      const r = await scopedApi<{ files: MemoryFile[]; dir?: string }>("/assistant/memory");
      setFiles(r.files ?? []);
      setMemoryError("");
    } catch (e) {
      setMemoryError(errorText(e));
      setFiles((f) => f ?? []);
    }
  };

  const load = () => {
    setError("");
    scopedApi<AssistantState>("/assistant").then(setInfo, (e) => setError(errorText(e)));
    scopedApi<{ tasks: Task[] }>("/assistant/tasks").then((r) => setTasks(r.tasks ?? []), () => setTasks([]));
    void loadMemory();
  };
  useEffect(load, []);

  const machine = machines.find((m) => m.id === info?.machine_id);
  const machineName = machine?.name || info?.machine_id || "";
  const entries = useMemo(() => memoryEntries(files ?? []), [files]);
  const index = files?.find((f) => f.name === MEMORY_INDEX);
  const on = tasks?.filter((task) => task.enabled).length ?? 0;
  const next = tasks ? upcoming(tasks, 1)[0] : undefined;

  if (!info && !error) return <Loading />;

  return (
    <>
      {!bare && <div className="mb-6 flex items-center gap-3 px-1">
        <PersonaAvatar
          size={44}
          fallback={
            <span className="flex size-11 shrink-0 items-center justify-center rounded-full bg-ink text-surface">
              <Icon name="spark" size={18} />
            </span>
          }
        />
        <div className="min-w-0 flex-1">
          <div className="text-[15px] font-semibold text-ink">{persona.name}</div>
          <div className="truncate text-[12px] text-ink-3">{machineName ? t("settings-assistantsettings.subtitle_on", { machine: machineName }) : t("settings-assistantsettings.subtitle")}</div>
        </div>
        {machine && <Pill tone={machine.online ? "green" : undefined}>{machine.online ? t("settings-assistantsettings.online") : t("settings-assistantsettings.offline")}</Pill>}
      </div>}
      {error && <div className="mb-4 flex items-center gap-3">
        <p role="alert" className="min-w-0 flex-1 text-[13px] break-words text-red">{error}</p>
        <Button icon="retry" onClick={load}>{t("settings-assistantsettings.retry")}</Button>
      </div>}

      <Group title={t("settings-assistantsettings.proactive_title")} footer={t("settings-assistantsettings.proactive_footer")}>
        <Row
          label={t("settings-assistantsettings.routines")}
          hint={tasks === null ? undefined : next ? t("settings-assistantsettings.next_task", { when: momentLabel(next.at, new Date()), title: next.title }) : t("settings-assistantsettings.nothing_scheduled")}
          onClick={() => navigate({ name: "routines" })}
        >
          {tasks !== null && <span>{on === 0 ? t("settings-assistantsettings.routines_off") : t("settings-assistantsettings.routines_on", { count: on })}</span>}
        </Row>
      </Group>

      <Group title={t("settings-assistantsettings.notifications_title")} footer={t("settings-assistantsettings.notifications_footer")}>
        <Row label={t("settings-assistantsettings.quiet_tasks")}>
          <Switch on={info?.main_tasks_quiet ?? true} onChange={(on) => void setQuiet(on)} disabled={!info || quietBusy} label={t("settings-assistantsettings.quiet_tasks")} />
        </Row>
      </Group>

      <Group title={t("settings-assistantsettings.start_title")} footer={t("settings-assistantsettings.start_footer")}>
        <Row label={t("settings-assistantsettings.start_requested")}>
          <Switch on={info?.start_requested_tasks ?? true} onChange={(on) => void setStartRequested(on)} disabled={!info || startBusy} label={t("settings-assistantsettings.start_requested")} />
        </Row>
      </Group>

      <Group title={t("settings-nav.tab_model_config")}>
        <a href={BASE + settingsHref("model-config", "agent.new", false, manager)} className="flex items-center justify-between px-4 py-3 text-[13px] text-ink hover:bg-hover">
          <span>{t("settings-assistantsettings.task_models")}</span><Icon name="chevronRight" size={12} className="text-ink-3" />
        </a>
        <a href={BASE + settingsHref("model-config", "assistant", false, manager)} className="flex items-center justify-between px-4 py-3 text-[13px] text-ink hover:bg-hover">
          <span>{t("settings-assistantsettings.main_chat_model")}</span><Icon name="chevronRight" size={12} className="text-ink-3" />
        </a>
      </Group>

      <Group title={t("settings-assistantsettings.default_permissions")} footer={t("settings-assistantsettings.default_permissions_hint")}>
        <Row label={t("settings-assistantsettings.default_permissions")}>
          <select aria-label={t("settings-assistantsettings.default_permissions")} value={info?.default_permission_mode ?? ""} disabled={!info || info.manager !== manager || permissionBusy}
            className="min-w-0 max-w-full rounded-control bg-field px-3 py-2 text-[13px] text-ink shadow-inset-field"
            onChange={(e) => void setPermission(e.target.value)}>
            <option value="" disabled>{t("settings-assistantsettings.default_permissions_unset")}</option>
            {PERMISSION_MODES.map((mode) => <option key={mode.id} value={mode.id}>{mode.title}</option>)}
          </select>
        </Row>
      </Group>

      <Group title={t("settings-assistantsettings.knows_title")} footer={t("settings-assistantsettings.knows_footer")}>
        {info && <AboutMe notesSaves={notesSaves} info={info} onSaved={(notes) => setInfo((i) => (i ? { ...i, notes, notes_effective: notes.trim() } : i))} />}
        <div className="flex items-center gap-2 px-4 pt-3 pb-1.5">
          <span className="flex-1 text-[13px] font-medium text-ink">{t("settings-assistantsettings.memory")}</span>
          {files && entries.length > 0 && <span className="text-[11.5px] text-ink-3 tabular-nums">{t("settings-assistantsettings.memory_count", { count: entries.length })}</span>}
        </div>
        {files === null ? (
          <div className="px-4 py-4">
            <Loading />
          </div>
        ) : entries.length === 0 ? (
          <p className="px-4 py-4 text-[12.5px] leading-relaxed text-ink-3">{memoryError ? t("settings-assistantsettings.memory_load_failed", { error: memoryError }) : t("settings-assistantsettings.memory_empty")}</p>
        ) : (
          entries.map((e) => <MemoryRow key={e.file.name} e={e} onOpen={() => setOpen({ file: e.file })} />)
        )}
        {index && entries.length > 0 && (
          <button type="button" onClick={() => setOpen({ file: index, index: true })} className="flex w-full items-center gap-3 whitespace-nowrap px-4 py-2.5 text-left transition-colors hover:bg-hover">
            <Icon name="list" size={14} className="shrink-0 text-ink-3" />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[13px] text-ink">{t("settings-assistantsettings.index")}</span>
              <span className="block truncate text-[11.5px] text-ink-3">{t("settings-assistantsettings.index_hint")}</span>
            </span>
            <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
          </button>
        )}
      </Group>

      <SkillsGroup manager={manager} />

      <Group
        title={t("settings-assistantsettings.runs_on_title")}
        footer={machineName ? t("settings-assistantsettings.runs_on_footer", { machine: machineName }) : t("settings-assistantsettings.runs_on_none")}
      >
        <Row label={t("settings-assistantsettings.machine")}>
          <span className="max-w-[220px] truncate">{machineName || t("settings-assistantsettings.machine_none")}</span>
        </Row>
        <Row label={t("settings-assistantsettings.cwd")} mono>
          <span className="max-w-[300px] truncate" title={info?.cwd}>{info?.cwd || "~/.persona/assistant"}</span>
        </Row>
        {info?.timezone && <Row label={t("settings-assistantsettings.timezone")}>{info.timezone}</Row>}
      </Group>

      {open && (
        <MemoryDialog
          scopedApi={scopedApi}
          key={open.file.name}
          file={open.file}
          title={open.index ? t("settings-assistantsettings.index") : entries.find((e) => e.file.name === open.file.name)?.title ?? open.file.name}
          readOnly={open.index}
          onClose={() => setOpen(null)}
          onChanged={() => void loadMemory()}
        />
      )}
    </>
  );
}

/** "About me": what Persona is told about you. Until saved here it shows hub.json's text; saving (even empty) makes this the
 *  one. Saved with "Save", and when leaving the page with changes. */
function AboutMe({ notesSaves, info, onSaved }: { notesSaves: ReturnType<typeof aboutMeSaves>; info: AssistantState; onSaved: (notes: string) => void }) {
  const original = info.notes ?? info.notes_effective ?? "";
  const [text, setText] = useState(() => notesSaves.pending?.value ?? notesSaves.saved ?? original);
  const [base, setBase] = useState(() => notesSaves.saved ?? original);
  const [busy, setBusy] = useState(false);
  const changed = text !== base;
  const latest = useRef({ text, changed });
  latest.current = { text, changed };
  const editor = useRef({ generation: 0, mounted: false });

  const acknowledge = async (value: string, promise: Promise<boolean>, quiet: boolean, notify = true) => {
    const generation = editor.current.generation;
    if (editor.current.mounted) setBusy(true);
    const current = () => notesSaves.current(generation) && (quiet || editor.current.mounted);
    try {
      if (!await promise || !current()) return;
      if (editor.current.mounted) {
        // Acknowledging A advances the baseline only; a newer edited B stays dirty.
        setBase(value);
        onSaved(value);
        void refreshAssistant().catch(() => {});
      }
      if (notify) toast(t(quiet ? "settings-assistantsettings.about_saved" : "settings-assistantsettings.saved_new_chats"), "ok");
    } catch (e) {
      if (current() && (notify || latest.current.text === value)) toast(t("settings-assistantsettings.about_save_failed", { error: errorText(e) }), "error");
    } finally {
      if (current() && editor.current.mounted) setBusy(false);
    }
  };
  const save = (value = text, quiet = false) => acknowledge(value, notesSaves.save(value), quiet);

  useEffect(() => {
    editor.current = { generation: notesSaves.attach(), mounted: true };
    // A remount may have read the hub before its predecessor's queued write committed.
    const pending = notesSaves.pending;
    if (pending) void acknowledge(pending.value, pending.promise, true, false);
    else if (notesSaves.saved !== undefined) { setBase(notesSaves.saved); onSaved(notesSaves.saved); }
    return () => {
      editor.current.mounted = false;
      const { text: value, changed: dirty } = latest.current;
      if (dirty || (notesSaves.pending && notesSaves.pending.value !== value)) void save(value, true);
      notesSaves.detach();
    };
  }, [notesSaves]);

  return (
    <div className="px-4 py-3">
      <div className="mb-2 flex items-center gap-2">
        <span className="flex-1 text-[13px] font-medium text-ink">{t("settings-assistantsettings.about_me")}</span>
        {info.notes === undefined && original && <span className="text-[11px] text-ink-3">{t("settings-assistantsettings.from_hub")}</span>}
      </div>
      <textarea
        className={cx(textareaClass, "min-h-[132px]")}
        rows={6}
        value={text}
        aria-label={t("settings-assistantsettings.about_me")}
        placeholder={t("settings-assistantsettings.about_placeholder")}
        onChange={(e) => setText(e.target.value)}
      />
      <div className="mt-2 flex items-start gap-3">
        <p className="min-w-0 flex-1 text-[11.5px] leading-relaxed text-ink-3">
          {t("settings-assistantsettings.about_note")}
          {info.notes === undefined && original ? t("settings-assistantsettings.about_note_hub") : ""}
        </p>
        {changed && <Button tone="quiet" disabled={busy} onClick={() => setText(base)}>{t("settings-assistantsettings.revert")}</Button>}
        <Button tone="primary" busy={busy} disabled={!changed} onClick={() => void save()}>{t("common.save")}</Button>
      </div>
    </div>
  );
}

function MemoryRow({ e, onOpen }: { e: MemoryEntry; onOpen: () => void }) {
  const type = memoryTypeLabel(e.type);
  return (
    <button type="button" onClick={onOpen} className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-hover">
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-center gap-2">
          <span className="truncate text-[13px] text-ink">{e.title}</span>
          {type && <Tag tone={memoryTypeTone(e.type)}>{type}</Tag>}
        </span>
        {e.detail && <span className="mt-0.5 block truncate text-[11.5px] text-ink-3">{e.detail}</span>}
      </span>
      <span className="shrink-0 text-[11px] text-ink-3">{ago(e.file.updated_at)}</span>
      <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
    </button>
  );
}

/** One memory rendered; "Edit" opens the whole file (frontmatter included) as text, "Delete" asks first. The index is read-only. */
function MemoryDialog({ scopedApi, file, title, readOnly, onClose, onChanged }: { scopedApi: typeof api; file: MemoryFile; title: string; readOnly?: boolean; onClose: () => void; onChanged: () => void }) {
  const [content, setContent] = useState(file.content);
  const [updated, setUpdated] = useState(file.updated_at);
  const [editing, setEditing] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const parsed = parseMemory(content);
  const type = memoryTypeLabel(parsed.type);

  const save = async () => {
    if (editing === null) return;
    setBusy(true);
    try {
      await scopedApi(`/assistant/memory/${enc(file.name)}`, { method: "PUT", json: { content: editing } });
      setContent(editing);
      setUpdated(new Date().toISOString());
      setEditing(null);
      toast(t("settings-assistantsettings.memory_saved"), "ok");
      onChanged();
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    try {
      await scopedApi(`/assistant/memory/${enc(file.name)}`, { method: "DELETE" });
      toast(t("settings-assistantsettings.deleted", { title }), "ok");
      onChanged();
      onClose();
    } catch (e) {
      toast(errorText(e), "error");
      setBusy(false);
    }
  };

  const footer = readOnly ? (
    <Button tone="quiet" onClick={onClose}>{t("settings-assistantsettings.close")}</Button>
  ) : editing !== null ? (
    <>
      <span className="mr-auto min-w-0 truncate font-mono text-[11px] text-ink-3">{file.name}</span>
      <Button tone="quiet" disabled={busy} onClick={() => setEditing(null)}>{t("common.cancel")}</Button>
      <Button tone="primary" busy={busy} disabled={editing === content} onClick={() => void save()}>{t("common.save")}</Button>
    </>
  ) : confirm ? (
    <>
      <span className="mr-auto min-w-0 text-[12px] text-ink-2">{t("settings-assistantsettings.delete_confirm", { title })}</span>
      <Button tone="quiet" disabled={busy} onClick={() => setConfirm(false)}>{t("common.cancel")}</Button>
      <Button tone="danger" busy={busy} onClick={() => void remove()}>{t("settings-assistantsettings.delete")}</Button>
    </>
  ) : (
    <>
      <Button tone="quiet" icon="trash" className="text-red enabled:hover:text-red" onClick={() => setConfirm(true)}>{t("settings-assistantsettings.delete")}</Button>
      <span className="flex-1" />
      <Button tone="secondary" icon="pencil" onClick={() => setEditing(content)}>{t("settings-assistantsettings.edit")}</Button>
    </>
  );

  return (
    <Dialog open onClose={() => !busy && onClose()} title={title} width={600} footer={footer}>
      {editing !== null ? (
        <textarea
          className={cx(textareaClass, "min-h-[320px] font-mono text-[12.5px]")}
          value={editing}
          autoFocus
          spellCheck={false}
          aria-label={t("settings-assistantsettings.edit_label", { name: file.name })}
          onChange={(e) => setEditing(e.target.value)}
        />
      ) : (
        <div className="-mx-4 max-h-[min(60vh,560px)] overflow-y-auto px-4">
          {!readOnly && (type || parsed.description || updated) && (
            <div className="mb-3 border-b border-line pb-3">
              <div className="flex flex-wrap items-center gap-2">
                {type && <Tag tone={memoryTypeTone(parsed.type)}>{type}</Tag>}
                {updated && <span className="text-[11.5px] text-ink-3">{t("settings-assistantsettings.updated", { when: ago(updated) })}</span>}
              </div>
              {parsed.description && <p className="mt-1.5 text-[12.5px] text-ink-2">{parsed.description}</p>}
            </div>
          )}
          <Markdown text={(readOnly ? content : parsed.body) || t("settings-assistantsettings.empty_body")} />
          {!readOnly && <p className="mt-3 font-mono text-[11px] text-ink-3">{file.name}</p>}
        </div>
      )}
    </Dialog>
  );
}
