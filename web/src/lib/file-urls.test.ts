import { afterEach, describe, expect, it, vi } from "vitest";
import { fileDownloadURL, fileThumbURL } from "./api";

describe("machine file URLs", () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.resetModules(); });

  it("carries the token in the query, since media elements send no header", () => {
    vi.stubGlobal("localStorage", { getItem: () => "jd test" });
    expect(fileDownloadURL("m 1", "a b.mp4", "/w")).toBe("/api/v1/machines/m%201/file/download?path=a%20b.mp4&cwd=%2Fw&token=jd%20test");
    expect(fileDownloadURL("m1", "/x.pdf", undefined, { inline: true })).toBe(
      "/api/v1/machines/m1/file/download?path=%2Fx.pdf&cwd=&inline=1&token=jd%20test",
    );
    expect(fileThumbURL("m1", "~/p.heic", "", 128)).toMatch(/^\/api\/v1\/machines\/m1\/file\/thumb\?path=~%2Fp.heic&cwd=&size=128&token=jd%20test&thumb=v2-/);
  });

  it("bypasses fresh pre-upgrade thumbnails and rechecks after a later node upgrade on page reload", async () => {
    vi.stubGlobal("localStorage", { getItem: () => "synthetic-token" });
    const legacy = "/api/v1/machines/m1/file/thumb?path=%2Fw%2Fportrait.jpg&cwd=%2Fw&size=128&token=synthetic-token";
    const cache = new Map([[legacy, { width: 60, height: 30 }]]);
    let nodeUpgraded = false;
    let requests = 0;
    const load = (url: string) => {
      if (!cache.has(url)) {
        requests++;
        cache.set(url, nodeUpgraded ? { width: 30, height: 60 } : { width: 60, height: 30 });
      }
      return cache.get(url);
    };
    const before = (await import("./api")).fileThumbURL("m1", "/w/portrait.jpg", "/w", 128);
    expect(before).not.toBe(legacy);
    expect(load(before)).toEqual({ width: 60, height: 30 }); // new client can first see an old node
    expect(load(before)).toEqual({ width: 60, height: 30 });
    expect(requests).toBe(1); // stable within a page, including its fresh HTTP cache
    nodeUpgraded = true;
    vi.resetModules(); // next page load; source path, size and mtime did not change
    const after = (await import("./api")).fileThumbURL("m1", "/w/portrait.jpg", "/w", 128);
    expect(after).not.toBe(before);
    expect(load(after)).toEqual({ width: 30, height: 60 });
    expect(requests).toBe(2);
    expect((await import("./api")).fileThumbURL("m1", "/w/portrait.jpg", "/w", 128)).toBe(after);
  });
});
