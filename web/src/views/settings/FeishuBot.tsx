// Settings › Connectors › Lark bot (docs/extensions/bots.md, the app's FeishuBotView): talk to Persona from Feishu. A row on the
// connectors page with the bot's state; its page (a dialog) has the status and the on / off switch, the app's
// credentials, pairing with a code sent to the bot, what the bot may do, the agents driven there, and how to make one.
// Each change goes to the hub at once and the page shows what the hub answered; a failed one leaves it as it was.
import { ChannelLink } from "./Channels";
import { useCallback, useEffect, useRef, useState } from "react";
import { errorText } from "../../lib/api";
import { t } from "../../i18n";
import { ago } from "../../lib/format";
import { documentPage, visiblePoll } from "../../lib/visible-poll";
import {
  botStateText, botStatus, botSummary, botTone, credentialsChanged, credentialsOf, credentialsUpdate, DEFAULT_FEATURES, DEFAULT_NUDGE_AFTER, livePairing, nudgeChoices, nudgeLabel, pairBot, pairingLeft,
  featureEdit, testBot, unpairBot, updateBot, type BotCredentials, type BotFeatures, type FeishuBotStatus,
} from "../../lib/feishu-bot";
import { Button, copyText, cx, Dialog, IconButton, Spinner, toast } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { Group, Pill, Row, Switch } from "./SettingsParts";

const controlClass = "h-8 rounded-control bg-field px-2.5 text-[12.5px] text-ink shadow-inset-field outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]";
const dotClass = { green: "bg-green", red: "bg-red", orange: "bg-orange" } as const;

const FEATURES: { key: keyof BotFeatures; icon: IconName; title: string; detail: string }[] = [
  { key: "main", icon: "spark", title: t("settings-feishubot.feature_main_title"), detail: t("settings-feishubot.feature_main_detail") },
  { key: "cards", icon: "hand", title: t("settings-feishubot.feature_cards_title"), detail: t("settings-feishubot.feature_cards_detail") },
  { key: "all_cards", icon: "bell", title: t("settings-feishubot.feature_all_cards_title"), detail: t("settings-feishubot.feature_all_cards_detail") },
  { key: "pushes", icon: "send", title: t("settings-feishubot.feature_pushes_title"), detail: t("settings-feishubot.feature_pushes_detail") },
  { key: "sync", icon: "loop", title: t("settings-feishubot.feature_sync_title"), detail: t("settings-feishubot.feature_sync_detail") },
  { key: "reports", icon: "list", title: t("settings-feishubot.feature_reports_title"), detail: t("settings-feishubot.feature_reports_detail") },
  { key: "board", icon: "pin", title: t("settings-feishubot.feature_board_title"), detail: t("settings-feishubot.feature_board_detail") },
  { key: "only_conclusions", icon: "check", title: t("settings-feishubot.feature_only_conclusions_title"), detail: t("settings-feishubot.feature_only_conclusions_detail") },
  { key: "agents", icon: "agent", title: t("settings-feishubot.feature_agents_title"), detail: t("settings-feishubot.feature_agents_detail") },
];

const STEPS = [
  t("settings-feishubot.step_create_app"),
  t("settings-feishubot.step_permissions"),
  t("settings-feishubot.step_events"),
  t("settings-feishubot.step_publish"),
];

/** The bot's state, kept by whoever shows it (the row and its page share one). */
function useBot() {
  const [status, setStatus] = useState<FeishuBotStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    setLoading(true);
    return botStatus()
      .then((s) => (setStatus(s), setError("")))
      .catch((e) => setError(t("settings-feishubot.status_error", { error: errorText(e) })))
      .finally(() => setLoading(false));
  }, []);
  useEffect(() => void load(), [load]);
  return { status, setStatus, loading, error, setError, load };
}

