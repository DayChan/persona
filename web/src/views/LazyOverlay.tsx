// Overlays a first screen does not draw (the command palette, New agent, dialogs and popovers opened by a click): their chunks load in
// the idle preload (lib/lazy-page.ts) or on the first open, and each mounts then and stays mounted for its exit.
import { Component, Suspense, type ComponentType, type ErrorInfo, type ReactNode, useState } from "react";
import { reportBoundary } from "../lib/error-reports";
import { lazyPage } from "../lib/lazy-page";
import { isChunkError } from "./PageBoundary";

/** True from the first render where `open` is, and kept so afterwards: a closed overlay stays mounted for its exit. */
export function useOpened(open: boolean): boolean {
  const [opened, setOpened] = useState(open);
  if (open && !opened) setOpened(true);
  return opened || open;
}

/**
 * Keeps a lazy overlay's failure (its chunk would not load: offline, or the hub was redeployed) from taking the shell
 * down, since it sits outside PageBoundary: the overlay shows nothing and is closed, so the next open from any entry
 * (shortcut, sidebar button) is a fresh open that tries again (lazyPage does not keep a failed load).
 */
export class OverlayBoundary extends Component<{ open: boolean; onClose: () => void; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    // a chunk that would not load is a warning (a redeploy), anything else a render crash (docs/web/web.md "Error reports")
    reportBoundary(error, "overlay_boundary", isChunkError(error), info.componentStack);
    // left open, an open request would not change the state, and the entry points would do nothing until a reload
    this.props.onClose();
  }

  componentDidUpdate(prev: { open: boolean }) {
    if (this.state.failed && this.props.open && !prev.open) this.setState({ failed: false });
  }

  render() {
    return this.state.failed ? null : this.props.children;
  }
}

/** An overlay from its own chunk, mounted while it is shown (a popover): behind OverlayBoundary and an empty Suspense. */
export function lazyOverlay<P extends { onClose: () => void }>(load: () => Promise<ComponentType<P>>): ComponentType<P & { open?: boolean }> {
  const Overlay = lazyPage(load);
  function LazyOverlay(props: P & { open?: boolean }) {
    return (
      <OverlayBoundary open={props.open ?? true} onClose={props.onClose}>
        <Suspense fallback={null}>
          <Overlay {...props} />
        </Suspense>
      </OverlayBoundary>
    );
  }
  return LazyOverlay;
}

/**
 * A dialog from its own chunk: nothing until its first open, then the dialog itself (as lazyOverlay), kept mounted so it
 * closes with its exit as before. The chunk also comes with the idle preload.
 */
export function lazyDialog<P extends { open: boolean; onClose: () => void }>(load: () => Promise<ComponentType<P>>): ComponentType<P> {
  const Overlay = lazyOverlay(load);
  function LazyDialog(props: P) {
    return useOpened(props.open) ? <Overlay {...props} /> : null;
  }
  return LazyDialog;
}
