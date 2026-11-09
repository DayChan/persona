// "Extensions": what extends this hub (docs/extensions/extensions.md), installed, upgraded, rolled back, switched and uninstalled from here as
// `personad extension` does on the hub's machine. Installing runs a new program there, so nothing installs before the
// page has shown what it may do (the preview) and the user agreed to exactly that (its permissions digest).
import { t } from "../../i18n";
import { tRich } from "../../i18n/rich";
import { useCallback, useEffect, useRef, useState } from "react";
import { previewBlocked } from "../../lib/extension-model";
import { bytes } from "../../lib/format";
import { Button, Collapse, cx, Dialog, toast, useNow } from "../../ui/atoms";
import { Switch } from "../../views/settings/SettingsParts";
import { AdminError, errorText, getExtensions, getMetricsOverview, installExtension, previewExtension, rollbackExtension, setExtensionEnabled, uninstallExtension, upgradeExtension, uploadExtension } from "../api";
import { AdminIcon, type AdminIconName } from "../icons";
import { canRollBack, canUpgrade, extensionRunNote, extensionSourceLabel, extensionStatus, pointLabel, sourceProblem, uploadProblem } from "../logic";
import { devDialog } from "../router";
import { ExtensionMarkets } from "./ExtensionMarket";
import { Permissions, PreviewBody } from "./ExtensionPreview";
import { ExtensionRunning, RunColumns } from "./ExtensionRunning";
import type { AdminExtension, ExtensionOverview, ExtensionPreview } from "../types";
import { Card, Dot, EmptyState, ErrorBox, Field, Loader, Notice, PageHeader, Segmented, Tag, useLoad, useVisiblePoll } from "../parts";

