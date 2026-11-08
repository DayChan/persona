import { useEffect, useRef } from "react";
import { t } from "../../i18n";
import { Button, cx, EASE } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";

export const settingsInputClass =
  "h-8 w-full rounded-control bg-field px-2.5 text-[12.5px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

export function Group({ title, footer, children, right }: { title?: React.ReactNode; footer?: React.ReactNode; children: React.ReactNode; right?: React.ReactNode }) {
  return (
    <section className="mb-6">
      {(title || right) && (
        <div className="mb-1.5 flex items-end gap-2 px-1">
          <h2 className="flex-1 text-[12px] font-medium text-ink-3">{title}</h2>
          {right}
        </div>
      )}
      <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">{children}</div>
      {footer && <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">{footer}</p>}
    </section>
  );
}

// A row's destructive action, confirmed in the row itself: the trigger gives way to "Cancel" and the confirm button. A
// popover under the trigger would be clipped by the card (Group's overflow-hidden) on the card's last row (#1504).
export function RowConfirm({ open, trigger, confirm, busy, onCancel, onConfirm }: {
  open: boolean;
  trigger: React.ReactNode;
  confirm: string;
  busy?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  // display:contents, so the row's flex layout sees the same children as before; it only scopes the focus moves below
  const box = useRef<HTMLSpanElement>(null);
  // which of the trigger's controls had the focus, counted from the end (ShortcutSettings' recorder keeps its place there
  // while the reset / clear buttons before it come and go), so closing hands it back to the same one
  const fromEnd = useRef(-1);
  const was = useRef(open);
  useEffect(() => {
    if (was.current === open) return;
    was.current = open;
    const el = box.current;
    if (!el) return;
    const list = [...el.querySelectorAll<HTMLElement>(FOCUSABLE)];
    // open: the trigger that had the focus is gone, so it goes to "Cancel" rather than dropping to <body>
    if (open) return list[0]?.focus({ preventScroll: true });
    // closed: back to the trigger, unless the focus has already moved somewhere else on purpose
    const active = document.activeElement;
    if (active && active !== document.body && !el.contains(active)) return;
    (list[list.length - 1 - fromEnd.current] ?? list[0])?.focus({ preventScroll: true });
  }, [open]);
  const remember = (e: React.FocusEvent) => {
    if (open || !box.current) return;
    const list = [...box.current.querySelectorAll<HTMLElement>(FOCUSABLE)];
    const i = list.indexOf(e.target as HTMLElement);
    if (i >= 0) fromEnd.current = list.length - 1 - i;
  };
  return (
    <span ref={box} className="contents" onFocus={remember}>
      {open ? (
        <span
          className="flex shrink-0 items-center gap-1"
          style={{ animation: `pop-in 160ms ${EASE} both` }}
          onKeyDown={(e) => {
            if (e.key !== "Escape" || busy) return;
            e.preventDefault();
            e.stopPropagation();
            onCancel();
          }}
        >
          <Button tone="quiet" disabled={busy} onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <Button tone="danger" busy={busy} onClick={onConfirm}>
            {confirm}
          </Button>
        </span>
      ) : (
        trigger
      )}
    </span>
  );
}

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function Row({ label, hint, children, onClick, mono, icon }: { label: React.ReactNode; hint?: React.ReactNode; children?: React.ReactNode; onClick?: () => void; mono?: boolean; icon?: IconName }) {
  const body = (
    <>
      {icon && (
        <span className="flex size-7 shrink-0 items-center justify-center rounded-control bg-inset text-ink-2 shadow-hairline">
          <Icon name={icon} size={14} />
        </span>
      )}
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] text-ink">{label}</div>
        {hint && <div className="mt-0.5 truncate text-[11.5px] text-ink-3">{hint}</div>}
      </div>
      {children !== undefined && <div className={cx("flex min-w-0 shrink-0 items-center gap-2 text-[12.5px] text-ink-2", mono && "font-mono")}>{children}</div>}
      {onClick && <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />}
    </>
  );
  return onClick ? (
    <button type="button" onClick={onClick} className="flex w-full items-center gap-3 whitespace-nowrap px-4 py-2.5 text-left transition-colors hover:bg-hover pointer-coarse:min-h-11">
      {body}
    </button>
  ) : (
    <div className="flex items-center gap-3 px-4 py-2.5">{body}</div>
  );
}

/** `animated`: a change of tone crossfades its colors (an extension's badge that the extension redraws, #2757). */
export function Pill({ children, tone, animated }: { children: React.ReactNode; tone?: "accent" | "green" | "red" | "orange"; animated?: boolean }) {
  return (
    <span
      style={animated ? { transitionTimingFunction: EASE } : undefined}
      className={cx(
        animated && "transition-[background-color,color,box-shadow] duration-300",
        "inline-flex h-5 shrink-0 items-center whitespace-nowrap rounded-full px-1.5 text-[11px] font-medium",
        tone === "accent" ? "bg-accent-tint text-accent-ink" : tone === "green" ? "bg-green-tint text-green" : tone === "red" ? "bg-red-tint text-red" : tone === "orange" ? "bg-orange-tint text-orange" : "bg-inset text-ink-2 shadow-hairline",
      )}
    >
      {children}
    </span>
  );
}

// The switch lives with the other atoms now (the chat's ultracode switch uses it too, #3428); settings pages keep importing it from here.
export { Switch } from "../../ui/atoms";
