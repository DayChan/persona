// First: the live screen's token is read before App's module takes any token out of the address bar.
import { screen } from "./screen/take";
import { initI18n } from "./i18n";
import "./styles.css";
import { applyLook } from "./ui/look";
import { logCaughtError } from "./lib/caught-error";

// Theme color and text size (docs/web/web.md "Appearance") before anything draws.
applyLook();

// The language's catalog loads before the rest of the app: their modules call t() as they load (docs/web/web.md "Internationalization").
// React and the PWA helper say no t(), so they load alongside it rather than after it.
// An overlay's history entry a reload left behind goes before the app reads the address (lib/back-close.ts, #5134).
const [, { StrictMode, createElement }, { createRoot }, { startPWA }] = await Promise.all([
  initI18n(),
  import("react"),
  import("react-dom/client"),
  import("./lib/pwa"),
  import("./lib/back-close").then((m) => m.settleOverlayEntry()),
]);

startPWA();

// The live screen (/screen/<machine>) is a page of its own: no sign-in flow, no sidebar, and noVNC loads only there.
const target = screen;
// a crash a boundary catches is logged with its component stack (lib/caught-error.ts)
const root = createRoot(document.getElementById("root")!, { onCaughtError: logCaughtError });
if (target) {
  const { ScreenView } = await import("./screen/ScreenView");
  root.render(createElement(StrictMode, null, createElement(ScreenView, { target })));
} else {
  // Error reports (docs/web/web.md "Error reports") start with the app: they go out only if the hub's hello has the feature.
  const [{ ExtensionThemeProvider }, { App }] = await Promise.all([import("./ui/ExtensionThemePicker"), import("./App"), import("./lib/error-reports-start")]);
  root.render(createElement(StrictMode, null, createElement(ExtensionThemeProvider, null, createElement(App))));
}