export function Extensions() {
  const { data, error, loading, reload, set } = useLoad(getExtensions);
  const [installing, setInstalling] = useState(() => ["install", "upload", "preview"].includes(devDialog()));
  const [upgrading, setUpgrading] = useState<AdminExtension | null>(null);
  const [removing, setRemoving] = useState<AdminExtension | null>(null);
  const [rolling, setRolling] = useState<AdminExtension | null>(null);

  // Dev only (screenshots): ?dialog=upgrade / uninstall opens it on the first extension it fits.
  const [devOpened, setDevOpened] = useState(false);
  useEffect(() => {
    const d = devDialog();
    if (!data || devOpened || (d !== "upgrade" && d !== "uninstall")) return;
    setDevOpened(true);
    const p = data.extensions.find(canUpgrade) ?? null;
    if (d === "upgrade") setUpgrading(p);
    else setRemoving(p);
  }, [data, devOpened]);

  // An extension's state moves on its own (starting, running, idle).
  useVisiblePoll(reload, 15_000, Boolean(data));

  // How each runs, over the last day (docs/extensions/extensions.md "Activity"); a hub from before #1857 has none (null).
  const [running, setRunning] = useState<Record<string, ExtensionOverview> | null>(null);
  const live = useRef(true);
  const loadRunning = useCallback(() => {
    void getMetricsOverview("24h").then(
      (o) => live.current && setRunning(Object.fromEntries(o.extensions.map((x) => [x.id, x]))),
      () => live.current && setRunning(null),
    );
  }, []);
  useEffect(() => {
    live.current = true;
    loadRunning();
    return () => {
      live.current = false;
    };
  }, [loadRunning]);
  useVisiblePoll(loadRunning, 60_000);

  const replace = (p: AdminExtension) => data && set({ ...data, extensions: data.extensions.map((x) => (x.id === p.id ? p : x)) });
  const missing = error instanceof AdminError && (error.status === 404 || error.code === "no_plugins");

  return (
    <>
      <PageHeader
        title={t("admin-nav.extensions")}
        subtitle={t("admin-pages-extensions.subtitle")}
        right={
          data?.can_install && (
            <Button tone="primary" onClick={() => setInstalling(true)}>
              <AdminIcon name="plus" size={13} />
              {t("admin-pages-extensions.install_extension")}
            </Button>
          )
        }
      />
      {missing ? (
        <Card>
          <EmptyState icon="puzzle" title={t("admin-pages-extensions.unsupported_title")}>
            {tRich("admin-pages-extensions.unsupported_detail", { command: <span className="font-mono">personad extension …</span> })}
          </EmptyState>
        </Card>
      ) : !data ? (
        loading ? <Loader /> : <ErrorBox error={error} onRetry={reload} />
      ) : (
        <>
          {/* A failed poll keeps the last list on screen; say so, or its states look live. */}
          {error != null && <ErrorBox error={error} onRetry={reload} />}
          {!data.can_install && (
            <Notice tone="gray" icon="info" title={t("admin-pages-extensions.no_db_title")}>
              {t("admin-pages-extensions.no_db_detail")}
            </Notice>
          )}
          {/* the markets first, above the installed list (#5017) */}
          {data.can_install && <ExtensionMarkets onChanged={reload} />}
          <Card
            title={t("admin-pages-extensions.count", { count: data.extensions.length })}
            footer={t("admin-pages-extensions.list_footer")}
          >
            {data.extensions.length === 0 ? (
              <EmptyState icon="puzzle" title={t("admin-pages-extensions.empty_title")} action={data.can_install && <Button onClick={() => setInstalling(true)}>{t("admin-pages-extensions.install_extension")}</Button>}>
                {t("admin-pages-extensions.empty_detail")}
              </EmptyState>
            ) : (
              data.extensions.map((p) => (
                <ExtensionRow key={p.id} p={p} run={running?.[p.id]} running={running !== null} onChanged={replace} onUpgrade={() => setUpgrading(p)} onRollBack={() => setRolling(p)} onRemove={() => setRemoving(p)} />
              ))
            )}
          </Card>
        </>
      )}
      <InstallDialog open={installing} uploadMost={data?.upload_most ?? 0} onClose={() => setInstalling(false)} onDone={reload} />
      <InstallDialog open={Boolean(upgrading)} upgrade={upgrading ?? undefined} uploadMost={data?.upload_most ?? 0} onClose={() => setUpgrading(null)} onDone={reload} />
      <RollBackDialog extension={rolling} onClose={() => setRolling(null)} onDone={(p) => (replace(p), setRolling(null))} />
      <UninstallDialog extension={removing} onClose={() => setRemoving(null)} onDone={() => (setRemoving(null), reload())} />
    </>
  );
}

