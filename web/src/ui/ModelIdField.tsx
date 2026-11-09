// "Other model…" (#1644): a model ID typed by hand, checked as it is typed with the hub's rule (lib/model-id.ts). The
// reason stays under the field while the ID can't be used; Enter or "Use" takes it, Esc puts the row back. Its keys are
// its own (a popover or menu around it skips `[data-model-id-field]`). Where a form around it has its own confirm (the
// session popover's Apply), `onValid` hands it each good ID as it is typed and `useButton={false}` drops "Use".
import { useEffect, useId, useRef, useState } from "react";
import { t } from "../i18n";
import { cleanModelId, modelIdRule, typedModelProblem } from "../lib/model-id";
import { composing } from "../lib/send-history";
import { Button, cx } from "./atoms";

export function ModelIdField({ initial = "", onUse, onValid, useButton = true, onCancel, note, disabled, className }: {
  /** What the field starts with (the typed pick, when there is one). */
  initial?: string;
  /** Enter (or "Use") on an ID the hub would take. */
  onUse: (id: string) => void;
  /** Each change: the ID when the hub would take it, "" while it is empty or can't be used. */
  onValid?: (id: string) => void;
  /** "Use" beside the field; off where the form's own button confirms. */
  useButton?: boolean;
  onCancel?: () => void;
  /** Under the rule: what using it does here. */
  note?: string;
  /** The launcher passes its own --model: nothing typed would reach the agent. */
  disabled?: boolean;
  className?: string;
}) {
  const [text, setText] = useState(initial);
  const hint = useId();
  const id = cleanModelId(text);
  const problem = typedModelProblem(text);
  const ok = Boolean(id) && !problem && !disabled;
  const valid = useRef(onValid);
  valid.current = onValid;
  useEffect(() => {
    valid.current?.(ok ? id : "");
  }, [ok, id]);
  // in a list that scrolls (a dialog, a menu): the field and what it says come into view, the reason as it appears
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    box.current?.scrollIntoView?.({ block: "nearest" });
  }, [problem]);
  const use = () => {
    if (ok) onUse(id);
  };
  return (
    <div ref={box} data-model-id-field className={cx("flex flex-col gap-1", className)}>
      <div className="flex items-center gap-1.5">
        <input
          autoFocus
          value={text}
          disabled={disabled}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (composing(e.nativeEvent)) return;
            if (e.key === "Enter") {
              e.preventDefault();
              e.stopPropagation();
              use();
            } else if (e.key === "Escape") {
              e.preventDefault();
              e.stopPropagation();
              onCancel?.();
            } else e.stopPropagation();
          }}
          aria-label={t("ui-modelidfield.label")}
          aria-invalid={problem ? true : undefined}
          aria-describedby={hint}
          placeholder={t("ui-modelidfield.placeholder")}
          spellCheck={false}
          autoComplete="off"
          autoCapitalize="off"
          className={cx(
            "h-8 min-w-0 flex-1 rounded-[6px] bg-inset px-2 font-mono text-[12.5px] text-ink outline-none placeholder:font-sans placeholder:text-ink-3 pointer-coarse:h-10 disabled:opacity-50",
            // one ring or the other: both set the same shadow, and the hairline would win
            problem ? "shadow-[0_0_0_1px_var(--red)]" : "shadow-hairline focus-visible:shadow-[0_0_0_1px_var(--line-strong)]",
          )}
        />
        {useButton && <Button tone="primary" disabled={!ok} onClick={use}>{t("ui-modelidfield.use")}</Button>}
      </div>
      {/* the rule, then what using it does here, each on its own line */}
      <p id={hint} role={problem ? "alert" : undefined} data-model-id-problem={problem ? "" : undefined} className={cx("px-0.5 text-[11.5px] leading-snug break-words whitespace-pre-line", problem ? "text-red" : "text-ink-3")}>
        {problem || [modelIdRule(), note].filter(Boolean).join("\n")}
      </p>
    </div>
  );
}
