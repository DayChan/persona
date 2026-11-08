import { describe, expect, it } from "vitest";
import { commentBlock, parseCommentBlock } from "./comments";
import {
  addressOf, commandLine, editCommand, elementQuote, keyMessage, localhostLink, modsOf, parseBinary, prettyHTML, previewOptions, previewProblems,
  previewSocketURL, safeExternal, toPage, type PickedElement, type PreviewServers,
} from "./preview";

const res: PreviewServers = {
  file: "/home/me/proj/.claude/launch.json",
  browser: "Google Chrome 131",
  servers: [
    { name: "web", port: 5173, command: ["npm", "run", "dev"], cwd: "/home/me/proj", listening: true, title: "Acme" },
    { name: "api", port: 8080, command: ["go", "run", "."], env: { PORT: "8080", MODE: "a b" }, listening: false },
    { name: "docs", port: 4000, url: "http://localhost:4000/guide?x=1#top", listening: false },
    { name: "staging", listening: false, problem: "not_local" },
  ],
};

describe("previewOptions", () => {
  it("lists the usable launch.json entries, then the agents' other ports", () => {
    const got = previewOptions(res, [{ port: 8080 }, { port: 3000, title: "Storybook" }]);
    expect(got.map((o) => [o.key, o.port, o.path, o.listening, o.from])).toEqual([
      ["launch:web", 5173, "/", true, "launch"],
      // an agent listens there: running, even if the probe missed it
      ["launch:api", 8080, "/", true, "launch"],
      ["launch:docs", 4000, "/guide?x=1#top", false, "launch"],
      ["port:3000", 3000, "/", true, "agent"],
    ]);
    expect(got[3].name).toBe("Storybook");
    expect(previewProblems(res).map((p) => p.name)).toEqual(["staging"]);
    expect(previewOptions(null, [])).toEqual([]);
  });

  it("shows the command as the node runs it", () => {
    expect(commandLine(previewOptions(res)[1])).toBe("env 'MODE=a b' 'PORT=8080' go run .");
    expect(commandLine({ command: ["npm", "run", "dev; rm -rf ~"] })).toBe("npm run 'dev; rm -rf ~'");
    expect(commandLine({ command: ["sh", "-c", "it's"] })).toBe(`sh -c 'it'\\''s'`);
  });
});

describe("the socket", () => {
  it("carries the port, the page and its size, and the token in the query", () => {
    const u = new URL(previewSocketURL({ protocol: "https:", host: "hub.example.com" }, "mac 1", 5173, "/a b", { w: 800.4, h: 600, dpr: 2, mobile: true }, "tok"));
    expect(u.protocol).toBe("wss:");
    expect(u.pathname).toBe("/api/v1/machines/mac%201/preview/connect");
    expect(Object.fromEntries(u.searchParams)).toEqual({ port: "5173", path: "/a b", w: "800", h: "600", dpr: "2", mobile: "1", token: "tok" });
    expect(new URL(previewSocketURL({ protocol: "http:", host: "h" }, "m", 1, "", { w: 1, h: 1, dpr: 1, mobile: false }, "")).search).toBe("?port=1&path=%2F&w=1&h=1&dpr=1");
  });

  it("reads pictures and screenshots", () => {
    const frame = new Uint8Array([0x66, 0, 0, 0, 7, 1, 144, 2, 88, 0xff, 0xd8]).buffer;
    const f = parseBinary(frame);
    expect(f).toMatchObject({ kind: "frame", seq: 7, w: 400, h: 600 });
    expect(Array.from((f as { data: Uint8Array }).data)).toEqual([0xff, 0xd8]);
    expect(parseBinary(new Uint8Array([0x73, 0, 0, 0, 3, 0x89]).buffer)).toMatchObject({ kind: "shot", id: 3 });
    expect(parseBinary(new Uint8Array([0x78, 0, 0, 0, 0]).buffer)).toBeNull();
    expect(parseBinary(new Uint8Array([0x66, 1]).buffer)).toBeNull();
  });
});

