// Pure logic of a session's files / Git panels and the file viewer (docs/apple/ios.md "File viewer", the session page's
// "Files" / Git; iOS: FilesPane, GitPane, FileViewer, Persona/Code). No DOM here: tested in files.test.ts.

import { decodeURIComponentSafe } from "./uri";

// ── wire types (docs/protocol/protocol.md: /machines/{mid}/fs, /file, /git/*) ─────────

export interface FsEntry {
  name: string;
  dir: boolean;
  symlink?: boolean;
  size: number;
  mtime?: string;
  /** Git ignores it (the tree dims it); only set inside a repository. */
  ignored?: boolean;
}

export interface FsListing {
  path: string;
  entries: FsEntry[];
  truncated: boolean;
}

export interface FileContent {
  path: string;
  name: string;
  size: number;
  mime?: string;
  text?: string;
  data?: string;
  truncated: boolean;
  is_dir: boolean;
  /** A directory's first 500 names; folders end with "/". */
  entries?: string[];
  offset?: number;
  /** Where the next page starts; 0 / missing: the end. */
  next?: number;
  /** Asked with stream=1 and binary: no `data`; the bytes come from fileDownloadURL (Range), `revision` pins them. */
  stream?: boolean;
  revision?: string;
}

export interface GitFile {
  path: string;
  orig_path?: string;
  /** The staged change ("." none, "?" untracked); in a commit, the change kind. */
  index: string;
  worktree: string;
  added: number;
  deleted: number;
  binary?: boolean;
}

export interface GitCommit {
  hash: string;
  short: string;
  subject: string;
  body?: string;
  author: string;
  date?: string;
  refs?: string;
}

export interface GitStatus {
  is_repo: boolean;
  root?: string;
  branch?: string;
  head?: GitCommit;
  upstream?: string;
  ahead?: number;
  behind?: number;
  files: GitFile[];
  stashes?: number;
  commits: GitCommit[];
  /** The upstream was deleted on the remote: ahead / behind are 0 and mean nothing, a push publishes the branch again. */
  gone?: boolean;
}

export interface GitDiff {
  path: string;
  diff: string;
  truncated: boolean;
}

export interface GitShow {
  commit: GitCommit;
  files: GitFile[];
  diff: string;
  truncated: boolean;
}

// ── paths ─────────────────────────────────────────────────

export function joinPath(dir: string, name: string): string {
  if (!dir) return name;
  return dir.replace(/\/+$/, "") + "/" + name.replace(/^\/+/, "");
}

export function dirName(p: string): string {
  const t = p.replace(/\/+$/, "");
  const i = t.lastIndexOf("/");
  return i < 0 ? "" : i === 0 ? "/" : t.slice(0, i);
}

export function fileName(p: string): string {
  const t = p.replace(/\/+$/, "");
  return t.slice(t.lastIndexOf("/") + 1) || p;
}

/** The folders of `path` below `root` (["a", "b"] for root/a/b), [] for root itself, null when it is not inside. */
export function below(root: string, path: string): string[] | null {
  const r = root.replace(/\/+$/, ""),
    p = path.replace(/\/+$/, "");
  if (p === r) return [];
  if (!p.startsWith(r + "/")) return null;
  return p.slice(r.length + 1).split("/").filter(Boolean);
}

