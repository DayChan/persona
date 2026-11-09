import { describe, expect, it } from "vitest";
import { gitMark, type GitFile, type GitStatus } from "./files";
import { gitMarkIndex, markFor } from "./git-marks";

const f = (path: string, index: string, worktree: string, orig_path?: string): GitFile => ({ path, index, worktree, added: 0, deleted: 0, ...(orig_path ? { orig_path } : {}) });

// Status order matters for folders: the first change inside one gives its letter.
const changes: GitFile[] = [
  f("Sources/Lib/new.ts", "A", "."),
  f("Sources/App/main.swift", "M", "M"), // staged and unstaged
  f("Sources/App/View.swift", ".", "M"),
  f("Sources/conflict.c", "U", "U"),
  f("Sources2/notes.md", "?", "?"), // a sibling whose name starts with "Sources"
  f("New/name.ts", "R", ".", "Old/name.ts"),
  f("gone/x.txt", "D", "."),
  f("build/", "?", "?"), // an untracked folder Git reports as one entry
  f("README.md", ".", "M"),
  f("dup.txt", "M", "."),
  f("dup.txt", ".", "D"), // a second entry for the same path: the first one wins, as in gitMark
  f("my dir/a b.txt", "?", "?"),
  f("a/b/c/d/e.txt", ".", "T"),
];

/** Every path the tree could ask about: each change, each of its folders, near misses, and the root edge cases. */
function queries(root: string): string[] {
  const r = root.replace(/\/+$/, "");
  const rels = new Set<string>(["", "Old/name.ts", "Old", "New", "Missing", "Missing/x", "Sources/", "Source", "Sources/App/main", "build", "build/x"]);
  for (const c of changes) {
    rels.add(c.path);
    const parts = c.path.split("/");
    for (let i = 1; i < parts.length; i++) rels.add(parts.slice(0, i).join("/"));
  }
  const out = [...rels].map((rel) => (rel ? `${r}/${rel}` : r || "/"));
  out.push(`${r}/`, `${r}2/Sources`, "/elsewhere/Sources", "");
  return out;
}

describe("git-marks", () => {
  const repo: GitStatus = { is_repo: true, root: "/u/w", files: changes, commits: [] };
  const statuses: [string, GitStatus | null | undefined][] = [
    ["repo", repo],
    ["root with a trailing slash", { ...repo, root: "/u/w/" }],
    ["repo at the file system root", { ...repo, root: "/" }],
    ["not a repo", { ...repo, is_repo: false }],
    ["no root", { ...repo, root: undefined }],
    ["no changes", { ...repo, files: [] }],
    ["null", null],
    ["undefined", undefined],
  ];

  it.each(statuses)("gives gitMark's letter for every path and folder (%s)", (_, st) => {
    const roots = st?.root ? [st.root] : ["/u/w"];
    let marked = 0;
    for (const root of roots)
      for (const full of queries(root))
        for (const dir of [false, true]) {
          const want = gitMark(st, full, dir);
          expect(markFor(st, full, dir), `${full} dir=${dir}`).toBe(want);
          if (want) marked++;
        }
    if (st?.is_repo && st.root && st.files.length) expect(marked).toBeGreaterThan(changes.length);
  });

  it("keeps gitMark's first-match letters", () => {
    expect(markFor(repo, "/u/w/Sources", true)).toBe("A");
    expect(markFor(repo, "/u/w/Sources/App", true)).toBe("M");
    expect(markFor(repo, "/u/w/Sources/conflict.c", false)).toBe("!");
    expect(markFor(repo, "/u/w/Sources2", true)).toBe("U");
    expect(markFor(repo, "/u/w/dup.txt", false)).toBe("M");
    expect(markFor(repo, "/u/w/build", true)).toBe("U");
    expect(markFor(repo, "/u/w/build", false)).toBeNull();
    expect(markFor(repo, "/u/w/Old/name.ts", false)).toBeNull();
    expect(markFor(repo, "/u/w/a/b/c", true)).toBe("T");
    expect(markFor(repo, "/u/w", true)).toBeNull();
  });

  it("reads each changed file once per status instead of once per row", () => {
    const G = 2000;
    const R = 1860;
    let reads = 0;
    const counted = (xs: GitFile[]) =>
      new Proxy(xs, {
        get(target, key, receiver) {
          if (typeof key === "string" && /^\d+$/.test(key)) reads++;
          return Reflect.get(target, key, receiver);
        },
      });
    const files = Array.from({ length: G }, (_, i) => f(`changed/d${i % 40}/f${i}.ts`, ".", "M"));
    // rows the changes are not in: gitMark scans every file for each of them
    const rows = Array.from({ length: R }, (_, i) => ({ full: `/u/w/src/m${i % 60}${i % 3 === 0 ? "" : `/r${i}.ts`}`, dir: i % 3 === 0 }));

    const st: GitStatus = { is_repo: true, root: "/u/w", files: counted(files), commits: [] };
    // gitMark on a twentieth of the rows keeps the test quick; all of them would be R x G = 3,720,000 reads
    const sample = rows.filter((_, i) => i % 20 === 0);
    for (const r of sample) gitMark(st, r.full, r.dir);
    expect(reads).toBe(sample.length * G);

    reads = 0;
    for (const r of rows) markFor(st, r.full, r.dir);
    expect(reads).toBeLessThanOrEqual(G);

    reads = 0;
    for (const r of rows) markFor(st, r.full, r.dir);
    expect(markFor(st, "/u/w/changed/d7", true)).toBe("M");
    expect(reads).toBe(0);

    // a refetched status is a new object and gets its own index
    const next: GitStatus = { ...st, files: counted(files.slice(1)) };
    reads = 0;
    expect(markFor(next, "/u/w/changed/d0/f0.ts", false)).toBeNull();
    expect(reads).toBe(G - 1);
    expect(gitMarkIndex(next)).not.toBe(gitMarkIndex(st));
  });
});
