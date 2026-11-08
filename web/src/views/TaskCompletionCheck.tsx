import { useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { currentLanguage, t } from "../i18n";
import { credentialOwner, onCredentialChange, subscribeCredentialOwner } from "../lib/api";
import { defaultManager, onDefaultManager, useKnownDefault } from "../lib/manager-choice";
import { checkTaskCompletion, completionCheckError, type TaskDoneCheck } from "../lib/task-completion-check";
import type { Todo } from "../lib/todos";
import { Button, Loading } from "../ui/atoms";

/** Each check belongs to one task, manager and login, including when an editor stays mounted across pairing. */
export function TaskCompletionCheck({ todo }: { todo: Todo }) {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const knownDefault = useKnownDefault();
  const manager = todo.manager || knownDefault || defaultManager();
  return <CompletionCheck key={JSON.stringify([todo.id, todo.manager, manager, owner])} todo={todo} manager={manager} owner={owner} />;
}

function CompletionCheck({ todo, manager, owner }: { todo: Todo; manager: string; owner: string }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [result, setResult] = useState<{ check: TaskDoneCheck; at: Date }>();
  const pending = useRef<AbortController | null>(null);
  const retired = useRef(false);
  useLayoutEffect(() => {
    retired.current = false;
    const retire = () => { retired.current = true; pending.current?.abort(); };
    const stop = onCredentialChange(retire);
    const stopManager = onDefaultManager(() => { if (!todo.manager && defaultManager() !== manager) retire(); });
    return () => { retire(); stop(); stopManager(); };
  }, [manager, todo.manager]);

  const run = async () => {
    if (retired.current || pending.current || owner !== credentialOwner()) return;
    const request = new AbortController();
    pending.current = request; // Admit synchronously, before React can render a disabled button.
    const current = () => !retired.current && pending.current === request && owner === credentialOwner()
      && manager === (todo.manager || defaultManager());
    setBusy(true);
    setError(undefined);
    setResult(undefined);
    try {
      const check = await checkTaskCompletion(todo.id, manager, request.signal);
      if (current()) setResult({ check, at: new Date() });
    } catch (e) {
      if (current()) setError(completionCheckError(e));
    } finally {
      if (current()) { pending.current = null; setBusy(false); }
    }
  };

  return (
    <section aria-label={t("todos.check_section")} aria-busy={busy} className="mt-4 min-w-0 rounded-control bg-inset p-3.5 shadow-hairline" data-testid="task-completion-check">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-[12.5px] font-semibold text-ink-2">{t("todos.check_section")}</span>
        <Button tone="secondary" icon="check" disabled={busy} onClick={() => void run()}>
          {t(error ? "todos.check_retry" : result ? "todos.check_again" : "todos.check_action")}
        </Button>
      </div>
      <p className="mt-2 text-[12.5px] leading-[1.6] text-ink-3">{t("todos.check_explanation")}</p>
      {todo.supervision && <p className="mt-2 text-[12.5px] leading-[1.6] text-ink-2">{t("todos.check_supervised")}</p>}
      {busy && <div className="mt-3"><Loading label={t("todos.check_wait")} /></div>}
      {error && <p role="alert" className="mt-3 text-[13px] leading-[1.6] text-red">{error}</p>}
      {result && <CompletionCheckResult check={result.check} at={result.at} />}
    </section>
  );
}

const verdictKeys: Record<string, string> = {
  done: "todos.check_done", not_done: "todos.check_not_done", waiting: "todos.check_waiting",
  needs_user: "todos.check_needs_user", unsure: "todos.check_unsure", undelivered: "todos.check_undelivered",
};
const skipKeys: Record<string, string> = {
  not_a_task: "todos.check_skip_manual", done: "todos.check_skip_done", reopened: "todos.check_skip_reopened",
};
const deliveryKeys: Record<string, string> = {
  live: "todos.check_delivery_live", none: "todos.check_delivery_none", pr: "todos.delivery_pr",
  merged: "todos.delivery_merged", off: "todos.delivery_off",
};
const unblockKeys: Record<string, string> = {
  user: "todos.unblock_user", agent: "todos.unblock_agent", other: "todos.unblock_other",
};

const labelKey = (keys: Record<string, string>, code: string, fallback: string) => Object.hasOwn(keys, code) ? keys[code] : fallback;

export function CompletionCheckResult({ check, at }: { check: TaskDoneCheck; at: Date }) {
  const delivery = check.delivery;
  return (
    <div role="status" className="mt-3 min-w-0 space-y-2 text-[13px] leading-[1.6] text-ink-2 break-words" data-testid="completion-check-result">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <strong className="text-ink">{t(labelKey(verdictKeys, check.verdict, "todos.check_unknown"))}</strong>
        <time dateTime={at.toISOString()} className="text-[12px] text-ink-3">{t("todos.check_at", { time: at.toLocaleString(currentLanguage()) })}</time>
      </div>
      <p className="whitespace-pre-wrap select-text">{check.reason || t("todos.check_no_reason")}</p>
      <p className="text-[12.5px] text-ink-3">{check.skip
        ? t(labelKey(skipKeys, check.skip, "todos.check_skip_unknown"))
        : t(check.verdict === "done" && check.would_close ? "todos.check_would_close" : "todos.check_would_not_close")}</p>
      {delivery && <div className="space-y-1">
        <p>{t("todos.check_delivery", { state: t(labelKey(deliveryKeys, delivery.state, "todos.check_delivery_unknown")) })}</p>
        {delivery.gate && <p className="whitespace-pre-wrap select-text">{t("todos.check_blocker", { gate: delivery.gate })}</p>}
        {delivery.unblock && <p>{t(labelKey(unblockKeys, delivery.unblock, "todos.check_unblock_unknown"))}</p>}
      </div>}
    </div>
  );
}