/** A path a Markdown file refers to (an image, a link), resolved against the file's folder; "" for a web URL / anchor. */
export function resolveRef(baseDir: string, ref: string): string {
  const src = ref.trim().split(/[?#]/)[0];
  if (!src || !isMachineRef(ref)) return "";
  const path = decodeURIComponentSafe(src);
  if (path.startsWith("~") || path.startsWith("/")) return normalize(path);
  return normalize(joinPath(baseDir, path));
}

/** A reference that points into the machine (relative, absolute or ~/), not a URL or an in-page anchor. */
export function isMachineRef(ref: string): boolean {
  const r = ref.trim();
  if (!r || r.startsWith("#") || r.startsWith("//")) return false;
  return !/^[a-z][a-z0-9+.-]*:/i.test(r);
}

function normalize(p: string): string {
  const abs = p.startsWith("/");
  const out: string[] = [];
  for (const part of p.split("/")) {
    if (!part || part === ".") continue;
    if (part === ".." && out.length && out[out.length - 1] !== ".." && out[out.length - 1] !== "~") out.pop();
    else if (part !== ".." || !abs) out.push(part);
  }
  return (abs ? "/" : "") + out.join("/");
}

// ── Git ───────────────────────────────────────────────────

export const isUntracked = (f: GitFile) => f.index === "?";
export const isConflicted = (f: GitFile) => f.index === "U" || f.worktree === "U";
export const isStaged = (f: GitFile) => !isUntracked(f) && f.index !== ".";
export const hasUnstaged = (f: GitFile) => !isUntracked(f) && f.worktree !== ".";

/** The one letter in the change list: untracked U, conflict !, then the staged change, then the working tree's. */
export function gitLetter(f: GitFile): string {
  if (isUntracked(f)) return "U";
  if (isConflicted(f)) return "!";
  return f.index !== "." ? f.index : f.worktree;
}

export type GitTone = "add" | "del" | "moved" | "changed";

/** How a letter is colored, like editors do: added green, deleted / conflicted red, renamed quiet, the rest changed. */
export function gitTone(letter: string): GitTone {
  if (letter === "A" || letter === "U") return "add";
  if (letter === "D" || letter === "!") return "del";
  if (letter === "R" || letter === "C") return "moved";
  return "changed";
}

/** The letter of a file, or for a folder the letter of the first change inside it; null when unchanged. */
export function gitMark(status: GitStatus | null | undefined, full: string, dir: boolean): string | null {
  if (!status?.is_repo || !status.root || !status.files.length) return null;
  const rel = below(status.root, full);
  if (!rel || !rel.length) return null;
  const r = rel.join("/");
  const f = dir ? status.files.find((x) => x.path.startsWith(r + "/")) : status.files.find((x) => x.path === r);
  return f ? gitLetter(f) : null;
}

/** The Git panel's header badge: commits ahead (↑) and behind (↓) the upstream and `*` for a dirty tree; "" when none. */
export function syncBadge(status: GitStatus | null | undefined): string {
  const parts = aheadBehind(status) ? [aheadBehind(status)] : [];
  if (status?.is_repo && status.files.length > 0) parts.push("*");
  return parts.join(" ");
}

/** "↑2 ↓1": commits ahead of and behind the upstream, "" when level (or not a repository). */
export function aheadBehind(status: GitStatus | null | undefined): string {
  if (!status?.is_repo) return "";
  const parts: string[] = [];
  if ((status.ahead ?? 0) > 0) parts.push(`↑${status.ahead}`);
  if ((status.behind ?? 0) > 0) parts.push(`↓${status.behind}`);
  return parts.join(" ");
}

/** Branch / tag names pointing at a commit ("HEAD -> main, origin/main" → ["main", "origin/main"]). */
export function refNames(refs?: string): string[] {
  return (refs ?? "")
    .split(",")
    .map((r) => r.trim())
    .map((r) => (r.startsWith("HEAD -> ") ? r.slice(8) : r))
    .filter((r) => r && r !== "HEAD");
}

export function diffTotals(files: GitFile[]): { added: number; deleted: number } {
  return files.reduce((t, f) => ({ added: t.added + (f.added || 0), deleted: t.deleted + (f.deleted || 0) }), { added: 0, deleted: 0 });
}

export type DiffKind = "add" | "del" | "context" | "hunk" | "info";

export interface DiffLine {
  kind: DiffKind;
  text: string;
  old?: number;
  new?: number;
}

/** A unified diff as lines with old / new line numbers; the file headers (diff --git, index, ---, +++) are dropped. */
export function parseDiff(diff: string): DiffLine[] {
  const out: DiffLine[] = [];
  let old = 0,
    nw = 0;
  let inHunk = false;
  for (const raw of diff.split("\n")) {
    if (raw.startsWith("diff --git")) {
      inHunk = false;
      continue;
    }
    if (!inHunk && (raw.startsWith("index ") || raw.startsWith("--- ") || raw.startsWith("+++ "))) continue;
    if (raw.startsWith("@@")) {
      // @@ -12,7 +12,9 @@ context
      const m = /^@@+ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(raw);
      if (m) {
        old = Number(m[1]);
        nw = Number(m[2]);
      }
      inHunk = true;
      out.push({ kind: "hunk", text: raw });
    } else if (inHunk && raw.startsWith("+")) {
      out.push({ kind: "add", text: raw.slice(1), new: nw++ });
    } else if (inHunk && raw.startsWith("-")) {
      out.push({ kind: "del", text: raw.slice(1), old: old++ });
    } else if (inHunk && raw.startsWith(" ")) {
      out.push({ kind: "context", text: raw.slice(1), old: old++, new: nw++ });
    } else if (/^(\\|Binary|new file|deleted file|similarity|rename |old mode|new mode)/.test(raw)) {
      out.push({ kind: "info", text: raw });
    }
  }
  return out;
}

/** A multi-file diff (git show) cut into one diff per file, keyed by the new path. */
export function splitDiff(diff: string): Record<string, string> {
  const res: Record<string, string> = {};
  let path: string | null = null;
  let buf: string[] = [];
  const flush = () => {
    if (path !== null) res[path] = buf.join("\n");
  };
  for (const line of diff.split("\n")) {
    if (line.startsWith("diff --git ")) {
      flush();
      buf = [];
      // diff --git a/x b/x → x (b/ taken from the end, so paths with spaces work)
      const i = line.lastIndexOf(" b/");
      path = i >= 0 ? line.slice(i + 3) : null;
    }
    buf.push(line);
  }
  flush();
  return res;
}

// ── the viewer ────────────────────────────────────────────

export type ViewKind = "directory" | "markdown" | "json" | "notebook" | "html" | "video" | "audio" | "code" | "image" | "pdf" | "sheet" | "office" | "binary";

const VIDEO_EXTS = ["mp4", "m4v", "mov", "webm", "mkv", "ogv", "avi"];
const AUDIO_EXTS = ["mp3", "m4a", "aac", "wav", "flac", "ogg", "oga", "opus", "caf", "aiff", "aif"];

/** Office documents drawn in the browser (docx-preview, lib/pptx.ts); the rest go through the machine's LibreOffice. */
export const OFFICE_RENDERED = ["docx", "docm", "dotx", "pptx", "pptm", "ppsx", "potx"];
const OFFICE_CONVERTED = ["doc", "dot", "ppt", "pps", "rtf", "odt", "odp", "pages", "key", "wps", "wpd"];

/** Tables: delimited text, and workbooks SheetJS reads (lib/sheets.ts). */
const DELIMITED_EXTS = ["csv", "tsv", "tab"];
const WORKBOOK_EXTS = ["xlsx", "xlsm", "xlsb", "xls", "ods", "numbers"];

export function extOf(name: string): string {
  const n = fileName(name).toLowerCase();
  const i = n.lastIndexOf(".");
  return i > 0 ? n.slice(i + 1) : "";
}

/** How the viewer shows a file. */
export function viewKind(f: FileContent): ViewKind {
  if (f.is_dir) return "directory";
  const mime = f.mime ?? "";
  if (f.text === undefined || f.text === null) {
    if (mime.startsWith("image/")) return "image";
    if (mime.startsWith("video/") || VIDEO_EXTS.includes(extOf(f.name))) return "video";
    if (mime.startsWith("audio/") || AUDIO_EXTS.includes(extOf(f.name))) return "audio";
    if (mime === "application/pdf" || extOf(f.name) === "pdf") return "pdf";
    if (WORKBOOK_EXTS.includes(extOf(f.name))) return "sheet";
    if ([...OFFICE_RENDERED, ...OFFICE_CONVERTED].includes(extOf(f.name))) return "office";
    return "binary";
  }
  const ext = extOf(f.name);
  if (ext === "rtf") return "office"; // RTF is text, but its source is markup nobody reads
  if (ext === "ipynb") return "notebook";
  if (["html", "htm", "xhtml"].includes(ext)) return "html";
  if (ext === "svg") return "image"; // drawn by <img>, where its scripts never run
  if (DELIMITED_EXTS.includes(ext) || mime === "text/csv" || mime === "text/tab-separated-values") return "sheet";
  if (["md", "markdown", "mdx", "mkd"].includes(ext) || mime === "text/markdown") return ext ? "markdown" : "code";
  if (["json", "jsonc", "json5", "geojson", "webmanifest"].includes(ext) || (mime.includes("json") && !["jsonl", "ndjson"].includes(ext))) return "json";
  return "code";
}

/** What a machine can draw a thumbnail of (node file.thumb: images; PDF, video, Office and iWork through Quick Look on
 * a Mac or pdftoppm / ffmpeg elsewhere), as iOS asks (FileThumbnails.extensions). Code and text keep their glyph; an
 * empty file has nothing to draw. */
export function thumbable(name: string, size: number): boolean {
  if (size <= 0) return false;
  const ext = extOf(name);
  return ["png", "jpg", "jpeg", "gif", "webp", "heic", "heif", "tiff", "tif", "bmp", "svg", "avif", "pdf"].includes(ext) || [...VIDEO_EXTS, ...OFFICE_RENDERED, ...OFFICE_CONVERTED, ...WORKBOOK_EXTS].includes(ext);
}

/** The icon family of a name in a listing. */
export function glyphOf(name: string): "image" | "doc" | "data" | "sheet" | "pdf" | "code" | "file" {
  const ext = extOf(name);
  if (["png", "jpg", "jpeg", "gif", "webp", "heic", "bmp", "ico", "svg", "avif", "tiff"].includes(ext)) return "image";
  if (["md", "markdown", "txt", "rst", "log", "rtf"].includes(ext)) return "doc";
  if (["json", "jsonl", "yml", "yaml", "toml", "plist", "xml", "ini", "env", "lock"].includes(ext)) return "data";
  if ([...DELIMITED_EXTS, ...WORKBOOK_EXTS].includes(ext)) return "sheet";
  if ([...OFFICE_RENDERED, ...OFFICE_CONVERTED].includes(ext)) return "doc";
  if (ext === "pdf") return "pdf";
  if (!ext || NOT_CODE_EXTS.includes(ext)) return "file";
  return "code";
}

/** Archives, installers, binaries, media and fonts: a plain file, not code (#4883: a .zip drew the <> glyph). */
const NOT_CODE_EXTS = [
  ...["zip", "gz", "tgz", "tar", "bz2", "xz", "zst", "7z", "rar", "dmg", "pkg", "iso", "deb", "rpm", "apk", "ipa", "jar", "whl"],
  ...["exe", "bin", "dll", "so", "dylib", "a", "o", "class", "wasm", "dat", "sqlite", "db"],
  ...["mp3", "wav", "flac", "m4a", "aac", "ogg", "opus", "mp4", "mov", "m4v", "webm", "mkv", "avi"],
  ...["woff", "woff2", "ttf", "otf"],
];

/** Text into lines; a final newline does not make an empty last line. */
export function splitLines(text: string): string[] {
  if (!text) return [];
  const lines = text.split(/\r?\n/);
  if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();
  return lines;
}

/** Columns a line takes in a monospaced font: wide (CJK, emoji) characters take two. */
export function columns(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c >= 0xd800 && c <= 0xdbff) {
      n += 2;
      i++;
    } else n += c >= 0x1100 && (c <= 0x115f || (c >= 0x2e80 && c <= 0xa4cf) || (c >= 0xac00 && c <= 0xd7a3) || (c >= 0xf900 && c <= 0xfaff) || (c >= 0xfe30 && c <= 0xfe4f) || (c >= 0xff00 && c <= 0xff60) || (c >= 0xffe0 && c <= 0xffe6)) ? 2 : 1;
  }
  return n;
}

