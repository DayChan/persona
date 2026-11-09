import { describe, expect, it, vi } from "vitest";
import { accelLabel, accelTarget, activation, closeTarget, edge, enabled, isContextKey, keyIntent, menuAtFromContext, renameCommit, rowLabel, step, type MenuItem } from "./menu-model";

const item = (key: string, extra: Partial<MenuItem> = {}): MenuItem => ({ key, label: key, onSelect: () => {}, ...extra });

const items: MenuItem[] = [
  item("copy", { accel: "c" }),
  item("off", { disabled: true, accel: "x" }),
  item("git", { accel: "g", children: [item("pull", { accel: "p" })] }),
  item("rename", { accel: "r", rename: { value: "build", commit: () => {} } }),
  item("close", { danger: true, closes: true, confirm: "确认关闭" }),
];

describe("moving through the rows", () => {
  it("skips rows that cannot be chosen and wraps round", () => {
    expect(step(items, -1, 1)).toBe(0);
    expect(step(items, 0, 1)).toBe(2);
    expect(step(items, 4, 1)).toBe(0);
    expect(step(items, 0, -1)).toBe(4);
    expect(step(items, 2, -1)).toBe(0);
  });
  it("starts from the last row going up from nothing", () => {
    expect(step(items, -1, -1)).toBe(4);
  });
  it("stays put when no row can be chosen", () => {
    expect(step([item("a", { disabled: true })], -1, 1)).toBe(-1);
    expect(step([], 0, 1)).toBe(0);
  });
  it("finds the ends", () => {
    expect(edge(items, false)).toBe(0);
    expect(edge([item("a", { disabled: true }), item("b")], false)).toBe(1);
    expect(edge([item("a"), item("b", { disabled: true })], true)).toBe(0);
    expect(edge([], true)).toBe(-1);
  });
});

describe("keys in an open menu", () => {
  const key = (k: string, active = -1, depth = 0, list = items, mods: object = {}) => keyIntent({ key: k, ...mods }, list, active, depth);

  it("↓ ↑ move, Home End jump", () => {
    expect(key("ArrowDown", 0)).toEqual({ type: "move", index: 2 });
    expect(key("ArrowUp", -1)).toEqual({ type: "move", index: 4 });
    expect(key("Home", 3)).toEqual({ type: "move", index: 0 });
    expect(key("End", 0)).toEqual({ type: "move", index: 4 });
  });
  it("Enter chooses the highlighted row and nothing before one is", () => {
    expect(key("Enter", 3)).toEqual({ type: "choose", index: 3 });
    expect(key("Enter", -1)).toEqual({ type: "none" });
    expect(key("Enter", 1)).toEqual({ type: "none" }); // a disabled row
  });
  it("→ opens a submenu only on a row that has one; ← goes back only inside one", () => {
    expect(key("ArrowRight", 2)).toEqual({ type: "choose", index: 2 });
    expect(key("ArrowRight", 0)).toEqual({ type: "none" });
    expect(key("ArrowLeft", 0, 1)).toEqual({ type: "back" });
    expect(key("ArrowLeft", 0, 0)).toEqual({ type: "none" });
  });
  it("Esc closes", () => {
    expect(key("Escape", 2, 1)).toEqual({ type: "close" });
  });
  it("Tab leaves: the menu closes and the focus moves on, at any level and with a row highlighted or not", () => {
    expect(key("Tab", 2)).toEqual({ type: "leave" });
    expect(key("Tab", -1, 1)).toEqual({ type: "leave" });
    expect(key("Tab", 0, 0, items, { shiftKey: true })).toEqual({ type: "leave" });
    expect(key("Tab", 0, 0, items, { ctrlKey: true })).toEqual({ type: "none" }); // the browser's tab switching
  });
  it("a letter chooses its row, in either case, never a disabled one", () => {
    expect(key("c")).toEqual({ type: "choose", index: 0 });
    expect(key("G")).toEqual({ type: "choose", index: 2 });
    expect(key("x")).toEqual({ type: "none" });
    expect(key("z")).toEqual({ type: "none" });
  });
  it("⌫ and Delete choose the close action", () => {
    expect(key("Backspace")).toEqual({ type: "choose", index: 4 });
    expect(key("Delete", 0)).toEqual({ type: "choose", index: 4 });
    expect(key("Backspace", -1, 0, [item("a")])).toEqual({ type: "none" });
  });
  it("leaves shortcuts with a modifier to the page", () => {
    expect(key("c", -1, 0, items, { ctrlKey: true })).toEqual({ type: "none" });
    expect(key("c", -1, 0, items, { metaKey: true })).toEqual({ type: "none" });
    expect(key("ArrowDown", -1, 0, items, { altKey: true })).toEqual({ type: "none" });
  });
  it("finds rows by letter and the close action directly", () => {
    expect(accelTarget(items, "r")).toBe(3);
    expect(accelTarget(items, "rr")).toBe(-1);
    expect(closeTarget(items)).toBe(4);
    expect(closeTarget([item("a", { closes: true, disabled: true })])).toBe(-1);
  });
});

