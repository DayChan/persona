// What the chat page shows, as a spec/fixtures/sync view (spec/fixtures/sync/README.md "Views"): the user's messages
// and each turn's answer in screen order, the cards and whether they can be answered, the streamed text. The page
// marks its bubbles and cards for this (data-user-bubble / data-send-state, data-answer, data-card, data-answered).
import type { Message, View } from "./fake-hub";

const text = (el: Element) => (el.textContent ?? "").replace(/\s+/g, " ").trim();

/** `sent`: the words of the case's sends on this device. One that failed stays in the chat as a failed bubble (#1122);
 *  its words back in the composer would be the message a second time, so the box counts as a copy. */
export function project(root: HTMLElement, sent: string[]): Required<Pick<View, "messages" | "cards" | "live">> {
  const messages: Message[] = [];
  let live: string | null = null;
  for (const el of root.querySelectorAll<HTMLElement>("[data-user-bubble], [data-answer]")) {
    if (el.hasAttribute("data-answer")) {
      if (el.dataset.answer === "streaming") live = text(el);
      else messages.push({ from: "agent", text: text(el) });
      continue;
    }
    const state = el.dataset.sendState;
    const words = el.querySelector("[data-bubble-text]");
    messages.push({ from: "user", text: words ? text(words) : "", state: state === "failed" ? "failed" : state ? "pending" : "shown" });
  }
  // the words of a send back in the box (a send that never went puts them back; ChatView keeps a failed one in the chat)
  const box = root.querySelector<HTMLTextAreaElement>("[data-chat-dock] textarea, [data-new-conversation] textarea")?.value ?? "";
  for (const words of sent) if (box.includes(words)) messages.push({ from: "user", text: words, state: "failed" });
  const cards: Record<string, "open" | "answering" | "answered"> = {};
  for (const el of root.querySelectorAll<HTMLElement>("[data-answered]")) cards[el.dataset.answered!] = "answered";
  for (const el of root.querySelectorAll<HTMLElement>("[data-card]")) {
    // only what answers it: its actions and hand back, not a toggle in its body
    const enabled = [...el.querySelectorAll<HTMLButtonElement>("[data-card-actions] button, button[data-card-answer]")].some((b) => !b.disabled && !b.closest("fieldset[disabled]"));
    cards[el.dataset.card!] = enabled ? "open" : "answering";
  }
  return { messages, cards, live };
}
