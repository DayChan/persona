// UI version 6 on the web (docs/extensions/extensions.md "UI version 6"): what a view's new parts read as, where a picture loads from
// (the device token only ever goes to the hub's own image path), and the new charts' and controls' arithmetic.
import { describe, expect, it } from "vitest";
import { fieldValues, readView, seriesColors, UI_VERSION, uiContext } from "./extension-ui";
import {
  arcPath, aspectRatio, candleColors, candleExtent, candleOf, colorOf, controlText, controlValue, controlsOf, hue, imageSrc, isImagePath, multilineRows, multiText, multiValues,
  readControl, readRowActions, readSpark, shareText, sliderStep, slices, snap, sparkPoints, areaBands,
} from "./extension-ui-v6";

const PATH = "/api/v1/extensions/home/-/image/eyJhIjoxfQ.c2ln";

describe("UI 6：版本", () => {
  it("asks for version 6", () => {
    expect(UI_VERSION).toBe(6);
    expect(uiContext().ui_version).toBe(6);
  });
});

describe("UI 6：图片的地址", () => {
  it("loads the hub's path with the device token, under the page's base", () => {
    expect(imageSrc({ ref: "cam", path: PATH }, "tok")).toBe(`${PATH}?token=tok`);
  });

  it("loads an https url as it is, without any token", () => {
    expect(imageSrc({ url: "https://example.com/a.jpg" }, "tok")).toBe("https://example.com/a.jpg");
  });

  it("loads nothing for a ref the hub did not give a path, nor for any other address", () => {
    expect(imageSrc({ ref: "cam" }, "tok")).toBeUndefined();
    expect(imageSrc({ url: "http://example.com/a.jpg" }, "tok")).toBeUndefined();
    expect(imageSrc({ url: "javascript:alert(1)" }, "tok")).toBeUndefined();
  });

  it("never sends the token to a path that is not the hub's image route", () => {
    for (const path of [
      "/api/v1/sessions/x/image",
      "/api/v1/extensions/home/-/ui/home",
      "//evil.example/api/v1/extensions/a/-/image/t",
      "https://evil.example/api/v1/extensions/a/-/image/t",
      "/api/v1/extensions/a/-/image/../../admin",
      "/api/v1/extensions/a/-/image/t?x=1",
      "/api/v1/extensions/a/b/-/image/t",
    ]) {
      expect(isImagePath(path), path).toBe(false);
      expect(imageSrc({ path }, "tok"), path).toBeUndefined();
    }
    expect(isImagePath(PATH)).toBe(true);
  });

  it("frames a picture at its aspect, auto from its size (4:3 until known)", () => {
    expect(aspectRatio("square")).toBe(1);
    expect(aspectRatio("wide")).toBeCloseTo(16 / 9);
    expect(aspectRatio("portrait")).toBeCloseTo(3 / 4);
    expect(aspectRatio(undefined)).toBeCloseTo(4 / 3);
    expect(aspectRatio("auto", { width: 1920, height: 1080 })).toBeCloseTo(16 / 9);
    expect(aspectRatio("auto", {})).toBeCloseTo(4 / 3);
  });
});

