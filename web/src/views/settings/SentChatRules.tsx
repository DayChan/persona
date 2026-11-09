import { useEffect, useRef, useState, type ReactNode } from "react";
import { t } from "../../i18n";
import { addChatRule, chatDestination, chatRuleProblem, chatRuleReason, listChatRules, revokeChatRule, sentOwner, type ChatRule, type ChatRules } from "../../lib/sent-chat-rules";
import { connectorName, whenText, type SentItem } from "../../lib/sent";
import { documentPage, visiblePoll } from "../../lib/visible-poll";
import { Button, Dialog, Loading } from "../../ui/atoms";
import { Group } from "./SettingsParts";

type Intent = { kind: "add"; row: SentItem } | { kind: "revoke"; rule: ChatRule };
type Failed = { rule: ChatRule; text: string };

/** Rules have their own lifetime and list: a ledger filter or pruning must not hide a permission. */
export function SentChatRules({ item, onChanging, onChanged, children }: { item: SentItem | null; onChanging: () => void; onChanged: () => Promise<void>; children: (action: ReactNode) => ReactNode }) {
  const [data, setData] = useState<ChatRules | null>(null);
  const [problem, setProblem] = useState<ReturnType<typeof chatRuleProblem> | null>(null);
  const [failed, setFailed] = useState<Map<string, Failed>>(() => new Map());
  const [intent, setIntent] = useState<Intent | null>(null);
  const [error, setError] = useState("");
  const [verificationFailed, setVerificationFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [reading, setReading] = useState(false);
  const intentRef = useRef<Intent | null>(null);
  const changeIntent = (value: Intent | null) => { intentRef.current = value; setIntent(value); };
  const abort = useRef<AbortController | null>(null);
  const life = useRef({ live: false, owner: sentOwner(), seq: 0, busy: false });
  const selection = useRef({ id: item?.id, generation: 0 });
  if (selection.current.id !== item?.id) selection.current = { id: item?.id, generation: selection.current.generation + 1 };
  const latest = useRef({ item, onChanging, onChanged });
  latest.current = { item, onChanging, onChanged };
  const current = () => life.current.live && life.current.owner === sentOwner();
  const read = async (verify = false) => {
    const l = life.current;
    if (!current() || (l.busy && !verify)) return;
    const seq = ++l.seq;
    abort.current?.abort();
    const controller = new AbortController(); abort.current = controller;
    setReading(true);
    try {
      const value = await listChatRules(controller.signal);
      if (!current() || seq !== l.seq) return;
      setData(value); setProblem(null);
    } catch (e) {
      if (current() && seq === l.seq) setProblem(chatRuleProblem(e));
      throw e;
    } finally { if (current() && seq === l.seq) setReading(false); }
  };
  useEffect(() => {
    const l = life.current;
    l.live = true;
    const stop = visiblePoll(() => { void read().catch(() => {}); }, 0, documentPage);
    return () => { l.live = false; l.seq++; abort.current?.abort(); stop(); };
    // This component is keyed by its owning settings lifetime; props do not restart reads.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const refreshRules = async () => {
    if (!current() || life.current.busy) return;
    if (!verificationFailed) { await read().catch(() => {}); return; }
    const l = life.current;
    l.busy = true; setBusy(true);
    const results = await Promise.allSettled([read(true), latest.current.onChanged()]);
    if (current()) { setVerificationFailed(results.some(r => r.status === "rejected")); l.busy = false; setBusy(false); }
  };
  const begin = (next: Intent) => {
    if (!current() || life.current.busy) return;
    setError(""); changeIntent(next);
  };
  const run = async (action: Intent) => {
    const l = life.current;
    if (!current() || l.busy || intentRef.current !== action || (action.kind === "add" && latest.current.item?.id !== action.row.id)) return;
    l.busy = true; l.seq++; latest.current.onChanging(); setBusy(true); setReading(false); setError(""); setVerificationFailed(false);
    const generation = selection.current.generation;
    const ownsSelection = () => action.kind === "revoke" || generation === selection.current.generation;
    let succeeded = false;
    try {
      if (action.kind === "add") await addChatRule(action.row.id);
      else await revokeChatRule(action.rule.id);
      if (!current()) return;
      succeeded = true;
      if (action.kind === "revoke") setFailed(have => { const next = new Map(have); next.delete(action.rule.id); return next; });
      if (ownsSelection()) changeIntent(null);
    } catch (e) {
      if (!current()) return;
      const p = chatRuleProblem(e, action.kind);
      if (action.kind === "add" && ownsSelection()) setError(p.text);
      if (action.kind === "revoke") {
        // A failed durable removal disappears from GET while remaining on disk. Keep the exact retry context.
        setFailed(have => new Map(have).set(action.rule.id, { rule: action.rule, text: p.text }));
        changeIntent(null);
      }
      if (p.unavailable && action.kind === "revoke") setProblem(p);
    } finally {
      if (current()) {
        // Reads admitted before/during the write cannot repopulate its old state.
        const results = await Promise.allSettled([read(true), latest.current.onChanged()]);
        if (current()) {
          if (succeeded) setVerificationFailed(results.some(r => r.status === "rejected"));
          l.busy = false; setBusy(false);
        }
      }
    }
  };
  useEffect(() => {
    if (intentRef.current?.kind === "add") { changeIntent(null); setError(""); }
  }, [item?.id]);
  const state = item?.chat_rule;
  const adding = intent?.kind === "add" && intent.row.id === item?.id;
  const unavailable = problem?.unavailable === true;
  const addAction = item && (
    <section className="flex flex-col items-start gap-3 text-sm text-ink-2">
      {state?.exists ? <p>{t("settings-sent.rule_exists")}</p>
        : !state ? <p>{t("settings-sent.rule_row_unsupported")}</p>
        : !state.can || !chatDestination(item) ? <p>{chatRuleReason(state.can ? "target" : state.reason)}</p>
        : adding ? <>
          <p className="break-words">{t(item.connector === "github" ? "settings-sent.rule_confirm_github" : "settings-sent.rule_confirm_lark", { connector: connectorName(item.connector), chat: chatDestination(item) })}</p>
          <p>{t("settings-sent.rule_scope")}</p>
          <div className="flex flex-wrap gap-2">
            <Button tone="primary" busy={busy} disabled={unavailable} onClick={() => void run(intent)}>{t("settings-sent.rule_allow")}</Button>
            <Button tone="quiet" disabled={busy} onClick={() => { changeIntent(null); setError(""); }}>{t("common.cancel")}</Button>
          </div>
        </> : <Button disabled={busy || unavailable || !data} onClick={() => begin({ kind: "add", row: item })}>{t("settings-sent.rule_add")}</Button>}
      {error && <p role="alert">{error}</p>}
      {unavailable && <p>{problem.text}</p>}
    </section>
  );
  const visible = new Map(data?.rules.map(r => [r.id, r]));
  for (const f of failed.values()) visible.set(f.rule.id, f.rule);
  return <>
    <Group title={t("settings-sent.rules_title")} footer={t("settings-sent.rules_footer")}>
      <div className="flex flex-col gap-3 px-4 py-3 text-sm text-ink-2">
        {data && <p>{t("settings-sent.rules_count", { count: data.rules.length, most: data.most })}</p>}
        {problem && <p role="alert">{problem.text}</p>}
        {!data && !problem && <Loading />}
        {data && !visible.size && <p>{t("settings-sent.rules_empty")}</p>}
        {[...visible.values()].map(rule => <div key={rule.id} className="flex flex-col items-start gap-2 border-t border-line pt-3">
          <p className="max-w-full break-words font-medium text-ink">{connectorName(rule.connector)} · {rule.chat}</p>
          {rule.summary && <p className="max-w-full break-words">{rule.summary}</p>}
          {rule.at && <time dateTime={rule.at}>{whenText(rule.at)}</time>}
          {failed.get(rule.id) && <p role="alert">{failed.get(rule.id)?.text}</p>}
          <Button disabled={busy || unavailable} onClick={() => { const action: Intent = { kind: "revoke", rule }; begin(action); if (failed.get(rule.id)) void run(action); }}>
            {t(failed.get(rule.id) ? "settings-sent.rule_retry_revoke" : "settings-sent.rule_revoke")}
          </Button>
        </div>)}
        {error && !item && <p role="alert">{error}</p>}
        {verificationFailed && <p role="alert">{t("settings-sent.rule_verify")}</p>}
        <Button tone="quiet" busy={reading} disabled={busy} onClick={() => { void refreshRules(); }}>{t("settings-sent.rules_refresh")}</Button>
      </div>
    </Group>
    {children(addAction)}
    <Dialog open={intent?.kind === "revoke"} onClose={() => { if (!life.current.busy) changeIntent(null); }} title={t("settings-sent.rule_revoke_title")} footer={<>
      <Button tone="quiet" disabled={busy} onClick={() => changeIntent(null)}>{t("common.cancel")}</Button>
      <Button tone="primary" busy={busy} onClick={() => { if (intent?.kind === "revoke") void run(intent); }}>{t("settings-sent.rule_revoke_confirm")}</Button>
    </>}>
      {intent?.kind === "revoke" && <p className="break-words text-sm text-ink-2">{t("settings-sent.rule_revoke_effect", { connector: connectorName(intent.rule.connector), chat: intent.rule.chat })}</p>}
    </Dialog>
  </>;
}