function ExtensionRow({
  p,
  run,
  running,
  onChanged,
  onUpgrade,
  onRollBack,
  onRemove,
}: {
  p: AdminExtension;
  /** how it ran over the last day; running: the hub counts at all */
  run?: ExtensionOverview;
  running: boolean;
  onChanged: (p: AdminExtension) => void;
  onUpgrade: () => void;
  onRollBack: () => void;
  onRemove: () => void;
}) {
  // Dev only (screenshots): ?dialog=details opens the rows that can roll back.
  const [open, setOpen] = useState(() => devDialog() === "details" && canRollBack(p));
  const [busy, setBusy] = useState(false);
  const st = extensionStatus(p);
  const toggle = async (on: boolean) => {
    setBusy(true);
    try {
      onChanged(await setExtensionEnabled(p.id, on));
      toast(on ? t("admin-pages-extensions.enabled", { name: p.name }) : t("admin-pages-extensions.disabled", { name: p.name }), "ok");
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      setBusy(false);
    }
  };
  const contributes = Object.entries(p.contributes ?? {});
  return (
    <div className="px-4 py-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="flex min-w-[200px] flex-1 items-center gap-3 text-left">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-[10px] bg-inset text-ink-2 shadow-hairline">
            <AdminIcon name="puzzle" size={16} />
          </span>
          <span className="min-w-0 flex-1">
            <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
              <span className="truncate text-[13.5px] font-medium text-ink">{p.name}</span>
              {p.version && <span className="font-mono text-[11.5px] text-ink-3">{p.version}</span>}
              {p.attached?.version && <Tag tone="red">{t("admin-logic.ext_program_version", { version: p.attached.version })}</Tag>}
              <Tag tone={p.source === "installed" ? "accent" : "gray"}>{extensionSourceLabel(p.source)}</Tag>
            </span>
            <span className="mt-1 flex min-w-0 items-center gap-1.5 text-[11.5px] text-ink-3">
              <Dot tone={st.tone} pulse={p.status === "starting"} />
              <span className="whitespace-nowrap">{st.label}</span>
              {p.description && (
                <>
                  <span>·</span>
                  <span className="truncate">{p.description}</span>
                </>
              )}
            </span>
          </span>
        </button>
        <RunColumns o={run} />
        {/* as wide as the widest row's buttons, so the run columns line up */}
        <div className={cx("ml-auto flex shrink-0 items-center justify-end gap-1.5", running && "sm:min-w-[248px]")}>
          {canUpgrade(p) && (
            <Button tone="quiet" className="h-7 px-2" onClick={onUpgrade}>
              <AdminIcon name="up" size={13} />
              {t("admin-pages-extensionmarket.upgrade")}
            </Button>
          )}
          {canRollBack(p) && (
            <Button tone="quiet" className="h-7 px-2" onClick={onRollBack}>
              <AdminIcon name="undo" size={13} />
              {t("admin-pages-extensions.roll_back")}
            </Button>
          )}
          {p.source === "installed" && (
            <button
              type="button"
              title={t("admin-pages-extensions.uninstall")}
              aria-label={t("admin-pages-extensions.uninstall_title", { name: p.name })}
              onClick={onRemove}
              className="flex size-7 shrink-0 items-center justify-center rounded-[8px] text-ink-3 transition-colors hover:bg-red-tint hover:text-red"
            >
              <AdminIcon name="trash" size={14} />
            </button>
          )}
          {!p.read_only && <Switch on={p.enabled} disabled={busy} onChange={(on) => void toggle(on)} label={t("admin-pages-extensions.enable_label", { name: p.name })} />}
          <button
            type="button"
            title={open ? t("admin-pages-extensions.collapse") : t("admin-pages-extensions.details")}
            aria-label={open ? t("admin-pages-extensions.collapse_label", { name: p.name }) : t("admin-pages-extensions.details_label", { name: p.name })}
            aria-expanded={open}
            onClick={() => setOpen((o) => !o)}
            className="flex size-7 shrink-0 items-center justify-center rounded-[8px] text-ink-3 transition-colors hover:bg-hover hover:text-ink"
          >
            <AdminIcon name="chevron" size={14} className={cx("transition-transform duration-200", open && "rotate-180")} />
          </button>
        </div>
      </div>
      {p.error && <p className="mt-2 ml-12 text-[12px] leading-relaxed text-red">{p.error}</p>}
      {p.needs_setup && p.needs_setup.length > 0 && (
        <p className="mt-2 ml-12 text-[12px] leading-relaxed text-orange">{t("admin-pages-extensions.needs_setup", { items: p.needs_setup.join(t("admin-pages-extensions.list_sep")) })}</p>
      )}
      <Collapse open={open} lazy>
        <div className="mt-3 ml-12 grid min-w-0 gap-3 pr-px pb-px">
          {p.permissions.length > 0 && <Permissions lines={p.permissions} title={t("admin-pages-extensions.can")} />}
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[12px]">
            {extensionRunNote(p) && <Meta label={t("admin-pages-extensions.now")}>{extensionRunNote(p)}</Meta>}
            {contributes.length > 0 && <Meta label={t("admin-pages-extensions.provides")}>{contributes.map(([point, ids]) => t("admin-pages-extensions.provides_item", { point: pointLabel(point), ids: ids.join(t("admin-pages-extensions.list_sep")) })).join(t("admin-pages-extensions.group_sep"))}</Meta>}
            {p.previous_version && <Meta label={t("admin-pages-extensions.previous_version")} mono>{p.previous_version}</Meta>}
            {p.origin && <Meta label={t("admin-pages-extensions.installed_from")} mono>{p.origin}</Meta>}
            {p.publisher && <Meta label={t("admin-pages-extensions.publisher")}>{p.publisher}</Meta>}
            {p.homepage && (
              <Meta label={t("admin-pages-extensions.homepage")}>
                <a href={p.homepage} target="_blank" rel="noreferrer noopener" className="text-accent-ink hover:underline">
                  {p.homepage}
                </a>
              </Meta>
            )}
            {p.read_only && <Meta label={t("admin-pages-extensions.changes")}>{t("admin-pages-extensions.changes_hub_json")}</Meta>}
          </dl>
          {p.warnings?.map((w) => (
            <p key={w} className="flex items-start gap-1.5 text-[12px] leading-relaxed text-orange">
              <AdminIcon name="alert" size={13} className="mt-0.5 shrink-0" />
              {w}
            </p>
          ))}
          {running && <ExtensionRunning id={p.id} />}
        </div>
      </Collapse>
    </div>
  );
}

