// @vitest-environment jsdom
// A localhost link clicked on an agent's page (docs/web/preview.md): a port of the agent's computer, shown in the
// preview panel, also when it names that computer by its host name; but this hub's own pages are hub pages, even
// when this page is opened at localhost (a hub on the agent's computer, iOS #2696).
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { openLink, opensInApp, previewTarget } from "./links";
import { closePanel, getPanel } from "./panel";
import { getState } from "./store";
import type { Machine, Session } from "./types";

describe("a localhost link on an agent's page", () => {
  beforeEach(() => {
    Object.assign(getState(), {
      features: ["preview"],
      sessions: { m1: [{ machine_id: "m1", session_id: "s1", cwd: "/w/site" } as Session] },
      machines: [{ id: "m1", name: "Studio", hostname: "studio.local", online: true } as Machine],
    });
    history.replaceState(null, "", "/s/m1/s1");
    closePanel();
  });
  afterEach(() => {
    Object.assign(getState(), { features: [], sessions: {}, machines: [] });
    history.replaceState(null, "", "/");
  });

  it("is that port of the agent's computer, by localhost or by the computer's name", () => {
    expect(previewTarget("http://localhost:5173/x")).toEqual({ mid: "m1", sid: "s1", cwd: "/w/site", port: 5173, path: "/x" });
    expect(previewTarget("http://studio:8000/docs")).toEqual({ mid: "m1", sid: "s1", cwd: "/w/site", port: 8000, path: "/docs" });
    expect(previewTarget("http://elsewhere.local:8000/")).toBeNull();
  });

  it("is not one of this hub's own pages: an agent's chat at this page's origin opens in the agent panel", () => {
    const own = `${location.origin}/s/m1/s9`;
    expect(new URL(own).hostname).toBe("localhost"); // jsdom serves the page at localhost, as a hub on that computer would
    expect(previewTarget(own)).toBeNull();
    expect(opensInApp(own)).toBe(true);
    openLink(own);
    expect(getPanel()).toMatchObject({ kind: "agent", mid: "m1", sid: "s9" });
  });

  it("is nothing on a hub without previews", () => {
    Object.assign(getState(), { features: ["sessions_delta"] });
    expect(previewTarget("http://localhost:5173/")).toBeNull();
  });
});
