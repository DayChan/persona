// A slider in the web's design system (a native range input, styled by .range in styles.css): the accent fills the
// track up to the thumb. `onInput` follows the thumb; `onCommit` comes once the value is settled: the pointer let go,
// or 0.6 s after the last arrow key (so a drag or a run of key presses is one change, not dozens).
import { useEffect, useRef } from "react";
import { cx } from "./atoms";

export function Slider({
  value,
  min,
  max,
  step,
  disabled,
  label,
  valueText,
  onInput,
  onCommit,
  className,
}: {
  value: number;
  min: number;
  max: number;
  step: number;
  disabled?: boolean;
  /** The slider's accessible name. */
  label: string;
  /** How the value is read out ("60 %", "4000 K"). */
  valueText?: string;
  onInput?: (v: number) => void;
  onCommit: (v: number) => void;
  className?: string;
}) {
  const ref = useRef<HTMLInputElement>(null);
  const keyed = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const commit = useRef(onCommit);
  commit.current = onCommit;
  // the native change event (React's onChange is the input event): a pointer's comes as it lets go, a key's at once
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const changed = () => {
      const v = Number(el.value);
      clearTimeout(timer.current);
      if (keyed.current) timer.current = setTimeout(() => commit.current(v), 600);
      else commit.current(v);
    };
    el.addEventListener("change", changed);
    return () => {
      el.removeEventListener("change", changed);
      clearTimeout(timer.current);
    };
  }, []);
  const fill = max > min ? Math.min(100, Math.max(0, ((value - min) / (max - min)) * 100)) : 0;
  return (
    <input
      ref={ref}
      type="range"
      min={min}
      max={max}
      step={step}
      value={value}
      disabled={disabled}
      aria-label={label}
      aria-valuetext={valueText}
      onKeyDown={() => (keyed.current = true)}
      onPointerDown={() => (keyed.current = false)}
      onChange={(e) => onInput?.(Number(e.target.value))}
      className={cx("range", className)}
      style={{ "--fill": `${fill}%` } as React.CSSProperties}
    />
  );
}