function Meta({ label, children, mono }: { label: string; children: React.ReactNode; mono?: boolean }) {
  return (
    <>
      <dt className="whitespace-nowrap text-ink-3">{label}</dt>
      <dd className={cx("min-w-0 break-all text-ink-2", mono && "font-mono text-[11.5px]")}>{children}</dd>
    </>
  );
}

type Way = "path" | "url" | "remote" | "upload";

const WAYS: { value: Way; label: string; icon: AdminIconName }[] = [
  { value: "path", label: t("admin-pages-extensions.way_path"), icon: "folder" },
  { value: "url", label: t("admin-pages-extensions.way_url"), icon: "web" },
  { value: "upload", label: t("admin-pages-extensions.way_upload"), icon: "upload" },
  { value: "remote", label: t("admin-pages-extensions.way_remote"), icon: "link" },
];

const WAY_FIELD: Record<Exclude<Way, "upload">, { label: string; placeholder: string; hint: string }> = {
  path: {
    label: t("admin-pages-extensions.path_label"),
    placeholder: "/opt/persona-extensions/example-notes",
    hint: t("admin-pages-extensions.path_hint"),
  },
  url: {
    label: t("admin-pages-extensions.url_label"),
    placeholder: "https://example.com/example-notes-1.2.0.tar.gz#sha256=…",
    hint: t("admin-pages-extensions.url_hint"),
  },
  remote: {
    label: "socket",
    placeholder: "remote:rpc=/run/example-notes.sock,http=/run/example-notes-http.sock",
    hint: t("admin-pages-extensions.remote_hint"),
  },
};

// sourceOf is what the hub is asked to preview for a typed value.
function sourceOf(way: Way, value: string): string {
  const v = value.trim();
  if (way === "remote" && v && !v.startsWith("remote:")) return `remote:${v}`;
  return v;
}

