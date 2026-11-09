// The icons UI version 6 adds (lib/extension-ui.ts NEW_ICONS_6), as the web's own glyphs: one meaning, one icon
// (the apps map the same names to their SF Symbols and Phosphor icons).
import type { NEW_ICONS_6 } from "../../lib/extension-ui";
import type { IconName } from "../../ui/icons";

export const NEW_ICON_GLYPH: Record<(typeof NEW_ICONS_6)[number], IconName> = {
  home: "home", sofa: "sofa", light: "light", power: "power", fan: "fan", thermometer: "thermometer", droplet: "droplet", lock: "lock",
  unlock: "unlock", door: "door", window: "window", camera: "camera", video: "video", play: "play", pause: "pause", stop: "stop",
  speaker: "speaker", tv: "tv", plug: "plug", battery: "battery", bolt: "bolt", sun: "sun", moon: "moon", cloud: "cloud", rain: "rain",
  snow: "snow", wind: "wind", leaf: "leaf", car: "car", location: "location", robot: "robot", wifi: "wifi", shield: "shield",
  trend_up: "trendUp", trend_down: "trendDown", wallet: "wallet", bank: "bank", heart: "heart", plus: "plus", minus: "minus",
  trash: "trash", edit: "pencil", refresh: "retry", share: "share", info: "info",
};
