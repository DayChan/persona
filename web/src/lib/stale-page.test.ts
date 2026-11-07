import { describe, expect, it } from "vitest";
import {
  afterPull,
  afterPush,
  FRESH_TALLY,
  newestItemAt,
  notLive,
  shouldPull,
  STALE_HINT_AFTER,
  STALE_PULL_EVERY_MS,
  STALE_QUIET_MS,
  type StaleView,
} from "./stale-page";

const T0 = Date.parse("2026-11-05T10:00:00Z");
const behind = (over: Partial<StaleView> = {}): StaleView => ({
  updatedAt: new Date(T0 + 60_000).toISOString(),
  arrivedUpdatedAt: new Date(T0 + 20_000).toISOString(),
  lastArrival: T0,
  lastPull: 0,
  now: T0 + STALE_QUIET_MS + 1,
  visible: true,
  pulling: false,
  ...over,
});

describe("shouldPull", () => {
  it("asks when a burst write 0.3 s later lost its push", () => {
    expect(shouldPull(behind({ updatedAt: new Date(T0 + 300).toISOString(), arrivedUpdatedAt: new Date(T0).toISOString() }))).toBe(true);
  });

  it("does not let an invalid arrival timestamp suppress recovery", () => {
    expect(shouldPull(behind({ arrivedUpdatedAt: "invalid" }))).toBe(true);
  });

  it("asks when the list is newer than the last arrival baseline and nothing has come for a while", () => {
    expect(shouldPull(behind())).toBe(true);
  });

  it("does not while the page is caught up, or the write is the item's own", () => {
    expect(shouldPull(behind({ arrivedUpdatedAt: new Date(T0 + 60_000).toISOString() }))).toBe(false);
    expect(shouldPull(behind({ arrivedUpdatedAt: new Date(T0 + 61_000).toISOString() }))).toBe(false); // an older list cannot make the page stale
    expect(shouldPull(behind({ updatedAt: undefined }))).toBe(false);
    expect(shouldPull(behind({ updatedAt: "not a time" }))).toBe(false);
  });

  it("waits for the stream: items came a moment ago", () => {
    expect(shouldPull(behind({ now: T0 + STALE_QUIET_MS - 1 }))).toBe(false);
    expect(shouldPull(behind({ lastArrival: T0 + 3000, now: T0 + 3000 + STALE_QUIET_MS - 1 }))).toBe(false);
  });

  it("asks at most once per interval, and not while one runs", () => {
    const now = T0 + 60_000;
    expect(shouldPull(behind({ now, lastPull: now - STALE_PULL_EVERY_MS + 1 }))).toBe(false);
    expect(shouldPull(behind({ now, lastPull: now - STALE_PULL_EVERY_MS }))).toBe(true);
    expect(shouldPull(behind({ pulling: true }))).toBe(false);
  });

  it("only while the page is visible", () => {
    expect(shouldPull(behind({ visible: false }))).toBe(false);
  });

  it("does not ask twice about a write that added no items", () => {
    const v = behind();
    expect(shouldPull({ ...v, checkedFor: v.updatedAt })).toBe(false);
    expect(shouldPull({ ...v, checkedFor: new Date(T0 + 30_000).toISOString() })).toBe(true); // it moved on since
  });

  it("a page with no arrival baseline asks once per write", () => {
    expect(shouldPull(behind({ arrivedUpdatedAt: undefined }))).toBe(true);
  });
});

describe("the tally", () => {
  it("says not live after repeated recoveries, and a push clears it", () => {
    let t = FRESH_TALLY;
    for (let i = 1; i < STALE_HINT_AFTER; i++) {
      t = afterPull(t, "found");
      expect(notLive(t)).toBe(false);
    }
    t = afterPull(t, "found");
    expect(notLive(t)).toBe(true);
    expect(notLive(afterPush(t))).toBe(false);
  });

  it("an ask that finds nothing is not a miss, and does not clear the misses", () => {
    let t = afterPull(FRESH_TALLY, "found");
    t = afterPull(t, "empty");
    expect(t.recovered).toBe(1);
    expect(notLive(afterPull(t, "found"))).toBe(true);
  });

  it("failed asks count on their own and a good one clears them", () => {
    let t = FRESH_TALLY;
    for (let i = 0; i < STALE_HINT_AFTER; i++) t = afterPull(t, "failed");
    expect(notLive(t)).toBe(true);
    expect(afterPull(t, "empty").failed).toBe(0);
  });

  it("a push on a clean tally changes nothing", () => {
    expect(afterPush(FRESH_TALLY)).toBe(FRESH_TALLY);
  });
});

describe("newestItemAt", () => {
  it("is the last item that has a time", () => {
    expect(newestItemAt([{ ts: "2026-11-05T10:00:00Z" }, { ts: "2026-11-05T10:00:05Z" }, {}])).toBe(Date.parse("2026-11-05T10:00:05Z"));
    expect(newestItemAt([{}, {}])).toBe(0);
    expect(newestItemAt([])).toBe(0);
  });
});
