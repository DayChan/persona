import { describe, expect, it } from "vitest";
import { initials, MAX_STACK, outcome, pushPage, readAction, readView, rowIcon, tableRows, UI_VERSION, uiContext } from "./extension-ui";

describe("UI 版本 3：新组件", () => {
  it("asks for version 6", () => {
    expect(UI_VERSION).toBe(6);
    expect(uiContext().ui_version).toBe(6);
  });

  it("reads markdown, table, empty and loading, and leaves out what cannot be drawn", () => {
    const v = readView({
      sections: [{
        items: [
          { type: "markdown", text: "## 标题\n正文 **粗** [链接](https://example.com)" },
          { type: "table", columns: [{ title: "名字" }, { title: "数", align: "end" }], rows: [["a", "1"], ["b"]] },
          { type: "table", rows: [["no columns"]] },
          { type: "empty", title: "还没有", detail: "等会儿再看", icon: "inbox", action: { title: "刷新", action: { id: "reload" } } },
          { type: "empty" },
          { type: "loading" },
          { type: "markdown" },
        ],
      }],
    });
    expect(v.sections[0].items.map((i) => i.type)).toEqual(["markdown", "table", "empty", "loading"]);
    const empty = v.sections[0].items[2];
    expect(empty.button).toEqual({ title: "刷新", action: { id: "reload" } });
    expect(empty.action).toBeUndefined();
    expect(tableRows(v.sections[0].items[1])).toEqual([["a", "1"], ["b", ""]]);
  });

  it("reads a row's icon, picture and thumbnail, and the keys", () => {
    const v = readView({
      sections: [{ key: "list", items: [
        { type: "row", key: "r1", title: "张三", avatar: { url: "https://example.com/a.png", text: "张" } },
        { type: "row", key: "r2", title: "文件", icon: "folder" },
        { type: "row", key: "r3", title: "图", thumbnail: { url: "https://example.com/t.png", aspect: "wide" } },
      ] }],
    });
    expect(v.sections[0].key).toBe("list");
    expect(v.sections[0].items.map((i) => i.key)).toEqual(["r1", "r2", "r3"]);
    expect(rowIcon("folder")).toBe("folder");
    expect(rowIcon("rocket")).toBeUndefined();
    expect(initials("", "zhang san")).toBe("Z");
    expect(initials("张三丰")).toBe("张三");
  });

  it("reads the search box, the filter and 加载更多", () => {
    const v = readView({
      search: { placeholder: "搜帖子", value: "go", action: { id: "search" } },
      filter: { name: "range", choices: [{ value: "day", title: "今天" }, { value: "week" }], value: "week", action: { id: "range" } },
      sections: [{ items: [{ type: "row", title: "x" }], more: { action: { id: "more" } } }],
    });
    expect(v.search).toEqual({ placeholder: "搜帖子", value: "go", action: { id: "search" } });
    expect(v.filter?.value).toBe("week");
    expect(v.sections[0].more).toEqual({ action: { id: "more" } });
    // a filter of one choice, a search without an action: not drawn
    const w = readView({ search: { placeholder: "x" }, filter: { name: "f", choices: [{ value: "a" }], action: { id: "f" } }, sections: [] });
    expect(w.search).toBeUndefined();
    expect(w.filter).toBeUndefined();
  });
});

describe("UI 版本 3：页面跳转", () => {
  it("reads a push on an action, its state as text", () => {
    expect(readAction({ id: "open", push: { state: { id: 3 }, title: "详情" } })).toEqual({ id: "open", push: { state: '{"id":3}', title: "详情" } });
    expect(readAction({ id: "open", push: { state: "s1", ui: "detail" } })).toEqual({ id: "open", push: { state: "s1", ui: "detail" } });
    expect(readAction({ confirm: "x" })).toBeUndefined();
  });

  it("carries out a push or a pop an action answers", () => {
    expect(outcome({ push: { state: "d" } })).toMatchObject({ push: { state: "d" }, reload: false });
    expect(outcome({ pop: true })).toMatchObject({ pop: true, reload: false });
    expect(outcome({})).toMatchObject({ reload: true });
    expect(outcome({ push: { state: "d" }, pop: true }).pop).toBeUndefined(); // a push wins
  });

  it("keeps the stack at most 8 deep: a push past it replaces the top page", () => {
    let s = [{ ui: "home" }];
    for (let i = 1; i < MAX_STACK; i++) s = pushPage(s, { state: String(i) }, "home");
    expect(s).toHaveLength(MAX_STACK);
    const t = pushPage(s, { state: "deep", ui: "detail" }, "home");
    expect(t).toHaveLength(MAX_STACK);
    expect(t[MAX_STACK - 1]).toEqual({ ui: "detail", state: "deep" });
    expect(t[MAX_STACK - 2]).toEqual(s[MAX_STACK - 2]);
  });
});