describe("input", () => {
  const key = { key: "a", code: "KeyA", keyCode: 65, location: 0, repeat: false, isComposing: false, altKey: false, ctrlKey: false, metaKey: false, shiftKey: false };

  it("counts modifiers the way the browser on the computer does", () => {
    expect(modsOf({ altKey: true, ctrlKey: true, metaKey: true, shiftKey: true })).toBe(15);
    expect(modsOf({ altKey: false, ctrlKey: false, metaKey: false, shiftKey: true })).toBe(8);
  });

  it("maps a point on the picture to the page", () => {
    const rect = { left: 100, top: 50, width: 200, height: 100 };
    expect(toPage(200, 100, rect, { w: 800, h: 400 })).toEqual({ x: 400, y: 200 });
    expect(toPage(0, 0, rect, { w: 800, h: 400 })).toEqual({ x: 0, y: 0 });
    expect(toPage(9999, 9999, rect, { w: 800, h: 400 })).toEqual({ x: 800, y: 400 });
  });

  it("sends characters with their text, other keys without", () => {
    expect(keyMessage(key, "down")).toEqual({ t: "key", type: "down", key: "a", code: "KeyA", key_code: 65, mods: 0, location: 0, text: "a" });
    expect(keyMessage(key, "up")).not.toHaveProperty("text");
    expect(keyMessage({ ...key, key: "Enter", code: "Enter", keyCode: 13 }, "down")).toMatchObject({ text: "\r" });
    expect(keyMessage({ ...key, key: "Backspace", code: "Backspace", keyCode: 8 }, "down")).not.toHaveProperty("text");
    expect(keyMessage({ ...key, ctrlKey: true }, "down")).toMatchObject({ mods: 2 });
    expect(keyMessage({ ...key, ctrlKey: true }, "down")).not.toHaveProperty("text");
    expect(keyMessage({ ...key, key: "😀" }, "down")).toMatchObject({ text: "😀" });
    // an input method at work: its text comes when it is done
    expect(keyMessage({ ...key, isComposing: true }, "down")).toBeNull();
    expect(keyMessage({ ...key, key: "Process" }, "down")).toBeNull();
    expect(keyMessage({ ...key, repeat: true }, "down")).toMatchObject({ repeat: true });
  });

  it("turns editing shortcuts into the page's commands", () => {
    const k = (key: string, m: Partial<typeof keyMod> = {}) => ({ ...keyMod, key, ...m });
    const keyMod = { key: "", altKey: false, ctrlKey: false, metaKey: false, shiftKey: false };
    expect(editCommand(k("a", { metaKey: true }), true)).toBe("selectAll");
    expect(editCommand(k("a", { ctrlKey: true }), true)).toBeNull(); // Ctrl+A on a Mac is the line's start
    expect(editCommand(k("A", { ctrlKey: true }), false)).toBe("selectAll");
    expect(editCommand(k("c", { metaKey: true }), true)).toBe("copy");
    expect(editCommand(k("x", { ctrlKey: true }), false)).toBe("cut");
    expect(editCommand(k("z", { metaKey: true }), true)).toBe("undo");
    expect(editCommand(k("z", { metaKey: true, shiftKey: true }), true)).toBe("redo");
    expect(editCommand(k("y", { ctrlKey: true }), false)).toBe("redo");
    expect(editCommand(k("v", { metaKey: true }), true)).toBeNull(); // the paste event brings it
    expect(editCommand(k("a"), true)).toBeNull();
  });
});

