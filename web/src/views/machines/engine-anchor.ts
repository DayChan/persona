// The machine page header's engine pill (#2842) opens Engine below: EngineSection renders <section id="engine">.
import { scrollBehavior } from "../../ui/stream/motion";

export const ENGINE_SECTION_ID = "engine";

/** Scroll to Engine (no animation when the system asks for less motion) and move focus to the chosen engine there (the
 * first checked one with host sets), else to the section, so the keyboard goes on from it rather than from the header. */
export function openEngineSection() {
  const section = document.getElementById(ENGINE_SECTION_ID);
  if (!section) return;
  section.scrollIntoView({ behavior: scrollBehavior(), block: "start" });
  const chosen = '[role="radio"][aria-checked="true"]:not(:disabled), [role="checkbox"][aria-checked="true"]:not(:disabled)';
  (section.querySelector<HTMLElement>(chosen) ?? section).focus({ preventScroll: true });
}
