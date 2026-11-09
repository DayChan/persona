// Usage sources: the account-pool gateways (relays…) whose subscription windows join the sidebar's usage bars
// (docs/server/usage-windows.md"Account-pool gateways", #2595). Add, edit, switch and remove them, try one with a single read-only read.
// The hub keeps the keys in hub.json and never sends one back: a source only says whether it has one.
import { t } from "../../i18n";
import { useEffect, useRef, useState } from "react";
import { Button, Dialog, toast } from "../../ui/atoms";
import { Switch } from "../../views/settings/SettingsParts";
import { addUsageSource, editUsageSource, errorText, getUsageSources, removeUsageSource, saveUsageSettings, testUsageSource } from "../api";
import { AdminIcon } from "../icons";
import { devDialog } from "../router";
import type { UsageSource, UsageSourceType, UsageTestResult } from "../types";
import { hubWords, needsKeyAgain, POLL_CHOICES, sourceStatus, testText, typeLabel, validUrl } from "../usage-sources";
import { Card, ConfirmButton, EmptyState, ErrorBox, Field, Line, Loader, PageHeader, Segmented, Tag, useLoad } from "../parts";

const message = (e: unknown) => hubWords(e) ?? errorText(e);

export function UsageSourcesPage() {
  const { data, error, loading, reload, set } = useLoad(getUsageSources);
  const [dialog, setDialog] = useState<"add" | UsageSource | null>(null);
  const [opened, setOpened] = useState(false);
  // dev only (screenshots): ?dialog=usage-add / usage-edit opens that dialog once the list is in
  useEffect(() => {
    if (!data || opened) return;
    setOpened(true);
    const want = devDialog();
    if (want === "usage-add") setDialog("add");
    else if (want === "usage-edit" && data.sources[0]) setDialog(data.sources[0]);
  }, [data, opened]);

  const [busy, setBusy] = useState(false);
  const settings = async (patch: { enabled?: boolean; poll_minutes?: number }) => {
    setBusy(true);
    try {
      set(await saveUsageSettings(patch));
    } catch (e) {
      toast(message(e), "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <PageHeader
        title={t("admin-nav.usage")}
        subtitle={t("admin-pages-usage.subtitle")}
        right={
          data && (
            <Button tone="primary" disabled={data.sources.length >= data.limit} onClick={() => setDialog("add")}>
              <AdminIcon name="plus" size={13} />
              {t("admin-pages-usage.add")}
            </Button>
          )
        }
      />
      {!data ? (
        loading ? <Loader /> : <ErrorBox error={error} onRetry={reload} />
      ) : (
        <>
          <Card footer={t("admin-pages-usage.settings_footer")}>
            <Line label={t("admin-pages-usage.master")} hint={t("admin-pages-usage.master_hint")}>
              <Switch on={data.enabled} disabled={busy} onChange={(enabled) => void settings({ enabled })} label={t("admin-pages-usage.master")} />
            </Line>
            <Line label={t("admin-pages-usage.interval")} hint={t("admin-pages-usage.interval_hint")}>
              <Segmented
                label={t("admin-pages-usage.interval")}
                value={String(data.poll_minutes)}
                options={[...new Set([...POLL_CHOICES, data.poll_minutes])].sort((a, b) => a - b).map((m) => ({ value: String(m), label: t("admin-pages-usage.minutes", { count: m }) }))}
                onChange={(v) => !busy && void settings({ poll_minutes: Number(v) })}
              />
            </Line>
          </Card>
          <Card title={t("admin-pages-usage.sources", { count: data.sources.length })} footer={t("admin-pages-usage.footer")}>
            {data.sources.length === 0 ? (
              <EmptyState icon="pulse" title={t("admin-pages-usage.empty_title")} action={<Button onClick={() => setDialog("add")}>{t("admin-pages-usage.add")}</Button>}>
                {t("admin-pages-usage.empty_detail")}
              </EmptyState>
            ) : (
              data.sources.map((s) => <SourceRow key={s.id} s={s} globalOn={data.enabled} onEdit={() => setDialog(s)} onChanged={reload} />)
            )}
          </Card>
        </>
      )}
      <SourceDialog target={dialog} onClose={() => setDialog(null)} onSaved={() => (setDialog(null), reload())} />
    </>
  );
}

function SourceRow({ s, globalOn, onEdit, onChanged }: { s: UsageSource; globalOn: boolean; onEdit: () => void; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  const st = sourceStatus(s, globalOn);
  const run = async (fn: () => Promise<unknown>, done?: string) => {
    setBusy(true);
    try {
      await fn();
      if (done) toast(done, "ok");
      onChanged();
    } catch (e) {
      toast(message(e), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3">
      <span className="flex size-9 shrink-0 items-center justify-center rounded-[10px] bg-inset text-ink-2 shadow-hairline">
        <AdminIcon name="pulse" size={16} />
      </span>
      <div className="min-w-[200px] flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-[13.5px] font-medium text-ink">{s.name}</span>
          <Tag>{typeLabel(s.type)}</Tag>
          <Tag tone={st.tone}>{st.label}</Tag>
        </div>
        <div className="mt-1 truncate font-mono text-[11.5px] text-ink-3" title={s.url}>
          {s.url}
        </div>
        {st.detail && <div className={st.tone === "red" ? "mt-0.5 text-[11.5px] leading-relaxed text-red" : "mt-0.5 text-[11.5px] leading-relaxed text-ink-3"}>{st.detail}</div>}
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <Switch on={s.enabled} disabled={busy} onChange={(enabled) => void run(() => editUsageSource(s.id, { enabled }))} label={t("admin-pages-usage.enable_label", { name: s.name })} />
        <Button tone="quiet" onClick={onEdit}>
          <AdminIcon name="pencil" size={13} />
          {t("admin-pages-usage.edit")}
        </Button>
        <ConfirmButton label={t("admin-pages-usage.remove")} confirm={t("admin-pages-usage.remove")} busy={busy} onConfirm={() => run(() => removeUsageSource(s.id), t("admin-pages-usage.removed", { name: s.name }))} />
      </div>
    </div>
  );
}

function SourceDialog({ target, onClose, onSaved }: { target: "add" | UsageSource | null; onClose: () => void; onSaved: () => void }) {
  const saved = target && target !== "add" ? target : null;
  const [type, setType] = useState<UsageSourceType>("relay");
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [key, setKey] = useState("");
  const [clearKey, setClearKey] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<UsageTestResult | null>(null);
  const testSequence = useRef(0);
  const clearTest = () => {
    testSequence.current++;
    setResult(null);
    setError("");
  };

  // a fresh form each time it opens
  useEffect(() => {
    testSequence.current++;
    if (!target) return;
    setType(saved?.type ?? "relay");
    setName(saved?.name ?? "");
    setUrl(saved?.url ?? "");
    setKey("");
    setClearKey(false);
    setError("");
    setResult(null);
    return () => { testSequence.current++; };
  }, [target, saved]);

  const moved = saved ? needsKeyAgain(saved, { url, type }) : false;
  const urlOk = validUrl(url);
  const keyNeeded = type === "relay" && !(saved?.has_key && !moved && !clearKey) && key.trim() === "";
  const ok = urlOk && !keyNeeded && (type !== "generic" || name.trim() !== "");

  const test = async () => {
    const sequence = ++testSequence.current;
    setTesting(true);
    setResult(null);
    setError("");
    try {
      const answer = await testUsageSource({ id: saved?.id, type, url, key: key.trim() || undefined, clear_key: clearKey || undefined });
      if (sequence === testSequence.current) setResult(answer);
    } catch (e) {
      if (sequence === testSequence.current) setError(message(e));
    } finally {
      setTesting(false);
    }
  };
  const save = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!ok || busy) return;
    setBusy(true);
    setError("");
    try {
      const body = { name: name.trim() || undefined, type, url: url.trim(), key: key.trim() || undefined, clear_key: clearKey || undefined };
      const r = saved ? await editUsageSource(saved.id, body) : await addUsageSource(body);
      toast(t("admin-pages-usage.saved", { name: r.source?.name ?? name }), "ok");
      onSaved();
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
  };
  const verdict = result ? testText(result) : null;
  return (
    <Dialog
      open={Boolean(target)}
      onClose={onClose}
      title={saved ? t("admin-pages-usage.edit_title") : t("admin-pages-usage.add_title")}
      width={540}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button busy={testing} disabled={!urlOk || (keyNeeded && type === "relay")} onClick={() => void test()}>
            {t("admin-pages-usage.test")}
          </Button>
          <Button tone="primary" busy={busy} disabled={!ok} onClick={() => void save()}>
            {t("admin-pages-usage.save")}
          </Button>
        </>
      }
    >
      <form onSubmit={save} className="space-y-3.5">
        <div>
          <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("admin-pages-usage.type")}</span>
          <Segmented
            label={t("admin-pages-usage.type")}
            value={type}
            options={[
              { value: "relay", label: typeLabel("relay") },
              { value: "generic", label: typeLabel("generic") },
            ]}
            onChange={(v) => (setType(v), clearTest())}
          />
        </div>
        <Field label={t("admin-pages-usage.name")} value={name} onChange={setName} placeholder={typeLabel(type)} maxLength={40} hint={t("admin-pages-usage.name_hint")} />
        <Field
          label={t("admin-pages-usage.url")}
          value={url}
          onChange={(v) => (setUrl(v), clearTest())}
          placeholder="https://gateway.example.com"
          mono
          autoCapitalize="none"
          error={url.trim() !== "" && !urlOk ? t("admin-pages-usage.url_invalid") : undefined}
          hint={type === "relay" ? t("admin-pages-usage.url_hint_relay") : t("admin-pages-usage.url_hint_generic")}
        />
        <Field
          label={t("admin-pages-usage.key")}
          type="password"
          value={key}
          onChange={(v) => (setKey(v), clearTest())}
          placeholder={saved?.has_key && !moved && !clearKey ? t("admin-pages-usage.key_saved") : ""}
          mono
          autoComplete="off"
          autoCapitalize="none"
          disabled={clearKey}
          hint={moved && saved?.has_key ? t("admin-pages-usage.key_retype") : type === "relay" ? t("admin-pages-usage.key_hint_relay") : t("admin-pages-usage.key_hint_generic")}
        />
        {saved?.has_key && type === "generic" && (
          <label className="flex items-center gap-2 text-[12.5px] text-ink-2">
            <input type="checkbox" checked={clearKey} onChange={(e) => (setClearKey(e.target.checked), clearTest())} />
            {t("admin-pages-usage.clear_key")}
          </label>
        )}
        {verdict && (
          <div role="status" className={`flex items-start gap-2 rounded-control px-3 py-2 text-[12.5px] leading-relaxed break-words ${verdict.ok ? "bg-green-tint text-green" : "bg-red-tint text-red"}`}>
            <AdminIcon name={verdict.ok ? "check" : "alert"} size={14} className="mt-0.5 shrink-0" />
            <span className="min-w-0">{verdict.text}</span>
          </div>
        )}
        {error && (
          <div role="alert" className="flex items-start gap-2 rounded-control bg-red-tint px-3 py-2 text-[12.5px] leading-relaxed break-words text-red">
            <AdminIcon name="alert" size={14} className="mt-0.5 shrink-0" />
            <span className="min-w-0">{error}</span>
          </div>
        )}
        <p className="text-[11.5px] leading-relaxed text-ink-3">{t("admin-pages-usage.test_note")}</p>
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}
