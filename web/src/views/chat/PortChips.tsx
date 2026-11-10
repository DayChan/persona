// The ports an agent's answer links on its computer, as chips under the answer (docs/operations/ports.md "Recognizing
// links in replies"; iOS PortChips): `:5173` with the page's title, or Stopped (dimmed) once nothing listens there.
// A click shows the page in the preview panel (docs/web/preview.md): a browser on that computer draws it, since this
// computer's localhost is not the agent's.
import { useMemo } from "react";
import { isHubPortLink } from "../../lib/links";
import { openPanel } from "../../lib/panel";
import { portLabel, portLinks, portURL, type PortLink } from "../../lib/port-links";
import { usePortStates, type PortState } from "../../lib/port-presence";
import { PREVIEW_FEATURE } from "../../lib/preview";
import { hubHas, useStore } from "../../lib/store";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";

/** The chips of an agent's finished answer; nothing in an assistant conversation, without a working folder, or on a
 *  hub without previews. Only an answer that might link a port subscribes to the store: a long chat has hundreds. */
export function ReplyPorts(props: { text: string; mid: string; sid: string; cwd?: string }) {
  return props.cwd && mightLinkPort(props.text) ? <PortChipRow {...props} /> : null;
}

/** A host with a port: what every port link has (portLinks decides). */
const HOST_PORT = /(?:\/\/[^\s/:]+|localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1?\]):\d/i;
export const mightLinkPort = (text: string) => HOST_PORT.test(text);

function PortChipRow({ text, mid, sid, cwd }: { text: string; mid: string; sid: string; cwd?: string }) {
  const previews = useStore((s) => hubHas(s.features, PREVIEW_FEATURE));
  const agent = useStore((s) => (s.sessions[mid] ?? []).some((x) => x.session_id === sid && !x.assistant));
  const machine = useStore((s) => s.machines.find((m) => m.id === mid));
  const hostname = machine?.hostname ?? "";
  const links = useMemo(() => {
    if (!previews || !agent || !cwd) return [];
    const names = hostname ? [hostname] : [];
    return portLinks(text, names).filter((l) => !isHubPortLink(l, names));
  }, [text, previews, agent, cwd, hostname]);
  const state = usePortStates(mid, links.map((l) => l.port));
  if (!links.length || !cwd) return null;
  return (
    <div className="mt-2 flex max-w-full flex-wrap gap-1.5" data-testid="port-chips">
      {links.map((l) => (
        <PortChip key={`${l.port}${l.path}${l.secure}`} link={l} state={state(l.port)} machine={machine?.name || mid} onOpen={() => openPanel({ kind: "preview", mid, sid, cwd, port: l.port, path: l.path })} />
      ))}
    </div>
  );
}

function PortChip({ link, state, machine, onOpen }: { link: PortLink; state?: PortState; machine: string; onOpen: () => void }) {
  const gone = state?.kind === "gone";
  const detail = state?.kind === "live" ? state.title : gone ? t("chat-portchips.stopped") : undefined;
  return (
    <button
      type="button"
      onClick={onOpen}
      data-port={link.port}
      title={`${gone ? t("chat-portchips.gone_hint") : t("chat-portchips.open_hint", { machine })}\n${portURL(link)}`}
      className={`flex max-w-full min-w-0 items-center gap-1.5 rounded-full bg-inset px-3 py-1 text-[12px] whitespace-nowrap text-ink-2 shadow-hairline transition-colors hover:bg-hover-2 ${gone ? "opacity-60" : ""}`}
    >
      <Icon name="web" size={13} className="shrink-0 text-ink-3" />
      <span className="shrink-0 font-mono text-ink tabular-nums">{portLabel(link)}</span>
      {detail && <span className="min-w-0 truncate text-ink-3">{detail}</span>}
    </button>
  );
}
