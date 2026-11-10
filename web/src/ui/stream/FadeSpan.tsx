// The spans and list items rehypeStreamWordFade marks (ported from cindy's StreamFadeSpan): the ref puts a segment on
// the message's timeline when it mounts, animationend settles it. Any other span (KaTeX, highlighting) passes through.
// Settling writes nothing to the page: the class (or a list item's marker attribute) goes at the chunk's next render,
// and the whole reply is drawn plain once the stream ends. Taking it off each element at its animationend made every
// word cost the page a style and layout pass of its own while a reply streamed (#2905).
import { useCallback, type AnimationEvent, type ComponentPropsWithoutRef, type CSSProperties } from "react";
import type { Element } from "hast";
import { scheduleSegment, type WordFadeState } from "./wordFade";

type Props<T extends "span" | "li"> = ComponentPropsWithoutRef<T> & { node?: Element; fade: WordFadeState };

const keyOf = (node?: Element) => (typeof node?.properties?.dataWfKey === "string" ? node.properties.dataWfKey : undefined);

// The ref owns the delay on a mounted node: setting it again at every parse would seek a running animation.
function withoutDelay(style: CSSProperties | undefined, key?: string): CSSProperties | undefined {
  if (!key || !style) return style;
  const { ["--wf-delay" as keyof CSSProperties]: _delay, ...rest } = style;
  return Object.keys(rest).length ? rest : undefined;
}

function useFade(fade: WordFadeState, node: Element | undefined, animation: string) {
  const key = keyOf(node);
  const settled = Boolean(key && fade.timeline.settled.has(key));
  const ref = useCallback(
    (el: HTMLElement | null) => {
      if (el && key && !settled) scheduleSegment(fade, key, el);
    },
    [fade, key, settled],
  );
  const onAnimationEnd = (e: AnimationEvent<HTMLElement>) => {
    if (!key || e.target !== e.currentTarget || e.animationName !== animation) return;
    fade.timeline.settled.add(key);
  };
  return { key, settled, ref, onAnimationEnd };
}

export function FadeSpan({ node, fade, className, style, ...props }: Props<"span">) {
  const f = useFade(fade, node, "stream-word-in");
  const cls = f.settled ? className?.split(/\s+/).filter((c) => c && c !== "stream-word").join(" ") || undefined : className;
  return <span key={f.key} ref={f.ref} {...props} className={cls} style={withoutDelay(style, f.key)} data-wf-key={f.key} onAnimationEnd={f.onAnimationEnd} />;
}

export function FadeListItem({ node, fade, style, ...props }: Props<"li">) {
  const f = useFade(fade, node, "stream-marker-in");
  const marker = (props as { "data-stream-marker"?: boolean })["data-stream-marker"];
  return <li ref={f.ref} {...props} data-stream-marker={f.settled ? undefined : marker} style={withoutDelay(style, f.key)} data-wf-key={f.key} onAnimationEnd={f.onAnimationEnd} />;
}
