import { describe, expect, it } from "vitest";
import { CapturePace } from "./capture-pace";

/** `count` frames 20 ms apart of wall clock from `from` ms, with the context's clock moving `rate` times as fast. */
function feed(pace: CapturePace, from: number, count: number, rate = 1, every = 20) {
  for (let i = 0; i < count; i++) pace.frame(from + i * every, ((from + i * every) / 1000) * rate);
}

describe("CapturePace", () => {
  it("is quiet while 50 frames a second come", () => {
    const pace = new CapturePace();
    feed(pace, 0, 251);
    expect(pace.check(5000, 5)).toBeNull();
    feed(pace, 5020, 250);
    expect(pace.check(10_000, 10)).toBeNull();
  });

  it("reports a window that fell behind the wall clock, with how far the context's clock moved", () => {
    const pace = new CapturePace();
    feed(pace, 0, 251);
    expect(pace.check(5000, 5)).toBeNull();
    // the context on a sink that runs at a quarter of real time: a frame every 80 ms
    feed(pace, 5080, 62, 0.25, 80);
    const lag = pace.check(10_000, 5 + 1.25);
    expect(lag).toEqual({ frames: 62, expected: 250, seconds: 5, contextSeconds: 1.25 });
  });

  it("tolerates a short burst at a window's edge", () => {
    const pace = new CapturePace();
    feed(pace, 0, 1);
    // 10 % short: a main thread busy for half a second just before the check
    feed(pace, 20, 226);
    expect(pace.check(5000, 5)).toBeNull();
  });

  it("starts counting at the first frame and judges no window shorter than two seconds", () => {
    const pace = new CapturePace();
    expect(pace.check(10_000, 10)).toBeNull();
    feed(pace, 10_000, 1);
    expect(pace.check(11_000, 11)).toBeNull();
    // nothing more came: the window from the first frame to now is judged once it is long enough
    expect(pace.check(13_000, 13)).toEqual({ frames: 0, expected: 150, seconds: 3, contextSeconds: 3 });
    // a new window from the last check
    expect(pace.check(13_500, 13.5)).toBeNull();
  });
});
