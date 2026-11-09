// Writes that survive a hub restart (#1574): one op id per logical operation, sent as X-Persona-Op-Id, so the hub
// does it once however many times it arrives; a request that died on the way (the old process going, a proxy
// answering 502/503/504) is sent again with the same id when the socket is back, or after a short wait, for up to a
// minute before the caller shows its failure; while the socket is down too (the hub redeploying) for as long as the
// page tells it as a restart (#4174).
import { api, isTransient } from "./api";
import { RESTARTING_MS } from "./connection";
import { isUUID, uuid } from "./format";
import { isOpen, onHubEvent } from "./store";

export const OP_HEADER = "X-Persona-Op-Id";
/** How long a write keeps trying through a gap before it fails. */
export const OP_RETRY_MS = 60_000;
/** How long it keeps trying while the socket is down as well: a hub that is away, not one that answers badly. */
export const OP_OFFLINE_MS = RESTARTING_MS;

export { isTransient };

/** The wait before attempt `n` (1-based): 0.5 s, 1 s, 2 s, then every 4 s. */
export function opDelay(n: number): number {
  return Math.min(4_000, 500 * 2 ** Math.max(0, n - 1));
}

export interface RetryClock {
  now(): number;
  /** Waits `ms`, or less when there is a reason to try sooner (the socket came back). */
  wait(ms: number): Promise<void>;
  /** The socket to the hub is open (missing: taken as open). */
  online?(): boolean;
}

const realClock: RetryClock = {
  now: () => Date.now(),
  wait: (ms) =>
    new Promise<void>((done) => {
      const finish = () => {
        clearTimeout(t);
        off();
        done();
      };
      const t = setTimeout(finish, ms);
      const off = onHubEvent("hello", finish); // the hub is back: try now
    }),
  online: () => isOpen(),
};

/**
 * Runs `run` until it answers, fails with something other than a gap, or `limit` ms have passed (then the last
 * error is thrown); while the socket is down, until `offlineLimit` (the hub redeploying: a message sent then goes
 * once it is back instead of landing in the box again), and `limit` again from when it is back. `run` must send the same op every time: the caller makes its
 * ids once, outside.
 */
export async function untilDelivered<T>(run: () => Promise<T>, clock: RetryClock = realClock, limit = OP_RETRY_MS, offlineLimit = OP_OFFLINE_MS): Promise<T> {
  const start = clock.now();
  let deadline = start + limit;
  let wasAway = false;
  for (let n = 1; ; n++) {
    try {
      return await run();
    } catch (e) {
      if (!isTransient(e)) throw e;
      const now = clock.now();
      const away = clock.online ? !clock.online() : false;
      if (away) deadline = Math.max(deadline, start + offlineLimit);
      // just back from being away (a hub still warming up): a whole ordinary window from here, not what was left of it
      else if (wasAway) deadline = Math.max(deadline, now + limit);
      wasAway = away;
      const left = deadline - now;
      if (left <= 0) throw e;
      await clock.wait(Math.min(opDelay(n), left));
    }
  }
}

/** A hub write with an op id (made here unless given), retried through a gap with that same id. */
export function apiOp<T>(path: string, init: Parameters<typeof api>[1] & { opId?: string } = {}, clock?: RetryClock): Promise<T> {
  const { opId = uuid(), ...rest } = init;
  return untilDelivered(() => {
    const headers = new Headers(rest.headers);
    // an id made before #1651 (a message still in the outbox) is no UUID: the hub would refuse it; client_msg_id
    // still keeps a message from going twice
    if (isUUID(opId)) headers.set(OP_HEADER, opId);
    return api<T>(path, { ...rest, headers });
  }, clock);
}
