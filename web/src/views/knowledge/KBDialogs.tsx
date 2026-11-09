// The knowledge page's dialogs: "Tag" (importance, relevance, tags, a project's kind and status), one day of a chat's
// archive opened at a found message, and one archived Moments post.
import { useEffect, useRef, useState } from "react";
import { api, enc, errorText, isAbort } from "../../lib/api";
import {
  dayTitle,
  dropTitle,
  IMPORTANCE,
  momentFeed,
  parseMessageLine,
  PROJECT_KINDS,
  PROJECT_STATUSES,
  tagForm,
  tagUpdate,
  type KBDay,
  type KBEntry,
  type KBMomentRef,
  type MessageLine,
  type TagForm,
} from "../../lib/kb";
import { Button, cx, Dialog, Loading, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { Marked } from "./KBParts";
import { t } from "../../i18n";
import { SegmentedTrack } from "../../ui/Segmented";
import { composing } from "../../lib/send-history";

function Segmented<T extends string>({ value, options, onChange, label }: { value: T; options: readonly { value: T; label: string }[]; onChange: (v: T) => void; label: string }) {
  return (
    <SegmentedTrack role="radiogroup" label={label} className="inline-flex max-w-full flex-wrap gap-0.5 rounded-[9px] p-0.5" thumbClassName="rounded-[7px]">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={value === o.value}
          data-selected={value === o.value}
          onClick={() => onChange(o.value)}
          className={cx(
            "relative z-[1] h-7 whitespace-nowrap rounded-[7px] px-3 text-[12.5px] font-medium transition-colors duration-150",
            value === o.value ? "text-ink" : "text-ink-2 hover:text-ink",
          )}
        >
          {o.label}
        </button>
      ))}
    </SegmentedTrack>
  );
}

function Field({ label, children }: { label: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="mb-4">
      <div className="mb-1.5 text-[12px] font-medium text-ink-2">{label}</div>
      {children}
    </div>
  );
}