describe("addresses", () => {
  it("shows a path on the server previewed, the whole address elsewhere", () => {
    expect(addressOf("http://localhost:5173/settings?tab=1#a", 5173)).toBe("/settings?tab=1#a");
    expect(addressOf("http://127.0.0.1:5173/", 5173)).toBe("/");
    expect(addressOf("http://localhost:3000/x", 5173)).toBe("http://localhost:3000/x");
    expect(addressOf("about:blank", 5173)).toBe("about:blank");
  });

  it("opens only web addresses in a new tab", () => {
    expect(safeExternal("https://example.com/a")).toBe("https://example.com/a");
    expect(safeExternal("javascript:alert(1)")).toBeNull();
    expect(safeExternal("data:text/html,x")).toBeNull();
    expect(safeExternal("file:///etc/passwd")).toBeNull();
    expect(safeExternal("not a url")).toBeNull();
  });

  it("knows a reply's localhost link", () => {
    expect(localhostLink("http://localhost:5173/x?y#z")).toEqual({ port: 5173, path: "/x?y#z" });
    expect(localhostLink("localhost:3000")).toEqual({ port: 3000, path: "/" });
    expect(localhostLink("https://app.localhost:8443/")).toEqual({ port: 8443, path: "/" });
    expect(localhostLink("http://127.0.0.1:8000/")).toEqual({ port: 8000, path: "/" });
    expect(localhostLink("http://[::1]:9000/")).toEqual({ port: 9000, path: "/" });
    expect(localhostLink("http://0.0.0.0:4000/")).toEqual({ port: 4000, path: "/" });
    expect(localhostLink("http://localhost/")).toBeNull();
    expect(localhostLink("https://example.com:5173/")).toBeNull();
    expect(localhostLink("http://192.0.2.4:5173/")).toBeNull();
    expect(localhostLink("ftp://localhost:21/")).toBeNull();
  });
});

describe("a picked element as a comment", () => {
  const el: PickedElement = {
    selector: "main > form > button.save",
    tag: "button",
    text: "  Save   changes ",
    html: '<button class="save" type="submit"><span class="icon"></span><span>Save changes</span></button>',
    component: "SaveButton",
    source: { file: "/home/me/proj/src/components/SaveButton.tsx", line: 12 },
    url: "http://localhost:5173/settings",
  };
  const at = { mid: "mac", sid: "s1", cwd: "/home/me/proj", port: 5173 };

  it("carries the page, the element, its component and HTML, and reads back", () => {
    const q = elementQuote(el, at);
    expect(q.comment?.element).toEqual({
      url: "http://localhost:5173/settings", selector: "main > form > button.save", component: "SaveButton", source: "src/components/SaveButton.tsx:12",
      text: "Save   changes", omitted: 0, mid: "mac", sid: "s1", cwd: "/home/me/proj", port: 5173,
    });
    const block = commentBlock({ ...q, comment: { ...q.comment!, body: "Make it primary" } }, 2);
    expect(block).toBe(
      [
        "Element comment 2",
        "Page: http://localhost:5173/settings",
        "Element: main > form > button.save",
        "Component: SaveButton (src/components/SaveButton.tsx:12)",
        "Text: Save changes",
        "```html",
        '<button class="save" type="submit"><span class="icon"></span><span>Save changes</span></button>',
        "```",
        "",
        "Make it primary",
      ].join("\n"),
    );
    const back = parseCommentBlock(block);
    expect(back?.comment?.body).toBe("Make it primary");
    expect(back?.comment?.element).toMatchObject({ url: el.url, selector: el.selector, component: "SaveButton", source: "src/components/SaveButton.tsx:12", text: "Save changes" });
    expect(back?.text).toBe(q.text);
  });

  it("cuts long HTML and says so", () => {
    const long = { ...el, html: Array.from({ length: 80 }, (_, i) => `<li>${i}</li>`).join("\n"), component: "", source: null };
    const q = elementQuote(long, at);
    expect(q.text.split("\n")).toHaveLength(60);
    expect(q.comment?.element?.omitted).toBe(20);
    const block = commentBlock(q, 1);
    expect(block).toContain("Truncated: the first 60 of 80 lines");
    expect(block).not.toContain("Component:");
    expect(parseCommentBlock(block)?.comment?.element?.omitted).toBe(20);
  });

  it("puts a long one-line HTML a tag a line", () => {
    expect(prettyHTML("<a>x</a>")).toBe("<a>x</a>");
    expect(prettyHTML(`<div>${"<p>x</p>".repeat(20)}</div>`).split("\n")).toHaveLength(22);
  });
});