/** A blob's bytes from base64. */
export function base64Bytes(data: string): Uint8Array<ArrayBuffer> {
  const bin = atob(data);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/**
 * JSON re-indented without parsing its values: key order and big numbers stay exactly as written (JSON.parse would
 * put integer-like keys first and round 64-bit ids). Returns null when the text is not valid JSON.
 */
export function prettyJSON(text: string, indent = "  "): string | null {
  try {
    JSON.parse(text);
  } catch {
    return null;
  }
  let out = "";
  let depth = 0;
  const n = text.length;
  for (let i = 0; i < n; i++) {
    const c = text[i];
    if (c === '"') {
      let j = i + 1;
      while (j < n && text[j] !== '"') j += text[j] === "\\" ? 2 : 1;
      out += text.slice(i, j + 1);
      i = j;
    } else if (c === "{" || c === "[") {
      let j = i + 1;
      while (j < n && /\s/.test(text[j])) j++;
      if (text[j] === (c === "{" ? "}" : "]")) {
        out += c + text[j];
        i = j;
      } else {
        depth++;
        out += c + "\n" + indent.repeat(depth);
      }
    } else if (c === "}" || c === "]") {
      depth--;
      out += "\n" + indent.repeat(depth) + c;
    } else if (c === ",") {
      out += ",\n" + indent.repeat(depth);
    } else if (c === ":") {
      out += ": ";
    } else if (!/\s/.test(c)) {
      out += c;
    }
  }
  return out;
}

// ── a small lexer: comments, strings, numbers (and JSON keys / constants, diff lines) ─────────

export type TokenKind = "comment" | "string" | "number" | "key" | "const" | "add" | "del" | "meta";

export interface Token {
  from: number;
  to: number;
  kind: TokenKind;
}

/** What is still open at the end of a line: a block comment or a multi-line string, and how it ends. */
export type LexState = { kind: "comment" | "string"; end: string } | null;

export interface Lang {
  id: string;
  line?: string[];
  block?: [string, string][];
  /** String delimiters, longest first; the ones in `multiline` may span lines. */
  strings?: string[];
  multiline?: string[];
  /** Backslash does not escape in these (shell '…', Go `…`). */
  raw?: string[];
  consts?: Set<string>;
  /** A quote opens a string only when the text before it matches (YAML: after "key:" / "- "; HTML: an attribute's =). */
  quoteAfter?: RegExp;
  json?: boolean;
  diff?: boolean;
}

const C_LIKE = { line: ["//"], block: [["/*", "*/"]] as [string, string][] };
const HASH = { line: ["#"] };
const BOOL = new Set(["true", "false", "null", "nil", "None", "True", "False", "undefined"]);

const LANGS: Record<string, Lang> = {
  swift: { id: "Swift", ...C_LIKE, strings: ['"""', '"'], multiline: ['"""'], consts: BOOL },
  go: { id: "Go", ...C_LIKE, strings: ['"', "'", "`"], multiline: ["`"], raw: ["`"], consts: BOOL },
  js: { id: "JavaScript", ...C_LIKE, strings: ['"', "'", "`"], multiline: ["`"], consts: BOOL },
  ts: { id: "TypeScript", ...C_LIKE, strings: ['"', "'", "`"], multiline: ["`"], consts: BOOL },
  rust: { id: "Rust", ...C_LIKE, strings: ['"'], multiline: ['"'], consts: BOOL },
  c: { id: "C / C++", ...C_LIKE, strings: ['"', "'"], consts: BOOL },
  objc: { id: "Objective-C", ...C_LIKE, strings: ['"', "'"], consts: new Set(["YES", "NO", "nil", "NULL", "true", "false"]) },
  java: { id: "Java", ...C_LIKE, strings: ['"""', '"', "'"], multiline: ['"""'], consts: BOOL },
  kotlin: { id: "Kotlin", ...C_LIKE, strings: ['"""', '"', "'"], multiline: ['"""'], consts: BOOL },
  scala: { id: "Scala", ...C_LIKE, strings: ['"""', '"', "'"], multiline: ['"""'], consts: BOOL },
  groovy: { id: "Groovy", ...C_LIKE, strings: ["'''", '"""', '"', "'"], multiline: ["'''", '"""'], consts: BOOL },
  csharp: { id: "C#", ...C_LIKE, strings: ['"', "'"], consts: BOOL },
  dart: { id: "Dart", ...C_LIKE, strings: ["'''", '"""', '"', "'"], multiline: ["'''", '"""'], consts: BOOL },
  php: { id: "PHP", line: ["//", "#"], block: [["/*", "*/"]], strings: ['"', "'"], consts: BOOL },
  css: { id: "CSS", block: [["/*", "*/"]], strings: ['"', "'"] },
  scss: { id: "SCSS", ...C_LIKE, strings: ['"', "'"] },
  proto: { id: "Protobuf", ...C_LIKE, strings: ['"', "'"], consts: BOOL },
  python: { id: "Python", ...HASH, strings: ['"""', "'''", '"', "'"], multiline: ['"""', "'''"], consts: BOOL },
  shell: { id: "Shell", ...HASH, strings: ['"', "'"], raw: ["'"], consts: BOOL },
  ruby: { id: "Ruby", ...HASH, strings: ['"', "'"], consts: BOOL },
  yaml: { id: "YAML", ...HASH, strings: ['"', "'"], quoteAfter: /(^|[:\-[{,])\s*$/, consts: new Set(["true", "false", "null", "yes", "no", "on", "off", "~"]) },
  toml: { id: "TOML", ...HASH, strings: ['"""', "'''", '"', "'"], multiline: ['"""', "'''"], raw: ["'''", "'"], consts: BOOL },
  ini: { id: "INI", line: ["#", ";"], strings: ['"'] },
  docker: { id: "Dockerfile", ...HASH, strings: ['"', "'"] },
  make: { id: "Makefile", ...HASH, strings: ['"', "'"] },
  cmake: { id: "CMake", ...HASH, strings: ['"'] },
  hcl: { id: "HCL", line: ["#", "//"], block: [["/*", "*/"]], strings: ['"'], consts: BOOL },
  r: { id: "R", ...HASH, strings: ['"', "'"], consts: new Set(["TRUE", "FALSE", "NULL", "NA"]) },
  perl: { id: "Perl", ...HASH, strings: ['"', "'"] },
  elixir: { id: "Elixir", ...HASH, strings: ['"""', '"', "'"], multiline: ['"""'], consts: BOOL },
  sql: { id: "SQL", line: ["--"], block: [["/*", "*/"]], strings: ["'", '"'], consts: new Set(["NULL", "TRUE", "FALSE", "null", "true", "false"]) },
  lua: { id: "Lua", line: ["--"], block: [["--[[", "]]"]], strings: ['"', "'"], consts: BOOL },
  haskell: { id: "Haskell", line: ["--"], block: [["{-", "-}"]], strings: ['"'], consts: new Set(["True", "False"]) },
  graphql: { id: "GraphQL", ...HASH, strings: ['"""', '"'], multiline: ['"""'], consts: BOOL },
  html: { id: "HTML", block: [["<!--", "-->"]], strings: ['"', "'"], quoteAfter: /=\s*$/ },
  xml: { id: "XML", block: [["<!--", "-->"]], strings: ['"', "'"], quoteAfter: /=\s*$/ },
  json: { id: "JSON", line: ["//"], block: [["/*", "*/"]], strings: ['"'], consts: new Set(["true", "false", "null"]), json: true },
  diff: { id: "diff", diff: true },
  markdown: { id: "Markdown" },
  text: { id: "" },
};

const BY_EXT: Record<string, string> = {
  swift: "swift", go: "go", js: "js", mjs: "js", cjs: "js", jsx: "js", ts: "ts", tsx: "ts", mts: "ts", cts: "ts",
  rs: "rust", c: "c", h: "c", cc: "c", cpp: "c", cxx: "c", hpp: "c", hh: "c", hxx: "c", m: "objc", mm: "objc",
  java: "java", kt: "kotlin", kts: "kotlin", scala: "scala", sc: "scala", groovy: "groovy", gradle: "groovy",
  cs: "csharp", dart: "dart", php: "php", css: "css", scss: "scss", less: "scss", sass: "scss", proto: "proto",
  py: "python", pyi: "python", sh: "shell", bash: "shell", zsh: "shell", fish: "shell", ksh: "shell", rb: "ruby",
  yml: "yaml", yaml: "yaml", toml: "toml", ini: "ini", cfg: "ini", conf: "ini", env: "ini", properties: "ini",
  dockerfile: "docker", mk: "make", cmake: "cmake", tf: "hcl", tfvars: "hcl", hcl: "hcl", r: "r", pl: "perl",
  pm: "perl", ex: "elixir", exs: "elixir", sql: "sql", lua: "lua", hs: "haskell", graphql: "graphql", gql: "graphql",
  html: "html", htm: "html", vue: "html", svelte: "html", xml: "xml", plist: "xml", svg: "xml", storyboard: "xml",
  xib: "xml", xcscheme: "xml", json: "json", jsonc: "json", json5: "json", jsonl: "json", ndjson: "json",
  geojson: "json", webmanifest: "json", diff: "diff", patch: "diff", md: "markdown", markdown: "markdown",
};

const BY_NAME: Record<string, string> = {
  dockerfile: "docker", containerfile: "docker", makefile: "make", gnumakefile: "make", "cmakelists.txt": "cmake",
  podfile: "ruby", gemfile: "ruby", rakefile: "ruby", fastfile: "ruby", brewfile: "ruby", ".zshrc": "shell",
  ".bashrc": "shell", ".bash_profile": "shell", ".profile": "shell", ".zprofile": "shell", ".zshenv": "shell",
  ".env": "ini", ".gitignore": "ini", ".gitconfig": "ini", ".editorconfig": "ini", ".npmrc": "ini",
  ".dockerignore": "ini", "go.mod": "go", "go.sum": "text", "package.json": "json",
};

/** The language of a file from its name, then its first line (#!/usr/bin/env python3, <?xml); null: plain text. */
export function languageOf(name: string, firstLine = ""): Lang | null {
  const n = fileName(name).toLowerCase();
  let id = BY_NAME[n] ?? (n.startsWith("dockerfile.") || n.endsWith(".dockerfile") ? "docker" : n.startsWith(".env.") ? "ini" : BY_EXT[extOf(n)]);
  if (!id && firstLine.startsWith("#!")) {
    // #!/usr/bin/env python3, #!/bin/bash -e → python, bash
    const words = firstLine.slice(2).trim().split(/\s+/);
    const cmd = words[0].endsWith("/env") ? (words.find((w, i) => i > 0 && !w.startsWith("-")) ?? "") : words[0];
    const prog = fileName(cmd).replace(/[\d.]+$/, "");
    id = /^(ba|z|k|da|fi)?sh$/.test(prog) ? "shell" : prog === "python" ? "python" : prog === "node" || prog === "deno" || prog === "bun" ? "js" : prog === "ruby" ? "ruby" : prog === "perl" ? "perl" : "";
  }
  if (!id && firstLine.startsWith("<?xml")) id = "xml";
  if (!id || id === "text") return null;
  return LANGS[id];
}

/** The language of a Markdown code fence (```swift). */
export function languageOfFence(name: string): Lang | null {
  const alias: Record<string, string> = {
    python3: "py", python: "py", javascript: "js", node: "js", typescript: "ts", bash: "sh", shell: "sh", console: "sh",
    shellscript: "sh", zsh: "sh", golang: "go", rust: "rs", kotlin: "kt", ruby: "rb", "c++": "cpp", "objective-c": "m",
    objc: "m", csharp: "cs", "c#": "cs", docker: "dockerfile", make: "mk", makefile: "mk", terraform: "tf", yml: "yaml",
  };
  const n = name.trim().toLowerCase();
  if (!n) return null;
  return n === "dockerfile" ? LANGS.docker : languageOf("x." + (alias[n] ?? n));
}

const IDENT = /[A-Za-z0-9_$]/;
const NUMBER = /(?:0[xX][0-9a-fA-F_]+|0[bB][01_]+|0[oO][0-7_]+|(?:\d[\d_]*)?\.?\d[\d_]*(?:[eE][+-]?\d+)?)[a-zA-Z]{0,3}/y;
const WORD = /[A-Za-z_$~][A-Za-z0-9_$]*/y;

/** One line's tokens, carrying a block comment / multi-line string from the line before and to the next. */
export function lexLine(line: string, lang: Lang, state: LexState): { tokens: Token[]; state: LexState } {
  const tokens: Token[] = [];
  const n = line.length;
  if (lang.diff) {
    if (/^(diff --git|index |--- |\+\+\+ |@@)/.test(line)) tokens.push({ from: 0, to: n, kind: "meta" });
    else if (line.startsWith("+")) tokens.push({ from: 0, to: n, kind: "add" });
    else if (line.startsWith("-")) tokens.push({ from: 0, to: n, kind: "del" });
    return { tokens, state: null };
  }
  let i = 0;
  if (state) {
    const end = findEnd(line, 0, state.end, state.kind === "string" && !lang.raw?.includes(state.end));
    if (end < 0) return { tokens: n ? [{ from: 0, to: n, kind: state.kind }] : [], state };
    tokens.push({ from: 0, to: end, kind: state.kind });
    i = end;
  }
  outer: while (i < n) {
    const c = line[i];
    for (const lc of lang.line ?? []) {
      // "#" is a comment only at the start or after a space: not in $#, ${#a}, url#frag, "#fff"
      if (line.startsWith(lc, i) && (lc !== "#" || i === 0 || /\s/.test(line[i - 1]))) {
        tokens.push({ from: i, to: n, kind: "comment" });
        break outer;
      }
    }
    for (const [open, close] of lang.block ?? []) {
      if (line.startsWith(open, i)) {
        const end = findEnd(line, i + open.length, close, false);
        if (end < 0) {
          tokens.push({ from: i, to: n, kind: "comment" });
          return { tokens, state: { kind: "comment", end: close } };
        }
        tokens.push({ from: i, to: end, kind: "comment" });
        i = end;
        continue outer;
      }
    }
    for (const d of lang.strings ?? []) {
      if (line.startsWith(d, i) && (!lang.quoteAfter || lang.quoteAfter.test(line.slice(Math.max(0, i - 24), i)))) {
        const end = findEnd(line, i + d.length, d, !lang.raw?.includes(d));
        if (end < 0) {
          tokens.push({ from: i, to: n, kind: "string" });
          if (lang.multiline?.includes(d)) return { tokens, state: { kind: "string", end: d } };
          break outer;
        }
        let kind: TokenKind = "string";
        if (lang.json && /^\s*:/.test(line.slice(end, end + 40))) kind = "key";
        tokens.push({ from: i, to: end, kind });
        i = end;
        continue outer;
      }
    }
    if (/[0-9]/.test(c) || (c === "." && /[0-9]/.test(line[i + 1] ?? ""))) {
      if (i === 0 || !IDENT.test(line[i - 1])) {
        NUMBER.lastIndex = i;
        const m = NUMBER.exec(line);
        if (m && m[0]) {
          tokens.push({ from: i, to: i + m[0].length, kind: "number" });
          i += m[0].length;
          continue;
        }
      }
    }
    if (/[A-Za-z_$~]/.test(c)) {
      WORD.lastIndex = i;
      const m = WORD.exec(line);
      const w = m?.[0] ?? c;
      if (lang.consts?.has(w) && (i === 0 || !IDENT.test(line[i - 1]))) tokens.push({ from: i, to: i + w.length, kind: "const" });
      i += w.length;
      continue;
    }
    i++;
  }
  return { tokens, state: null };
}

/** Where a string / comment that started before `from` ends (just past its closing delimiter), or -1. */
function findEnd(line: string, from: number, close: string, escapes: boolean): number {
  for (let j = from; j < line.length; j++) {
    if (escapes && line[j] === "\\") {
      j++;
      continue;
    }
    if (line.startsWith(close, j)) return j + close.length;
  }
  return -1;
}

/** The lexer's state at the start of each line, up to `upTo` (exclusive), extending `states` in place. */
export function extendStates(lines: string[], lang: Lang, states: LexState[], upTo: number) {
  if (!states.length) states.push(null);
  const end = Math.min(upTo, lines.length);
  for (let i = states.length - 1; i < end; i++) states[i + 1] = lexLine(lines[i], lang, states[i]).state;
}

// ── Markdown files ────────────────────────────────────────

/** A fence line's run of backticks or tildes (three or more): ```` opens a block only ```` or longer ends. */
function openingFence(t: string): string {
  return /^(`{3,}|~{3,})/.exec(t)![1];
}

/** Whether `t` ends the block `fence` opened (CommonMark): the same character, at least as many, nothing after. */
function closesFence(t: string, fence: string): boolean {
  const run = /^(`+|~+)/.exec(t)?.[1];
  return !!run && run[0] === fence[0] && run.length >= fence.length && !t.slice(run.length).trim();
}

/**
 * The HTML READMEs use (centered <p>, <img width=…>, image grids in <table>) as Markdown; other tags dropped, their
 * text kept. react-markdown shows raw HTML as text. Code fences are left alone. (iOS MarkdownHTML.simplify)
 */
export function simplifyHTML(md: string): string {
  if (!md.includes("<")) return md;
  const out: string[] = [];
  let fence: string | null = null;
  let table: string[] | null = null;
  for (const line of md.split("\n")) {
    const t = line.trim();
    if (fence) {
      if (closesFence(t, fence)) fence = null;
      out.push(line);
      continue;
    }
    if (t.startsWith("```") || t.startsWith("~~~")) {
      fence = openingFence(t);
      out.push(line);
      continue;
    }
    if (table) {
      table.push(line);
      if (/<\/table>/i.test(t)) {
        out.push(...htmlTable(table.join("\n")));
        table = null;
      }
      continue;
    }
    if (/^<table\b/i.test(t)) {
      if (/<\/table>/i.test(t)) out.push(...htmlTable(t));
      else table = [line];
      continue;
    }
    if (t.startsWith("<") && !t.startsWith("<http")) {
      const s = inlineHTML(t);
      if (s) out.push(s);
      continue;
    }
    out.push(line);
  }
  if (table) out.push(...table);
  return out.join("\n");
}

function htmlTable(html: string): string[] {
  const rows = captures(/<tr\b[^>]*>([\s\S]*?)<\/tr>/gi, html)
    .map((row) => captures(/<t[dh]\b[^>]*>([\s\S]*?)<\/t[dh]>/gi, row).map((c) => inlineHTML(c).replace(/\|/g, "\\|")))
    .filter((r) => r.length);
  if (!rows.length) return [];
  const width = Math.max(...rows.map((r) => r.length));
  const line = (cells: string[]) => "| " + [...cells, ...Array(width - cells.length).fill("")].join(" | ") + " |";
  return ["", line(rows[0]), line(Array(width).fill("---")), ...rows.slice(1).map(line), ""];
}

/** Images and links into Markdown, line breaks into spaces, other tags dropped, entities decoded. */
export function inlineHTML(html: string): string {
  let s = html.replace(/<img\b[^>]*>/gi, (tag) => {
    const src = attr("src", tag);
    return src ? `![${attr("alt", tag) ?? ""}](${src})` : "";
  });
  s = s.replace(/<a\b[^>]*>([\s\S]*?)<\/a>/gi, (tag, inner: string) => {
    const text = inner.replace(/<[^>]+>/g, "");
    const href = attr("href", tag);
    return href ? `[${text}](${href})` : text;
  });
  s = s.replace(/<br\s*\/?>/gi, " ").replace(/<[^>]+>/g, "");
  for (const [k, v] of [
    ["&nbsp;", " "],
    ["&lt;", "<"],
    ["&gt;", ">"],
    ["&quot;", '"'],
    ["&amp;", "&"],
  ])
    s = s.split(k).join(v);
  return s.split(/\s+/).filter(Boolean).join(" ");
}

function attr(name: string, tag: string): string | null {
  return new RegExp(`\\b${name}\\s*=\\s*["']([^"']*)["']`, "i").exec(tag)?.[1] ?? null;
}

function captures(re: RegExp, s: string): string[] {
  return [...s.matchAll(re)].map((m) => m[1]);
}

/**
 * A long Markdown file cut between top-level blocks into pieces of about `target` lines, so the page lays out only
 * what is on screen. Never inside a code fence or display math, before an indented line (a list item's continuation,
 * indented code) or a table's rows; link definitions go with every piece so references still resolve. (iOS MarkdownChunks)
 */
export function markdownChunks(text: string, target = 40): string[] {
  const lines = text.split("\n");
  if (lines.length <= target) return [text];
  const defs = lines.filter((l) => /^ {0,3}\[[^\]]+\]:\s*\S/.test(l));
  const out: string[] = [];
  let cur: string[] = [];
  let fence: string | null = null;
  // The delimiter that ends an open display formula ($$ … $$ or \[ … \]), which may hold blank lines.
  let math: string | null = null;
  lines.forEach((line, i) => {
    const t = line.trim();
    if (fence) {
      if (closesFence(t, fence)) fence = null;
    } else if (math) {
      if (t.includes(math)) math = null;
    } else if (t.startsWith("```") || t.startsWith("~~~")) fence = openingFence(t);
    else if ((t.match(/(?<!\\)\$\$/g) ?? []).length % 2) math = "$$";
    else if (/(?<!\\)\\\[/.test(t) && !t.slice(t.lastIndexOf("\\[")).includes("\\]")) math = "\\]";
    cur.push(line);
    if (fence || math || cur.length < target || t || i + 1 >= lines.length) return;
    const next = lines[i + 1];
    if (next.startsWith(" ") || next.startsWith("\t") || !next.trim()) return;
    out.push(cur.join("\n"));
    cur = [];
  });
  if (cur.length) out.push(cur.join("\n"));
  if (!defs.length || out.length < 2) return out;
  const tail = "\n\n" + defs.join("\n");
  return out.map((c) => c + tail);
}

// ── tool steps ────────────────────────────────────────────

/** The file a Read / Edit / Write / MultiEdit / NotebookEdit step works on (Codex's file changes come as Edit), and the line Read starts at. */
export function toolFile(name: string | undefined, input: unknown): { path: string; line?: number } | null {
  if (!name || !/^(Read|Edit|Write|MultiEdit|NotebookEdit)$/.test(name)) return null;
  const inp = (input ?? {}) as Record<string, unknown>;
  const p = [inp.file_path, inp.notebook_path, inp.path].find((v) => typeof v === "string" && v.trim()) as string | undefined;
  if (!p) return null;
  const off = Number(inp.offset);
  return { path: p.trim(), ...(name === "Read" && Number.isFinite(off) && off > 1 ? { line: Math.floor(off) } : {}) };
}
