// Start an agent on one of your computers: machine, folder (recent ones first), a preset (a tier of the model profile in use,
// #2225) or launcher, model and effort for this start, Claude Code's ultracode switch (#3428: off each time, never
// remembered), the first message.
import { useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { hubLabel } from "../lib/mesh";
import { api, ApiError, credentialOwner, enc, errorText, getToken, isAbort, subscribeCredentialOwner } from "../lib/api";
import { defaultManager, useKnownDefault } from "../lib/manager-choice";
import { pickAttachment, releaseAttachments, type LocalAttachment } from "../lib/attachments";
import { browserStartKeys, launchAgent, presetPrompt, type NewAgentPreset } from "../lib/new-agent";
import { AttachmentTray } from "../ui/AttachmentTray";
import { modelPurpose, modelSummary, type ModelBinding } from "../lib/model-config";
import {
  defaultLabel, effortChoices, fitEffort, followedBinding, launcherAgent, launcherModels, machineLaunchers, modelName, oneOff, pickerLauncher, startPicks,
  takesEffort, takesTypedModel, tierLeftOut, forgetCustomModel, typedModelOffered, withoutCustom, type LauncherDefault, type PickerTier,
} from "../lib/launcher-models";
import { BYPASS_MODE, BYPASS_WARNING, EFFORTS, effortTitle, PERMISSION_MODES } from "../lib/session-actions";
import { baseName, shortPath } from "../lib/format";
import { addAgentWorktree, agentBranch, agentNameHint, suggestedPath, worktreeStatus, type WorktreeStatus } from "../lib/worktree";
import { getState, useStore } from "../lib/store";
import type { Launcher } from "../lib/types";
import { AgentMark } from "../ui/AgentMark";
import { Button, cx, Dialog, Spinner, Switch, toast } from "../ui/atoms";
import { Pill } from "./settings/SettingsParts";
import { startBlocked, ultracodeStartOffered } from "../lib/ultracode";
import { UltracodeStartRow } from "./chat/Ultracode";
import { Icon } from "../ui/icons";
import { ModelPicker, type ModelList } from "../ui/ModelPicker";
import { t } from "../i18n";
import { useAddComputer } from "./machines/AddComputerEntry";
import { currentHosts, engineName, startHosts } from "../lib/engines";
import { initialLauncher, launcherBlock } from "../lib/launcher-health";
import { navigate } from "../lib/router";
import { LauncherBlockedHelp } from "./LauncherBlockedHelp";

interface Dirs {
  path: string;
  parent?: string;
  entries: { name: string; path: string; is_git?: boolean }[];
  recent?: { path: string; name: string; last_used?: string }[];
}

export function NewAgent({ open, onClose, preset }: { open: boolean; onClose: () => void; /** Filled in when another page opens it (the knowledge base's "Open agent"). */ preset?: NewAgentPreset | null }) {
  const machines = useStore((s) => s.machines);
  const online = useMemo(() => machines.filter((m) => m.online), [machines]);
  // no computer at all (docs/assistant/onboarding.md 4.5): the "Computer" field says why it is empty and adds one, nothing else to fill in
  const loaded = useStore((s) => s.loaded);
  const none = loaded && machines.length === 0;
  const adding = useAddComputer();
  const [mid, setMid] = useState(() => localStorage.getItem("new.machine") ?? "");
  const [cwd, setCwd] = useState("");
  const [launcher, setLauncher] = useState("");
  const [launchers, setLaunchers] = useState<Launcher[]>([]);
  const [unified, setUnified] = useState(true);
  const [modelDefault, setModelDefault] = useState("");
  // the hub's "Default: …" for "New agent" (#2455); undefined from an older hub
  const [hubDefault, setHubDefault] = useState<LauncherDefault | null | undefined>(undefined);
  // "Models"' "New agent" as it applies now (provider, model, effort); null: the hub has no model configuration
  const [configured, setConfigured] = useState<ModelBinding | null>(null);
  const [loadingModels, setLoadingModels] = useState(false);
  const [launchersOf, setLaunchersOf] = useState(""); // the machine `launchers` and `configured` are for
  // the grayed-out launcher whose "how to fix" is open (#1643); "": none
  const [helpFor, setHelpFor] = useState("");
  // this start's model (an id from the list; "": the default) and effort ("": the launcher's), never saved
  const [models, setModels] = useState<ModelList>({ status: "idle" });
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  // the presets this computer offers (tiers of the profile in use, from `tiersProfile`), and the one picked: it fills the
  // launcher, model and effort as picking them by hand would; a pick by hand after it unmarks it. Never saved.
  const [tiers, setTiers] = useState<PickerTier[]>([]);
  const [tiersProfile, setTiersProfile] = useState("");
  const [tier, setTier] = useState<PickerTier | null>(null);
  // ultracode for this start: off whenever the sheet opens, never saved (the server's rule: no choice carries over)
  const [ultracode, setUltracode] = useState(false);
  const features = useStore((s) => s.features);
  // Claude Code's permission mode for this start ("": the launcher's), remembered per computer like iOS's mode.<machine>
  const [mode, setMode] = useState("");
  const [host, setHost] = useState(""); // the engine host to open in (#2576); "": the machine's default
  const [picking, setPicking] = useState(false);
  const [query, setQuery] = useState("");
  const [reload, setReload] = useState(0);
  const refreshModels = useRef(false);
  const search = useRef<HTMLInputElement>(null);
  const [dirs, setDirs] = useState<Dirs | null>(null);
  const [browse, setBrowse] = useState("");
  const [loadingDirs, setLoadingDirs] = useState(false);
  const [prompt, setPrompt] = useState(() => localStorage.getItem("draft:new-agent") ?? "");
  const keys = useMemo(browserStartKeys, []);
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  const [dragging, setDragging] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const submitted = useRef(false);
  const preparation = useRef<AbortController | null>(null);
  const promptRevision = useRef(0);
  const credential = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const knownDefault = useKnownDefault();
  const manager = machines.find((m) => m.id === mid)?.manager || knownDefault || defaultManager();
  const targetOnline = online.some((m) => m.id === mid);
  const retirePreparation = () => {
    const previous = preparation.current;
    preparation.current = null;
    previous?.abort();
  };
  const dismiss = () => {
    retirePreparation();
    // Keep admission closed until another opening, including events in the same React batch.
    submitted.current = true;
    onClose();
  };
  // a preset's folder, put in once its computer is the chosen one (choosing a computer loads that one's last folder)
  const presetCwd = useRef<{ mid: string; cwd: string } | null>(null);
  const attachmentsRef = useRef(attachments);
  attachmentsRef.current = attachments;

  useEffect(() => {
    if (!open) return;
    submitted.current = false;
    setEffort("");
    setTier(null);
    setUltracode(false);
    setInWorktree(false);
    setWtBranch("");
    setWtError("");
  }, [open]);
  // a worktree of its own (#6115): offered for a folder in a Git repository (what the computer says of it), off each
  // time the sheet opens
  const [inWorktree, setInWorktree] = useState(false);
  const [wtBranch, setWtBranch] = useState("");
  const [wtBusy, setWtBusy] = useState(false);
  const [wtError, setWtError] = useState("");
  // Cleanup fences completions even when the transport ignores abort. A target change starts a new admission scope.
  useLayoutEffect(() => {
    submitted.current = false;
    setWtBusy(false);
    setWtError("");
    return retirePreparation;
  }, [open, mid, cwd, manager, credential, targetOnline]);
  const [repo, setRepo] = useState<WorktreeStatus | null>(null);
  useEffect(() => {
    setRepo(null);
    const dir = cwd.trim();
    if (!open || !mid || !dir) return;
    const c = new AbortController();
    const timer = setTimeout(() => void worktreeStatus(mid, dir, c.signal).then((st) => { if (!c.signal.aborted) setRepo(st); }), 400);
    return () => {
      clearTimeout(timer);
      c.abort();
    };
  }, [open, mid, cwd]);
  const worktreeOffered = Boolean(repo?.path);
  useEffect(() => {
    if (!open || !preset) return;
    if (preset.prompt) {
      promptRevision.current++;
      setPrompt((draft) => presetPrompt(preset.prompt, draft));
    }
    if (preset.machineId && preset.cwd && online.some((m) => m.id === preset.machineId)) {
      presetCwd.current = { mid: preset.machineId, cwd: preset.cwd };
      setMid(preset.machineId);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, preset]);
  useEffect(() => {
    if (prompt) localStorage.setItem("draft:new-agent", prompt);
    else localStorage.removeItem("draft:new-agent");
  }, [prompt]);
  useEffect(() => () => releaseAttachments(attachmentsRef.current), []);

  const addFiles = (files: FileList | File[]) => {
    const picked: LocalAttachment[] = [];
    for (const file of Array.from(files)) {
      try { picked.push(pickAttachment(file)); }
      catch (e) { toast(errorText(e), "error"); }
    }
    setAttachments((current) => [...current, ...picked]);
  };

  useEffect(() => {
    // a preset's computer (set just above, in the same commit) is not overridden by the first one online
    if (open && !presetCwd.current && (!mid || !online.some((m) => m.id === mid)) && online[0]) setMid(online[0].id);
  }, [open, online, mid]);

  useEffect(() => {
    if (!open || !mid) return;
    localStorage.setItem("new.machine", mid);
    setLaunchers([]);
    setHelpFor("");
    const controller = new AbortController();
    setLauncher("");
    setTiers([]);
    setTier(null);
    setModelDefault("");
    setHubDefault(undefined);
    setConfigured(null);
    setLoadingModels(true);
    Promise.all([
      machineLaunchers(mid, { signal: controller.signal }),
      modelPurpose("agent.new").catch((e) => {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }),
    ])
      .then(([r, config]) => {
        if (controller.signal.aborted) return;
        const ls = r.launchers ?? [];
        setLaunchers(ls);
        setHubDefault(r.default);
        setTiers(r.tiers ?? []);
        setTiersProfile(r.tiers_profile?.name ?? "");
        setLaunchersOf(mid);
        setUnified(config !== null);
        if (config) {
          setModelDefault(modelSummary(config.purpose, config.providers));
          setConfigured(config.purpose.effective ?? {});
          setLauncher("");
        } else {
          // the last one used here, unless this computer can't run it now (#1643): then the first that can
          setLauncher(initialLauncher(localStorage.getItem(`new.launcher.${mid}`), ls));
        }
      })
      .catch((e) => { if (!controller.signal.aborted) toast(errorText(e), "error"); })
      .finally(() => { if (!controller.signal.aborted) setLoadingModels(false); });
    const p = presetCwd.current;
    if (p?.mid === mid) presetCwd.current = null;
    setCwd(p?.mid === mid ? p.cwd : (localStorage.getItem(`new.cwd.${mid}`) ?? ""));
    setMode(localStorage.getItem(`new.mode.${mid}`) ?? "");
    setBrowse("");
    return () => {
      controller.abort();
      setLaunchersOf("");
    };
  }, [open, mid]);

  useEffect(() => {
    if (!open || !mid) return;
    const controller = new AbortController();
    setLoadingDirs(true);
    api<Dirs>(`/machines/${enc(mid)}/dirs?path=${enc(browse)}`, { signal: controller.signal })
      .then((dirs) => { if (!controller.signal.aborted) setDirs(dirs); })
      .catch((error) => { if (!controller.signal.aborted && !isAbort(error)) setDirs(null); })
      .finally(() => { if (!controller.signal.aborted) setLoadingDirs(false); });
    return () => controller.abort();
  }, [open, mid, browse]);

  // The model list of the launcher that will run (the chosen one, or the model configuration's); a new machine or launcher starts over
  // from the default. Starting never waits for it.
  const followed = followedBinding(launcher, unified ? configured : null);
  const listLauncher = launchersOf === mid ? pickerLauncher(launcher, followed) : "";
  useEffect(() => {
    // a new launcher starts from its default, but a preset picked for it keeps its model (every pick by hand unmarks it)
    setModel(tier?.launcher === listLauncher ? (tier.model ?? "") : "");
    setPicking(false);
    setQuery("");
  }, [open, mid, listLauncher]);
  const pickTier = (x: PickerTier) => {
    setHelpFor("");
    if (tier?.name === x.name) {
      // picked again: back to the defaults (the model settings' launcher where there is one)
      setTier(null);
      if (unified) setLauncher("");
      setModel("");
      setEffort("");
      return;
    }
    // the list of another launcher is not this one's: until it is in, nothing is judged by it (the preset's effort stays)
    if (x.launcher !== listLauncher) setModels({ status: "loading" });
    setTier(x);
    setLauncher(x.launcher);
    setModel(x.model ?? "");
    setEffort(x.effort ?? "");
  };
  useEffect(() => {
    if (!open || !mid || !listLauncher) {
      setModels({ status: "idle" });
      return;
    }
    const controller = new AbortController();
    const refresh = refreshModels.current;
    refreshModels.current = false;
    setModels((m) => ({ status: "loading", list: refresh ? m.list : undefined }));
    launcherModels(mid, listLauncher, { refresh, signal: controller.signal })
      .then((list) => {
        if (controller.signal.aborted) return;
        setModels({ status: "ready", list });
        // a typed ID the list doesn't have stays picked (#1644); an older hub's null has no picker
        setModel((id) => (list ? id : ""));
      })
      .catch((e) => { if (!controller.signal.aborted) setModels({ status: "error", error: errorText(e) }); });
    return () => controller.abort();
  }, [open, mid, listLauncher, reload]);
  const reloadModels = () => {
    refreshModels.current = true;
    setReload((n) => n + 1);
  };
  // Forget an ID typed before (#1644): off the list at once, back where it was when the hub says no.
  const forgetModel = (id: string) => {
    const list = models.list;
    if (!list || !mid || !listLauncher) return;
    const { list: without, restore } = withoutCustom(list, id);
    setModels((m) => ({ ...m, list: without }));
    forgetCustomModel(mid, listLauncher, id).catch((e) => {
      setModels((m) => (m.list ? { ...m, list: restore(m.list) } : m));
      toast(t("newagent.forget_failed", { id, error: errorText(e) }), "error");
    });
  };
  useEffect(() => { if (picking) search.current?.focus(); }, [picking]);

  // Where it opens, when the machine enables more than one engine that can start sessions (#2576).
  const picked = online.find((m) => m.id === mid);
  const hosts = picked ? startHosts(picked) : [];
  const hostDefault = picked ? currentHosts(picked).default : "";
  useEffect(() => { setHost(""); }, [open, mid]);
  const agent = launcherAgent(listLauncher, launchers, models.list);
  // the launchers this computer can't start now (#1643), each said under the choices with a way to see how to fix it; the
  // one this start runs (the chosen one, or the one Model settings' default runs) first, said so: Start stays enabled
  const runs = launchersOf === mid ? listLauncher : "";
  const defaultRuns = unified && launchersOf === mid ? pickerLauncher("", followedBinding("", configured)) : "";
  const defaultBlock = defaultRuns ? launcherBlock(launchers.find((l) => l.name === defaultRuns)) : null;
  const blocked = launchers
    .flatMap((l) => {
      const block = launcherBlock(l);
      return block ? [{ l, block }] : [];
    })
    .sort((a, b) => Number(b.l.name === runs) - Number(a.l.name === runs));
  const help = blocked.find((b) => b.l.name === helpFor);
  // a model ID can be typed (#1644): Claude Code or Codex whose launcher passes the pick on, also with an empty list or none read
  const typing = typedModelOffered(agent, models.list) && models.list !== null;
  // a launcher that passes its own --model keeps its locked row where an ID could otherwise be typed, empty list or not
  const lockedTyping = takesTypedModel(agent) && Boolean(models.list?.locked);
  const pickedModel = models.list?.models.find((m) => m.id === model);
  const efforts = effortChoices(models.list, model, followed);
  const effortsKey = efforts.join(" ");
  useEffect(() => { setEffort((e) => fitEffort(e, effortsKey ? effortsKey.split(" ") : [])); }, [effortsKey]);
  // offered with Claude Code whose effort can be set here; grayed when this computer's Claude Code is known to be too old
  const ultracodeShown = !loadingModels && efforts.length > 0 && ultracodeStartOffered(features, agent);
  // what of the picked preset this launcher can't take, said once its list is in
  const tierMissed = tier && listLauncher === tier.launcher && models.status === "ready" ? tierLeftOut(tier, { agent, list: models.list, followed }) : [];
  const ultracodeBlocked = ultracodeShown ? startBlocked(picked?.claude_version) : "";

  const start = () => {
    if (!open || submitted.current || loadingModels || !online.some((m) => m.id === mid) || !cwd.trim()) return;
    submitted.current = true;
    const controller = new AbortController();
    preparation.current = controller;
    const owner = credentialOwner();
    const token = getToken();
    const revision = promptRevision.current;
    const snapshot = { text: prompt.trim(), attachments: [...attachments] };
    const current = () => preparation.current === controller && !controller.signal.aborted && owner === credentialOwner()
      && getState().machines.some((m) => m.id === mid && m.online)
      && manager === (getState().machines.find((m) => m.id === mid)?.manager || defaultManager());
    localStorage.setItem(`new.cwd.${mid}`, cwd.trim());
    if (launcher && !unified) localStorage.setItem(`new.launcher.${mid}`, launcher);
    const picks = startPicks({ agent, list: models.list, model, effort, followed });
    // a Codex keeps its own modes; the choice stays for this computer's next Claude Code
    const permissionMode = takesEffort(agent) ? mode : "";
    if (takesEffort(agent)) {
      if (mode) localStorage.setItem(`new.mode.${mid}`, mode);
      else localStorage.removeItem(`new.mode.${mid}`);
    }
    const opensIn = host && host !== currentHosts(picked ?? {}).default ? host : undefined; // only a choice away from the default
    // only an explicit "on" is sent: off is what a new Claude is anyway
    const ultra = ultracodeShown && !ultracodeBlocked && ultracode ? true : undefined;
    const launch = (dir: string) => {
      if (!current()) return;
      // The launch owns its previews; removing or unmounting the editable draft cannot revoke them.
      const sent = snapshot.attachments.map((a) => ({ ...a, url: a.url ? URL.createObjectURL(a.file) : undefined }));
      launchAgent({ machineId: mid, cwd: dir, launcher, ...picks, permissionMode: permissionMode || undefined, host: opensIn, ultracode: ultra, text: snapshot.text, attachments: sent });
      const ids = new Set(snapshot.attachments.map((a) => a.id));
      releaseAttachments(attachmentsRef.current.filter((a) => ids.has(a.id)));
      attachmentsRef.current = attachmentsRef.current.filter((a) => !ids.has(a.id));
      setAttachments(attachmentsRef.current);
      if (promptRevision.current === revision) setPrompt("");
      dismiss();
    };
    // in a worktree of its own (#6115): made first; a failure says why here, and nothing starts
    if (inWorktree && worktreeOffered) {
      setWtBusy(true);
      setWtError("");
      void addAgentWorktree(mid, cwd.trim(), agentNameHint(prompt, cwd), wtBranch, { signal: controller.signal, token, tokenOwner: owner })
        .then(
          (wt) => launch(wt.path),
          (e) => {
            if (!current()) return;
            submitted.current = false;
            setWtError(errorText(e));
          },
        )
        .finally(() => {
          if (!current()) return;
          preparation.current = null;
          setWtBusy(false);
        });
      return;
    }
    launch(cwd.trim());
  };

  if (none)
    return (
      <>
        <Dialog
          open={open}
          onClose={dismiss}
          title={t("newagent.title")}
          width={520}
          footer={
            <Button tone="quiet" onClick={dismiss}>
              {t("common.cancel")}
            </Button>
          }
        >
          <Field label={t("newagent.computer")}>
            <div data-testid="new-agent-no-computer" className="flex flex-col items-start gap-3 rounded-control bg-inset px-3.5 py-3 shadow-hairline">
              <div className="flex items-start gap-2.5">
                <Icon name="machine" size={15} className="mt-0.5 shrink-0 text-ink-3" />
                <div className="flex min-w-0 flex-col gap-0.5">
                  <span className="text-[13px] font-medium text-ink">{t("newagent.no_computer_title")}</span>
                  <span className="text-[12.5px] leading-relaxed text-ink-2">{t("newagent.no_computer_body")}</span>
                </div>
              </div>
              <Button
                tone="primary"
                icon="plus"
                onClick={() => {
                  dismiss(); // the add flow is a dialog of its own, not one over this
                  adding.open();
                }}
              >
                {t("machines.add")}
              </Button>
            </div>
          </Field>
        </Dialog>
        {adding.dialog}
      </>
    );

  return (
    <>
      <Dialog
        open={open}
        onClose={dismiss}
        title={t("newagent.title")}
        width={520}
        footer={
          <>
            <span className="mr-auto min-w-0 truncate text-[11.5px] text-ink-3">{cwd ? shortPath(cwd) : t("newagent.pick_folder")}</span>
            <Button tone="quiet" onClick={dismiss}>
              {t("common.cancel")}
            </Button>
            <Button tone="primary" busy={wtBusy} disabled={loadingModels || !online.some((m) => m.id === mid) || !cwd.trim()} onClick={start}>
              {t("newagent.start")}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          <Field label={t("newagent.computer")}>
            <div className="flex flex-wrap gap-1.5">
              {machines.map((m) => (
                <button
                  key={m.id}
                  type="button"
                  disabled={!m.online}
                  onClick={() => setMid(m.id)}
                  className={cx(
                    "flex h-8 items-center gap-1.5 rounded-control px-2.5 text-[12.5px] whitespace-nowrap transition-[background-color,box-shadow,color] disabled:opacity-40",
                    mid === m.id ? "bg-ink font-medium text-surface" : "bg-surface text-ink-2 shadow-btn enabled:hover:bg-hover enabled:hover:text-ink",
                  )}
                >
                  <span className={cx("size-1.5 rounded-full", m.online ? "bg-green" : "bg-ink-3")} />
                  {m.name}
                  {hubLabel(m, machines) && <span className={mid === m.id ? "opacity-70" : "text-ink-3"}>{hubLabel(m, machines)}</span>}
                </button>
              ))}
            </div>
          </Field>

          <Field label={t("newagent.folder")}>
            <input
              value={cwd}
              onChange={(e) => setCwd(e.target.value)}
              placeholder={t("newagent.folder_placeholder")}
              spellCheck={false}
              className="h-9 w-full rounded-control bg-inset px-3 font-mono text-[12.5px] text-ink shadow-hairline outline-none placeholder:font-sans placeholder:text-ink-3 focus:shadow-[0_0_0_1px_var(--line-strong)]"
            />
            <div className="mt-1.5 max-h-52 overflow-y-auto rounded-control bg-surface p-1 shadow-hairline">
              {!browse && (dirs?.recent?.length ?? 0) > 0 && (
                <>
                  <div className="px-2 pt-1 pb-0.5 text-[10.5px] font-medium tracking-[0.06em] text-ink-3 uppercase">{t("newagent.recent")}</div>
                  {dirs!.recent!.slice(0, 6).map((d) => (
                    <DirRow key={d.path} name={d.name || baseName(d.path)} path={d.path} sub={shortPath(d.path)} selected={cwd === d.path} onPick={() => setCwd(d.path)} />
                  ))}
                </>
              )}
              <div className="flex items-center gap-1 px-2 pt-1.5 pb-0.5 text-[10.5px] font-medium tracking-[0.06em] text-ink-3 uppercase">
                {browse && dirs?.parent !== undefined && (
                  <button type="button" aria-label={t("newagent.parent_folder")} onClick={() => setBrowse(dirs!.parent!)} className="-ml-1 flex size-4.5 items-center justify-center rounded-[4px] hover:bg-hover hover:text-ink">
                    <Icon name="chevronLeft" size={12} stroke={2.4} />
                  </button>
                )}
                <span className="truncate normal-case tracking-normal">{dirs?.path ? shortPath(dirs.path) : t("newagent.home_folder")}</span>
                {loadingDirs && <Spinner size={10} />}
              </div>
              {dirs?.entries?.map((d) => (
                <DirRow key={d.path} name={d.name} path={d.path} git={d.is_git} selected={cwd === d.path} onPick={() => setCwd(d.path)} onOpen={() => setBrowse(d.path)} />
              ))}
            </div>
          </Field>
          {worktreeOffered && (
            <div data-testid="new-agent-worktree" className="-mt-2 rounded-control bg-surface px-3 py-2 shadow-hairline">
              <div className="flex items-center gap-2">
                <Icon name="branch" size={13} stroke={2.2} className="shrink-0 text-ink-2" />
                <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-ink">{t("newagent.worktree")}</span>
                <Switch on={inWorktree} onChange={setInWorktree} label={t("newagent.worktree")} />
              </div>
              <p className="mt-1 pl-[21px] text-[11.5px] leading-snug text-ink-3">{t("newagent.worktree_hint")}</p>
              {inWorktree && (
                <label className="mt-1.5 flex items-center gap-2 pl-[21px]">
                  <span className="shrink-0 text-[11.5px] text-ink-3">{t("newagent.worktree_branch")}</span>
                  <input
                    value={wtBranch}
                    onChange={(e) => {
                      setWtBranch(e.target.value);
                      setWtError("");
                    }}
                    placeholder={agentBranch(agentNameHint(prompt, cwd))}
                    spellCheck={false}
                    data-worktree-branch
                    className="h-7 min-w-0 flex-1 rounded-control bg-inset px-2 font-mono text-[12px] text-ink shadow-hairline outline-none placeholder:text-ink-3 focus:shadow-[0_0_0_1px_var(--line-strong)]"
                  />
                </label>
              )}
              {inWorktree && repo?.checkouts && (
                <p data-worktree-path className="mt-1 truncate pl-[21px] font-mono text-[11px] text-ink-3">
                  {t("newagent.worktree_path", { path: shortPath(suggestedPath(repo.checkouts, wtBranch.trim() || agentBranch(agentNameHint(prompt, cwd)))) })}
                </p>
              )}
              {wtError && <p role="alert" className="mt-1 pl-[21px] text-[11.5px] leading-snug text-red">{t("newagent.worktree_failed", { error: wtError })}</p>}
            </div>
          )}

          {hosts.length > 1 && (
            <Field label={t("lib-engines.start_in")}>
              <div role="radiogroup" aria-label={t("lib-engines.start_in")} data-testid="start-host" className="flex flex-wrap gap-1.5">
                {hosts.map((h) => (
                  <Choice key={h} radio selected={(host || hostDefault) === h} onClick={() => setHost(h)}>
                    {engineName(h, picked)}
                    {h === hostDefault ? ` · ${t("lib-engines.default")}` : ""}
                  </Choice>
                ))}
              </div>
            </Field>
          )}

          {tiers.length > 0 && (
            <Field label={t("newagent.preset")}>
              <div role="radiogroup" aria-label={t("newagent.preset")} className="flex flex-wrap gap-1.5">
                {tiers.map((x) => {
                  // its launcher failed its check here (#1643): still a choice, marked; the list below says why
                  const block = launcherBlock(launchers.find((l) => l.name === x.launcher));
                  return (
                    <Choice key={x.name} radio mono selected={tier?.name === x.name} onClick={() => pickTier(x)}
                      title={[x.description, tierRuns(x), block?.reason].filter(Boolean).join("\n")}>
                      <span className="flex items-center gap-1.5">
                        {block && <Icon name="warning" size={12} className="text-orange" />}
                        {x.name}
                      </span>
                    </Choice>
                  );
                })}
              </div>
              <p data-testid="new-agent-preset-hint" aria-live="polite" className={cx("mt-1.5 flex min-w-0 items-start gap-1.5 text-[11.5px]", tierMissed.length ? "text-orange" : "text-ink-3")}>
                {tierMissed.length > 0 && <Icon name="warning" size={12} className="mt-px shrink-0" />}
                <span className="min-w-0 [overflow-wrap:anywhere]">
                  {tierMissed.length
                    ? t("newagent.preset_left_out", { what: tierMissed.map((v) => (v === tier?.effort ? effortTitle(v) : v)).join(" · ") })
                    : tier
                      ? tier.description || tierRuns(tier)
                      : t("newagent.preset_hint", { profile: tiersProfile })}
                </span>
              </p>
            </Field>
          )}

          {(unified || launchers.length > 1 || blocked.length > 0) && (
            <Field label={unified ? t("newagent.launcher_override") : t("newagent.launcher")}>
              <div className="flex flex-wrap gap-1.5">
                {unified && (
                  <Choice selected={launcher === ""} onClick={() => { setTier(null); setLauncher(""); }} title={defaultBlock ? `${modelDefault} · ${defaultBlock.reason}` : modelDefault}>
                    {defaultBlock ? (
                      // what the default runs can't start here (#1643): marked, still the default, said in the list below
                      <span data-testid="new-agent-default-blocked" className="flex items-center gap-1.5">
                        <Icon name="warning" size={12} className="text-orange" />
                        {hubDefault ? defaultLabel(hubDefault) : t("newagent.follow_model_settings")}
                      </span>
                    ) : hubDefault ? (
                      defaultLabel(hubDefault)
                    ) : (
                      t("newagent.follow_model_settings")
                    )}
                  </Choice>
                )}
                {launchers.map((l) => {
                  const block = launcherBlock(l);
                  return (
                    <Choice
                      key={l.name}
                      mono
                      selected={launcher === l.name}
                      // grayed out with why, never hidden (#1643): choosing it shows how to fix it instead of picking it
                      blocked={Boolean(block)}
                      onClick={() => (block ? setHelpFor((open) => (open === l.name ? "" : l.name)) : (setTier(null), setLauncher(l.name)))}
                      title={block ? block.reason : l.model}
                    >
                      <span className="flex items-center gap-1.5">
                        {block ? <Icon name="warning" size={12} className="text-orange" /> : <AgentMark agent={l.agent_type || l.agent} size={13} className="text-current" />}
                        {l.name}
                      </span>
                    </Choice>
                  );
                })}
              </div>
              {blocked.length > 0 && (
                <ul data-testid="new-agent-blocked" className="mt-1.5 flex flex-col gap-1">
                  {blocked.map(({ l, block }) => (
                    // one flow of words that wraps (never cut off: a phone has no hover for a tooltip), the link a unit
                    <li key={l.name} data-runs={l.name === runs || undefined} className="flex min-w-0 items-start gap-1.5 text-[11.5px] leading-snug text-ink-3">
                      <Icon name="warning" size={12} className="mt-px shrink-0 text-orange" />
                      <p className="min-w-0 [overflow-wrap:anywhere]">
                        {l.name === runs && (
                          <>
                            <Pill tone="orange">{t("newagent.runs_this_time")}</Pill>{" "}
                          </>
                        )}
                        <span className="font-mono text-ink-2">{l.name}</span>{" "}
                        <span>{block.reason}</span>{" "}
                        <button
                          type="button"
                          aria-expanded={helpFor === l.name}
                          aria-controls={helpFor === l.name ? "new-agent-blocked-help" : undefined}
                          aria-label={t("newagent.how_to_fix_named", { name: l.name })}
                          onClick={() => setHelpFor((open) => (open === l.name ? "" : l.name))}
                          className="whitespace-nowrap text-accent-ink hover:underline"
                        >
                          {t("newagent.how_to_fix")}
                        </button>
                      </p>
                    </li>
                  ))}
                </ul>
              )}
              {help && (
                <LauncherBlockedHelp
                  name={help.l.name}
                  block={help.block}
                  onTryAnyway={() => {
                    setTier(null);
                    setLauncher(help.l.name);
                    setHelpFor("");
                  }}
                  onAddProvider={() => {
                    onClose();
                    navigate("/settings?tab=models&add=1"); // the provider wizard, as readiness's connect_model
                  }}
                  onClose={() => setHelpFor("")}
                />
              )}
              {unified && (
                <p className="mt-1.5 flex min-w-0 items-center gap-1.5 text-[11.5px] text-ink-3">
                  {launcher && <Pill tone="accent">{oneOff()}</Pill>}
                  <span className="min-w-0 truncate">{launcher ? t("newagent.override_hint") : modelDefault || t("newagent.default_hint")}</span>
                </p>
              )}
            </Field>
          )}

          {listLauncher && models.status !== "idle" && !(models.status === "ready" && !models.list?.models.length && !typing && !lockedTyping) && (
            <Field label={t("newagent.model")}><ModelPicker
              models={models}
              model={model}
              followed={followed}
              ownModel={launchers.find((l) => l.name === listLauncher)?.model}
              open={picking}
              onToggle={() => setPicking((o) => !o)}
              query={query}
              onQuery={setQuery}
              search={search}
              onPick={(id) => {
                setTier(null);
                setModel(id);
                setPicking(false);
                setQuery("");
              }}
              onReload={reloadModels}
              custom={typing}
              typing={typing}
              onForget={forgetModel}
              failedNote={t("newagent.models_failed")}
            /></Field>
          )}

          {takesEffort(agent) && (
            <Field label={t("newagent.permission_mode")}>
              <div role="radiogroup" aria-label={t("newagent.permission_mode")} className="flex flex-wrap gap-1.5">
                <Choice radio selected={mode === ""} onClick={() => setMode("")}>{t("newagent.follow_launcher")}</Choice>
                {PERMISSION_MODES.map((m) => (
                  <Choice key={m.id} radio selected={mode === m.id} onClick={() => setMode(m.id)} title={m.detail}>
                    {m.title}
                  </Choice>
                ))}
              </div>
              <p className={cx("mt-1.5 text-[11.5px]", mode === BYPASS_MODE ? "text-red" : "text-ink-3")}>
                {mode === BYPASS_MODE ? BYPASS_WARNING : PERMISSION_MODES.find((m) => m.id === mode)?.detail ?? t("newagent.permission_mode_hint")}
              </p>
            </Field>
          )}

          {!loadingModels && takesEffort(agent) && (
            <Field label={t("newagent.effort")}>
              {efforts.length > 0 ? (
                <div className="flex flex-wrap gap-1.5">
                  <Choice selected={effort === ""} onClick={() => { setTier(null); setEffort(""); }}>{followed.effort ? t("newagent.follow_model_settings") : t("newagent.follow_launcher")}</Choice>
                  {efforts.map((id) => (
                    <Choice key={id} selected={effort === id} onClick={() => { setTier(null); setEffort(id); }} title={EFFORTS.find((e) => e.id === id)?.detail}>
                      {effortTitle(id)}
                    </Choice>
                  ))}
                </div>
              ) : (
                <p className="flex h-7 items-center text-[11.5px] text-ink-3">
                  {pickedModel ? t("newagent.effort_unsupported", { model: modelName(pickedModel) }) : t("newagent.effort_unsupported_default")}
                </p>
              )}
              {ultracodeShown && <div data-testid="new-agent-ultracode" className="mt-2"><UltracodeStartRow on={ultracode} onChange={setUltracode} blocked={ultracodeBlocked} /></div>}
            </Field>
          )}

          <Field label={t("newagent.first_message_optional")}>
            <div className={cx("flex flex-col gap-2 rounded-control bg-inset shadow-hairline focus-within:shadow-[0_0_0_1px_var(--line-strong)]", dragging && "outline-2 outline-accent")}
              onDragOver={(e) => { if (e.dataTransfer.types.includes("Files")) { e.preventDefault(); setDragging(true); } }}
              onDragLeave={(e) => { if (!e.currentTarget.contains(e.relatedTarget as Node)) setDragging(false); }}
              onDrop={(e) => { e.preventDefault(); setDragging(false); addFiles(e.dataTransfer.files); }}>
              {attachments.length > 0 && <div className="px-3 pt-2"><AttachmentTray attachments={attachments} onRemove={(id) => {
                releaseAttachments(attachments.filter((a) => a.id === id));
                setAttachments((current) => current.filter((a) => a.id !== id));
              }} /></div>}
              <textarea
                value={prompt}
                onChange={(e) => { promptRevision.current++; setPrompt(e.target.value); }}
                onPaste={(e) => { if (e.clipboardData.files.length) { e.preventDefault(); addFiles(e.clipboardData.files); } }}
                onKeyDown={(e) => {
                  if (e.nativeEvent.isComposing || e.keyCode === 229) return;
                  if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); start(); }
                }}
                rows={3}
                aria-label={t("newagent.first_message")}
                placeholder={keys ? t("newagent.prompt_placeholder_keys", { keys }) : t("newagent.prompt_placeholder")}
                className="w-full resize-none bg-transparent px-3 py-2 text-[13px] leading-relaxed text-ink outline-none placeholder:text-ink-3"
              />
              <div className="flex items-center gap-2 px-2 pb-2">
                <input ref={fileInput} type="file" multiple hidden onChange={(e) => { if (e.currentTarget.files) addFiles(e.currentTarget.files); e.currentTarget.value = ""; }} />
                <button type="button" aria-label={t("newagent.add_files")} title={t("newagent.add_files")} onClick={() => fileInput.current?.click()} className="flex size-7 shrink-0 items-center justify-center rounded-control text-ink-3 hover:bg-hover hover:text-ink"><Icon name="plus" size={16} stroke={2} /></button>
                <span className="min-w-0 text-[11.5px] text-ink-3">{t("newagent.attach_hint")}</span>
              </div>
            </div>
          </Field>
        </div>
      </Dialog>
      {adding.dialog}
    </>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1.5 text-[12px] font-medium text-ink-2">{label}</div>
      {children}
    </div>
  );
}

/** What a preset runs on, in a line: its launcher, model and effort (its name). */
function tierRuns(x: PickerTier): string {
  return [x.launcher, x.model, x.effort && effortTitle(x.effort)].filter(Boolean).join(" · ");
}

function Choice({ selected, onClick, title, mono, radio, blocked, children }: { selected: boolean; onClick: () => void; title?: string; mono?: boolean; /** One of a radiogroup. */ radio?: boolean; /** Grayed out: this computer can't start it now (#1643); a click explains instead of picking. */ blocked?: boolean; children: React.ReactNode }) {
  return (
    <button
      type="button"
      role={radio ? "radio" : undefined}
      aria-checked={radio ? selected : undefined}
      aria-pressed={radio ? undefined : selected}
      aria-disabled={blocked || undefined}
      onClick={onClick}
      title={title}
      className={cx(
        "h-7 rounded-control px-2.5 text-[12px] whitespace-nowrap transition-colors",
        mono && "font-mono",
        selected ? "bg-ink text-surface" : "bg-surface text-ink-2 shadow-btn hover:bg-hover hover:text-ink",
        // dimmed like an offline computer's chip, and a little less so under the pointer: it still explains itself
        blocked && (selected ? "opacity-60" : "opacity-45 hover:opacity-70"),
      )}
    >
      {children}
    </button>
  );
}

// The model for this start: a row showing the pick, opening into a searchable list (80–110 models through a gateway).
function DirRow({ name, path, sub, git, selected, onPick, onOpen }: { name: string; path: string; sub?: string; git?: boolean; selected: boolean; onPick: () => void; onOpen?: () => void }) {
  return (
    <div className={cx("group/dir flex h-8 items-center gap-2 rounded-[6px] px-2 transition-colors pointer-coarse:min-h-11", selected ? "bg-accent-tint" : "hover:bg-hover")}>
      <button type="button" onClick={onPick} onDoubleClick={onOpen} className="flex min-w-0 flex-1 items-center gap-2 self-stretch text-left" title={path}>
        <Icon name={git ? "git" : "folder"} size={13} className={selected ? "text-accent-ink" : "text-ink-3"} />
        <span className={cx("shrink-0 truncate text-[12.5px]", selected ? "font-medium text-accent-ink" : "text-ink")}>{name}</span>
        {sub && <span className="min-w-0 truncate font-mono text-[11px] text-ink-3">{sub}</span>}
      </button>
      {onOpen && (
        <button type="button" aria-label={t("newagent.open_folder", { name })} onClick={onOpen} className="touch-hit flex size-5 items-center justify-center rounded-[4px] text-ink-3 opacity-0 transition-opacity group-hover/dir:opacity-100 hover:bg-hover-2 hover:text-ink focus-visible:opacity-100 [@media(hover:none)]:opacity-100">
          <Icon name="chevronRight" size={12} stroke={2.4} />
        </button>
      )}
    </div>
  );
}
