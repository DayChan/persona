// Thumbnails for the Files panel's rows (#2907, iOS #1370 draws the same): the machine's picture of an image, a PDF,
// a video or a document (node file.thumb, hub feature `file_thumb`). A row asks only once it is on screen, a few at a
// time so a folder of pictures does not hold every connection the page has, and a file the machine cannot draw is not
// asked again this page load (it keeps its glyph). The URL names the file's size and modified time, so the browser's
// copy (the hub allows a day) is used for this page load, until the file or thumbnail processing changes.
import { fileThumbURL } from "./api";

export const FILE_THUMB_FEATURE = "file_thumb";

/** How many thumbnails load at once. */
export const THUMB_PARALLEL = 4;

/** A file's thumbnail at `px` on its long side, versioned by what the listing says about the file. */
export function thumbSrc(mid: string, path: string, cwd: string, px: number, size: number, mtime?: string): string {
  return `${fileThumbURL(mid, path, cwd, px)}&v=${encodeURIComponent(`${size}-${mtime ?? ""}`)}`;
}

/** Thumbnails that did not load (no_thumbnail, an older node, the file gone), by their URL. */
const failed = new Set<string>();
export const thumbFailed = (src: string) => failed.has(src);
export const markThumbFailed = (src: string) => void failed.add(src);

/** A queue of turns: `take` starts `run` once fewer than `limit` are running, `done` hands its turn on. */
export class Slots {
  private running = 0;
  private waiting: (() => void)[] = [];
  constructor(private readonly limit: number) {}

  /** Starts `run` now or when a turn frees; the returned function gives the turn back (or leaves the queue). */
  take(run: () => void): () => void {
    let state: "waiting" | "running" | "done" = "waiting";
    const start = () => {
      state = "running";
      this.running++;
      run();
    };
    if (this.running < this.limit) start();
    else this.waiting.push(start);
    return () => {
      if (state === "done") return;
      if (state === "waiting") this.waiting = this.waiting.filter((w) => w !== start);
      else {
        this.running--;
        this.waiting.shift()?.();
      }
      state = "done";
    };
  }

  get busy() {
    return this.running;
  }
  get queued() {
    return this.waiting.length;
  }
}

export const thumbSlots = new Slots(THUMB_PARALLEL);
