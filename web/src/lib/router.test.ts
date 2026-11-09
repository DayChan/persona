import { describe, expect, it } from "vitest";
import { parse, showsSession } from "./router";

describe("showsSession", () => {
  it("knows an agent's chat by its own address and by its tab's (#3405)", () => {
    expect(showsSession(parse("/s/mac/s1"), "mac", "s1")).toBe(true);
    expect(showsSession(parse("/t/mac/space/slot/s1"), "mac", "s1")).toBe(true);
    expect(showsSession(parse("/s/mac/s2"), "mac", "s1")).toBe(false);
    expect(showsSession(parse("/t/other/space/slot/s1"), "mac", "s1")).toBe(false);
    expect(showsSession(parse("/t/mac/space/slot"), "mac", "s1")).toBe(false);
    expect(showsSession(parse("/todos"), "mac", "s1")).toBe(false);
  });
});

it("opens a request by its own id without inventing a session", () => {
  expect(parse("/requests/request-one")).toEqual({ name: "request", id: "request-one" });
  expect(showsSession(parse("/requests/request-one"), "example-computer", "")).toBe(false);
});
