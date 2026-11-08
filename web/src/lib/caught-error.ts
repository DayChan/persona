// What an error boundary caught (PageBoundary, OverlayBoundary), for createRoot's onCaughtError (#1574). React's own
// handler logs only the error, so a production crash says nothing about which component threw; this logs its component
// stack after it. What the boundaries draw and report does not change. Production names are minified: the frames'
// file and position map back with a source-mapped build of the same commit.

export function logCaughtError(error: unknown, info: { componentStack?: string | null }): void {
  console.error(error);
  if (info.componentStack) console.error(`The error above was caught by an error boundary. Component stack:${info.componentStack}`);
}
