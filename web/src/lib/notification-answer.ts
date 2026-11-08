// Allow / Deny on a permission card's system notification (#4464, the app's NotificationActions): the service worker
// hands the click to an open page of the app (lib/sw-click.ts clickNotification), which answers the card as the card's
// own buttons do (lib/card-answer.ts), with its own token, and stays where it is. The worker never sees the token.
import { t } from "../i18n";
import { toast } from "../ui/atoms";
import { stripBase } from "./base";
import { answerCard } from "./card-answer";
import { postNotification } from "./notify";
import { allowBody, denyBody, needsFieldValues } from "./quick-answers";
import { href, navigate } from "./router";
import { getState } from "./store";
import type { Resolution } from "./answered";
import type { NoteMessage } from "./sw-click";
import type { Interaction } from "./types";

/** sw-click.ts NOTE_MESSAGE, written again: importing a value from the worker's module splits it into a chunk the
 *  classic service worker can't load (the test holds the two the same). */
export const NOTE_MESSAGE = "persona.notification";

export type ClickPlan = { open: string } | { ix: Interaction; allow: boolean; body: Resolution };

/** Cards being answered from a notification now: a second click on one is not sent again. */
const answering = new Set<string>();
/** Cards whose Could not allow / Could not deny notification is up while the card still waits: it goes when the card does (#5070). */
const failed = new Set<string>();
const failedTag = (id: string) => `answer-failed#${id}`;

/** The failure notifications whose card is no longer pending (answered elsewhere, withdrawn): to take back. */
export function failedTagsOver(pending: readonly Interaction[]): string[] {
  const ids = new Set(pending.map((ix) => ix.id));
  const over = [...failed].filter((id) => !ids.has(id));
  over.forEach((id) => failed.delete(id));
  return over.map(failedTag);
}

const isNote = (m: unknown): m is NoteMessage => {
  const n = m as Partial<NoteMessage> | null;
  return n?.type === NOTE_MESSAGE && typeof n.ix === "string" && typeof n.until === "number" && ["allow", "deny", "open"].includes(n.action ?? "");
};

/**
 * What this page does with a click the worker hands it: answer the card (the body the card's own Allow / Deny send), or
 * open the conversation (a plain click). Nothing (the worker opens the conversation itself) for a message too late for
 * the worker to still be waiting, a card this page does not have (answered already, or not loaded yet), one that is
 * no permission card, or one already being answered from a notification.
 */
export function planClick(m: unknown, interactions: readonly Interaction[], now: number): ClickPlan | undefined {
  // until is the worker's Date.now() and now the page's: one browser on one machine, so the same clock
  if (!isNote(m) || now > m.until) return undefined;
  if (m.action === "open") return m.url?.startsWith("/") && !m.url.startsWith("//") ? { open: m.url } : undefined;
  const ix = interactions.find((x) => x.id === m.ix);
  if (ix?.kind !== "permission" || needsFieldValues(ix) || answering.has(ix.id)) return undefined;
  const allow = m.action === "allow";
  return { ix, allow, body: allow ? allowBody() : denyBody() };
}

/** A message from the worker: says on its port whether this page took the click, then carries it out. */
export function onWorkerMessage(e: Pick<MessageEvent, "data" | "ports">): void {
  if ((e.data as { type?: unknown } | null)?.type !== NOTE_MESSAGE) return;
  const plan = planClick(e.data, getState().interactions, Date.now());
  e.ports[0]?.postMessage(plan !== undefined);
  if (!plan) return;
  if ("open" in plan) return navigate(stripBase(plan.open));
  void answer(plan.ix, plan.allow, plan.body);
}

async function answer(ix: Interaction, allow: boolean, body: Resolution) {
  answering.add(ix.id);
  try {
    const r = await answerCard(ix, body);
    if (r.ok) return;
    // nobody is looking at the page: the failure is a notification of its own, a click opens the conversation
    const route = { name: "session", mid: ix.machine_id, sid: ix.session_id } as const;
    if (document.visibilityState === "visible" && document.hasFocus()) toast(r.error, "error");
    else {
      postNotification({ title: allow ? t("notify.allow_failed") : t("notify.deny_failed"), body: r.error, tag: failedTag(ix.id), url: href(route) }, () => navigate(route));
      // a card that is over already (expired, withdrawn) keeps it: the notification is the only word of why
      if (getState().interactions.some((x) => x.id === ix.id)) failed.add(ix.id);
      else failed.delete(ix.id); // an earlier failure's mark must not take this one back
    }
  } finally {
    answering.delete(ix.id);
  }
}

/** Listens for the worker's clicks (from useAttentionSignals); nothing where there is no service worker. */
export function listenForNotificationClicks(): () => void {
  const sw = typeof navigator === "undefined" ? undefined : navigator.serviceWorker;
  if (!sw) return () => {};
  sw.addEventListener("message", onWorkerMessage);
  sw.startMessages?.();
  return () => sw.removeEventListener("message", onWorkerMessage);
}
