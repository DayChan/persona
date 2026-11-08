// "What this computer can do" on a machine's page (docs/server/machines.md "Self-check", Moshi's Doctor tab): what `personad node doctor` finds on
// the computer, first as features ("Approval cards on your phone", "Status and notifications", "Terminal and engine", …) with "Ready" / "Partly working" / "Problem" / "Off", then all
// the individual checks behind "Show all checks". It checks when the page opens and on "Check again"; "Fix" (after one confirmation)
// has the node write its own generated files again and re-check. A node too old for the self-check says so, with the
// same update action the deployment strip has. Reading and fixing are for a full device (the hub refuses the rest).
import { useCallback, useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, ApiError, enc, errorText, isAbort, pastHub } from "../../lib/api";
import {
  CHECK_STATUS_LABEL,
  deriveFeatures,
  doctorUnavailable,
  FEATURE_STATE_LABEL,
  fixableChecks,
  fixResult,
  healthContext,
  infoChecks,
  summarize,
  type DoctorCheck,
  type DoctorReport,
  type DoctorUnavailable,
  type FeatureState,
} from "../../lib/node-health";
import type { Machine } from "../../lib/types";
import { Button, cx, Dialog, EASE, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { useDeploy } from "./MachineDeploy";

const STATE_TONE: Record<FeatureState, { pill: string; dot: string }> = {
  ready: { pill: "bg-green-tint text-green", dot: "bg-green" },
  partial: { pill: "bg-orange-tint text-orange", dot: "bg-orange" },
  broken: { pill: "bg-red-tint text-red", dot: "bg-red" },
  off: { pill: "bg-field text-ink-3", dot: "bg-line-strong" },
};

const CHECK_TONE = { ok: "bg-green-tint text-green", warn: "bg-orange-tint text-orange", fail: "bg-red-tint text-red" } as const;

type Load = { kind: "loading" } | { kind: "report"; report: DoctorReport } | { kind: "unavailable"; why: NonNullable<DoctorUnavailable> } | { kind: "error"; text: string };

export function NodeHealth({ machine, heading }: { machine: Machine; heading: React.ReactNode }) {
  const mid = machine.id;
  const online = machine.online;
  const [load, setLoad] = useState<Load>({ kind: "loading" });
  const [busy, setBusy] = useState(false); // a re-check or a fix is running over the report that is shown
  const [fixing, setFixing] = useState(false);
  const [confirmFix, setConfirmFix] = useState(false);
  const [all, setAll] = useState(false);
  const seq = useRef(0);

  const check = useCallback(
    async (signal?: AbortSignal) => {
      const mine = ++seq.current;
      setBusy(true);
      try {
        // the hub waits 45 s for the node's self-check (hub/node_doctor.go)
        const report = await api<DoctorReport>(`/machines/${enc(mid)}/doctor`, { signal, timeout: pastHub(45_000) });
        if (mine === seq.current) setLoad({ kind: "report", report: { ...report, checks: report.checks ?? [] } });
      } catch (e) {
        if (isAbort(e) || mine !== seq.current) return;
        const why = doctorUnavailable(e instanceof ApiError ? { status: e.status, message: e.message, isJson: e.isJson } : { message: errorText(e) });
        setLoad(why ? { kind: "unavailable", why } : { kind: "error", text: errorText(e) });
      } finally {
        if (mine === seq.current) setBusy(false);
      }
    },
    [mid],
  );

  // Once when the page opens (or the computer changes, or comes online); an offline computer has nothing to ask.
  useEffect(() => {
    setAll(false);
    if (!online) {
      seq.current++;
      setLoad({ kind: "loading" });
      setBusy(false);
      return;
    }
    setLoad({ kind: "loading" });
    const ctrl = new AbortController();
    void check(ctrl.signal);
    return () => ctrl.abort();
  }, [mid, online, check]);

  const fix = async () => {
    setConfirmFix(false);
    setFixing(true);
    seq.current++; // a check still on its way would show the older report
    try {
      const report = await api<DoctorReport>(`/machines/${enc(mid)}/doctor/fix`, { method: "POST" });
      setLoad({ kind: "report", report: { ...report, checks: report.checks ?? [] } });
      const r = fixResult(report);
      toast(r.text, r.tone);
    } catch (e) {
      toast(t("node-health.fix_failed", { error: errorText(e) }), "error");
    } finally {
      setFixing(false);
      setBusy(false);
    }
  };

  const report = load.kind === "report" ? load.report : null;
  const features = report ? deriveFeatures(report, healthContext(machine)) : [];
  const summary = summarize(features);
  const fixable = report ? fixableChecks(report) : [];
  const info = report ? infoChecks(report) : [];

  return (
    <section data-testid="node-health">
      {heading}
      <div className="overflow-hidden rounded-card bg-surface shadow-card" style={{ animation: `fade-up 420ms ${EASE} both` }}>
        {!online ? (
          <p className="px-4 py-3 text-[12.5px] text-ink-3">{t("node-health.offline")}</p>
        ) : load.kind === "loading" ? (
          <p className="flex items-center gap-2 px-4 py-3 text-[12.5px] text-ink-2" role="status">
            <Spinner />
            {t("node-health.checking")}
          </p>
        ) : load.kind === "unavailable" ? (
          <Unavailable why={load.why} machine={machine} />
        ) : load.kind === "error" ? (
          <div className="flex flex-wrap items-center gap-2 px-4 py-3">
            <p className="min-w-0 flex-1 break-words text-[12.5px] text-red" role="alert">
              {t("node-health.check_error", { error: load.text })}
            </p>
            <Button tone="secondary" icon="retry" busy={busy} onClick={() => void check()}>
              {t("node-health.recheck")}
            </Button>
          </div>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3">
              <span className={cx("flex size-6 shrink-0 items-center justify-center rounded-full", summary.attention === 0 ? "bg-green-tint text-green" : "bg-orange-tint text-orange")}>
                <Icon name={summary.attention === 0 ? "check" : "warning"} size={13} stroke={2.4} />
              </span>
              <div className="min-w-40 flex-1" role="status">
                <p className="text-[13.5px] font-medium text-ink">{summary.title}</p>
                {summary.off && <p className="mt-0.5 text-[11.5px] text-ink-3">{summary.off}</p>}
              </div>
              {fixable.length > 0 && (
                <Button tone="primary" icon="tool" busy={fixing} disabled={busy && !fixing} onClick={() => setConfirmFix(true)}>
                  {t("node-health.fix")}
                </Button>
              )}
              <Button tone="secondary" icon="retry" busy={busy && !fixing} disabled={fixing} onClick={() => void check()}>
                {t("node-health.recheck")}
              </Button>
            </div>
            <ul className="divide-y divide-line border-t border-line" aria-label={t("node-health.features_label")}>
              {features.map((f) => (
                <li key={f.id} data-feature={f.id} data-state={f.state} className="flex items-start gap-3 px-4 py-2.5">
                  <span className={cx("mt-[7px] size-1.5 shrink-0 rounded-full", STATE_TONE[f.state].dot)} aria-hidden />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-[13px] font-medium text-ink">{f.name}</p>
                    <p className="mt-0.5 break-words text-[11.5px] leading-relaxed text-ink-3">{f.state === "ready" || !f.note ? f.detail : f.note}</p>
                  </div>
                  <span className={cx("inline-flex h-5 shrink-0 items-center whitespace-nowrap rounded-full px-2 text-[11px] font-medium", STATE_TONE[f.state].pill)}>{FEATURE_STATE_LABEL[f.state]}</span>
                </li>
              ))}
              {info.map((c) => (
                <li key={c.name} data-info={c.name} className="flex items-start gap-3 px-4 py-2.5">
                  <span className="mt-[2px] flex size-4 shrink-0 items-center justify-center text-ink-3">
                    <Icon name="info" size={14} />
                  </span>
                  <div className="min-w-0 flex-1">
                    <p className="break-words text-[12.5px] text-ink-2">{c.detail}</p>
                    {c.hint && <p className="mt-0.5 break-words text-[11.5px] leading-relaxed text-ink-3">{c.hint}</p>}
                  </div>
                </li>
              ))}
            </ul>
            <div className="border-t border-line">
              <button
                type="button"
                aria-expanded={all}
                aria-controls={`checks-${mid}`}
                onClick={() => setAll((v) => !v)}
                className="flex w-full items-center gap-1.5 px-4 py-2 text-left text-[12px] text-ink-2 transition-colors hover:bg-hover hover:text-ink"
              >
                <Icon name={all ? "chevron" : "chevronRight"} size={13} />
                <span className="whitespace-nowrap">{all ? t("node-health.checks_hide") : t("node-health.checks_show", { count: report!.checks.length })}</span>
              </button>
              {all && (
                <ul id={`checks-${mid}`} className="divide-y divide-line border-t border-line" aria-label={t("node-health.checks_label")}>
                  {report!.checks.map((c) => (
                    <CheckRow key={c.name} c={c} />
                  ))}
                </ul>
              )}
            </div>
          </>
        )}
      </div>
      <Dialog
        open={confirmFix}
        onClose={() => setConfirmFix(false)}
        title={t("node-health.fix_dialog_title")}
        footer={
          <>
            <Button tone="quiet" onClick={() => setConfirmFix(false)}>
              {t("common.cancel")}
            </Button>
            <Button tone="primary" onClick={() => void fix()}>
              {t("node-health.fix")}
            </Button>
          </>
        }
      >
        <p className="text-[12.5px] leading-relaxed text-ink-2">
          {t("node-health.fix_dialog_body", { files: fixable.map((c) => c.name).join(t("node-health.list_separator")) })}
        </p>
      </Dialog>
    </section>
  );
}

function CheckRow({ c }: { c: DoctorCheck }) {
  const items = (c.items ?? []).slice(0, 5);
  return (
    <li data-check={c.name} data-status={c.status} className="flex items-start gap-3 px-4 py-2.5">
      <span className={cx("mt-px inline-flex h-5 shrink-0 items-center whitespace-nowrap rounded-full px-2 text-[11px] font-medium", CHECK_TONE[c.status])}>{CHECK_STATUS_LABEL[c.status]}</span>
      <div className="min-w-0 flex-1">
        <p className="break-all font-mono text-[12px] text-ink">{c.name}</p>
        <p className="mt-0.5 break-words text-[11.5px] leading-relaxed text-ink-2">{c.detail}</p>
        {c.status !== "ok" && c.hint && <p className="mt-0.5 break-words text-[11.5px] leading-relaxed text-ink-3">{c.hint}</p>}
        {c.status !== "ok" && items.length > 0 && (
          <ul className="mt-1 space-y-0.5">
            {items.map((it) => (
              <li key={it} className="break-all font-mono text-[11px] text-ink-3">
                {it}
              </li>
            ))}
          </ul>
        )}
      </div>
    </li>
  );
}

function Unavailable({ why, machine }: { why: NonNullable<DoctorUnavailable>; machine: Machine }) {
  const { deployment, pending, deploy } = useDeploy(machine);
  if (why === "old-node") {
    return (
      <div className="flex flex-wrap items-center gap-2 px-4 py-3">
        <p className="min-w-0 flex-1 break-words text-[12.5px] text-ink-2" role="status">
          {!deployment.supported && deployment.reason ? t("node-health.old_node_reason", { reason: deployment.reason }) : t("node-health.old_node")}
        </p>
        {deployment.supported && (
          <Button tone="secondary" icon="retry" busy={deployment.busy || pending} disabled={!deployment.canDeploy || pending} title={deployment.reason || undefined} onClick={() => void deploy()}>
            {pending && !deployment.busy ? t("node-health.requesting") : deployment.label}
          </Button>
        )}
      </div>
    );
  }
  const text =
    why === "old-hub"
      ? t("node-health.old_hub")
      : why === "forbidden"
        ? t("node-health.forbidden")
        : t("node-health.went_offline");
  return (
    <p className="px-4 py-3 text-[12.5px] text-ink-3" role="status">
      {text}
    </p>
  );
}
