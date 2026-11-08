import { useCallback, useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { getExtensionMarketSettings, setExtensionMarketSettings, validMirrorURL, type ExtensionMarketSettings } from "../../lib/extension-market-settings";
import { Button, Loading } from "../../ui/atoms";
import { ConfigurationManager } from "./ConfigurationManager";
import { Group, Row, settingsInputClass } from "./SettingsParts";

/** Independent of the hub's updater: older hubs and failed update reads must not hide download settings. */
export function ExtensionDownloadMirrors() {
  return <ConfigurationManager title={t("settings-hubsettings.mirror_manager")} rowLabel={t("settings-hubsettings.mirror_manager_row")} hint={t("settings-hubsettings.mirror_manager_hint")} hideSingle>
    {(manager) => <MirrorEditor manager={manager} />}
  </ConfigurationManager>;
}

function MirrorEditor({ manager }: { manager: string }) {
  const [saved, setSaved] = useState<ExtensionMarketSettings | null>(null);
  const [draft, setDraft] = useState<ExtensionMarketSettings | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [readError, setReadError] = useState("");
  const [writeError, setWriteError] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  // Retire UI updates on unmount; the API tracks writes so a returning editor waits before rereading.
  const sequence = useRef(0);
  const load = useCallback(async () => {
    const n = ++sequence.current;
    setLoading(true);
    setReadError("");
    try {
      const value = await getExtensionMarketSettings(manager);
      if (n !== sequence.current) return;
      setSaved(value);
      setDraft(value);
    } catch (e) {
      if (n === sequence.current) setReadError(errorText(e));
    } finally {
      if (n === sequence.current) setLoading(false);
    }
  }, [manager]);
  useEffect(() => {
    void load();
    return () => { sequence.current++; };
  }, [load]);

  const save = async (value: ExtensionMarketSettings) => {
    if (!saved || loading || saving || readError) return;
    const n = ++sequence.current;
    setSaving(true);
    setConfirmed(false);
    setWriteError("");
    try {
      const result = await setExtensionMarketSettings(manager, value);
      if (n !== sequence.current) return;
      setSaved(result);
      setDraft(result);
      setConfirmed(true);
    } catch (e) {
      if (n === sequence.current) setWriteError(errorText(e));
    } finally {
      if (n === sequence.current) setSaving(false);
    }
  };
  const edit = (field: keyof ExtensionMarketSettings, value: string) => {
    setDraft((d) => d && { ...d, [field]: value });
    setConfirmed(false);
    setWriteError("");
  };
  const value = draft && { registry_mirror: draft.registry_mirror.trim(), catalog_mirror: draft.catalog_mirror.trim() };
  const invalid = !!value && (!validMirrorURL(value.registry_mirror) || !validMirrorURL(value.catalog_mirror));
  const dirty = !!saved && !!value && (value.registry_mirror !== saved.registry_mirror || value.catalog_mirror !== saved.catalog_mirror);
  const disabled = loading || saving || !saved || !!readError;

  return <Group title={t("settings-hubsettings.mirrors")} footer={t("settings-hubsettings.mirror_security")}>
    <p className="px-4 py-3 text-[12.5px] leading-relaxed text-ink-3">{t("settings-hubsettings.mirror_scope")}</p>
    {loading && <Loading />}
    {readError && <div className="flex items-center gap-3 px-4 py-3">
      <p role="alert" className="min-w-0 flex-1 text-[12.5px] break-words text-red">{t("settings-hubsettings.mirror_read_failed", { error: readError })}</p>
      <Button icon="retry" onClick={() => void load()}>{t("settings-hubsettings.retry")}</Button>
    </div>}
    {draft && <>
      <Row label={<label className="flex min-w-0 flex-col gap-1.5">
        <span className="truncate">{t("settings-hubsettings.registry_mirror")}</span>
        <input type="url" autoComplete="off" spellCheck={false} className={settingsInputClass} value={draft.registry_mirror}
          disabled={disabled} onChange={(e) => edit("registry_mirror", e.target.value)} aria-describedby="registry-mirror-hint" />
      </label>} />
      <p id="registry-mirror-hint" className="px-4 py-3 text-[12.5px] leading-relaxed text-ink-3">{t("settings-hubsettings.registry_mirror_hint")}</p>
      <Row label={<label className="flex min-w-0 flex-col gap-1.5">
        <span className="truncate">{t("settings-hubsettings.catalog_mirror")}</span>
        <input type="url" autoComplete="off" spellCheck={false} className={settingsInputClass} value={draft.catalog_mirror}
          disabled={disabled} onChange={(e) => edit("catalog_mirror", e.target.value)} aria-describedby="catalog-mirror-hint" />
      </label>} />
      <p id="catalog-mirror-hint" className="px-4 py-3 text-[12.5px] leading-relaxed text-ink-3">{t("settings-hubsettings.catalog_mirror_hint")}</p>
      {invalid && <p role="alert" className="px-4 py-3 text-[12.5px] break-words text-red">{t("settings-hubsettings.mirror_invalid")}</p>}
      {writeError && <p role="alert" className="px-4 py-3 text-[12.5px] break-words text-red">{t("settings-hubsettings.mirror_write_failed", { error: writeError })}</p>}
      {confirmed && <p role="status" className="px-4 py-3 text-[12.5px] text-green">{t("settings-hubsettings.mirror_saved")}</p>}
      <div className="flex justify-end gap-2 px-4 py-2.5">
        <Button disabled={disabled || !(saved?.registry_mirror || saved?.catalog_mirror)} onClick={() => void save({ registry_mirror: "", catalog_mirror: "" })}>
          {t("settings-hubsettings.mirror_clear")}
        </Button>
        <Button tone="accent" busy={saving} disabled={disabled || invalid || !dirty} onClick={() => value && void save(value)}>
          {t("settings-hubsettings.mirror_save")}
        </Button>
      </div>
    </>}
  </Group>;
}
