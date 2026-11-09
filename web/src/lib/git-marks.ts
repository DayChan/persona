// The Files tree's Git marks read from an index of the status instead of a scan per row. gitMark (lib/files.ts) looks
// through every changed file for each path it is asked about, so a tree of R rows over G changes costs R x G on every
// render; the index is built once per status object (one pass over its files) and each row is a Map lookup.
import { below, gitLetter, type GitStatus } from "./files";

export interface GitMarkIndex {
  /** The letter of each changed file, by its path below the repository root. */
  files: Map<string, string>;
  /** For each folder that holds a change, the letter of the first change inside it, in status order. */
  dirs: Map<string, string>;
}

// A status is never changed in place (a refetch is a new object), so its index lives as long as the object does.
const indexes = new WeakMap<GitStatus, GitMarkIndex>();

/** The index of one status, built on first use. The first entry wins everywhere, as gitMark's find() does. */
export function gitMarkIndex(status: GitStatus): GitMarkIndex {
  const known = indexes.get(status);
  if (known) return known;
  const files = new Map<string, string>();
  const dirs = new Map<string, string>();
  for (const f of status.files) {
    const p = f.path;
    const letter = gitLetter(f);
    if (!files.has(p)) files.set(p, letter);
    // every prefix that ends right before a "/" is a folder gitMark's startsWith(folder + "/") would match
    for (let i = p.indexOf("/"); i >= 0; i = p.indexOf("/", i + 1)) {
      const d = p.slice(0, i);
      if (!dirs.has(d)) dirs.set(d, letter);
    }
  }
  const index = { files, dirs };
  indexes.set(status, index);
  return index;
}

/** The same letter as gitMark(status, full, dir), read from the status's index; null when unchanged. */
export function markFor(status: GitStatus | null | undefined, full: string, dir: boolean): string | null {
  if (!status?.is_repo || !status.root || !status.files.length) return null;
  const rel = below(status.root, full);
  if (!rel || !rel.length) return null;
  const index = gitMarkIndex(status);
  return (dir ? index.dirs : index.files).get(rel.join("/")) ?? null;
}
