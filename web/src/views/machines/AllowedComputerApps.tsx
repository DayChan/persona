// Persisted manager approvals, independent of the node's live computer-control state.
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { api, ApiError, credentialOwner, enc, errorText, getToken, subscribeCredentialOwner } from "../../lib/api";
import { defaultManager, MANAGER_HEADER, useKnownDefault } from "../../lib/manager-choice";
import { Button, Dialog, Spinner } from "../../ui/atoms";
import { formatDate, t } from "../../i18n";

type ComputerApp = { bundle_id: string; name: string; at?: string };
type Answer = { apps: ComputerApp[]; manager?: string };
const OWNER_HEADER = "X-Persona-Computer-Apps-Owner";
const text = (key: string) => t(`machines-allowedcomputerapps.${key}`);
function failure(error: unknown) {
  if (error instanceof ApiError && error.code === "machine_owner_changed") return text("owner_changed");
  if (error instanceof ApiError && error.status === 403) return text("denied");
  if (error instanceof ApiError && error.status === 404) return text("unsupported");
  return errorText(error);
}
function appsFrom(answer: Answer, manager: string): ComputerApp[] {
  if (answer.manager && answer.manager !== manager) throw new Error(text("owner_changed"));
  if (!Array.isArray(answer.apps) || answer.apps.some((app) => !app || typeof app.bundle_id !== "string" || typeof app.name !== "string")) {
    throw new Error(text("invalid_response"));
  }
  return answer.apps;
}

export function AllowedComputerApps({ mid, manager }: { mid: string; manager?: string }) {
  const fallback = useKnownDefault();
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const resolved = manager || fallback;
  if (!resolved)
    return (
      <p role="status" className="px-4 py-3 text-[12px] text-ink-3">
        {t("common.loading")}
      </p>
    );
  return <OwnedApps key={JSON.stringify([mid, resolved, owner])} mid={mid} manager={resolved} owner={owner} followsDefault={!manager} />;
}

