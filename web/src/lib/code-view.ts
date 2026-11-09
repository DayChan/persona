// UI version 4's code hosting components (docs/extensions/extensions.md "UI version 4", #2951), the logic without the looks: a file
// tree built from whole paths, a diff's hunks as numbered rows (one column, or old and new side by side), the gaps
// between hunks, the threads at their lines, and syntax highlighting (highlight.js's common grammars through lowlight,
// loaded the first time a code or diff is drawn; plain until then). Drawn by views/extensions/CodeViews.tsx.
import { useEffect, useState } from "react";

export type FileStatus = "added" | "modified" | "deleted" | "renamed" | string;

export interface TreeEntry {
  path: string;
  kind?: "file" | "dir" | string;
  status?: FileStatus;
  additions?: number;
  deletions?: number;
  comments?: number;
  badge?: { text: string; tone?: string };
  /** UI version 5 */
  viewed?: boolean;
  generated?: boolean;
}

export interface Hunk {
  header?: string;
  old_start: number;
  new_start: number;
  lines: string[];
}

export interface Comment {
  id?: string;
  author: string;
  avatar?: { url?: string; text?: string; ref?: string; path?: string };
  at?: string;
  body?: string;
  badge?: { text: string; tone?: string };
  event?: boolean;
}

export interface Thread {
  id?: string;
  side: "old" | "new" | string;
  line: number;
  start_line?: number;
  resolved?: boolean;
  outdated?: boolean;
  comments: Comment[];
  reply?: { id: string; confirm?: string; submit?: boolean; values?: Record<string, string> };
  /** UI version 5: a draft of the pending review; resolving it */
  pending?: boolean;
  resolve?: { id: string; confirm?: string; submit?: boolean; values?: Record<string, string> };
  /** A session review's draft (#151, not an extension's): deleting it. */
  remove?: { id: string };
}

// ── file tree ─────────────────────────────────────────

/** A node of the tree: a directory (with children) or a file. `name` may be a folded chain (a/b/c). */
export interface TreeNode {
  name: string;
  path: string;
  dir: boolean;
  entry?: TreeEntry;
  children: TreeNode[];
  /** A directory given without anything under it: opened by asking the extension (expand). */
  unloaded?: boolean;
  /** Files under it (for a directory), and their additions and deletions. */
  files: number;
}

/**
 * The tree of entries by their paths: directories first, then files, each by name; a directory with only one
 * directory under it folds into one node (src/upload). A directory named only by the paths under it is made up.
 */
export function buildTree(entries: readonly TreeEntry[]): TreeNode[] {
  const root: TreeNode = { name: "", path: "", dir: true, children: [], files: 0 };
  const dirs = new Map<string, TreeNode>([["", root]]);
  const dirOf = (path: string): TreeNode => {
    const got = dirs.get(path);
    if (got) return got;
    const cut = path.lastIndexOf("/");
    const parent = dirOf(cut < 0 ? "" : path.slice(0, cut));
    const node: TreeNode = { name: path.slice(cut + 1), path, dir: true, children: [], files: 0 };
    parent.children.push(node);
    dirs.set(path, node);
    return node;
  };
  for (const e of entries) {
    const path = e.path.replace(/^\/+|\/+$/g, "");
    if (!path) continue;
    if (e.kind === "dir") {
      dirOf(path).entry = e;
      continue;
    }
    const cut = path.lastIndexOf("/");
    const parent = dirOf(cut < 0 ? "" : path.slice(0, cut));
    parent.children.push({ name: path.slice(cut + 1), path, dir: false, entry: e, children: [], files: 1 });
  }
  const finish = (n: TreeNode): TreeNode => {
    n.children = n.children.map(finish).sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1));
    if (n.dir) {
      n.files = n.children.reduce((s, c) => s + c.files, 0);
      n.unloaded = n.children.length === 0 && n.entry?.kind === "dir";
    }
    // fold a chain of single directories (never a directory still to load, never the root)
    while (n.dir && n.path && n.children.length === 1 && n.children[0].dir && !n.children[0].unloaded) {
      const only = n.children[0];
      n = { ...only, name: `${n.name}/${only.name}` };
    }
    return n;
  };
  return finish(root).children;
}

