// Machines › Add computer (docs/assistant/onboarding.md 4.2 "Your computers ↔ hub", 7.2 #11): pick the system, name it, and the hub adds the
// computer and gives its Persona Link a pairing link (POST /api/v1/auth/pair-codes {"add"}, feature machines_add). The
// dialog then says how to use the link on that system, waits for the computer to come online (the countdown and the
// polling of Settings › Devices › Connect a new device) and asks, on the same screen, whether it is yours (…/confirm | …/reject). With
// `confirm` it opens straight at that question for a computer already waiting to be confirmed (the main conversation's
// "Confirm computer"), and offers a new link if that computer is not online.
import { useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import {
  addBody,
  addErrorText,
  addStage,
  canAddComputer,
  COMPUTER_OSES,
  confirmStage,
  isPending,
  knownOS,
  LINK_LINUX_DOC,
  neverConnected,
  linuxCommand,
  PERSONA_LINK_INSTALL_DOC,
  rememberOS,
  windowsCommand,
  type ComputerCodeReply,
  type ComputerOS,
} from "../../lib/add-computer";
import { api, ApiError, enc, errorText } from "../../lib/api";
import { readinessChanged } from "../../lib/readiness";
import { clock, secondsLeft } from "../../lib/connect-device";
import { useStore } from "../../lib/store";
import { documentPage, visiblePoll } from "../../lib/visible-poll";
import type { Machine } from "../../lib/types";
import { Button, copyText, Dialog, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Field, inputClass, Segmented } from "../assistant/parts";

const POLL_MS = 3000;
const NAME_MAX = 40; // the hub's limit (docs/protocol/protocol.md "Pair codes for computers")

/** A computer to give a new code to (one added before that has not connected), instead of adding one. */
export interface PendingComputer {
  id: string;
  name: string;
}

type Outcome = "confirmed" | "rejected" | null;

export function AddComputer({ open, onClose, again, confirm }: { open: boolean; onClose: () => void; again?: PendingComputer | null; confirm?: PendingComputer | null }) {
  const [os, setOS] = useState<ComputerOS>("mac");
  const [name, setName] = useState("");
  const [code, setCode] = useState<ComputerCodeReply | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const [polled, setPolled] = useState<Machine[] | null>(null);
  const [outcome, setOutcome] = useState<Outcome>(null);
  const [answering, setAnswering] = useState<"confirm" | "reject" | null>(null);
  // the hub refused a new one (too_many_pending): the computers in the way are listed with "Remove"
  const [tooMany, setTooMany] = useState(false);
  const [removing, setRemoving] = useState("");
  const seen = useRef(false);
  // Only the latest pair-codes request may land: a reply to one made before the dialog closed or switched computers is dropped.
  const seq = useRef(0);
  const listed = useStore((s) => s.machines);
  const canAdd = useStore((s) => canAddComputer(s.features));

  const reset = () => {
    setCode(null);
    setError("");
    setPolled(null);
    setOutcome(null);
    setAnswering(null);
    setTooMany(false);
    seen.current = false;
  };

  // body: {"add"} for a new computer, {"machine"} for a new code for one added before
  const make = async (body: object, chosen?: ComputerOS) => {
    const mine = ++seq.current;
    setBusy(true);
    setError("");
    try {
      const reply = await api<ComputerCodeReply>("/auth/pair-codes", { method: "POST", json: body });
      if (mine !== seq.current) return;
      if (chosen && reply.machine) rememberOS(reply.machine, chosen);
      reset();
      setNow(Date.now());
      setCode(reply);
    } catch (e) {
      if (mine !== seq.current) return;
      setError(addErrorText(e));
      setTooMany(e instanceof ApiError && e.code === "too_many_pending");
    } finally {
      if (mine === seq.current) setBusy(false);
    }
  };

  useEffect(() => {
    seq.current++; // drop any reply still on its way
    setBusy(false);
    if (!open) return;
    reset();
    setName("");
    if (again) {
      setOS(knownOS(listed.find((m) => m.id === again.id)?.os, again.id) ?? "mac");
      void make({ machine: again.id });
    } else if (confirm) setOS(knownOS(listed.find((m) => m.id === confirm.id)?.os, confirm.id) ?? "mac");
    else setOS("mac");
  }, [open, again?.id, confirm?.id]);

  const id = code?.machine ?? confirm?.id ?? "";
  const fromStore = listed.find((m) => m.id === id);
  const fromPoll = polled?.find((m) => m.id === id);
  // The WebSocket's list or the polled one, whichever has it online first.
  const machine = [fromStore, fromPoll].find((m) => m?.online) ?? fromStore ?? fromPoll;
  if (machine) seen.current = true;
  const left = code ? secondsLeft(code.expires_at, now) : 0;
  const stage = code ? addStage(machine, left, seen.current) : confirm ? confirmStage(machine) : null;
  const waiting = open && !!code && !outcome && (stage === "waiting" || stage === "expired");

  useEffect(() => {
    if (!waiting) return;
    const tick = setInterval(() => setNow(Date.now()), 1000);
    // Every 3 s while the tab is shown, and once on coming back, so a computer that came online meanwhile shows at once.
    // The code was just made, so the first read still waits one period: visiblePoll's immediate one is skipped.
    let started = false;
    const stop = visiblePoll(() => {
      if (!started) return;
      api<{ machines?: Machine[] }>("/machines")
        .then((r) => setPolled(r.machines ?? []))
        .catch(() => {});
    }, POLL_MS, documentPage);
    started = true;
    return () => (clearInterval(tick), stop());
  }, [waiting]);

  const answer = async (what: "confirm" | "reject") => {
    setAnswering(what);
    setError("");
    try {
      await api(`/machines/${enc(id)}/${what}`, { method: "POST" });
      setOutcome(what === "confirm" ? "confirmed" : "rejected");
      void readinessChanged(); // the main conversation's card follows (it may have been waiting on this computer)
    } catch (e) {
      setError(errorText(e));
    } finally {
      setAnswering(null);
    }
  };

  // "Remove" one of the computers that keep a new one from being added; the list follows the hub's next machines push
  const remove = async (m: Machine) => {
    setRemoving(m.id);
    try {
      await api(`/machines/${enc(m.id)}/reject`, { method: "POST" });
      // room for the new one now: back to the plain form, "Next" tries again
      setTooMany(false);
      setError("");
      void readinessChanged();
    } catch (e) {
      setError(errorText(e));
      setTooMany(false);
    } finally {
      setRemoving("");
    }
  };
  const blocking = tooMany ? listed.filter(isPending) : [];

  const shown = code?.name || machine?.name || again?.name || confirm?.name || name.trim();
  // a new link for a computer that never came online (or for the one being confirmed, which is not online)
  const relink = again ?? (confirm && code ? confirm : null);
  let body: React.ReactNode;
  let footer: React.ReactNode = (
    <Button tone="quiet" onClick={onClose}>
      {t("machines-addcomputer.close")}
    </Button>
  );

  if (!code && again) {
    body = error ? <Failure text={error} onRetry={() => void make({ machine: again.id })} /> : <Busy />;
  } else if (!code && !confirm) {
    const trimmed = name.trim();
    const submit = () => trimmed && !busy && void make(addBody(trimmed, os), os);
    body = (
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <div>
          <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("machines-addcomputer.os")}</span>
          <Segmented<ComputerOS> label={t("machines-addcomputer.os")} value={os} onChange={setOS} options={COMPUTER_OSES.map((v) => ({ v, label: t(`machines-addcomputer.os_${v}`) }))} />
        </div>
        <Field label={t("machines-addcomputer.name")} note={t("machines-addcomputer.name_note")}>
          <input
            className={inputClass}
            value={name}
            maxLength={NAME_MAX}
            autoFocus
            placeholder={t(`machines-addcomputer.name_placeholder_${os}`)}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        {tooMany && blocking.length > 0 ? (
          <TooMany machines={blocking} removing={removing} onRemove={(m) => void remove(m)} />
        ) : (
          error && (
            <p role="alert" className="rounded-[8px] bg-red-tint px-3 py-2 text-[12.5px] leading-relaxed text-red">
              {error}
            </p>
          )
        )}
      </form>
    );
    footer = (
      <>
        <Button tone="quiet" onClick={onClose}>
          {t("machines-addcomputer.cancel")}
        </Button>
        <Button tone="primary" disabled={!trimmed} busy={busy} onClick={submit}>
          {t("machines-addcomputer.next")}
        </Button>
      </>
    );
  } else if (outcome === "confirmed" || stage === "connected") {
    body = <Result icon="check" tone="text-green" title={t("machines-addcomputer.confirmed", { name: shown })} hint={t("machines-addcomputer.confirmed_hint")} />;
    footer = <Button tone="primary" onClick={onClose}>{t("machines-addcomputer.done")}</Button>;
  } else if (outcome === "rejected" || stage === "gone") {
    body = (
      <Result
        icon="trash"
        tone="text-ink-3"
        title={t(outcome === "rejected" ? "machines-addcomputer.rejected" : "machines-addcomputer.gone", { name: shown })}
        hint={t("machines-addcomputer.rejected_hint")}
      />
    );
    footer = <Button tone="primary" onClick={onClose}>{t("machines-addcomputer.done")}</Button>;
  } else if (stage === "joined" && machine) {
    const about = [machine.system?.name || machine.os, machine.hostname].filter(Boolean).join(" · ");
    body = (
      <div className="flex flex-col items-center gap-2 py-4 text-center" data-stage="joined">
        <span className="mb-1 flex size-11 items-center justify-center rounded-full bg-orange-tint text-orange">
          <Icon name="machine" size={19} />
        </span>
        <p className="text-[14px] font-medium text-ink">{t("machines-addcomputer.joined", { name: shown })}</p>
        {about && <p className="text-[12px] text-ink-3">{about}</p>}
        <p className="max-w-[340px] text-[12.5px] leading-relaxed text-ink-2">{t("machines-addcomputer.joined_hint")}</p>
        {error && <p role="alert" className="text-[12.5px] text-red">{error}</p>}
      </div>
    );
    footer = (
      <>
        <Button tone="secondary" busy={answering === "reject"} disabled={!!answering} onClick={() => void answer("reject")}>
          {t("machines-addcomputer.reject")}
        </Button>
        <Button tone="primary" busy={answering === "confirm"} disabled={!!answering} onClick={() => void answer("confirm")}>
          {t("machines-addcomputer.confirm")}
        </Button>
      </>
    );
  } else if (stage === "offline") {
    body = (
      <div className="flex flex-col items-center gap-2 py-6 text-center" data-stage="offline">
        <span className="mb-1 flex size-11 items-center justify-center rounded-full bg-orange-tint text-orange">
          <Icon name="machine" size={19} />
        </span>
        <p className="text-[14px] font-medium text-ink">{t("machines-addcomputer.offline", { name: shown })}</p>
        <p className="max-w-[340px] text-[12.5px] leading-relaxed text-ink-2">{t(canAdd ? "machines-addcomputer.offline_hint" : "machines-addcomputer.offline_hint_old")}</p>
        {error && <p role="alert" className="text-[12.5px] text-red">{error}</p>}
      </div>
    );
    footer = (
      <>
        <Button tone="quiet" onClick={onClose}>
          {t("machines-addcomputer.close")}
        </Button>
        {/* not recognised: remove it here, as the machines page offers */}
        <Button tone="secondary" busy={answering === "reject"} disabled={!!answering || busy} onClick={() => void answer("reject")}>
          {t("machines-addcomputer.remove")}
        </Button>
        {canAdd && (
          // a new link revokes the old pairing: the first choice only for a computer that never connected (one that is
          // only asleep comes back by itself)
          <Button tone={machine?.error_code === "never_connected" ? "primary" : "secondary"} busy={busy} disabled={!!answering} onClick={() => void make({ machine: id })}>
            {t("machines-addcomputer.new_link")}
          </Button>
        )}
      </>
    );
  } else if (stage === "expired") {
    body = (
      <div className="flex flex-col items-center gap-3 py-8 text-center">
        <p className="text-[13px] text-ink-2">{t("machines-addcomputer.expired")}</p>
        {error && <p role="alert" className="text-[12.5px] text-red">{error}</p>}
        <Button tone="primary" busy={busy} onClick={() => void make({ machine: id })}>
          {t("machines-addcomputer.new_code")}
        </Button>
      </div>
    );
  } else if (!code) {
    body = <Busy />; // confirming: every stage without a code is drawn above
  } else {
    body = (
      <div className="flex flex-col gap-4" data-stage="waiting">
        {relink && (
          // The hub does not know which system a computer that never connected runs: let the user switch.
          <Segmented<ComputerOS> label={t("machines-addcomputer.os")} value={os} onChange={setOS} options={COMPUTER_OSES.map((v) => ({ v, label: t(`machines-addcomputer.os_${v}`) }))} />
        )}
        <Instructions os={os} code={code} />
        <div className="flex items-center justify-between gap-3 rounded-[10px] bg-field px-3 py-2.5 text-[12.5px] text-ink-2">
          <span className="flex min-w-0 items-center gap-2">
            <Spinner />
            <span className="truncate">{t("machines-addcomputer.waiting", { name: shown })}</span>
          </span>
          <span className="shrink-0 whitespace-nowrap tabular-nums text-ink-3">{t("machines-addcomputer.expires", { time: clock(left) })}</span>
        </div>
      </div>
    );
  }

  return (
    <Dialog open={open} onClose={onClose} width={480} title={again ? t("machines-addcomputer.title_again", { name: again.name }) : confirm ? t("machines-addcomputer.title_confirm") : t("machines-addcomputer.title")} footer={footer}>
      {body}
    </Dialog>
  );
}