export function FeishuBot({ onChannel }: { onChannel?: (id: string) => void } = {}) {
  const bot = useBot();
  const [open, setOpen] = useState(() => new URLSearchParams(location.search).get("connector") === "feishu-bot");
  const tone = botTone(bot.status);
  return (
    <>
      <Group title={t("settings-feishubot.title")} footer={t("settings-feishubot.footer")}>
        <button type="button" onClick={() => setOpen(true)} className="flex w-full items-center gap-3 whitespace-nowrap px-4 py-3 text-left transition-colors hover:bg-hover">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-accent-tint text-accent-ink">
            <Icon name="send" size={16} />
          </span>
          <span className="min-w-0 flex-1">
            <span className="block truncate text-[13.5px] font-medium text-ink">{bot.status?.bot_name || t("settings-feishubot.title")}</span>
            <span className="mt-0.5 flex items-center gap-1.5 text-[11.5px] text-ink-3">
              <span className={cx("size-1.5 shrink-0 rounded-full", tone ? dotClass[tone] : "bg-line-strong")} />
              <span className="truncate">{bot.error ? t("settings-feishubot.status_unreadable") : botStateText(bot.status, bot.loading)}</span>
            </span>
          </span>
          {bot.status && <Pill tone={tone}>{botSummary(bot.status)}</Pill>}
          <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
        </button>
      </Group>
      {onChannel && <ChannelLink id="feishu" onOpen={onChannel} />}
      {open && <FeishuBotDialog bot={bot} onClose={() => setOpen(false)} />}
    </>
  );
}