/** The directories on the way to path (to open them when it is selected). */
export function ancestors(path: string): string[] {
  const parts = path.split("/");
  return parts.slice(0, -1).map((_, i) => parts.slice(0, i + 1).join("/"));
}

/** The entries whose path has q in it (any case), and the directories over them. */
export function filterEntries(entries: readonly TreeEntry[], q: string): TreeEntry[] {
  const needle = q.trim().toLowerCase();
  if (!needle) return [...entries];
  return entries.filter((e) => e.kind !== "dir" && e.path.toLowerCase().includes(needle));
}

/** The letter and tone a file's status is drawn with. */
export function statusMark(status?: FileStatus): { letter: string; tone: "positive" | "danger" | "caution" | "neutral" } | undefined {
  switch (status) {
    case "added":
      return { letter: "A", tone: "positive" };
    case "deleted":
      return { letter: "D", tone: "danger" };
    case "renamed":
      return { letter: "R", tone: "caution" };
    case "modified":
      return { letter: "M", tone: "caution" };
  }
  return undefined;
}

// ── diff ──────────────────────────────────────────────

export type RowKind = "context" | "add" | "del" | "note";

/** One line of a hunk: its kind, its numbers in the old and the new file, its text without the prefix. */
export interface DiffRow {
  kind: RowKind;
  old?: number;
  new?: number;
  text: string;
  /** Its place among the old (or new) side's lines of the hunk: where its highlighting is. */
  oldAt?: number;
  newAt?: number;
}

export function hunkRows(h: Hunk): DiffRow[] {
  let o = h.old_start;
  let n = h.new_start;
  let oi = 0;
  let ni = 0;
  return h.lines.map((line): DiffRow => {
    const sign = line[0] ?? " ";
    const text = line.slice(1);
    if (sign === "+") return { kind: "add", new: n++, text, newAt: ni++ };
    if (sign === "-") return { kind: "del", old: o++, text, oldAt: oi++ };
    if (sign === "\\") return { kind: "note", text: line.slice(1).trim() };
    return { kind: "context", old: o++, new: n++, text, oldAt: oi++, newAt: ni++ };
  });
}

/** A hunk's old and new side as text, for highlighting each with its own state (a comment open across lines). */
export function hunkSides(h: Hunk): { old: string; new: string } {
  const old: string[] = [];
  const nw: string[] = [];
  for (const line of h.lines) {
    const sign = line[0] ?? " ";
    if (sign === "-" || sign === " " || line === "") old.push(line.slice(1));
    if (sign === "+" || sign === " " || line === "") nw.push(line.slice(1));
  }
  return { old: old.join("\n"), new: nw.join("\n") };
}

/** A row of the side by side view: the old line at the start, the new at the end (one of them may be missing). */
export interface SplitRow {
  old?: DiffRow;
  new?: DiffRow;
  note?: string;
}

/** Rows side by side: context on both, a run of deletions paired with the additions after it, line by line. */
export function splitRows(rows: readonly DiffRow[]): SplitRow[] {
  const out: SplitRow[] = [];
  for (let i = 0; i < rows.length; ) {
    const r = rows[i];
    if (r.kind === "context") {
      out.push({ old: r, new: r });
      i++;
      continue;
    }
    if (r.kind === "note") {
      out.push({ note: r.text });
      i++;
      continue;
    }
    const dels: DiffRow[] = [];
    const adds: DiffRow[] = [];
    while (i < rows.length && rows[i].kind === "del") dels.push(rows[i++]);
    while (i < rows.length && rows[i].kind === "add") adds.push(rows[i++]);
    for (let k = 0; k < Math.max(dels.length, adds.length); k++) out.push({ old: dels[k], new: adds[k] });
  }
  return out;
}

