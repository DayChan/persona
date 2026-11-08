# Reviewing a session's changes

Review what an agent changed inside its session, the way you review a pull request, and send the review to the
agent as its next message (#151). The idea comes from Claude Desktop's in-session review: comments become the
agent's next message. Only the interaction is borrowed, not the code.

This page covers the node call, the hub route and the web panel. The iOS / Mac version is a follow-up (see the end).

## Scopes

A review compares the **working tree** with one of three bases, so it always shows the files as they are on disk now:

| Scope | Base | What it shows |
| --- | --- | --- |
| `uncommitted` | `HEAD` | staged and unstaged changes, and new files Git does not track yet |
| `branch` | the merge base of `HEAD` and the default branch (or the branch named in `base`) | the branch's commits, plus everything above |
| `commit` | the commit in `base` | everything since that commit, plus everything above |

The default branch is the one `origin/HEAD` points at. If there is none, it is the first of `origin/main`,
`origin/master`, `main`, `master` that exists. `branch` includes uncommitted work because an agent's changes are
often not committed yet, and those are what you want to review.

Before the first commit, `uncommitted` compares with the empty tree (`base.empty`), so every file is new. A detached
`HEAD` works like a branch with no name (`head.branch` is empty).

## Node: `git.review`

`{cwd, scope, base?, paths?}` → `GitReview` (`server/internal/proto/review.go`, `server/internal/node/gitreview.go`).

The call is read only. It runs `git diff` / `ls-files` / `check-attr` through `gitCmd`, which never prompts and takes
no optional locks, and it writes nothing (no index, no temporary files).

What it runs, from the repository root:

1. `git diff -M --raw -z <base>` and `--numstat` list the files Git knows and their line counts. Renames are paired
   the same way in every command.
2. `git ls-files --others --exclude-standard` lists the new files. A folder in that list is another repository inside
   this one; it is skipped.
3. Every path, both sides of a rename included, goes through the same guard as `file.read` (`guardRepoPath`). A key,
   a home's credentials folder, the data folder's secrets: the file is listed with `refused`, without hunks or line
   counts, and is left out of the patch by an exclude pathspec. Asking for it by `paths` gives the same answer.
4. `git check-attr linguist-generated` marks generated files (`generated`), which the panel folds, as GitHub does.
5. One `git diff -M -U3 <base>` patch is read section by section into each file's hunks. Explicit options make it
   independent of the user's config: `--no-color`, `--no-ext-diff`, `--no-textconv`, `a/` and `b/` prefixes. Each
   section is matched to the next file of the raw list by its `diff --git` line. If they disagree (a file changed
   between the two commands), the rest of the files come without hunks and are asked for one by one.
6. A new file becomes one hunk of added lines, which is what `git diff --no-index /dev/null <file>` would show. A NUL
   in the first 8000 bytes means binary, as Git decides; a link's content is where it points.

Hunks use the shape the web's diff review already draws (`lib/code-view.ts` `Hunk`): `old_start`, `new_start`, and
`lines` with their prefix (`" "`, `"+"`, `"-"`, `"\"`), plus `header`, the text after the second `@@`. A side with no
lines starts after the line the header names: `-4,0` becomes `old_start: 5`. This keeps the hidden-line counts
between hunks right.

`revision` is the new side's blob id from the patch's `index` line. It changes when the file changes, which resets a
"viewed" mark.

### How much one answer carries

| Limit | One page | A file asked for on its own (`paths`) |
| --- | --- | --- |
| hunks in total | 4 MB | 2 MB |
| one file's hunks | 256 KB or 3000 lines | 2 MB or 20,000 lines |
| files listed | 1000 (`truncated` beyond) | — |

- **A file over its share** comes with `big` and its line counts but no hunks.
- **Once the page is full**, the files after it also come as `big`. The node stops reading the patch there.
- **A `big` file** is asked for again with `scope: "commit"`, `base: <base.hash>` and `paths: [path]`, or
  `[old_path, path]` for a rename so the pair is still found. Pinning the base keeps it the same review even if the
  branch moved meanwhile.
- **A new file** over 32 MB isn't read to count its lines.

### Refusals

`rpc.Error` codes, passed on by the hub as 409 with `code`:

| Code | When |
| --- | --- |
| `not_git` | `cwd` is not in a Git repository |
| `bad_scope` | a scope other than the three, or `commit` without a `base` |
| `bad_base` | `base` is not a commit (or branch) of the repository; also a `base` starting with `-` or holding `:` or a space |
| `no_default_branch` | `branch` without a `base`, and none of the default branch names exists |
| `no_merge_base` | `HEAD` and that branch have no commit in common |

## Hub: `GET /api/v1/machines/{mid}/git/review`

`?cwd=&scope=&base=&path=…` (`path` repeatable) relays to `git.review` and returns its answer as is
(`server/internal/hub/gitreview.go`). It needs a full device token, like `git/diff`: an answer-only device (a watch)
has no `machines/{mid}/` routes. A node from before the call answers "unknown method", which the hub turns into 409
`node_outdated` with a message saying to update it. Hub feature `git_review`.

Tests: `server/internal/node/gitreview_test.go` runs against real repositories, covering each scope, renames, new
files, binaries, deletions, odd names (spaces, a tab, CJK), generated files, a conflict, a detached `HEAD`, no default
branch, unrelated histories, no commit yet, guarded files and files too big for a page.
`server/internal/hub/gitreview_test.go` covers the relay, the refusals and an old node.

## Web: the Review panel

`web/src/views/session/ReviewPanel.tsx`, logic in `web/src/lib/session-review.ts`, shown when the hub has `git_review`.

- **Opening it:** ⋯ › **Review changes** in a session's chat, or **Review** over the Git panel's change list (that one
  opens at Uncommitted). It is a session panel: it closes and comes back with the session, like Files and Git.
- **What it draws:** the extension code review (`views/extensions/DiffReview.tsx`, `lib/code-review.ts`), fed with
  `diff` items built from `git/review` instead of an extension's view: word diffs, Hide whitespace, viewed marks (kept
  in this browser by path, revision and hunks), generated and big files folded, keys (`n` / `p`, `j` / `k`, `v`, `c`,
  `?`, only while the focus is in the panel), ranges picked on the line numbers, suggestions.