function FeishuBotDialog({ bot, onClose }: { bot: ReturnType<typeof useBot>; onClose: () => void }) {
  const { status, setStatus, loading, error, setError, load } = bot;
  const [form, setForm] = useState<BotCredentials>(() => (status ? credentialsOf(status) : { appID: "", appSecret: "", domain: "feishu" }));
  const [saving, setSaving] = useState(false);
  const [confirmUnpair, setConfirmUnpair] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const changed = credentialsChanged(status, form);
  const pairing = livePairing(status, now);
  const features = status?.features ?? DEFAULT_FEATURES;
  const nudgeAfter = status?.nudge_after ?? DEFAULT_NUDGE_AFTER;
  // Read at answer time, not when the request (or the pairing poll) started: typing meanwhile must count.
  const changedRef = useRef(changed);
  changedRef.current = changed;

  // A newer status: fill the form unless something is being typed (replace: the answer to saving the credentials).
  const adopt = (s: FeishuBotStatus, replace = false) => {
    if (replace || !changedRef.current) setForm(credentialsOf(s));
    setStatus(s);
  };
  useEffect(() => {
    if (status && !changed) setForm(credentialsOf(status));
    // only when the hub's credentials change, not on each keystroke
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status?.app_id, status?.domain]);

  // Waiting for the code: tick the countdown and look again every 3 s until someone uses it, while the tab is shown, and
  // once right away on coming back. The status that carried the code is fresh, so visiblePoll's immediate first read is skipped.
  useEffect(() => {
    if (!pairing || status?.owner) return;
    const tick = setInterval(() => setNow(Date.now()), 1000);
    let started = false;
    const stop = visiblePoll(() => { if (started) void botStatus().then((s) => adopt(s), () => {}); }, 3000, documentPage);
    started = true;
    return () => (clearInterval(tick), stop());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pairing?.code, status?.owner]);

  const run = async (what: string, call: () => Promise<FeishuBotStatus>, done?: string, replace = false) => {
    setSaving(true);
    try {
      adopt(await call(), replace);
      setError("");
      if (done) toast(done, "ok");
      return true;
    } catch (e) {
      setError(t("settings-feishubot.error_with", { what, error: errorText(e) }));
      return false;
    } finally {
      setSaving(false);
    }
  };

  const test = async () => {
    setSaving(true);
    try {
      await testBot();
      toast(t("settings-feishubot.test_sent"), "ok");
      setError("");
    } catch (e) {
      setError(t("settings-feishubot.test_failed", { error: errorText(e) }));
    } finally {
      setSaving(false);
    }
  };

  const tone = botTone(status);
  const agents = status?.agents ?? [];

  return (
    <Dialog open onClose={onClose} title={t("settings-feishubot.title")} width={560}>
      <Group footer={t("settings-feishubot.footer")}>
        <div className="flex items-center gap-3 px-4 py-3">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-accent-tint text-accent-ink">
            <Icon name="send" size={16} />
          </span>
          <div className="min-w-0 flex-1">
            <div className="truncate text-[13.5px] font-medium text-ink">{status?.bot_name || t("settings-feishubot.title")}</div>
            <div className="mt-0.5 flex items-center gap-1.5 text-[11.5px] text-ink-3">
              <span className={cx("size-1.5 shrink-0 rounded-full", tone ? dotClass[tone] : "bg-line-strong")} />
              <span className="truncate" title={botStateText(status, loading)}>{botStateText(status, loading)}</span>
            </div>
          </div>
          {loading || saving ? <Spinner /> : <IconButton icon="retry" label={t("settings-feishubot.recheck")} onClick={() => void load()} />}
        </div>
        <Row label={t("settings-feishubot.enable")}>
          <Switch on={status?.enabled ?? false} disabled={!status || saving} label={t("settings-feishubot.enable")} onChange={(on) => void run(on ? t("settings-feishubot.enable_failed") : t("settings-feishubot.disable_failed"), () => updateBot({ enabled: on }))} />
        </Row>
        {!!status?.last_event_at && <Row label={t("settings-feishubot.last_event")}>{ago(new Date(status.last_event_at * 1000).toISOString())}</Row>}
        {error && <p role="alert" className="px-4 py-2.5 text-[12px] break-words text-red">{error}</p>}
      </Group>

      <Group title={t("settings-feishubot.credentials")} footer={status?.from_config ? t("settings-feishubot.credentials_from_config") : t("settings-feishubot.credentials_secret_note")}>
        <Row label="App ID">
          <input value={form.appID} onChange={(e) => setForm({ ...form, appID: e.target.value })} placeholder={t("settings-feishubot.required")} aria-label="App ID" autoComplete="off" spellCheck={false} className={cx(controlClass, "w-[240px] font-mono")} />
        </Row>
        <Row label="App Secret">
          <input
            type="password"
            value={form.appSecret}
            onChange={(e) => setForm({ ...form, appSecret: e.target.value })}
            placeholder={status?.has_secret ? t("settings-feishubot.secret_saved") : t("settings-feishubot.required")}
            aria-label="App Secret"
            autoComplete="new-password"
            className={cx(controlClass, "w-[240px]")}
          />
        </Row>
        <Row label={t("settings-feishubot.platform")}>
          <select value={form.domain} onChange={(e) => setForm({ ...form, domain: e.target.value })} aria-label={t("settings-feishubot.platform")} className={cx(controlClass, "w-[240px]")}>
            <option value="feishu">{t("settings-feishubot.platform_feishu")}</option>
            <option value="lark">Lark</option>
            {form.domain !== "feishu" && form.domain !== "lark" && <option value={form.domain}>{form.domain}</option>}
          </select>
        </Row>
        <div className="flex justify-end px-4 py-2.5">
          <Button tone="accent" busy={saving && changed} disabled={!changed || !form.appID.trim() || saving} onClick={() => void run(t("settings-feishubot.save_failed"), () => updateBot(credentialsUpdate(form)), t("settings-feishubot.credentials_saved"), true)}>
            {t("settings-feishubot.verify_save")}
          </Button>
        </div>
      </Group>

      {status?.has_secret && (
        <>
          <Group
            title={t("settings-feishubot.pairing")}
            footer={status.owner || pairing ? undefined : status.enabled ? t("settings-feishubot.pair_hint") : t("settings-feishubot.pair_enable_first")}
          >
            {status.owner ? (
              <>
                <Row label={t("settings-feishubot.paired")}>{status.owner.name || t("settings-feishubot.lark_user")}</Row>
                {status.target && <Row label={t("settings-feishubot.target")}>{status.target}</Row>}
                {confirmUnpair ? (
                  <div className="flex items-center gap-2 bg-red-tint/50 px-4 py-2.5">
                    <span className="min-w-0 flex-1 text-[12px] leading-snug text-ink-2">{t("settings-feishubot.unpair_confirm")}</span>
                    <Button tone="quiet" onClick={() => setConfirmUnpair(false)}>{t("common.cancel")}</Button>
                    <Button tone="danger" busy={saving} onClick={() => void run(t("settings-feishubot.unpair_failed"), unpairBot, t("settings-feishubot.unpaired")).then((ok) => ok && setConfirmUnpair(false))}>
                      {t("settings-feishubot.unpair")}
                    </Button>
                  </div>
                ) : (
                  <div className="flex justify-end gap-2 px-4 py-2.5">
                    <Button tone="quiet" icon="x" onClick={() => setConfirmUnpair(true)} className="text-red enabled:hover:text-red">
                      {t("settings-feishubot.unpair")}
                    </Button>
                    <Button icon="send" disabled={saving || !status.connected} onClick={() => void test()}>
                      {t("settings-feishubot.send_test")}
                    </Button>
                  </div>
                )}
              </>
            ) : pairing ? (
              <div className="flex items-end gap-3 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="text-[12px] text-ink-3">{t("settings-feishubot.pair_code_prompt")}</div>
                  <div className="mt-1 font-mono text-[28px] font-semibold tracking-[0.12em] text-ink tabular-nums select-all">{pairing.code}</div>
                  <div className="mt-0.5 text-[11.5px] text-ink-3 tabular-nums">{pairingLeft(pairing.expires_at, now)}</div>
                </div>
                <Button icon="copy" onClick={() => void copyText(pairing.code).then(() => toast(t("settings-feishubot.code_copied"), "ok"))}>
                  {t("settings-feishubot.copy_code")}
                </Button>
              </div>
            ) : (
              <div className="flex justify-end px-4 py-2.5">
                <Button tone="accent" icon="link" disabled={saving || !status.enabled} onClick={() => void run(t("settings-feishubot.pair_code_failed"), pairBot)}>
                  {t("settings-feishubot.pair")}
                </Button>
              </div>
            )}
          </Group>

          <Group title={t("settings-feishubot.features")} footer={t("settings-feishubot.features_footer")}>
            {FEATURES.map((f) => {
              // "All cards" needs "cards"; "only conclusions" needs task progress, without which the hub sends no conclusions either.
              const off = (f.key === "all_cards" && !features.cards) || (f.key === "only_conclusions" && features.reports === false);
              return (
                <div key={f.key} className={cx("flex items-start gap-3 px-4 py-2.5", off && "opacity-50")}>
                  <Icon name={f.icon} size={15} className="mt-0.5 shrink-0 text-accent" />
                  <div className="min-w-0 flex-1">
                    <div className="text-[13px] text-ink">{f.title}</div>
                    <div className="mt-0.5 text-[11.5px] leading-snug text-ink-3">{f.detail}</div>
                  </div>
                  <Switch on={features[f.key] ?? false} disabled={saving || off} label={f.title} onChange={(on) => void run(t("settings-feishubot.save_failed"), () => updateBot({ features: featureEdit(features, f.key, on) }))} />
                </div>
              );
            })}
            <div className="flex items-start gap-3 px-4 py-2.5">
              <Icon name="clock" size={15} className="mt-0.5 shrink-0 text-accent" />
              <div className="min-w-0 flex-1">
                <div className="text-[13px] text-ink">{t("settings-feishubot.nudge_title")}</div>
                <div className="mt-0.5 text-[11.5px] leading-snug text-ink-3">{t("settings-feishubot.nudge_detail")}</div>
              </div>
              <select
                value={nudgeAfter}
                disabled={saving}
                aria-label={t("settings-feishubot.nudge_title")}
                onChange={(e) => void run(t("settings-feishubot.save_failed"), () => updateBot({ nudge_after: Number(e.target.value) }))}
                className={cx(controlClass, "shrink-0")}
              >
                {nudgeChoices(nudgeAfter).map((m) => (
                  <option key={m} value={m}>
                    {nudgeLabel(m)}
                  </option>
                ))}
              </select>
            </div>
          </Group>
        </>
      )}

      {agents.length > 0 && (
        <Group title={t("settings-feishubot.agents")}>
          {agents.map((a) => (
            <Row key={a.session_id} label={a.title}>
              <span className="max-w-[160px] truncate">{a.machine_id}</span>
            </Row>
          ))}
        </Group>
      )}

      <Group title={t("settings-feishubot.howto")} footer={t("settings-feishubot.howto_footer")}>
        {STEPS.map((s, i) => (
          <div key={i} className="flex items-baseline gap-3 px-4 py-2">
            <span className="w-3 shrink-0 text-[11.5px] font-semibold text-accent tabular-nums">{i + 1}</span>
            <span className="text-[11.5px] leading-relaxed text-ink-3">{s}</span>
          </div>
        ))}
      </Group>

    </Dialog>
  );
}