/** How many lines lie hidden before hunk i (after the one before it), or undefined when not known (the file's end). */
export function gapBefore(hunks: readonly Hunk[], i: number): number {
  const h = hunks[i];
  if (i === 0) return Math.max(0, h.new_start - 1);
  const prev = hunks[i - 1];
  const end = prev.new_start + prev.lines.filter((l) => l[0] !== "-" && l[0] !== "\\").length;
  return Math.max(0, h.new_start - end);
}

/** Threads by where they show: under the line they are at (side:line), or at the end when it is not in the diff. */
export function placeThreads(threads: readonly Thread[], hunks: readonly Hunk[]): { at: Map<string, Thread[]>; rest: Thread[] } {
  const lines = new Set<string>();
  for (const h of hunks)
    for (const r of hunkRows(h)) {
      if (r.old !== undefined) lines.add(`old:${r.old}`);
      if (r.new !== undefined) lines.add(`new:${r.new}`);
    }
  const at = new Map<string, Thread[]>();
  const rest: Thread[] = [];
  for (const t of threads) {
    const key = `${t.side === "old" ? "old" : "new"}:${t.line}`;
    if (!t.outdated && lines.has(key)) at.set(key, [...(at.get(key) ?? []), t]);
    else rest.push(t);
  }
  return { at, rest };
}

// ── highlighting ──────────────────────────────────────

/** A run of text and the highlight.js classes over it ("" plain). */
export interface Token {
  text: string;
  cls: string;
}

const BY_EXT: Record<string, string> = {
  go: "go", ts: "typescript", tsx: "typescript", mts: "typescript", cts: "typescript", js: "javascript", jsx: "javascript", mjs: "javascript",
  cjs: "javascript", py: "python", rb: "ruby", rs: "rust", java: "java", kt: "kotlin", kts: "kotlin", swift: "swift", c: "c", h: "c",
  cc: "cpp", cpp: "cpp", cxx: "cpp", hpp: "cpp", hh: "cpp", m: "objectivec", mm: "objectivec", cs: "csharp", php: "php", sh: "bash",
  bash: "bash", zsh: "bash", yaml: "yaml", yml: "yaml", json: "json", md: "markdown", markdown: "markdown", html: "xml", htm: "xml",
  xml: "xml", svg: "xml", vue: "xml", css: "css", scss: "scss", less: "less", sql: "sql", toml: "ini", ini: "ini", cfg: "ini",
  lua: "lua", r: "r", pl: "perl", pm: "perl", graphql: "graphql", gql: "graphql", diff: "diff", patch: "diff", vb: "vbnet",
  proto: "protobuf", thrift: "thrift", dockerfile: "dockerfile", mk: "makefile", gradle: "groovy", groovy: "groovy", scala: "scala",
  dart: "dart", ex: "elixir", exs: "elixir", erl: "erlang", hs: "haskell", clj: "clojure", txt: "plaintext",
};
const BY_NAME: Record<string, string> = { makefile: "makefile", dockerfile: "dockerfile", "go.mod": "go", gemfile: "ruby", rakefile: "ruby", "cmakelists.txt": "cmake" };
const ALIASES: Record<string, string> = { golang: "go", "c++": "cpp", "c#": "csharp", shell: "bash", objc: "objectivec", "objective-c": "objectivec", js: "javascript", ts: "typescript", py: "python", html: "xml", text: "plaintext" };

/** The grammar for a file: its language as the extension says (a linguist name or an extension), else from its path. */
export function languageOf(language?: string, path?: string): string {
  const l = (language ?? "").trim().toLowerCase();
  if (l) return ALIASES[l] ?? BY_EXT[l] ?? l;
  const name = (path ?? "").split("/").pop()?.toLowerCase() ?? "";
  if (BY_NAME[name]) return BY_NAME[name];
  const ext = name.includes(".") ? name.split(".").pop()! : "";
  return BY_EXT[ext] ?? "";
}

