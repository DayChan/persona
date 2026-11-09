import { useEffect, useState } from "react";
import { ApiError, errorText } from "../lib/api";
import { pair } from "../lib/auth";
import { here, rememberHub } from "../lib/hubs";
import { Button, Collapse, cx, Spinner } from "../ui/atoms";
import { AppMark } from "../ui/AppMark";
import { Icon } from "../ui/icons";
import { OtherHubs } from "./Hubs";
import { t } from "../i18n";
import { tRich } from "../i18n/rich";

const code = "whitespace-nowrap rounded-[5px] bg-field px-1 py-0.5 font-mono text-[11.5px] text-ink-2 shadow-hairline";

// Sign in without a terminal (docs/assistant/onboarding.md 4.2, 4.5): first a login code from a device that is already signed
// in (Settings › Devices › Connect a new device › Browser makes a `#token=jp_…` link: opened here it signs in by itself, App.tsx), then a
// pasted pairing / login link. The hub's own token and the commands that print it are folded under "Advanced". `error` is
// what went wrong with a token that came in the link.
export function Login({ onDone, error: linkError = "" }: { onDone: () => void; error?: string }) {
  const [link, setLink] = useState("");
  const [master, setMaster] = useState("");
  const [busy, setBusy] = useState<"" | "link" | "master">("");
  const [error, setError] = useState(linkError);
  const [advanced, setAdvanced] = useState(false);
  const [noHub, setNoHub] = useState(false);
  useEffect(() => void rememberHub(here()), []);

  const submit = (from: "link" | "master") => async (e: React.FormEvent) => {
    e.preventDefault();
    const tok = (from === "link" ? link : master).trim();
    if (!tok || busy) return;
    setBusy(from);
    setError("");
    try {
      await pair(tok);
      onDone();
    } catch (err) {
      const msg = errorText(err);
      const invalid = (err instanceof ApiError && err.status === 401) || msg === "invalid token";
      setError(invalid ? t(from === "link" ? "login.invalid_link" : "login.invalid_token") : msg);
    } finally {
      setBusy("");
    }
  };

  const field = "flex h-10 items-center gap-2 rounded-control bg-inset px-3 shadow-hairline focus-within:shadow-[0_0_0_1px_var(--line-strong)]";
  const input = "min-w-0 flex-1 bg-transparent font-mono text-[13px] text-ink outline-none placeholder:font-sans placeholder:text-ink-3";

  return (
    <div className="flex min-h-full items-center justify-center bg-page px-5 py-16">
      <div className="w-full max-w-[380px]" style={{ animation: "fade-up 450ms cubic-bezier(0.23,1,0.32,1) both" }}>
        <div className="mb-6 flex flex-col items-center gap-3 text-center">
          <AppMark size={44} radius={12} className="shadow-raised" />
          <div>
            <h1 className="text-[18px] font-semibold tracking-tight text-ink">{t("login.title")}</h1>
            <p className="mt-1 text-[13px] leading-relaxed text-ink-2">{t("login.subtitle")}</p>
          </div>
        </div>

        <section className="rounded-card bg-surface p-4 shadow-card" aria-labelledby="login-phone">
          <h2 id="login-phone" className="text-[13.5px] font-semibold text-ink">
            {t("login.phone_title")}
          </h2>
          <ol className="mt-2.5 space-y-2 text-[12.5px] leading-relaxed text-ink-2">
            {[
              tRich("login.phone_step_open", { path: <span className="font-medium text-ink">{t("login.phone_path")}</span> }),
              t("login.phone_step_scan"),
              t("login.phone_step_copy"),
            ].map((step, i) => (
              <li key={i} className="flex gap-2.5">
                <span aria-hidden className="mt-px flex h-[18px] w-[18px] shrink-0 items-center justify-center rounded-full bg-field text-[11px] font-semibold text-ink-2 shadow-hairline">
                  {i + 1}
                </span>
                <span className="min-w-0">{step}</span>
              </li>
            ))}
          </ol>
        </section>

        <form onSubmit={submit("link")} className="mt-3 rounded-card bg-surface p-1.5 shadow-card">
          <label className={field}>
            <Icon name="link" size={14} className="text-ink-3" />
            <input
              autoFocus
              type="text"
              value={link}
              onChange={(e) => setLink(e.target.value)}
              placeholder={t("login.link_placeholder")}
              aria-label={t("login.link_placeholder")}
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              className={input}
            />
          </label>
          <Button type="submit" tone="primary" disabled={!link.trim() || busy !== ""} className="mt-1.5 h-9 w-full">
            {busy === "link" ? <Spinner /> : t("login.sign_in")}
          </Button>
        </form>

        <p className="mt-3 min-h-5 text-center text-[12.5px] text-red" role="alert">
          {error}
        </p>
        <p className="text-center text-[11.5px] leading-relaxed text-ink-3">{t("login.credential_note")}</p>

        <div className="mt-5 flex flex-col gap-1">
          <Disclosure open={noHub} onToggle={() => setNoHub((v) => !v)} label={t("login.no_hub")} id="login-no-hub">
            <div className="space-y-1.5 px-3 pb-2 text-[12px] leading-relaxed text-ink-2">
              <p>{t("login.no_hub_what")}</p>
              <p>{t("login.no_hub_yours")}</p>
              <p>{t("login.no_hub_others")}</p>
            </div>
          </Disclosure>
          <Disclosure open={advanced} onToggle={() => setAdvanced((v) => !v)} label={t("login.advanced")} id="login-advanced">
            <form onSubmit={submit("master")} className="px-3 pb-2">
              <p className="flex items-start gap-1.5 text-[12px] leading-relaxed text-ink-2">
                <Icon name="warning" size={13} className="mt-0.5 shrink-0 text-orange" />
                <span>{t("login.master_warning")}</span>
              </p>
              <p className="mt-1.5 text-[11.5px] leading-[1.9] text-ink-3">
                {tRich("login.master_where", {
                  pair: <code className={code}>personad hub pair --legacy</code>,
                  jq: <code className={code}>jq -r .token ~/.persona/hub.json</code>,
                })}
              </p>
              <div className="mt-2 rounded-card bg-surface p-1.5 shadow-card">
                <label className={field}>
                  <Icon name="key" size={14} className="text-ink-3" />
                  <input
                    type="password"
                    value={master}
                    onChange={(e) => setMaster(e.target.value)}
                    placeholder={t("login.master_placeholder")}
                    aria-label={t("login.master_placeholder")}
                    autoComplete="off"
                    className={input}
                  />
                </label>
                <Button type="submit" tone="secondary" disabled={!master.trim() || busy !== ""} className="mt-1.5 h-9 w-full">
                  {busy === "master" ? <Spinner /> : t("login.connect")}
                </Button>
              </div>
            </form>
          </Disclosure>
        </div>
        <OtherHubs />
      </div>
    </div>
  );
}

/** A quiet row that folds what is under it up and down. */
function Disclosure({ open, onToggle, label, id, children }: { open: boolean; onToggle: () => void; label: string; id: string; children: React.ReactNode }) {
  return (
    <div>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        aria-controls={id}
        className="flex h-8 w-full items-center gap-1.5 whitespace-nowrap rounded-control px-3 text-left text-[12.5px] text-ink-2 transition-colors hover:bg-hover hover:text-ink"
      >
        <Icon name="chevronRight" size={12} className={cx("shrink-0 text-ink-3 transition-transform duration-200", open && "rotate-90")} />
        <span className="truncate">{label}</span>
      </button>
      <div id={id}>
        <Collapse open={open}>{children}</Collapse>
      </div>
    </div>
  );
}