describe("choosing a row", () => {
  const close = items[4];
  it("a danger row with a confirm label asks first, then runs", () => {
    expect(activation(close, null)).toBe("confirm");
    expect(rowLabel(close, null)).toBe("close");
    expect(activation(close, "close")).toBe("run");
    expect(rowLabel(close, "close")).toBe("确认关闭");
  });
  it("the question belongs to its own row", () => {
    expect(activation(close, "copy")).toBe("confirm");
    expect(rowLabel(close, "copy")).toBe("close");
  });
  it("a danger row with no confirm label runs at once, as the existing menus do", () => {
    expect(activation(item("delete", { danger: true }), null)).toBe("run");
  });
  it("rows with a submenu or a rename input open them, disabled ones do nothing", () => {
    expect(activation(items[2], null)).toBe("children");
    expect(activation(items[3], null)).toBe("rename");
    expect(activation(items[1], null)).toBe("none");
    expect(activation(undefined, null)).toBe("none");
    expect(activation(item("plain"), null)).toBe("run");
  });
  it("shows the letter, or ⌫ on the close action", () => {
    expect(accelLabel(items[0])).toBe("C");
    expect(accelLabel(items[4])).toBe("⌫");
    expect(accelLabel(item("x"))).toBeUndefined();
  });
});

describe("inline rename", () => {
  it("commits a changed, trimmed name", () => {
    expect(renameCommit("build", "  deploy ")).toBe("deploy");
  });
  it("commits nothing for an empty or unchanged name", () => {
    expect(renameCommit("build", "   ")).toBeNull();
    expect(renameCommit("build", "build")).toBeNull();
    expect(renameCommit(" build ", "build")).toBeNull();
  });
});

describe("opening on a row", () => {
  const row = {} as HTMLElement;
  it("a right click opens at the pointer", () => {
    expect(menuAtFromContext({ clientX: 120, clientY: 48, currentTarget: row })).toEqual({ point: { x: 120, y: 48 } });
  });
  it("the menu key and Shift+F10 send no position: the row's own place", () => {
    expect(menuAtFromContext({ clientX: 0, clientY: 0, currentTarget: row })).toEqual({ anchor: row });
  });
  it("knows the keys that ask for a menu", () => {
    expect(isContextKey({ key: "ContextMenu" })).toBe(true);
    expect(isContextKey({ key: "F10", shiftKey: true })).toBe(true);
    expect(isContextKey({ key: "F10" })).toBe(false);
    expect(isContextKey({ key: "F10", shiftKey: true, ctrlKey: true })).toBe(false);
    expect(isContextKey({ key: "Enter" })).toBe(false);
  });
});

// A menu driven the way a person does, through the same rules the component uses.
describe("a whole interaction", () => {
  it("⌫ asks, a second ⌫ runs; moving away takes the question back", () => {
    const ran = vi.fn();
    const list = [item("copy", { accel: "c" }), item("close", { danger: true, closes: true, confirm: "确认关闭", onSelect: ran })];
    let confirming: string | null = null;
    const press = (k: string, active = -1) => {
      const intent = keyIntent({ key: k }, list, active, 0);
      if (intent.type !== "choose") return;
      const it = list[intent.index];
      const a = activation(it, confirming);
      if (a === "confirm") confirming = it.key;
      else if (a === "run") {
        confirming = null;
        void it.onSelect();
      }
    };
    press("Backspace");
    expect(confirming).toBe("close");
    expect(ran).not.toHaveBeenCalled();
    press("Backspace");
    expect(ran).toHaveBeenCalledTimes(1);
    expect(confirming).toBeNull();
  });
});

describe("note rows (#4054)", () => {
  it("are read, not chosen: the keys and the pointer pass over them", () => {
    const list = [item("a", { note: true }), item("b"), item("c", { note: true })];
    expect(enabled(list[0])).toBe(false);
    expect(step(list, -1, 1)).toBe(1);
    expect(step(list, 1, 1)).toBe(1);
    expect(edge(list, true)).toBe(1);
    expect(keyIntent({ key: "Enter" }, list, 0, 0)).toEqual({ type: "none" });
    expect(activation(list[0], null)).toBe("none");
  });
});