/** How to use the link on that system: one line on Linux and Windows, Persona Link on a Mac. */
function Instructions({ os, code }: { os: ComputerOS; code: ComputerCodeReply }) {
  if (os === "linux") {
    const { line, installs } = linuxCommand(code);
    return (
      <Steps>
        <Step n={1}>
          {t(installs ? "machines-addcomputer.linux_step_install" : "machines-addcomputer.linux_step_pair")}
          {!installs && (
            <>
              {" "}
              <a href={LINK_LINUX_DOC} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-0.5 whitespace-nowrap text-accent-ink hover:underline">
                {t("machines-addcomputer.linux_install_link")}
                <Icon name="external" size={11} />
              </a>
            </>
          )}
        </Step>
        <Command line={line} label={t("machines-addcomputer.copy_command")} />
        <Step n={2}>{t("machines-addcomputer.linux_step_done")}</Step>
      </Steps>
    );
  }
  if (os === "windows") {
    return (
      <Steps>
        <Step n={1}>{t("machines-addcomputer.windows_step_get")}</Step>
        <Step n={2}>{t("machines-addcomputer.windows_step_run")}</Step>
        <Command line={windowsCommand(code.pair_url)} label={t("machines-addcomputer.copy_command")} />
        <p className="pl-7 text-[11.5px] leading-relaxed text-ink-3">{t("machines-addcomputer.windows_note")}</p>
      </Steps>
    );
  }
  const share = typeof navigator !== "undefined" && typeof navigator.share === "function";
  return (
    <Steps>
      <Step n={1}>
        {t("machines-addcomputer.mac_step_install")}{" "}
        <a href={PERSONA_LINK_INSTALL_DOC} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-0.5 whitespace-nowrap text-accent-ink hover:underline">
          {t("machines-addcomputer.mac_install_link")}
          <Icon name="external" size={11} />
        </a>
      </Step>
      <Step n={2}>{t("machines-addcomputer.mac_step_link")}</Step>
      <div className="flex flex-wrap gap-2 pl-7">
        <Button icon="copy" onClick={() => void copyText(code.pair_url)}>
          {t("machines-addcomputer.copy_link")}
        </Button>
        {share && (
          <Button icon="send" onClick={() => void navigator.share({ title: t("machines-addcomputer.share_title"), text: code.pair_url }).catch(() => {})}>
            {t("machines-addcomputer.share_link")}
          </Button>
        )}
      </div>
    </Steps>
  );
}

