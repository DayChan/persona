// Inbox (docs/assistant/inbox.md): Feishu messages, mail, Feishu documents and Moments in one list, sorted by the AI's triage,
// with the agents waiting on you at the head of "Needs you". Wide: the list (380px) and the opened item side by side;
// narrow: the opened item takes the list's place, with a way back. "Select" (top right) picks several items to mark them
// done at once (#2059).
import { useEffect, useState } from "react";
import { useInbox, refreshInbox, type InboxItem } from "../../lib/inbox";
import { sameManager } from "../../lib/manager-choice";
import { newDraft, useMailAccounts } from "../../lib/mail";
import { navigate } from "../../lib/router";
import { t } from "../../i18n";
import { Button, cx, IconButton, Spinner } from "../../ui/atoms";
import { ComposeDialog } from "./ComposeDialog";
import { DetailPlaceholder, InboxDetail } from "./InboxDetail";
import { InboxList } from "./InboxList";
import { PolicyDialogHost } from "./PolicyDialog";
import { FolderDialogHost } from "./InboxFolders";

/** The item a route opens: its id of its manager (none: the default's). A link that names no manager (from before
 *  several managers, or written by hand) opens the default's copy, else the one manager that has it. */
export function routedItem(items: InboxItem[], id: string | undefined, manager: string | undefined): InboxItem | undefined {
  if (!id) return undefined;
  const same = items.filter((x) => x.id === id);
  return same.find((x) => sameManager(x.manager, manager)) ?? (manager ? undefined : same[0]);
}

export function Inbox({ id, manager, onToggleSidebar }: { id?: string; manager?: string; onToggleSidebar: () => void }) {
  const item = useInbox((s) => routedItem(s.items, id, manager));
  const loaded = useInbox((s) => s.loaded);
  const loading = useInbox((s) => s.loading);
  const accounts = useMailAccounts();
  const [composing, setComposing] = useState(false);
  // Selecting (#2059): the picked items' keys (itemKey); null: not selecting. Esc leaves it.
  const [picked, setPicked] = useState<Set<string> | null>(null);
  const picking = picked !== null;
  useEffect(() => {
    if (!picking) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented) setPicked(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [picking]);
  return (
    <div className="@container flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("inbox-inbox.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        {id && <IconButton icon="chevronLeft" label={t("inbox-inbox.back_to_inbox")} onClick={() => navigate({ name: "inbox" })} className="@3xl:hidden" />}
        <h1 className="min-w-0 flex-1 truncate text-[14px] font-semibold text-ink">{t("inbox-inbox.title")}</h1>
        {loading && loaded && <Spinner />}
        {picking ? (
          <Button tone="quiet" onClick={() => setPicked(null)}>{t("inbox-inbox.select_done")}</Button>
        ) : (
          <>
            {/* the list's: not over an opened item that has the list's place */}
            <Button tone="quiet" onClick={() => setPicked(new Set())} className={cx(id && "@max-3xl:hidden")}>{t("inbox-inbox.select")}</Button>
            <IconButton icon="compose" label={accounts.length ? t("inbox-inbox.compose") : t("inbox-inbox.compose_no_account")} onClick={() => setComposing(true)} disabled={!accounts.length} />
            <IconButton icon="retry" label={t("inbox-inbox.refresh")} onClick={() => void refreshInbox()} disabled={loading} />
          </>
        )}
      </header>
      <div className="flex min-h-0 flex-1">
        <section aria-label={t("inbox-inbox.list")} className={cx("min-h-0 w-full flex-col @3xl:flex @3xl:w-[380px] @3xl:shrink-0 @3xl:border-r @3xl:border-line", id ? "hidden" : "flex")}>
          <InboxList selected={item ?? (id ? { id, manager } : undefined)} picked={picked} onPicked={setPicked} />
        </section>
        <section aria-label={t("inbox-inbox.detail")} className={cx("min-h-0 min-w-0 flex-1 flex-col @3xl:flex", id ? "flex" : "hidden")}>
          {id ? <InboxDetail item={item} loaded={loaded} /> : <DetailPlaceholder />}
        </section>
      </div>
      <PolicyDialogHost />
      <FolderDialogHost />
      {composing && <ComposeDialog draftKey="new" initial={newDraft(accounts[0]?.id ?? "")} accounts={accounts} onClose={() => setComposing(false)} />}
    </div>
  );
}
