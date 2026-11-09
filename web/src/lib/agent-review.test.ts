// @vitest-environment jsdom
// The independent review's endpoints (#1808): starting one posts to the session's review address with an empty body,
// and busyReason says why the menu item is disabled while the agent works.
import { afterEach, describe, expect, it, vi } from "vitest";

import { t } from "../i18n";
import { busyReason, startAgentReview } from "./agent-review";
import type { Session } from "./types";

const agent = (o: Partial<Session>): Session => ({ machine_id: "m", session_id: "s", title: "Demo", cwd: "/code", status: "idle", ...o });

/** A hub that answers every request with `body` and records the requests. */
function hub(body: unknown): { calls: { method: string; url: string; init?: RequestInit }[] } {
  const calls: { method: string; url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn().mockImplementation((url: string, init?: RequestInit) => {
    calls.push({ method: init?.method ?? "GET", url, init });
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  }));
  return { calls };
}

afterEach(() => vi.unstubAllGlobals());

describe("starting an independent review", () => {
  it("posts to the session's review address with an empty body", async () => {
    const started = { running: true, reviewer_session_id: "r1", started_at: "2026-11-09T10:00:00Z" };
    const { calls } = hub(started);
    await expect(startAgentReview("mac 1", "s/2")).resolves.toEqual(started);
    expect(calls[0]!.url).toBe("/api/v1/machines/mac%201/sessions/s%2F2/review");
    expect(calls[0]!.init!.method).toBe("POST");
    expect(calls[0]!.init!.body).toBeUndefined(); // an empty body: the hub starts a review, nothing to say
  });

  it("throws the hub's own words back (the menu toasts them as they come)", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ error: "这个 Agent 正在忙", code: "session_busy" }), { status: 409 }))));
    await expect(startAgentReview("m", "s")).rejects.toMatchObject({ message: "这个 Agent 正在忙" });
  });
});

describe("why the menu item is disabled", () => {
  it("says the agent is busy while it works, waits on an answer, or is still starting", () => {
    expect(busyReason(agent({ status: "working" }))).toBe(t("chat-agent-review.busy"));
    expect(busyReason(agent({ status: "blocked" }))).toBe(t("chat-agent-review.busy"));
    expect(busyReason(agent({ pending_interactions: 1 }))).toBe(t("chat-agent-review.busy"));
    expect(busyReason(agent({ state: "needs_you" }))).toBe(t("chat-agent-review.busy"));
    expect(busyReason(agent({ starting: true }))).toBe(t("chat-agent-review.busy"));
  });

  it("says nothing of an agent free to be reviewed", () => {
    expect(busyReason(agent({ status: "idle" }))).toBeUndefined();
    expect(busyReason(agent({ status: "offline" }))).toBeUndefined(); // the hub's own refusal says that one
  });
});