function Steps({ children }: { children: React.ReactNode }) {
  return <div className="flex flex-col gap-2.5">{children}</div>;
}

function Step({ n, children }: { n: number; children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-2.5 text-[13px] leading-relaxed text-ink">
      <span className="mt-px flex size-[18px] shrink-0 items-center justify-center rounded-full bg-accent-tint text-[11px] font-semibold text-accent-ink tabular-nums">{n}</span>
      <span className="min-w-0">{children}</span>
    </div>
  );
}

/** A command to paste, whole, with its copy button (it carries the pairing link: no wrapping into a shell's two lines). */
function Command({ line, label }: { line: string; label: string }) {
  return (
    <div className="ml-7 flex items-start gap-2 rounded-[10px] bg-inset p-2 pl-3">
      <code className="min-w-0 flex-1 select-all break-all py-1 font-mono text-[11.5px] leading-relaxed text-ink">{line}</code>
      <Button icon="copy" onClick={() => void copyText(line)}>
        {label}
      </Button>
    </div>
  );
}

/** The computers waiting that keep a new one from being added (the hub takes 3): those that never connected say so,
 *  not "Waiting for you" (there is nothing to confirm yet), and each can be removed here. */
function TooMany({ machines, removing, onRemove }: { machines: Machine[]; removing: string; onRemove: (m: Machine) => void }) {
  const never = machines.every(neverConnected);
  return (
    <div role="alert" className="flex flex-col gap-2 rounded-[8px] bg-orange-tint px-3 py-2.5" data-too-many>
      <p className="text-[12.5px] leading-relaxed text-ink">
        {t(never ? "machines-addcomputer.too_many_never" : "machines-addcomputer.too_many_waiting", { count: machines.length })}
      </p>
      <ul className="flex flex-col gap-1">
        {machines.map((m) => (
          <li key={m.id} className="flex min-h-8 items-center gap-2">
            <Icon name="machine" size={13} className="shrink-0 text-ink-3" />
            <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">{m.name}</span>
            <span className="shrink-0 text-[11.5px] whitespace-nowrap text-ink-3">
              {neverConnected(m) ? t("machines-addcomputer.never_connected") : m.online ? t("machines-addcomputer.waiting_confirm") : t("machines-addcomputer.offline_short")}
            </span>
            <Button tone="secondary" busy={removing === m.id} disabled={!!removing} onClick={() => onRemove(m)}>
              {t("machines-addcomputer.remove")}
            </Button>
          </li>
        ))}
      </ul>
    </div>
  );
}

function Result({ icon, tone, title, hint }: { icon: "check" | "trash"; tone: string; title: string; hint: string }) {
  return (
    <div className="flex flex-col items-center gap-2 py-8 text-center">
      <Icon name={icon} size={28} stroke={2.2} className={tone} />
      <p className="text-[14px] font-medium text-ink">{title}</p>
      <p className="max-w-[340px] text-[12px] leading-relaxed text-ink-3">{hint}</p>
    </div>
  );
}

function Busy() {
  return (
    <div className="flex justify-center py-10">
      <Spinner size={16} />
    </div>
  );
}

function Failure({ text, onRetry }: { text: string; onRetry: () => void }) {
  return (
    <div className="flex flex-col items-center gap-3 py-6 text-center">
      <p role="alert" className="text-[13px] text-red">{text}</p>
      <Button onClick={onRetry}>{t("machines-addcomputer.retry")}</Button>
    </div>
  );
}
