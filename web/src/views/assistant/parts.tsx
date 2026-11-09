// Small controls the assistant pages share ("Scheduled tasks", "All chats", Settings › Assistant). Tokens only (src/styles.css).
import { cx, EASE } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { SegmentedTrack } from "../../ui/Segmented";

export const inputClass =
  "h-9 w-full rounded-control bg-field px-3 text-[13px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

export const textareaClass =
  "w-full resize-y rounded-control bg-field px-3 py-2 text-[13px] leading-relaxed text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

/** An on/off switch; a click on it doesn't reach the row under it. */
export function Switch({ checked, onChange, label, disabled }: { checked: boolean; onChange: (on: boolean) => void; label: string; disabled?: boolean }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={(e) => {
        e.stopPropagation();
        onChange(!checked);
      }}
      onKeyDown={(e) => e.stopPropagation()}
      className={cx(
        "touch-hit relative inline-flex h-5 w-[34px] shrink-0 items-center rounded-full transition-colors duration-200 disabled:opacity-45",
        checked ? "bg-green" : "bg-line-strong",
      )}
    >
      <span
        aria-hidden
        className="absolute top-0.5 left-0.5 size-4 rounded-full bg-white shadow-btn"
        style={{ transform: checked ? "translateX(14px)" : "none", transition: `transform 220ms ${EASE}` }}
      />
    </button>
  );
}

/** A row of mutually exclusive choices ("Once" / "Every day" / "Interval"). `compact`: tighter segments for a crowded bar; `dot` on an option: something there wants a look. */
export function Segmented<T extends string>({ value, options, onChange, label, compact, wrap }: { value: T; options: { v: T; label: string; dot?: boolean }[]; onChange: (v: T) => void; label: string; compact?: boolean; wrap?: boolean }) {
  return (
    <SegmentedTrack role="radiogroup" label={label} className={cx("flex gap-0.5 rounded-control p-0.5", wrap && "flex-wrap")} thumbClassName="rounded-[7px]">
      {options.map((o) => (
        <button
          key={o.v}
          type="button"
          role="radio"
          aria-checked={value === o.v}
          data-selected={value === o.v}
          onClick={() => onChange(o.v)}
          className={cx(
            "relative z-[1] h-7 flex-1 whitespace-nowrap rounded-[7px] text-[12.5px] transition-colors duration-150",
            compact ? "px-2.5" : "px-3",
            value === o.v ? "font-medium text-ink" : "text-ink-3 hover:text-ink-2",
          )}
        >
          {o.label}
          {o.dot && <span aria-hidden className="absolute top-0.5 right-0.5 size-1.5 rounded-full bg-attention" />}
        </button>
      ))}
    </SegmentedTrack>
  );
}

/** A labelled field with an optional note under it. */
export function Field({ label, note, children, error }: { label: React.ReactNode; note?: React.ReactNode; children: React.ReactNode; error?: boolean }) {
  return (
    <label className="block">
      <span className="mb-1 block text-[12px] font-medium text-ink-2">{label}</span>
      {children}
      {note && <span className={cx("mt-1 block text-[11.5px] leading-relaxed", error ? "text-red" : "text-ink-3")}>{note}</span>}
    </label>
  );
}

/** A section heading inside a dialog or a page column. */
export function SectionTitle({ children, right }: { children: React.ReactNode; right?: React.ReactNode }) {
  return (
    <div className="mb-1.5 flex items-end gap-2 px-1">
      <h2 className="flex-1 text-[12px] font-medium text-ink-3">{children}</h2>
      {right}
    </div>
  );
}

/** A rounded square holding a row's icon. */
export function IconTile({ icon, tone = "accent" }: { icon: IconName; tone?: "accent" | "red" | "muted" | "green" | "orange" }) {
  const tones = {
    accent: "bg-accent-tint text-accent-ink",
    red: "bg-red-tint text-red",
    muted: "bg-field text-ink-3",
    green: "bg-green-tint text-green",
    orange: "bg-orange-tint text-orange",
  };
  return (
    <span className={cx("flex size-8 shrink-0 items-center justify-center rounded-[9px]", tones[tone])}>
      <Icon name={icon} size={15} />
    </span>
  );
}

/** An empty page or section: an icon, a line, what to do. */
export function Empty({ icon, title, children, action }: { icon: IconName; title: string; children?: React.ReactNode; action?: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center px-6 py-14 text-center">
      <span className="mb-3 flex size-11 items-center justify-center rounded-full bg-field text-ink-3">
        <Icon name={icon} size={19} />
      </span>
      <p className="text-[13.5px] font-medium text-ink">{title}</p>
      {children && <p className="mt-1 max-w-sm text-[12.5px] leading-relaxed text-ink-3">{children}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

/** A tag in one of the tones (a memory's kind). */
export function Tag({ children, tone = "neutral" }: { children: React.ReactNode; tone?: "accent" | "orange" | "green" | "red" | "neutral" }) {
  const tones = {
    accent: "bg-accent-tint text-accent-ink",
    orange: "bg-orange-tint text-orange",
    green: "bg-green-tint text-green",
    red: "bg-red-tint text-red",
    neutral: "bg-field text-ink-2",
  };
  return <span className={cx("inline-flex h-5 shrink-0 items-center whitespace-nowrap rounded-full px-2 text-[11px] font-medium", tones[tone])}>{children}</span>;
}
