// A request from a script outside an agent session has a card but no chat.
import { useStore } from "../lib/store";
import { navigate } from "../lib/router";
import { IconButton, Loading } from "../ui/atoms";
import { AnsweredCard, InteractionCard } from "./chat/Interactions";
import { t } from "../i18n";

export function RequestPage({ id, onToggleSidebar }: { id: string; onToggleSidebar: () => void }) {
  const loaded = useStore((s) => s.loaded);
  const request = useStore((s) => s.interactions.find((ix) => ix.id === id));
  const answered = useStore((s) => s.answered[id]);
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("request-page.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <IconButton icon="chevronLeft" label={t("request-page.back")} onClick={() => navigate({ name: "home" })} />
        <h1 className="min-w-0 flex-1 truncate text-sm font-semibold text-ink">{t("request-page.title")}</h1>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-3xl p-3 sm:p-4">
          {!loaded ? <Loading /> : request ? <InteractionCard key={request.id} ix={request} /> : answered ? <AnsweredCard card={answered} /> : <p role="status" className="text-sm text-ink-2">{t("request-page.ended")}</p>}
        </div>
      </div>
    </div>
  );
}
