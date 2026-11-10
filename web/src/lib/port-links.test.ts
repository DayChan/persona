// Links to a port of the agent's computer (docs/operations/ports.md "Recognizing links in replies"): the same cases as
// iOS's PortLinksTests, so the clients agree on what a dev server link is.
import { describe, expect, it } from "vitest";
import { MAX_PORT_LINKS, portLabel, portLink, portLinks, portURL } from "./port-links";
import { localhostLink } from "./preview";

describe("a port link", () => {
  it("is a local host with a port", () => {
    expect(portLink("http://localhost:5173/")).toEqual({ port: 5173, path: "/", secure: false });
    expect(portLink("http://127.0.0.1:8000/docs?x=1#top")).toEqual({ port: 8000, path: "/docs?x=1#top", secure: false });
    expect(portLink("http://0.0.0.0:3000")).toEqual({ port: 3000, path: "/", secure: false });
    expect(portLink("http://[::1]:4173/app")).toEqual({ port: 4173, path: "/app", secure: false });
    expect(portLink("http://[::]:4174/")).toEqual({ port: 4174, path: "/", secure: false });
    expect(portLink("https://localhost:8443/")).toEqual({ port: 8443, path: "/", secure: true });
    expect(portLink("http://app.localhost:5173/")).toEqual({ port: 5173, path: "/", secure: false });
  });

  it("is the computer's own name, with or without .local", () => {
    expect(portLink("http://MacBook.local:3000/pricing", ["MacBook"])).toEqual({ port: 3000, path: "/pricing", secure: false });
    expect(portLink("http://server:8080/", ["server.local"])).toEqual({ port: 8080, path: "/", secure: false });
    expect(portLink("http://server:8080/"), "another computer's name").toBeNull();
  });

  it("is not another host, a LAN address, a link with no port or another scheme", () => {
    expect(portLink("https://example.com:8443/")).toBeNull();
    expect(portLink("http://192.0.2.10:23140/"), "a LAN address is not that computer's loopback").toBeNull();
    expect(portLink("http://localhost/"), "no port").toBeNull();
    expect(portLink("ftp://localhost:21/")).toBeNull();
    expect(portLink("not a url")).toBeNull();
  });
});

describe("port links in an answer", () => {
  it("finds scheme links, Markdown links and bare localhost:port, in order, once each", () => {
    // i18n-ignore: an agent's reply as it is written
    const text = [
      "开发服务器起好了：http://localhost:5173/ ，改动会热更新。",
      "  ➜  Local:   http://localhost:5173/",
      "  ➜  Network: http://192.0.2.7:5173/",
      "API 在 127.0.0.1:8000/docs，旧的 `localhost:3000` 停了。文档看 [这里](http://localhost:4000/guide)。别的：https://example.com/a",
    ].join("\n");
    expect(portLinks(text)).toEqual([
      { port: 5173, path: "/", secure: false },
      { port: 8000, path: "/docs", secure: false },
      { port: 3000, path: "/", secure: false },
      { port: 4000, path: "/guide", secure: false },
    ]);
    // i18n-ignore: an agent's reply as it is written
    expect(portLinks("没有链接。")).toEqual([]);
    expect(portLinks("see http://example.com:3000/ and mylocalhost:3000")).toEqual([]);
  });

  it("leaves sentence punctuation out of a link", () => {
    expect(portLinks("Open http://localhost:5173/settings. Or localhost:8080/api, then (localhost:9000).")).toEqual([
      { port: 5173, path: "/settings", secure: false },
      { port: 8080, path: "/api", secure: false },
      { port: 9000, path: "/", secure: false },
    ]);
  });

  it("knows the computer's name in an answer, and stops at MAX_PORT_LINKS", () => {
    expect(portLinks("on http://studio.local:3000/ now", ["studio"])).toEqual([{ port: 3000, path: "/", secure: false }]);
    const many = Array.from({ length: MAX_PORT_LINKS + 3 }, (_, i) => `http://localhost:${3000 + i}/`).join(" ");
    expect(portLinks(many)).toHaveLength(MAX_PORT_LINKS);
  });

  it("labels a chip :port with a short path, and copies the address on that computer", () => {
    expect(portLabel({ port: 5173, path: "/", secure: false })).toBe(":5173");
    expect(portLabel({ port: 8000, path: "/docs", secure: false })).toBe(":8000/docs");
    expect(portLabel({ port: 8000, path: "/a-very-long-path/that/goes/on", secure: false })).toBe(":8000/a-very-long-path/that/…");
    expect(portURL({ port: 8443, path: "/x?y", secure: true })).toBe("https://localhost:8443/x?y");
  });

  it("is what a clicked localhost link opens too, with the computer's name", () => {
    expect(localhostLink("localhost:3000")).toEqual({ port: 3000, path: "/" });
    expect(localhostLink("http://studio.local:3000/a", ["studio"])).toEqual({ port: 3000, path: "/a" });
    expect(localhostLink("http://studio.local:3000/a")).toBeNull();
  });
});
