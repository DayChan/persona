import { useState } from "react";
import { t } from "../../i18n";
import { isRelayContext, sourceLabel } from "../../lib/message-source";
import { parseUserText } from "../../lib/turns";
import type { UserMessage } from "../../lib/turns";
import { cx } from "../../ui/atoms";
import { Markdown } from "../../ui/Markdown";
import { useChatContext } from "./chat-context";
import { useExpanded } from "./expanded";
import { useFullText } from "./FullText";
import { keepLineBreaks } from "./Messages";
import { useReveal } from "./search-reveal";
import { AttachedPhotos } from "./AttachedPhotos";
import { FileCards } from "./FileCards";
import { Thumb } from "./Steps";
import { ContextGroup } from "./ContextGroup";

/** Relayed words and automatic inputs are sources in one chat, never participants or user bubbles. */
export function SourceMessage({ msg, mid, sid }: { msg: UserMessage; mid: string; sid: string }) {
  const { scope } = useChatContext();
  const [open, setOpen] = useExpanded(`${scope}source:${msg.id}`);
  const [measuredLong, setMeasuredLong] = useState(false);
  const full = useFullText(msg.injected ? undefined : msg.rawInput?.find((item) => item.kind === "text" && item.full_size), mid, sid);
  const text = msg.injected ? msg.injected.detail || msg.injected.line : full.text ? parseUserText(full.text).text : msg.text;
  const long = measuredLong || text.length > 180 || text.split("\n").length > 3;
  useReveal(scope, [msg.id], () => {
    if (open) return;
    setOpen(true);
    return () => setOpen(false);
  });
  const label = sourceLabel(msg.source!);
  const contexts = (full.text ? parseUserText(full.text).contexts : msg.contexts).filter((context) => context.kind !== "message-source" && !isRelayContext(context));
  return (
    <div className="chat-source" data-message-source={msg.source!.kind} data-source-conversation={msg.source!.conversation_id} data-items={[msg.id, ...msg.images.map((item) => item.id)].join(" ")}>
      {!msg.continuedSource && <span className="chat-source-label" title={label}>@{label}</span>}
      <div className={cx("chat-source-body", long && !open && "chat-source-folded")} data-source-folded={long && !open || undefined} ref={(el) => {
        if (!el || open) return;
        const line = parseFloat(getComputedStyle(el).lineHeight);
        if (line && el.scrollHeight > line * 3.5 && !measuredLong) setMeasuredLong(true);
      }}>
        <Markdown text={keepLineBreaks(text)} mid={mid} small />
      </div>
      {long && <button type="button" className="chat-source-toggle" onClick={() => setOpen((value) => !value)} aria-expanded={open}>
        {t(open ? "chat-segmentview.collapse" : "chat-segmentview.expand")}
      </button>}
      {contexts.length > 0 && <ContextGroup contexts={contexts} />}
      {msg.images.length > 0 && <div className="flex flex-wrap gap-1.5">{msg.images.map((image) => <Thumb key={image.id} item={image} mid={mid} sid={sid} />)}</div>}
      {msg.photos && <AttachedPhotos paths={msg.photos} mid={mid} sid={sid} />}
      {msg.files && <FileCards files={msg.files} mid={mid} />}
      {!msg.injected && full.more}
    </div>
  );
}
