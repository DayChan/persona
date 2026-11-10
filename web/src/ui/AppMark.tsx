// Persona's mark on the page (the sidebar's header, the sign-in page): the chosen look's (avatar, #3200, lib/look-icon.ts).
// With Mochi, the default, it is the iOS app icon itself (apple/Persona/Resources/Assets.xcassets/AppIcon.appiconset
// icon.png / icon-dark.png, scaled into public/ at 64, 128 and 256 px), not a redrawing; another look shows its avatar or
// face. The page's theme picks the light or the dark picture (it can differ from the system's, so not a <picture>): one
// <img> for the theme in effect, so only that file is fetched. views/theme.ts sets the class before the first render, so
// the first paint already has the right one; a switch swaps the picture. The app icon's srcset lets the browser take the
// smallest file sharp at this size and the screen's pixel ratio (32 px at 2x: the 64 px file, which is also the favicon).
import { BASE } from "../lib/base";
import { useLookIcon } from "../lib/look-icon";
import { cx } from "./atoms";
import { useDark } from "./use-dark";

const appIcon = (name: string): { src: string; srcSet?: string } => ({
  src: `${BASE}/${name}.png`,
  srcSet: `${BASE}/${name}-64.png 64w, ${BASE}/${name}-128.png 128w, ${BASE}/${name}.png 256w`,
});

export function AppMark({ size, radius, className }: { size: number; radius: number; className?: string }) {
  const look = useLookIcon();
  const dark = useDark();
  const style = { width: size, height: size, borderRadius: radius };
  const pic: { src: string; srcSet?: string } = look ? { src: dark ? look.dark : look.light } : appIcon(dark ? "app-icon-dark" : "app-icon");
  return (
    <span aria-hidden className={cx("relative inline-flex shrink-0 overflow-hidden", className)} style={style}>
      <img {...pic} sizes={pic.srcSet && `${size}px`} alt="" draggable={false} width={size} height={size} className="shrink-0 object-cover select-none" style={style} />
    </span>
  );
}
