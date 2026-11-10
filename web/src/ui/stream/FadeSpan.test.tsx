// @vitest-environment jsdom
// A word that finished fading writes nothing to the page at its animationend (#2905): with a reply streaming, each such
// write cost a style and layout pass per word. It is settled on the timeline, and its chunk's next render drops the class.
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Element } from "hast";
import { expect, it } from "vitest";
import { FadeListItem, FadeSpan } from "./FadeSpan";
import { createWordFadeState } from "./wordFade";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ended = (el: HTMLElement, animationName: string) => el.dispatchEvent(Object.assign(new Event("animationend", { bubbles: true }), { animationName }));

it("settles a word and a list dot at animationend without touching them; the next render drops the class and marker", () => {
  const fade = createWordFadeState();
  const node = (key: string) => ({ type: "element", tagName: "span", properties: { dataWfKey: key }, children: [] }) as Element;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const draw = () =>
    act(() =>
      root.render(
        <ul>
          <FadeListItem node={node("wf-0")} fade={fade} {...{ "data-stream-marker": true }}>
            <FadeSpan node={node("wf-0")} fade={fade} className="stream-word">
              word
            </FadeSpan>
          </FadeListItem>
        </ul>,
      ),
    );
  draw();
  const li = host.querySelector("li")!;
  const span = host.querySelector("span")!;
  act(() => {
    ended(span, "stream-word-in");
    ended(li, "stream-marker-in");
  });
  expect(fade.timeline.settled.has("wf-0")).toBe(true);
  expect(span.className).toBe("stream-word");
  expect(li.hasAttribute("data-stream-marker")).toBe(true);
  draw();
  expect(span.hasAttribute("class")).toBe(false);
  expect(li.hasAttribute("data-stream-marker")).toBe(false);
  act(() => root.unmount());
  host.remove();
});
