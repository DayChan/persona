/** Only pass resources created by this driver, never a shared/CDP-connected browser. */
export function ownedCleanup(resources) {
  let pending;
  // Normal completion and the deadline share one attempt, including an in-flight close.
  return () => pending ??= Promise.allSettled([
    async () => { await resources().browser?.close(); },
    async () => { await resources().vite?.close(); },
    async () => {
      const mock = resources().mock;
      try {
        mock?.closeAllConnections();
      } finally {
        if (mock?.listening) await new Promise((done, reject) => mock.close((error) => error ? reject(error) : done()));
      }
    },
  ].map((close) => close())).then((results) => results.filter((r) => r.status === "rejected").map((r) => r.reason));
}