function OwnedApps({ mid, manager, owner, followsDefault }: { mid: string; manager: string; owner: string; followsDefault: boolean }) {
  const [token] = useState(getToken);
  const [apps, setApps] = useState<ComputerApp[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [verified, setVerified] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [choice, setChoice] = useState<ComputerApp | null>(null);
  const [busy, setBusy] = useState(false);
  const [writeError, setWriteError] = useState("");
  const [notice, setNotice] = useState("");
  const life = useRef({
    active: true,
    verified: false,
    revision: 0,
    reading: false,
    writing: false,
    choice: null as ComputerApp | null,
    read: null as AbortController | null,
    write: null as AbortController | null,
  });
  const path = `/machines/${enc(mid)}/computer/apps`;
  const precondition = `?expected_manager=${enc(manager)}`;
  const current = () => life.current.active && credentialOwner() === owner && (!followsDefault || defaultManager() === manager);
  const options = { token, tokenOwner: owner, headers: { [MANAGER_HEADER]: manager } };

  const refresh = async () => {
    const state = life.current;
    if (!current() || state.reading || state.writing) return;
    state.reading = true;
    state.verified = false;
    state.choice = null;
    setVerified(false);
    setChoice(null);
    setWriteError("");
    setNotice("");
    const revision = ++state.revision;
    const controller = new AbortController();
    state.read = controller;
    setLoading(true);
    setLoadError("");
    try {
      let proof: string | null = null;
      const result = appsFrom(
        await api<Answer>(`${path}${precondition}`, {
          ...options,
          signal: controller.signal,
          onResponse: (headers) => {
            proof = headers.get(OWNER_HEADER);
          },
        }),
        manager,
      );
      if (current() && revision === state.revision) {
        setApps(result);
        state.verified = proof === manager;
        setVerified(state.verified);
        if (!state.verified) setLoadError(text("revoke_unsupported"));
      }
    } catch (error) {
      if (current() && revision === state.revision) setLoadError(failure(error));
    } finally {
      if (current() && revision === state.revision) {
        state.reading = false;
        setLoading(false);
      }
    }
  };
  useEffect(() => {
    const state = life.current;
    state.active = true;
    void refresh();
    return () => {
      state.active = false;
      state.revision++;
      state.reading = false;
      state.read?.abort();
      state.write?.abort();
    };
    // This keyed boundary captures one machine, manager and credential generation.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const close = (expected: ComputerApp | null) => {
    if (!current() || life.current.writing || life.current.choice !== expected) return;
    life.current.choice = null;
    setChoice(null);
    setWriteError("");
  };
  const revoke = async (app: ComputerApp | null) => {
    const state = life.current;
    if (!current() || !app || state.choice !== app || state.writing || !state.verified) return;
    state.writing = true; // synchronous admission, including two clicks in the same render
    state.revision++; // a read begun before this write cannot restore its old list
    state.read?.abort();
    state.reading = false;
    setLoading(false);
    setBusy(true);
    setWriteError("");
    setNotice("");
    const controller = new AbortController();
    state.write = controller;
    try {
      let proof: string | null = null;
      const result = appsFrom(
        await api<Answer>(`${path}/revoke/${enc(app.bundle_id)}${precondition}`, {
          ...options,
          method: "DELETE",
          signal: controller.signal,
          onResponse: (headers) => {
            proof = headers.get(OWNER_HEADER);
          },
        }),
        manager,
      );
      if (current() && proof !== manager) {
        state.verified = false;
        setVerified(false);
        setLoadError(text("revoke_unsupported"));
        throw new Error(text("revoke_unsupported"));
      }
      if (!current()) return;
      setApps(result);
      setLoadError("");
      if (!result.some((row) => row.bundle_id === app.bundle_id)) setNotice(text("revoked"));
      state.choice = null;
      setChoice(null);
    } catch (error) {
      if (current()) setWriteError(failure(error));
    } finally {
      if (current()) {
        state.writing = false;
        setBusy(false);
      }
    }
  };

  return (
    <div className="border-t border-line px-4 py-3" data-testid="allowed-computer-apps">
      <div className="flex items-center gap-2">
        <h3 className="min-w-0 flex-1 text-[13px] font-medium text-ink">{text("title")}</h3>
        <Button tone="quiet" disabled={loading || busy} onClick={() => void refresh()}>
          {text("refresh")}
        </Button>
      </div>
      <p className="mt-1 text-[11.5px] leading-relaxed text-ink-3">{text("scope")}</p>
      {loading && (
        <p role="status" className="mt-3 flex items-center gap-2 text-[12px] text-ink-3">
          <Spinner />
          {t("common.loading")}
        </p>
      )}
      {loadError && (
        <div role="alert" className="mt-3 text-[12px] text-red">
          <p>{loadError}</p>
          <Button tone="quiet" onClick={() => void refresh()}>
            {text("retry")}
          </Button>
        </div>
      )}
      {apps?.length === 0 && <p className="mt-3 text-[12px] text-ink-3">{text("empty")}</p>}
      {!!apps?.length && (
        <ul className="mt-2 divide-y divide-line">
          {apps.map((app) => (
            <li key={app.bundle_id} className="flex items-start gap-3 py-3">
              <div className="min-w-0 flex-1">
                <p className="break-words text-[13px] text-ink">{app.name}</p>
                <p className="break-all font-mono text-[11.5px] text-ink-3">{app.bundle_id}</p>
                {app.at && Number.isFinite(Date.parse(app.at)) && (
                  <p className="mt-1 text-[11.5px] text-ink-3">
                    <time dateTime={app.at}>
                      {t("machines-allowedcomputerapps.approved_at", {
                        at: formatDate(app.at, { dateStyle: "medium", timeStyle: "short" }),
                      })}
                    </time>
                  </p>
                )}
              </div>
              <Button
                tone="secondary"
                disabled={busy || !verified}
                onClick={() => {
                  if (!current() || life.current.writing || life.current.choice || !life.current.verified) return;
                  const confirmation = { ...app };
                  life.current.choice = confirmation;
                  setChoice(confirmation);
                  setWriteError("");
                }}
              >
                {text("revoke")}
              </Button>
            </li>
          ))}
        </ul>
      )}
      {notice && (
        <p role="status" className="mt-2 text-[12px] text-ink-2">
          {notice}
        </p>
      )}
      <Dialog
        open={!!choice}
        onClose={() => close(choice)}
        title={text("confirm_title")}
        footer={
          <>
            <Button tone="quiet" disabled={busy} onClick={() => close(choice)}>
              {t("common.cancel")}
            </Button>
            <Button tone="danger" busy={busy} disabled={!verified} onClick={() => void revoke(choice)}>
              {writeError ? text("retry") : text("revoke")}
            </Button>
          </>
        }
      >
        <p className="text-[12.5px] leading-relaxed text-ink-2">{text("confirm_detail")}</p>
        <p className="mt-3 break-words text-[13px] font-medium text-ink">{choice?.name}</p>
        <p className="mt-1 break-all font-mono text-[12px] text-ink-3">{choice?.bundle_id}</p>
        {writeError && (
          <p role="alert" className="mt-3 text-[12px] text-red">
            {writeError}
          </p>
        )}
      </Dialog>
    </div>
  );
}