function InstallDialog({ open, upgrade, uploadMost, onClose, onDone }: { open: boolean; upgrade?: AdminExtension; uploadMost: number; onClose: () => void; onDone: () => void }) {
  const [way, setWay] = useState<Way>("path");
  const [value, setValue] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [preview, setPreview] = useState<ExtensionPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!open) return;
    setWay(devDialog() === "upload" ? "upload" : "path");
    setValue("");
    setFile(null);
    setPreview(null);
    setError("");
    // Dev only (screenshots): ?dialog=preview shows a preview straight away.
    if (devDialog() === "preview" || devDialog() === "upgrade") void previewExtension("dir:/opt/persona-extensions/example-notes", upgrade?.id).then(setPreview, () => {});
  }, [open, upgrade?.id]);

  const typed = way === "upload" ? "" : sourceProblem(sourceOf(way, value));
  const fileProblem = way === "upload" && file ? uploadProblem(file, uploadMost) : "";
  const ready = way === "upload" ? Boolean(file) && !fileProblem : Boolean(value.trim()) && !typed;

  const look = async () => {
    setBusy(true);
    setError("");
    try {
      setPreview(way === "upload" && file ? await uploadExtension(file, upgrade?.id) : await previewExtension(sourceOf(way, value), upgrade?.id));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const agree = async () => {
    if (!preview) return;
    setBusy(true);
    setError("");
    try {
      const p = upgrade ? await upgradeExtension(upgrade.id, preview) : await installExtension(preview);
      toast(upgrade ? t("admin-pages-extensionmarket.upgraded", { name: p.name, version: p.version ?? "" }) : t("admin-pages-extensionmarket.installed", { name: p.name }), "ok");
      onDone();
      onClose();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const title = upgrade ? t("admin-pages-extensionmarket.upgrade_title", { name: upgrade.name }) : t("admin-pages-extensions.install_extension");
  const now = useNow(1000);
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      width={540}
      footer={
        preview ? (
          <>
            <Button tone="quiet" onClick={() => (setPreview(null), setError(""))}>
              {t("admin-pages-extensions.another")}
            </Button>
            <Button tone="primary" busy={busy} disabled={previewBlocked(preview, now)} onClick={() => void agree()}>
              {upgrade ? (preview.more_permissions ? t("admin-pages-extensionmarket.agree_upgrade") : t("admin-pages-extensionmarket.upgrade")) : t("admin-pages-extensionmarket.agree_install")}
            </Button>
          </>
        ) : (
          <>
            <Button tone="quiet" onClick={onClose}>
              {t("admin-parts.cancel")}
            </Button>
            <Button tone="primary" busy={busy} disabled={!ready} onClick={() => void look()}>
              {t("admin-pages-extensions.preview")}
            </Button>
          </>
        )
      }
    >
      {preview ? (
        <PreviewBody pv={preview} />
      ) : (
        <div className="grid gap-3">
          {upgrade && (
            <p className="text-[12.5px] leading-relaxed text-ink-2">
              {tRich("admin-pages-extensions.upgrade_intro", { version: <span className="font-mono text-ink">{upgrade.version}</span> })}
            </p>
          )}
          <Segmented value={way} options={WAYS} onChange={(w) => (setWay(w), setError(""))} label={t("admin-pages-extensions.where_from")} />
          {way === "upload" ? (
            <div>
              <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("admin-pages-extensions.package")}</span>
              <div className="flex flex-wrap items-center gap-2 rounded-control bg-inset px-3 py-2.5 shadow-hairline">
                <Button onClick={() => input.current?.click()}>
                  <AdminIcon name="upload" size={13} />
                  {t("admin-pages-extensions.choose_file")}
                </Button>
                <span className={cx("min-w-0 flex-1 truncate text-[12.5px]", file ? "text-ink" : "text-ink-3")}>{file ? `${file.name} · ${bytes(file.size)}` : t("admin-pages-extensions.no_file")}</span>
                <input ref={input} type="file" accept=".tar.gz,.tgz,.zip,application/gzip,application/zip" hidden onChange={(e) => (setFile(e.target.files?.[0] ?? null), (e.target.value = ""))} />
              </div>
              <span className={cx("mt-1 block text-[11.5px] leading-relaxed", fileProblem ? "text-red" : "text-ink-3")}>
                {fileProblem || t("admin-pages-extensions.upload_hint", { mb: Math.floor(uploadMost / (1 << 20)) || 200 })}
              </span>
            </div>
          ) : (
            <Field key={way} label={WAY_FIELD[way].label} value={value} onChange={setValue} placeholder={WAY_FIELD[way].placeholder} mono autoFocus error={value.trim() ? typed : ""} hint={WAY_FIELD[way].hint} />
          )}
          {error && <p className="text-[12.5px] leading-relaxed text-red">{error}</p>}
        </div>
      )}
      {preview && error && <p className="mt-3 text-[12.5px] leading-relaxed text-red">{error}</p>}
    </Dialog>
  );
}

function RollBackDialog({ extension, onClose, onDone }: { extension: AdminExtension | null; onClose: () => void; onDone: (p: AdminExtension) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => setError(""), [extension]);
  const go = async () => {
    if (!extension) return;
    setBusy(true);
    try {
      const p = await rollbackExtension(extension.id);
      toast(t("admin-pages-extensions.rolled_back", { name: p.name, version: p.version ?? "" }), "ok");
      onDone(p);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={Boolean(extension)}
      onClose={onClose}
      title={extension ? t("admin-pages-extensions.roll_back_title", { name: extension.name }) : ""}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("admin-parts.cancel")}
          </Button>
          <Button tone="primary" busy={busy} onClick={() => void go()}>
            {t("admin-pages-extensions.back_to", { version: extension?.previous_version ?? "" })}
          </Button>
        </>
      }
    >
      <p className="text-[12.5px] leading-relaxed text-ink-2">
        {tRich("admin-pages-extensions.roll_back_detail", { from: <span className="font-mono text-ink">{extension?.version}</span>, to: <span className="font-mono text-ink">{extension?.previous_version}</span> })}
      </p>
      {error && <p className="mt-2 text-[12.5px] text-red">{error}</p>}
    </Dialog>
  );
}