interface HastNode {
  type: string;
  value?: string;
  properties?: { className?: string[] | string };
  children?: HastNode[];
}

/** A highlighted tree (lowlight's) as lines of tokens: what is open across a line break goes on in the next line. */
export function tokenLines(root: HastNode): Token[][] {
  const lines: Token[][] = [[]];
  const walk = (n: HastNode, cls: string) => {
    if (n.type === "text") {
      const parts = (n.value ?? "").split("\n");
      parts.forEach((p, i) => {
        if (i > 0) lines.push([]);
        if (p) lines[lines.length - 1].push({ text: p, cls });
      });
      return;
    }
    const own = n.properties?.className;
    const here = [cls, ...(Array.isArray(own) ? own : own ? [own] : [])].filter(Boolean).join(" ");
    for (const c of n.children ?? []) walk(c, here);
  };
  walk(root, "");
  return lines;
}

type Lowlight = { highlight: (lang: string, text: string) => HastNode; registered: (lang: string) => boolean };
let lowlight: Lowlight | null = null;
let loading: Promise<Lowlight> | null = null;
const waiting = new Set<() => void>();

function loadLowlight(): Promise<Lowlight> {
  // Destructured so Rollup drops the entry's `all` re-export (every grammar, ~780 kB) and shares the common-grammar
  // chunk rehype-highlight loads.
  loading ??= import("lowlight").then(({ createLowlight, common }) => {
    lowlight = createLowlight(common) as unknown as Lowlight;
    waiting.forEach((f) => f());
    waiting.clear();
    return lowlight;
  });
  return loading;
}

/** text highlighted as lang, line by line; null while the grammars load, or for a language it has none for. */
export function highlightLines(text: string, lang: string): Token[][] | null {
  if (!lowlight || !lang || lang === "plaintext" || !lowlight.registered(lang) || text.length > 400_000) return null;
  try {
    return tokenLines(lowlight.highlight(lang, text));
  } catch {
    return null;
  }
}

/** Highlighting is wanted: loads the grammars (once), drawing again when they come. */
export function useHighlighter(wanted: boolean): boolean {
  const [ready, setReady] = useState(!!lowlight);
  useEffect(() => {
    if (!wanted || lowlight) return;
    const done = () => setReady(true);
    waiting.add(done);
    void loadLowlight().catch(() => {});
    return () => {
      waiting.delete(done);
    };
  }, [wanted]);
  return ready || !!lowlight;
}

// ── preferences ───────────────────────────────────────

const MODE_KEY = "persona.diff.mode";

/** How diffs are shown: one column, or old and new side by side (only where there is room). Kept in this browser. */
export function diffMode(): "unified" | "split" {
  try {
    return localStorage.getItem(MODE_KEY) === "split" ? "split" : "unified";
  } catch {
    return "unified";
  }
}

export function setDiffMode(mode: "unified" | "split") {
  try {
    localStorage.setItem(MODE_KEY, mode);
  } catch {
    // private mode: only for this page
  }
  window.dispatchEvent(new CustomEvent(MODE_KEY));
}

/** The diff mode, following a change made by any diff on the page. */
export function useDiffMode(): ["unified" | "split", (m: "unified" | "split") => void] {
  const [mode, setMode] = useState(diffMode);
  useEffect(() => {
    const on = () => setMode(diffMode());
    window.addEventListener(MODE_KEY, on);
    return () => window.removeEventListener(MODE_KEY, on);
  }, []);
  return [mode, setDiffMode];
}

/** Whether a view needs the room of a wide page: split, or with code or diffs in it. */
export function wideView(view?: { layout?: string; sections: { items: { type: string }[] }[] } | null): boolean {
  if (!view) return false;
  return view.layout === "split" || view.sections.some((s) => s.items.some((it) => it.type === "diff" || it.type === "code"));
}
