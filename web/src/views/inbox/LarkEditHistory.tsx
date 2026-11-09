// "Edit history" of a Feishu message (iOS LarkEditHistoryView, docs/extensions/lark-migration.md "Message display"): the earlier
// versions the Lark gateway kept, oldest first, each with when it was replaced. An encrypted version shows the public face
// the hub sent (never the version's text); with "Edit history" turned off meanwhile the list says it is hidden.
import type { ReactNode } from "react";
import { shortTime, type LarkMessage, type LarkPart } from "../../lib/inbox";
import { showsHistory, versionParts, type LarkMessageDisplay } from "../../lib/lark-display";
import { t } from "../../i18n";
import { Dialog } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

export function LarkEditHistoryDialog({ message, display, renderParts, onClose }: {
  message: LarkMessage;
  display: LarkMessageDisplay;
  /** How the chat draws a message's parts. */
  renderParts: (parts: LarkPart[]) => ReactNode;
  onClose: () => void;
}) {
  const versions = showsHistory(display, message) ? message.edit_history ?? [] : [];
  return (
    <Dialog open onClose={onClose} title={t("inbox-larkthread.edit_history")} width={520}>
      {versions.length ? (
        <ol className="-mx-1 max-h-[60vh] divide-y divide-line overflow-y-auto">
          {versions.map((v, i) => (
            <li key={i} className="px-1 py-2.5">
              <p className="mb-1 flex items-center gap-2 text-[11px] text-ink-3 tabular-nums">
                <span>{shortTime(v.edited_at)}</span>
                {v.olk !== undefined && v.olk !== null && (
                  <span className="inline-flex items-center gap-1 rounded-[4px] bg-field px-1 text-[10.5px] whitespace-nowrap">
                    <Icon name="lock" size={10} />{t("inbox-larkthread.edit_version_encrypted")}
                  </span>
                )}
              </p>
              {renderParts(versionParts(v))}
            </li>
          ))}
        </ol>
      ) : (
        <p className="py-4 text-center text-[12.5px] text-ink-3">{t("inbox-larkthread.edit_history_hidden")}</p>
      )}
    </Dialog>
  );
}