- **Scopes:** a segmented control picks Uncommitted / Branch / Since a commit. Since a commit opens a menu of the 30
  latest commits (`git/log`). The choice is remembered per session, and the line beside it says what the files are
  compared with.
- **Lines between hunks:** the new side is the file on disk, so they are read with `file` once per file and review and
  cached. A removed file has none.
- **A big file** waits for **Load diff**. It is then asked for on its own with the base pinned (`scope=commit`,
  `base=<hash>`), and if it is too big even then, it says so.
- **What isn't shown:** a file the node refuses (a key) says it holds secrets; an empty file and a mode-only change say
  so instead of "no changes".
- **A comment on lines** offers **Start a review** / **Add to review** (a draft) or **Comment now** (straight to the
  message box as a code comment card, as the Git panel's line comments do).
  - Drafts are kept per session in `localStorage` (`persona.review:<mid>/<sid>`). They survive a reload, are drawn as
    threads at their lines with **Delete**, and carry the diff rows of their lines (removed rows between them
    included). A draft on old-side lines made against another scope shows as outdated, because those numbers belong
    to that diff.
- **Finish review** opens the summary.
  - **Send review** sends one message: every draft as a `Code comment N` block, in file then line order, then the
    summary. It goes through the session's composer (`ComposerHandle.send`, the same delivery as typing it). The
    review is cleared only once the send succeeds; a failed send keeps it, the summary too.
  - **Add to message box** puts the drafts above the message box as comment cards and the summary into it, to edit
    first.
  - **Discard drafts** asks first.
  - On a phone the panel closes after either, back to the chat.
- **When the agent changes files** (`fs_changed` for Git), a bar says so and offers **Refresh**: the diff doesn't move
  under the reader.
- **States:** reading; nothing changed (Uncommitted offers the branch, Branch offers Uncommitted); not a repository;
  an old node; no default branch and no merge base (both offer Uncommitted); a commit or branch that isn't there; any
  other failure with Retry. A chat that takes no messages shows the review read-only.

The comment block names its diff in the `Diff:` line (`lib/comments.ts` `DiffScope` kind `review`):

| Scope | `Diff:` |
| --- | --- |
| uncommitted | `uncommitted` |
| branch | `branch since <base short hash>` |
| commit | `since <base short hash>` |

A sent or edited message reads it back (`parseDiffLabel`), and the comment card opens the Review panel at its line.

Tests: `web/src/lib/session-review.test.ts` (the items, a comment's rows, drafts and their storage, the message and
reading it back); `scripts/test/web_review_e2e.py` against the mock hub's `MOCK_FILES=1 MOCK_REVIEW=1`
(`scripts/mock/mock_review.py`).

## iOS / Mac (follow-up)

The same review on Apple (UI version 5's code review there is #3280): a panel over `git/review` and the same message
format. Left to the msg-render lane; written up in #151.
