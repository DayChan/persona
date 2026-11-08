import { useEffect, useId, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, enc, errorText } from "../../lib/api";
import type { KBUpdate } from "../../lib/kb";
import { Button } from "../../ui/atoms";
import { Card, SectionTitle } from "./KBParts";

type Props = {
  kind: string;
  id: string;
  value: string;
  auth: { token: string; tokenOwner: string; headers: Record<string, string> };
  current: () => boolean;
  refresh: () => Promise<boolean>;
  onSaved: () => void;
};

/** Explicit corrections stay separate from the inferred profile. Each opening starts from authoritative detail. */
export function ConfirmedFacts(props: Props) {
  const [editing, setEditing] = useState(false);
  return (
    <section>
      <SectionTitle title={t("knowledge-confirmedfacts.title")} right={!editing && (
        <Button tone="quiet" onClick={() => props.current() && setEditing(true)}>
          {t(props.value ? "knowledge-confirmedfacts.edit" : "knowledge-confirmedfacts.add")}
        </Button>
      )} />
      <Card>
        <div className="p-4">
          {editing ? <FactsEditor {...props} onClose={() => setEditing(false)} /> : (
            <>
              <p className="whitespace-pre-wrap break-words text-sm leading-relaxed text-ink select-text">{props.value || t("knowledge-confirmedfacts.empty")}</p>
              <p className="mt-2 text-xs leading-relaxed text-ink-3">{t("knowledge-confirmedfacts.hint")}</p>
            </>
          )}
        </div>
      </Card>
    </section>
  );
}

function FactsEditor({ kind, id, value, auth, current, refresh, onSaved, onClose }: Props & { onClose: () => void }) {
  const [initial] = useState(value);
  const [draft, setDraft] = useState(value);
  const [phase, setPhase] = useState<"edit" | "saving" | "refreshing" | "refresh-error">("edit");
  const [error, setError] = useState("");
  const busy = useRef(false);
  const alive = useRef(true);
  const label = useId();
  const hint = useId();
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const valid = () => alive.current && current();

  const readback = async () => {
    setPhase("refreshing");
    setError("");
    try {
      const loaded = await refresh();
      if (!valid()) return;
      if (!loaded) { setPhase("refresh-error"); return; }
      onSaved();
      onClose();
    } catch (err) {
      if (valid()) { setError(errorText(err)); setPhase("refresh-error"); }
    } finally {
      if (valid()) busy.current = false;
    }
  };

  const save = async () => {
    // Admit synchronously: two events can arrive before React paints the busy button.
    if (busy.current || !valid() || phase !== "edit" || draft === initial) return;
    busy.current = true;
    setPhase("saving");
    setError("");
    try {
      const update: KBUpdate = { pinned: draft };
      await api(`/kb/${enc(kind)}/${enc(id)}`, { method: "PUT", json: update, ...auth });
    } catch (err) {
      if (valid()) { setError(errorText(err)); setPhase("edit"); busy.current = false; }
      return;
    }
    if (valid()) await readback();
  };

  return (
    <form onSubmit={(event) => { event.preventDefault(); void save(); }}>
      <label id={label} htmlFor={`${label}-input`} className="mb-2 block text-sm font-medium text-ink">{t("knowledge-confirmedfacts.title")}</label>
      <textarea
        id={`${label}-input`}
        aria-labelledby={label}
        aria-describedby={hint}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        disabled={phase !== "edit"}
        rows={6}
        autoFocus
        className="w-full resize-y rounded-control bg-field p-3 text-sm leading-relaxed text-ink shadow-btn outline-none placeholder:text-ink-3 focus:ring-1 focus:ring-accent disabled:opacity-70"
      />
      <p id={hint} className="mt-2 text-xs leading-relaxed text-ink-3">{t("knowledge-confirmedfacts.edit_hint")}</p>
      {phase === "refresh-error" ? (
        <div role="alert" className="mt-3 break-words text-sm leading-relaxed text-ink-2">
          <p>{t("knowledge-confirmedfacts.refresh_failed", { error })}</p>
          <Button tone="secondary" className="mt-3" onClick={() => { if (!busy.current && valid()) { busy.current = true; void readback(); } }}>{t("knowledge-confirmedfacts.retry_refresh")}</Button>
        </div>
      ) : (
        <>
          {error && <p role="alert" className="mt-3 break-words text-sm text-red">{t("knowledge-confirmedfacts.save_failed", { error })}</p>}
          {phase === "refreshing" && <p role="status" className="mt-3 text-sm text-ink-2">{t("knowledge-confirmedfacts.refreshing")}</p>}
          <div className="mt-4 flex flex-wrap justify-end gap-2">
            <Button tone="quiet" disabled={phase !== "edit"} onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" tone="primary" busy={phase === "saving" || phase === "refreshing"} disabled={phase !== "edit" || draft === initial}>
              {t(error ? "knowledge-confirmedfacts.retry_save" : "common.save")}
            </Button>
          </div>
        </>
      )}
    </form>
  );
}
