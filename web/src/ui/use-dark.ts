import { useSyncExternalStore } from "react";

/** Whether the page is in dark mode now (the class views/theme.ts puts on <html> before the first render). */
export function useDark(): boolean {
  return useSyncExternalStore(
    (on) => {
      const mo = new MutationObserver(on);
      mo.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
      return () => mo.disconnect();
    },
    () => document.documentElement.classList.contains("dark"),
    () => false,
  );
}