describe("UI 6：读页面", () => {
  it("keeps an image it can load, a grid and a carousel; drops an image it cannot", () => {
    const v = readView({
      sections: [{
        items: [
          { type: "image", src: { ref: "cam", path: PATH }, alt: "前门" },
          { type: "image", src: { ref: "cam" }, alt: "没有 path" },
          { type: "image", src: { url: "https://example.com/a.jpg" } },
          { type: "grid", tiles: [{ title: "客厅" }, { nope: 1 }] },
          { type: "carousel", tiles: [] },
        ],
      }],
    });
    expect(v.sections[0].items.map((i) => i.type)).toEqual(["image", "grid"]);
    expect(v.sections[0].items[1].tiles).toHaveLength(1);
  });

  it("reads a row's control and actions, and drops what does not read", () => {
    const v = readView({
      sections: [{
        items: [
          { type: "row", key: "a", title: "灯", control: { kind: "switch", name: "a", on: true, action: { id: "t" } }, actions: [
            { title: "删除", role: "destructive", action: { id: "rm", confirm: "删？" } },
            { title: "改名", action: { id: "mv" } },
            { title: "", action: { id: "x" } },
          ] },
          { type: "row", key: "b", title: "坏", control: { kind: "knob", name: "b", action: { id: "t" } }, actions: [{ title: "无动作" }] },
          { type: "row", key: "c", title: "滑", control: { kind: "slider", name: "c", value: 5, min: 9, max: 1, action: { id: "t" } } },
        ],
      }],
    });
    const [a, b, c] = v.sections[0].items;
    expect(a.control).toMatchObject({ kind: "switch", name: "a", on: true });
    // a destructive one goes last
    expect(a.actions?.map((x) => x.title)).toEqual(["改名", "删除"]);
    expect(b.control).toBeUndefined();
    expect(b.actions).toBeUndefined();
    expect(c.control).toBeUndefined();
  });

  it("reads a tile's control only when it is a switch", () => {
    const v = readView({ sections: [{ items: [{ type: "grid", tiles: [
      { key: "x", title: "A", control: { kind: "switch", name: "x", action: { id: "t" } } },
      { key: "y", title: "B", control: { kind: "stepper", name: "y", value: 1, step: 1, action: { id: "t" } } },
    ] }] }] });
    const tiles = v.sections[0].items[0].tiles!;
    expect(tiles[0].control?.kind).toBe("switch");
    expect(tiles[1].control).toBeUndefined();
    expect([...controlsOf(v).keys()]).toEqual(["x"]);
  });

  it("starts a multichoice field from its value, an empty array without one", () => {
    const v = readView({ sections: [{ items: [
      { type: "field", name: "m", kind: "multichoice", value: '["a"]', choices: [{ value: "a" }, { value: "b" }] },
      { type: "field", name: "n", kind: "multichoice", choices: [{ value: "a" }] },
      { type: "field", name: "d", kind: "date", value: "2026-11-06" },
    ] }] });
    expect(fieldValues(v)).toEqual({ m: '["a"]', n: "[]", d: "2026-11-06" });
  });
});

describe("UI 6：控件的数", () => {
  const slider = readControl({ kind: "slider", name: "b", value: 60, min: 0, max: 100, action: { id: "b" } })!;
  const stepper = readControl({ kind: "stepper", name: "t", value: 22.5, min: 16, max: 30, step: 0.5, unit: "°C", display: "22.5 °C", action: { id: "t" } })!;

  it("writes a switch's value true / false and a number's as it is", () => {
    expect(controlValue(readControl({ kind: "switch", name: "s", on: true, action: { id: "s" } })!)).toBe("true");
    expect(controlValue(readControl({ kind: "switch", name: "s", action: { id: "s" } })!)).toBe("false");
    expect(controlValue(stepper)).toBe("22.5");
  });

  it("gives a slider without a step a hundredth of its range", () => {
    expect(sliderStep(slider)).toBe(1);
  });

  it("snaps to the step, held to min and max, without float dust", () => {
    expect(snap(22.5 + 0.5 * 3, stepper)).toBe(24);
    expect(snap(0.1 + 0.2, readControl({ kind: "stepper", name: "x", value: 0, step: 0.1, action: { id: "x" } })!)).toBe(0.3);
    expect(snap(31, stepper)).toBe(30);
    expect(snap(10, stepper)).toBe(16);
    expect(snap(60.4, slider)).toBe(60);
  });

  it("writes the extension's display for the value it drew, else the number and unit", () => {
    expect(controlText(stepper, 22.5)).toBe("22.5 °C");
    expect(controlText(stepper, 23)).toBe("23 °C");
  });
});

describe("UI 6：行动作", () => {
  it("keeps four at most", () => {
    const a = readRowActions(Array.from({ length: 6 }, (_, i) => ({ title: `a${i}`, action: { id: `a${i}` } })));
    expect(a).toHaveLength(4);
  });
});

