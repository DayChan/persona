// A queued message read apart for its row and for "Pull back to edit" (#3032). Every queue text here is made up, in the
// shapes Claude Code keeps them (server/internal/transcript/testdata/claude-queued-images.jsonl, node keepImagePaths).
import { describe, expect, it } from "vitest";
import { contextXML, withContexts } from "./links";
import { parseQueued, queuedDraft, queuedMarks } from "./queue";

const A = "/Users/me/.persona/uploads/2026-11-10/aaaa1111.jpg";
const B = "/Users/me/.persona/uploads/2026-11-10/bbbb2222.jpg";

describe("parseQueued", () => {
  it("reads the words and the photos of a message sent with two photos", () => {
    const raw = `[Image #1] [Image #2]\n\n\nPlease check these two screens\n<attached_images>\npath: ${A}\npath: ${B}\n</attached_images>`;
    expect(parseQueued(raw)).toEqual({ text: "Please check these two screens", photos: [A, B], files: [], contexts: [] });
  });

  it("takes bare paths too, once each, and nothing else in the block", () => {
    const raw = `Look\n\n<attached_images>\n${A}\npath: ${A}\n~/Desktop/shot.png\nnot a path\n</attached_images>`;
    expect(parseQueued(raw).photos).toEqual([A, "~/Desktop/shot.png"]);
    expect(parseQueued(raw).text).toBe("Look");
  });

  it("keeps the words of an older queue whose block came back empty", () => {
    expect(parseQueued("[Image #6]rename the screenshots\n<attached_images>\n</attached_images>")).toEqual({ text: "rename the screenshots", photos: [], files: [], contexts: [] });
  });

  it("leaves out the stand-in words a message with photos only went with, in each language the apps send", () => {
    for (const words of ["Please look at the image", "Please look at the photos", "See the images", "请看图片", "画像を見てください"]) {
      expect(parseQueued(`[Image #3] ${words}\n\n<attached_images>\npath: ${A}\n</attached_images>`)).toEqual({ text: "", photos: [A], files: [], contexts: [] });
    }
  });

  it("keeps the same words when they are the whole message", () => {
    expect(parseQueued("请看图片").text).toBe("请看图片");
    expect(parseQueued("Please look at the image").text).toBe("Please look at the image");
  });

  it("gives contexts back with every attribute, and drops the stand-in a context-only message went with", () => {
    const quote = { kind: "context" as const, title: "Design notes", url: "https://example.com/doc", text: "the second paragraph" };
    const workspace = { kind: "workspace" as const, title: "web", url: "", text: "~/code/web", attrs: { machine: "laptop", cwd: "/home/me/code/web" } };
    const p = parseQueued(withContexts([quote, workspace], ""));
    expect(p.text).toBe("");
    expect(p.contexts).toEqual([quote, workspace]);
    // sent again, it is the same tag
    expect(contextXML(p.contexts[1] as typeof workspace)).toBe(contextXML(workspace));
  });

  it("keeps what was typed after contexts and photos", () => {
    const raw = `${withContexts([{ kind: "context", title: "Notes", url: "", text: "a & <b>" }], "Use this", true)}\n\n<attached_images>\npath: ${A}\n</attached_images>`;
    const p = parseQueued(raw);
    expect(p.text).toBe("Use this");
    expect(p.contexts).toEqual([{ kind: "context", title: "Notes", url: "", text: "a & <b>" }]);
    expect(p.photos).toEqual([A]);
  });

  it("reads files and videos sent as <persona-file> tags", () => {
    const raw = `Cut the intro <persona-file kind="video" path="/Users/me/Movies/take 1.mov" name="take 1.mov" size="2048" duration="12"/> <persona-file kind="file" path="/tmp/spec.pdf" name="spec.pdf"/>`;
    const p = parseQueued(raw);
    expect(p.text).toBe("Cut the intro");
    expect(p.files.map((f) => [f.kind, f.path])).toEqual([["video", "/Users/me/Movies/take 1.mov"], ["file", "/tmp/spec.pdf"]]);
    // into the box, a file goes as its path, quoted when it needs to be, the way this composer sends one
    expect(queuedDraft(p)).toBe("Cut the intro '/Users/me/Movies/take 1.mov' /tmp/spec.pdf");
  });

  it("leaves plain text as it is", () => {
    const raw = "first line\n\n  - a list\n> a quote";
    expect(parseQueued(raw)).toEqual({ text: raw, photos: [], files: [], contexts: [] });
  });
});

describe("queuedMarks", () => {
  it("counts each kind a message carries, only the ones it has", () => {
    const raw = `${withContexts([{ kind: "context", title: "Notes", url: "", text: "x" }], "Here", true)} <persona-file kind="video" path="/v.mov"/> <persona-file kind="file" path="/a.pdf"/>\n\n<attached_images>\npath: ${A}\npath: ${B}\n</attached_images>`;
    expect(queuedMarks(parseQueued(raw))).toEqual([
      { kind: "photo", count: 2 },
      { kind: "video", count: 1 },
      { kind: "file", count: 1 },
      { kind: "context", count: 1 },
    ]);
    expect(queuedMarks(parseQueued("just words"))).toEqual([]);
  });
});