function UninstallDialog({ extension, onClose, onDone }: { extension: AdminExtension | null; onClose: () => void; onDone: () => void }) {
  const [keep, setKeep] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [warnings, setWarnings] = useState<string[] | null>(null);
  useEffect(() => (setKeep(false), setError(""), setWarnings(null)), [extension]);
  const go = async () => {
    if (!extension || busy || warnings) return;
    setBusy(true);
    try {
      const result = await uninstallExtension(extension.id, keep);
      if (result.warnings?.length) setWarnings(result.warnings);
      else {
        toast(t("admin-pages-extensions.uninstalled", { name: extension.name }), "ok");
        onDone();
      }
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={Boolean(extension)}
      onClose={() => !busy && (warnings ? onDone() : onClose())}
      title={extension ? t(warnings ? "admin-pages-extensions.uninstalled" : "admin-pages-extensions.uninstall_title", { name: extension.name }) : ""}
      footer={
        warnings ? <Button tone="primary" onClick={onDone}>{t("ui-atoms.close")}</Button> : <>
          <Button tone="quiet" onClick={onClose} disabled={busy}>
            {t("admin-parts.cancel")}
          </Button>
          <Button tone="danger" busy={busy} onClick={() => void go()}>
            {t("admin-pages-extensions.uninstall")}
          </Button>
        </>
      }
    >
      {warnings ? <div role="status">
        <p className="text-[12.5px] leading-relaxed text-ink-2">{t("settings-extensionmarket.uninstalled_warnings")}</p>
        <ul className="mt-3 space-y-2">
          {warnings.map((warning, index) => <li key={index} className="rounded-control bg-orange-tint px-3 py-2 text-[12.5px] leading-relaxed whitespace-pre-wrap break-all text-orange">{warning}</li>)}
        </ul>
      </div> : <>
      <p className="text-[12.5px] leading-relaxed text-ink-2">
        {t("admin-pages-extensions.uninstall_detail")}
      </p>
      <label className="mt-3 flex cursor-pointer items-start gap-2.5 rounded-control bg-inset px-3 py-2.5 shadow-hairline">
        <input type="checkbox" checked={keep} onChange={(e) => setKeep(e.target.checked)} className="mt-0.5 size-3.5 shrink-0 accent-[var(--accent)]" />
        <span className="min-w-0">
          <span className="block text-[12.5px] font-medium text-ink">{t("admin-pages-extensions.keep_data")}</span>
          <span className="mt-0.5 block text-[11.5px] leading-relaxed break-all text-ink-3">
            {tRich("admin-pages-extensions.keep_data_detail", { path: <span className="font-mono">~/.persona/extensions/data/{extension?.id}/</span> })}
          </span>
        </span>
      </label>
      {error && <p className="mt-2 text-[12.5px] text-red">{error}</p>}
      </>}
    </Dialog>
  );
}
