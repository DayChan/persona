import { useEffect, useState } from "react";
import { ApiError, errorText } from "../../lib/api";
import { modelProfiles, rollbackModelProfile, switchModelProfile, type ModelProfiles as Profiles } from "../../lib/model-profiles";
import { onHubEvent } from "../../lib/store";
import { t } from "../../i18n";
import { Button, Dialog } from "../../ui/atoms";
import { Group, Pill, Row } from "./SettingsParts";

export function ModelProfiles({ manager, onChanged }: { manager?: string; onChanged: () => void }) {
  const [data, setData] = useState<Profiles | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const load = async () => {
    try { setData(await modelProfiles(manager)); setError(""); }
    catch (e) { if (!(e instanceof ApiError && e.status === 404)) setError(errorText(e)); }
  };
  useEffect(() => { void load(); return onHubEvent("model_profile", () => { void load(); onChanged(); }); }, []);
  const apply = async (id?: string) => {
    setBusy(true); setError("");
    try { setData(await (id ? switchModelProfile(id, manager) : rollbackModelProfile(manager))); setSelected(null); onChanged(); }
    catch (e) { setError(errorText(e)); }
    finally { setBusy(false); }
  };
  const profile = data?.profiles.find((p) => p.id === selected);
  if (!data && !error) return null; // old hubs do not expose profiles
  return <>
    <Group title={t("settings-modelprofiles.title")} footer={t("settings-modelprofiles.footer")}>
      {data?.profiles.map((p) => <Row key={p.id} label={p.name} hint={p.description} onClick={() => { setSelected(p.id); setError(""); }}>
        {p.active && <Pill>{t("settings-modelprofiles.active")}</Pill>}
      </Row>)}
      {data?.rollback && <Row label={t("settings-modelprofiles.rollback")} hint={data.rollback.to || t("settings-modelprofiles.custom")}>
        <Button tone="quiet" busy={busy} onClick={() => void apply()}>{t("settings-modelprofiles.rollback")}</Button>
      </Row>}
    </Group>
    {!profile && error && <p role="alert" className="mb-4 text-[13px] text-red">{error}</p>}
    <Dialog open={!!profile} title={profile?.name ?? ""} onClose={() => { if (!busy) { setSelected(null); setError(""); } }} footer={<>
      <Button tone="quiet" disabled={busy} onClick={() => { setSelected(null); setError(""); }}>{t("common.cancel")}</Button>
      <Button busy={busy} onClick={() => profile && void apply(profile.id)}>{t("settings-modelprofiles.apply")}</Button>
    </>}>
      <p className="mb-4 text-[12px] text-ink-3">{profile?.description}</p>
      <p className="mb-4 text-[12px] text-ink-3">{t("settings-modelprofiles.changes", { count: profile?.changes.length ?? 0 })}</p>
      <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-control bg-field p-3 text-[12px] text-ink">{JSON.stringify({ purposes: profile?.purposes, launchers: profile?.launchers, tiers: profile?.tiers }, null, 2)}</pre>
      {error && <p role="alert" className="mt-3 text-[12px] text-red">{error}</p>}
    </Dialog>
  </>;
}
