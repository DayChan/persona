import { t } from "../i18n";

/** "Recommended" beside the option Claude suggests (its "(Recommended)" suffix, iOS RecommendedTag); `assistant`: the
 *  assistant's pick instead, when it left the question to the user with a recommendation (`delegate.recommend`, #544). */
export function RecommendedTag({ assistant = false }: { assistant?: boolean }) {
  return (
    <span data-assistant-pick={assistant || undefined} className="ml-1.5 inline-block rounded-[4px] bg-accent-tint px-1 align-[1px] text-[11px] font-medium whitespace-nowrap text-accent-ink">
      {assistant ? t("chat-delegate.recommended") : t("chat-interactions.recommended")}
    </span>
  );
}
