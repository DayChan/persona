// The quiet notice over the inbox list naming the sources the hub could not read this round (#1849,
// docs/assistant/inbox.md "Triage status"): their items stay, but may be behind, so the list should not look complete.
// Neutral, not the triage banner's orange: nothing waits and nothing is wrong with the list itself. Not dismissable: it
// goes with the next good read (the list's next pull). The same lines and place as Android's InboxUnreadNotice.
import { triageUnread, type InboxTriage } from "../../lib/inbox";
import { Icon } from "../../ui/icons";

export function UnreadNotice({ triage }: { triage: InboxTriage | null | undefined }) {
  const lines = triageUnread(triage);
  if (!lines.length) return null;
  return (
    <div role="status" data-inbox-unread className="mt-3 flex items-start gap-2.5 rounded-card bg-field px-3 py-2.5 text-ink-2">
      <Icon name="info" size={14} stroke={2.2} className="mt-0.5 shrink-0 text-ink-3" />
      <div className="min-w-0 flex-1 text-[12.5px] leading-[1.5] text-pretty break-words">
        {lines.map((line, i) => <p key={i}>{line}</p>)}
      </div>
    </div>
  );
}
