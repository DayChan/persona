// "Disconnect this browser" in Settings › General and the account menu (#1873, lib/disconnect.ts): the button is busy
// while the hub revokes the credential; a revoke that fails, or a credential the hub cannot revoke from here, asks
// before signing out instead of signing out as if it had worked.
import { useState } from "react";
import { handleUnauthorized } from "../lib/api";
import { revokeThisBrowser, signOutHere } from "../lib/disconnect";
import { Button, Dialog } from "../ui/atoms";
import { t } from "../i18n";

type Problem = { error: string; id: string } | { nothing: true };

export function useDisconnect(): { start: () => void; busy: boolean; dialog: React.ReactNode } {
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<Problem | null>(null);
  const start = async () => {
    if (busy) return;
    setBusy(true);
    // signing out on purpose: a request that gets a 401 meanwhile must not show "this browser was signed out"
    const before = handleUnauthorized(() => {});
    const r = await revokeThisBrowser();
    if (r.ok) return signOutHere();
    handleUnauthorized(before); // still signed in here until the user says otherwise
    setBusy(false);
    setProblem("nothing" in r ? { nothing: true } : { error: r.error, id: r.id });
  };
  const close = () => !busy && setProblem(null);
  const signOut = () => {
    handleUnauthorized(() => {});
    signOutHere(problem && "id" in problem ? problem.id : "");
  };
  const dialog = (
    <Dialog
      open={problem !== null}
      onClose={close}
      title={problem && "nothing" in problem ? t("disconnect.nothing_title") : t("disconnect.failed_title")}
      footer={
        <>
          <Button tone="quiet" onClick={close} disabled={busy}>
            {t("common.cancel")}
          </Button>
          {problem && "nothing" in problem ? (
            <Button tone="danger" onClick={signOut}>
              {t("disconnect.sign_out")}
            </Button>
          ) : (
            <>
              <Button tone="danger" onClick={signOut} disabled={busy}>
                {t("disconnect.sign_out_anyway")}
              </Button>
              <Button tone="primary" onClick={() => void start()} busy={busy}>
                {t("disconnect.retry")}
              </Button>
            </>
          )}
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">{problem && "nothing" in problem ? t("disconnect.nothing_body") : t("disconnect.failed_body")}</p>
      {/* why, in the hub's (or the page's) own words */}
      {problem && "error" in problem && <p className="mt-2 text-[12px] leading-relaxed text-ink-3">{problem.error}</p>}
    </Dialog>
  );
  return { start: () => void start(), busy, dialog };
}