/** Tagging set by hand: the hub keeps it over the automatic one (it is locked). */
export function TagEditor({ entry, open, onClose, onSaved }: { entry: KBEntry; open: boolean; onClose: () => void; onSaved: () => void }) {
  const [f, setF] = useState<TagForm>(() => tagForm(entry));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    setF(tagForm(entry));
    setError("");
    // the form starts from the entry each time it opens, not on every reload behind it
  }, [open]);
  const project = entry.kind === "projects";
  const set = <K extends keyof TagForm>(k: K, v: TagForm[K]) => setF((x) => ({ ...x, [k]: v }));

  const save = async () => {
    if (saving) return; // Enter in the tags field saves too: a second one while the first save is out
    setSaving(true);
    setError("");
    try {
      await api(`/kb/${enc(entry.kind)}/${enc(entry.id)}`, { method: "PUT", json: tagUpdate(entry.kind, f) });
      toast(t("knowledge-kbdialogs.tags_saved"), "ok");
      onSaved();
      onClose();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={460}
      title={<span className="line-clamp-2">{t("knowledge-kbdialogs.tag_title", { name: entry.name })}</span>}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={saving} onClick={save}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <Field label={t("knowledge-kbdialogs.importance")}>
        <Segmented label={t("knowledge-kbdialogs.importance")} value={f.importance} options={IMPORTANCE.map((p) => ({ value: p as string, label: p }))} onChange={(v) => set("importance", v)} />
      </Field>
      <Field
        label={
          <span className="flex items-center">
            <span className="flex-1">{t("knowledge-kbdialogs.relevance")}</span>
            <span className="tabular-nums text-ink">{f.relevance}</span>
          </span>
        }
      >
        <input type="range" min={0} max={100} step={5} value={f.relevance} onChange={(e) => set("relevance", Number(e.target.value))} aria-label={t("knowledge-kbdialogs.relevance")} className="w-full accent-accent" />
      </Field>
      <Field label={t("knowledge-kbdialogs.tags")}>
        <input
          value={f.tags}
          onChange={(e) => set("tags", e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && !composing(e.nativeEvent) && void save()}
          placeholder={t("knowledge-kbdialogs.tags_placeholder")}
          aria-label={t("knowledge-kbdialogs.tags")}
          className="h-9 w-full rounded-control bg-surface px-3 text-[13px] text-ink shadow-btn outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)]"
        />
      </Field>
      {project && (
        <>
          <Field label={t("knowledge-kbdialogs.kind")}>
            <Segmented label={t("knowledge-kbdialogs.project_kind")} value={f.projectKind} options={PROJECT_KINDS} onChange={(v) => set("projectKind", v)} />
          </Field>
          <Field label={t("knowledge-kbdialogs.status")}>
            <Segmented label={t("knowledge-kbdialogs.project_status")} value={f.status} options={PROJECT_STATUSES as readonly { value: string; label: string }[]} onChange={(v) => set("status", v)} />
          </Field>
        </>
      )}
      <p className="text-[11.5px] leading-relaxed text-ink-3">{t("knowledge-kbdialogs.locked_hint")}</p>
      {error && <p className="mt-2 text-[12.5px] text-red">{error}</p>}
    </Dialog>
  );
}

/** One archived message: who and when on a line, then what was said, the searched words in bold. */
export function MessageView({ m, query = "", clamp }: { m: MessageLine; query?: string; clamp?: boolean }) {
  return (
    <div className="min-w-0">
      {(m.sender || m.time) && (
        <div className="flex items-baseline gap-2">
          <span className="truncate text-[12px] font-semibold text-ink">{m.sender}</span>
          <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">{m.time}</span>
        </div>
      )}
      <p className={cx("text-[13px] leading-relaxed whitespace-pre-wrap text-ink-2", clamp && "line-clamp-4")}>
        <Marked text={m.text} query={query} />
      </p>
    </div>
  );
}

/** One day of a chat's archive, opened at a found message (marked and scrolled to), with the days around it. */
export function DayDialog({ chat, chatName, day, focus, query, onClose }: { chat: string; chatName: string; day: string | null; focus?: number; query?: string; onClose: () => void }) {
  const [shown, setShown] = useState(day);
  const [page, setPage] = useState<KBDay | null>(null);
  const [error, setError] = useState("");
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => setShown(day), [day]);
  useEffect(() => {
    if (!shown) return;
    setPage(null);
    setError("");
    const ac = new AbortController();
    api<KBDay>(`/kb/chats/${enc(chat)}/days/${enc(shown)}`, { signal: ac.signal })
      .then(setPage)
      .catch((e) => !isAbort(e) && setError(errorText(e)));
    return () => ac.abort();
  }, [chat, shown]);
  const msgs = page?.messages ?? [];
  // The found line may be a continuation of a longer message: the last one starting at or before it.
  const target = shown === day && focus !== undefined ? msgs.filter((m) => m.n <= focus).pop()?.n : undefined;
  useEffect(() => {
    if (target === undefined) return;
    box.current?.querySelector(`[data-n="${target}"]`)?.scrollIntoView({ block: "center" });
  }, [target, page]);

  return (
    <Dialog
      open={Boolean(day)}
      onClose={onClose}
      width={560}
      title={
        <span className="flex min-w-0 items-baseline gap-2">
          <span className="whitespace-nowrap">{shown ? dayTitle(shown) : ""}</span>
          <span className="truncate text-[12px] font-normal text-ink-3">{chatName}</span>
        </span>
      }
      footer={
        <>
          <Button tone="quiet" disabled={!page?.prev} onClick={() => page?.prev && setShown(page.prev)}>
            <Icon name="chevronLeft" size={13} stroke={2.2} />
            {page?.prev ? dayTitle(page.prev) : t("knowledge-kbdialogs.prev_day")}
          </Button>
          <span className="flex-1" />
          <Button tone="quiet" disabled={!page?.next} onClick={() => page?.next && setShown(page.next)}>
            {page?.next ? dayTitle(page.next) : t("knowledge-kbdialogs.next_day")}
            <Icon name="chevronRight" size={13} stroke={2.2} />
          </Button>
        </>
      }
    >
      <div ref={box} className="-mx-4 max-h-[60vh] overflow-y-auto px-4">
        {!page && !error && (
          <div className="py-8">
            <Loading />
          </div>
        )}
        {error && <p className="py-6 text-[12.5px] text-red">{error}</p>}
        {page && (
          <>
            <p className="mb-2 text-[11.5px] text-ink-3">{t("knowledge-kbdialogs.message_count", { count: msgs.length })}</p>
            <div className="flex flex-col gap-1 pb-1">
              {msgs.map((m) => (
                <div key={m.n} data-n={m.n} className={cx("rounded-[8px] px-2.5 py-1.5", m.n === target && "bg-accent-tint")}>
                  <MessageView m={parseMessageLine(m.text)} query={query} />
                </div>
              ))}
            </div>
          </>
        )}
      </div>
    </Dialog>
  );
}

/** One archived post (Moments, or the feed it names): its text and every comment as they were archived. */
export function MomentDialog({ moment, onClose }: { moment: KBMomentRef | null; onClose: () => void }) {
  const [md, setMd] = useState<string | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!moment) return;
    setMd(null);
    setError("");
    const ac = new AbortController();
    api<{ markdown: string }>(`/kb/moments/${enc(moment.id)}`, { signal: ac.signal })
      .then((r) => setMd(dropTitle(r.markdown ?? "").trim()))
      .catch((e) => !isAbort(e) && setError(errorText(e)));
    return () => ac.abort();
  }, [moment]);
  return (
    <Dialog open={Boolean(moment)} onClose={onClose} width={600} title={moment ? momentFeed(moment) : t("knowledge-kbdialogs.moments")}>
      <div className="-mx-4 max-h-[65vh] overflow-y-auto px-4 pb-1">
        {moment?.note && (
          <p className="mb-3 flex items-center gap-1.5 text-[12.5px] font-medium text-accent-ink">
            <Icon name="link" size={13} />
            {moment.note}
          </p>
        )}
        {md === null && !error && (
          <div className="py-8">
            <Loading />
          </div>
        )}
        {error && <p className="py-6 text-[12.5px] text-red">{error}</p>}
        {md !== null && <Markdown text={md} small />}
      </div>
    </Dialog>
  );
}