describe("UI 6：输入", () => {
  it("reads a multichoice value as a list, anything else as none", () => {
    expect(multiValues('["a","b"]')).toEqual(["a", "b"]);
    expect(multiValues("a,b")).toEqual([]);
    expect(multiValues('{"a":1}')).toEqual([]);
    expect(multiValues('["a",1]')).toEqual(["a"]);
  });

  it("writes the chosen ones in the choices' order", () => {
    expect(multiText(["c", "a"], [{ value: "a" }, { value: "b" }, { value: "c" }])).toBe('["a","c"]');
    expect(multiText([], [{ value: "a" }])).toBe("[]");
  });

  it("grows a multiline box from three lines to eight", () => {
    expect(multilineRows("")).toBe(3);
    expect(multilineRows("a\nb\nc\nd")).toBe(4);
    expect(multilineRows("\n".repeat(20))).toBe(8);
  });
});

describe("UI 6：颜色", () => {
  it("knows the eleven names and nothing else (no hex)", () => {
    expect(colorOf("teal")).toBe("teal");
    expect(colorOf("#ff0000")).toBeUndefined();
    expect(hue("blue")).toBe("var(--hue-blue)");
    expect(hue("chartreuse")).toBe("var(--ink-3)");
  });

  it("colors a series by its named color before its tone and the palette", () => {
    expect(seriesColors([{ color: "orange", points: [] }, { tone: "danger", points: [] }, { points: [] }])).toEqual(["var(--hue-orange)", "var(--red)", "var(--series-1)"]);
  });
});

describe("UI 6：图表的几何", () => {
  it("draws a spark across its box, a flat one in the middle", () => {
    expect(sparkPoints([1, 2], 10, 10).line).toBe("1.0,9.0 9.0,1.0");
    expect(sparkPoints([3, 3], 10, 10).line).toBe("1.0,5.0 9.0,5.0");
    expect(sparkPoints([1], 10, 10).line).toBe("");
    expect(readSpark({ values: [1] })).toBeUndefined();
    expect(readSpark({ values: [1, Number.NaN, 2] })?.values).toEqual([1, 2]);
  });

  it("stacks areas on the series before them", () => {
    const s = [{ points: [{ label: "a", value: 1 }, { label: "b", value: 2 }] }, { points: [{ label: "a", value: 3 }, { label: "b", value: 4 }] }];
    expect(areaBands(s, true)[1]).toEqual([{ from: 1, to: 4 }, { from: 2, to: 6 }]);
    expect(areaBands(s, false)[1]).toEqual([{ from: 0, to: 3 }, { from: 0, to: 4 }]);
  });

  it("reads a candle, and the lows and highs of all", () => {
    expect(candleOf({ value: 10, open: 9, high: 11, low: 8, close: 10 })).toEqual({ open: 9, high: 11, low: 8, close: 10, up: true });
    expect(candleOf({ value: 10 })).toBeNull();
    expect(candleExtent([{ value: 10, open: 9, high: 11, low: 8 }, { value: 12, open: 12, high: 13, low: 11.5 }])).toEqual([8, 13]);
    expect(candleColors()).toEqual({ up: "var(--green)", down: "var(--red)" });
    expect(candleColors("red")).toEqual({ up: "var(--red)", down: "var(--green)" });
  });

  it("cuts slices by share and writes the shares", () => {
    const s = slices([1, 3, 0, -2]);
    expect(s.map((x) => x.share)).toEqual([0.25, 0.75, 0, 0]);
    expect(s[1].a1).toBeCloseTo(Math.PI * 2);
    expect(shareText(0.25)).toBe("25%");
    expect(shareText(0.054)).toBe("5.4%");
    expect(shareText(0.004)).toBe("<1%");
  });

  it("draws an arc: a pie's from the middle, a donut's a ring, a whole one as two halves", () => {
    expect(arcPath(10, 10, 10, 0, 0, Math.PI / 2)).toBe("M10,10L10.00,0.00A10,10 0 0 1 20.00,10.00Z");
    expect(arcPath(10, 10, 10, 5, 0, Math.PI / 2)).toBe("M10.00,0.00A10,10 0 0 1 20.00,10.00L15.00,10.00A5,5 0 0 0 10.00,5.00Z");
    expect(arcPath(10, 10, 10, 0, 0, Math.PI * 2).match(/Z/g)).toHaveLength(2);
  });
});
