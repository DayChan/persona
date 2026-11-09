# Lessons: collaboration and process: multiple Agents changing the repo at once, commits, migration numbers, scripts

Pitfalls, odd behavior and the trade-offs made at the time (formerly in `CLAUDE.md` ("Lessons"); index in `docs/lessons/README.md`). Put new lessons in the matching section; if none fits, start a new one.

- Privacy hooks must resolve the tracked default branch, not assume `origin/master` (#733). A new remote branch has an all-zero old SHA, so falling back to that SHA and skipping an unavailable range silently lets its first push through. Test real first pushes to an isolated bare repository, retain existing-ref and deletion controls, and refuse a new branch when no scan base is available.

- PR merges and progress comments must be checked serially (#2024): `gh pr merge` can fail on a new conflict; if a compound shell command does not stop, the "merged" comment after it still goes out. Exit when the merge command fails, and read the PR's `state` again before posting a completion comment; mark it done only on `MERGED`. `mergeable: UNKNOWN` is not confirmation that it can merge, nor proof of a conflict; read back a definite state before acting. On a false report, edit the original comment first, keeping the verified results and the unfinished items; never leave two contradicting progress comments.

- Migration inventories must distinguish actual extension packages from SDK templates, schema examples and duplicate application worktrees (#5956). Deduplicate real package IDs across repository copies before counting runtime kinds; a manifest with a command under `runtime` is executable even when `runtime.mode` is omitted. Finding and classifying authoring fixtures is not evidence of a third marketplace or additional migrated extensions.

- Resuming a workflow-owned agent with `SendMessage` before the runtime registered it created a second writer in the same worktree (#5956). Use only workflow pipeline stages to resume workflow-owned work. If writers collide, stop both, preserve all files in a full backup, then designate one recovery writer before changing anything.

- Synthetic Git tests must remove inherited repository-locator variables from each child process environment (#1161): `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, `GIT_COMMON_DIR`, `GIT_OBJECT_DIRECTORY` and `GIT_ALTERNATE_OBJECT_DIRECTORIES` can redirect a command even when it uses `git -C` with a temporary fixture. Use independent test Git metadata, not the reviewed worktree's refs or index. If a test accidentally writes to that worktree, verify its branch, HEAD, index and protected files after restoring only the test's changes; report the write rather than claiming an entirely read-only review. This occurred during the Sentry source review: a local test commit changed refs/index; those changes were restored and protected source/diff hashes remained unchanged. No push occurred. Never prune the shared repository to hide test objects.

- Multiple agents working in this repo at once: each uses its own simulator (`SIM=<udid> scripts/app/sim.sh`); when someone else's unfinished files do not compile, build your own changes in a `git worktree`;
  commits stage only your own files / hunks.
- mock hub ports collide too (2026-10-31: another lane's `mock_hub.py` held 18794, your own mock with `MOCK_DRAWER=1` could not start, the tests connected to someone else's mock
  and reported "chat not loaded"): before starting a mock, check with `lsof -nP -iTCP:<port> -sTCP:LISTEN`; on failure read the mock's log, not just the test error.
  Stop your own mock by port (`kill $(lsof -t -iTCP:<port> -sTCP:LISTEN)`), not `pkill -f mock_hub.py`: it also kills mocks other lanes are using (hit on 2026-10-31).
  A remote `pkill -f <path>` over ssh also matches the `bash -c '…'` ssh itself started (its command line contains the same path), killing this ssh along with the commands after it: stop by the recorded pid / process group.
- Scripts must run on macOS's bundled Python 3.9 (2026-11-03, #579): `def f(x: str | None = None)` written on a newer Python raises `TypeError: unsupported operand type(s) for |` on import under 3.9, so TestFlight could not be released from the Mac mini, while all tests passed on the newer Python. If annotations use `X | None`, add `from __future__ import annotations` at the top of the file; it cannot rescue uses outside annotations like `isinstance(x, int | str)` or `A = int | None`; use a tuple / `Optional` there. On 3.9, `datetime.fromisoformat` accepts only 3 or 6 fractional-second digits, while RFC 3339 times written by Go have 0–9; pad to 6 digits before parsing (`ts` in `redact_transcript.py`). `scripts/test/test_script_layout.py` scans source for `X | Y` in annotations and syntax newer than 3.9; it cannot catch `|` outside annotations.
- Worktrees for deploying the site / releasing must be created on a freshly fetched `origin/main` (2026-11-04): `git worktree add --detach … origin/main` uses the locally recorded `origin/main`,
  which can be behind after a few minutes without a fetch. That time the `SITE_ONLY=1 publish.sh` worktree was 7 minutes older than #1164 (Hong Kong entry, relay trusting the IP forwarded by the entry); `wrangler deploy` replaced the live
  `worker.mjs` wholesale with the old version, and for about 30 minutes the relay again rate-limited only by the entry's egress IP; the old `wrangler.jsonc` also tried to bind the site's apex domain, already handed to DNS, back to the Worker
  (Cloudflare rejected it with `externally managed DNS records`, publish exited 1, but the script had already uploaded). Run `git fetch` before creating the worktree, and check `git rev-parse HEAD origin/main`
  match before deploying; when publish errors, first check whether the live site already switched to this code, then decide how to fix it.
- Trimmed Go build metadata can omit linker flags, so a serving-binary verifier must read exact `vcs.revision` and clean-source settings instead of searching for `main.commit` in the flags (#1553). Keep the serving/built binary hash comparison. A hub deployment already publishes its web artifact; acknowledge that only with a matching commit and exact built/installed tree receipt, rather than repeat a stale deploy after main advances.
- Unattended releases need locks shared with manual entry points, not only a worker lock (#1477). Use the hub's existing promotion lock for deployments from every build computer, resolving its state root from the installed service rather than assuming a default directory; use a build-computer release lock through upload and TestFlight processing. A successful upload followed by a processing timeout must resume the same build number, not upload another binary. Hub notification acceptance is not a phone-display receipt; installation acceptance needs an observed notification.
- Two sessions releasing at once collide on the build number (2026-11-04): both started `release.sh` (without a build number) in the same minute and got the same time-based number `202611040002`;
  the one that finished uploading first went into TestFlight, the later upload was rejected by App Store Connect (`Redundant Binary Upload`, exit 70), and only one archive with that name was left in Organizer.
  Before releasing, check `pgrep -fl scripts/release/release.sh` for someone else releasing; to release, get a number with `asc_testflight.py next-build` and pass it explicitly to `release.sh <build number>`.
  To retry the rejected one with the same commit without changing the number: first check with `asc_testflight.py status` whether that number is already VALID and the same commit; if so, continue with `wait` / `external`.
- Multiple agents merging into `main` at once: on a merge or rebase conflict, do not overwrite others' changes with your own old copy of the whole file (on 2026-10-25 one rebase reverted the light-blue bubbles and the notification navigation fix entirely);
  resolve only the conflict hunks, and before pushing check `git diff origin/main...HEAD --stat` for files that are not yours. Before building for TestFlight, confirm `main` is the fixed version.
- Multiple agents changing the same repo in parallel (6 workers on the Lark workplace, see `docs/extensions/feishu.md`): each gets its own worktree + its own cloned simulator; new types get a domain prefix
  (`FeishuSearchXxx`…, otherwise names collide on `main` and the build fails); on merge or rebase conflicts, files outside your own directory always take the `origin/main` version; before pushing check
  that `git diff origin/main --stat` contains only your files (14fe469 once overwrote half the repo with an old copy; 941d067 restored it); when the main working tree has someone else's uncommitted changes,
  do your fix in `git worktree add --detach /tmp/x origin/main`.
- Database migration numbers are split into ranges per lane (00x1…); before picking a number, check `server/internal/db/migrations/` in every worktree and `schema_migrations` in the `persona_test` database:
  another lane's unmerged migration may already hold the number (2026-10-30, 0051 collided; switched to 0061).
- A migration already on the default branch is frozen (`scripts/ci/migration_freeze.py`, also from `scripts/check-fast.sh`): editing, deleting or renaming it fails. A comment-only or whitespace-only change passes. Repair an applied migration by adding a new one.
- On 2026-11-01 `ios/` was renamed to `apple/` (the iOS, watch and macOS Apps and shared code are in the same XcodeGen project): when rebasing an old branch / worktree,
  files newly created under `ios/` must be moved to `apple/` by hand (git follows the rename for changes to existing files).
- On 2026-10-28 the whole git history was rewritten once (removing intranet and personal information; the GitHub repo was deleted and recreated): all earlier commit hashes are invalid;
  old clones on other machines must be deleted and cloned again, and must not push, or the old history would be pushed back.
- Scripts that wait on background jobs / logs always have a timeout, and exit with a one-line verdict on success, failure and timeout (on 2026-10-29 an `until grep` that waited only for success idled for over five hours).
  Resident processes a script starts along the way (mock hub) must have the whole background group's output redirected: `( cd … && exec python3 … ) > log 2>&1 < /dev/null &`.
  Written as `(cd … && python3 … > log 2>&1 &)`, the background shell layer still holds the script's stdout, so `script | tail` never sees the end,
  and the session waiting on it is never woken even long after the script finished (2026-11-01, stalled for almost 6 hours).
- macOS sed does not understand `\b`; for whole-word replacement use `perl -pi -e 's/\bFoo\b/Bar/g'`.
- When the temp disk is too small for Go builds, point `GOTMPDIR` at a **short physical directory outside the repo**, and give `GOCACHE` a cache of this lane's own (#2469). `go test` passes that temp directory to tests: putting it in the worktree makes fixtures that are "not in a repo" look like a real repo to Git, which may even try a real fetch/push; a path that is too long makes Unix sockets fail with `bind: invalid argument`. Do not hide a long directory behind a short symlink: Git's canonical root and the fixture's logical path diverge, and directory-rejection tests may change their result. Keep the failure record, rerun in a suitable directory, and do not delete the shared cache or raise test baselines.
- Server tests used to all connect to the same `persona_test` database: several lanes and deploys running at once wiped each other, migrated each other's tables, and tests randomly failed on empty or someone else's data (#545, #556, #583).
  Now test packages that use the database call `dbtest.Main` (`server/internal/db/dbtest`) in `TestMain`: each test binary creates its own
  `persona_test_<time>_<random>` next to `PERSONA_TEST_DATABASE_URL` and drops it afterwards (ones left behind by a killed run are cleaned by the next run after 6 hours); subprocesses started in tests (fake node) reuse the parent's database.
  New test packages that use the database must add `TestMain` the same way, not connect to the shared database directly. When the role lacks permission to create databases, it falls back to locking and queuing on the shared database.
- After adding a large dependency (#167's tailscale / gvisor, one zip is 21 MB), the first `go test` on the hub machine: that machine's outbound network is slow, `go` hung on the download for over twenty minutes,
  and finally reported `unexpected EOF` (2026-11-01). Fix: from `server/go.sum`, list the files missing from the hub's `~/go/pkg/mod/cache/download`, download them on the dev machine from
  proxy.golang.org (.zip / .mod / .info), rsync them to a directory on the hub, and test with `GOPROXY=file://<that directory>,https://proxy.golang.org`.

- `gh pr edit` may also query org / discussion fields when updating the body, so a token with only the `repo` scope reports missing `read:org` (2026-11-02).
  Updating the body does not need more permissions: pass `{"body": "…"}` as JSON on stdin to `gh api --method PATCH repos/<owner>/<repo>/pulls/<number> --input -`, which only touches the repo's PR endpoint.

- Privacy scans in a separate worktree locate the root from the checkout the script belongs to (#336), regardless of the caller's current directory. The old version used the caller's `git rev-parse --show-toplevel`: calling `/tmp/<worktree>/scripts/privacy_check.py` from the main working tree scanned the main working tree and also read its `CLAUDE.local.md` (2026-11-02, #341). When the two give different results, first confirm the script version and the scan root; do not skip or loosen the rules.
- Static web help text, form placeholders, mocks and example machine IDs in tests must also avoid personal config values (2026-11-03, #627): even a common machine-type word, if it matches the local config, makes the repo-wide privacy scan block the release. Use explicit example identifiers for IDs, example.com for domains, describe machine types in prose; do not change the sensitive-word list or add exemptions for the page.
- Older versions of `gh pr checks` do not support `--fail-fast` (2026-11-02, #341): check `--help` first and use the supported `--watch` / `--interval`; exit code 1 from a bad argument is not a CI failure; look at the specific check's conclusion.

## Source migration acceptance and dependency provenance

- Cold full-view HTTP source tests must use the real manager lifecycle (#1161, #1184). An SDK fixture that waits until loading finishes before taking a snapshot can miss suspension canceling the request after every HTTP response. Test cold mount, resume and actions through the actual bridge, preserve loading/error/retry evidence, and require the intended contribution to finish; HTTP 200 alone is not acceptance.
- Generate public npm lockfiles with an explicit public registry and isolated npm configuration, then inspect every `resolved` download origin (#1161). Public package names and a clean private-term scan do not prove that all dependency URLs are public: the terms dictionary may not include an inherited registry host. Check provenance separately, without publishing blocked host names or weakening privacy rules.

## Portable JavaScript test output

Node 24 removed `--experimental-default-type=module`; a portable SDK driver that works on Node 22 can therefore fail before running any assertions (#1163). Give freshly compiled temporary `.js` output its own `package.json` with `"type": "module"` instead of depending on an experimental CLI flag or on the caller's package scope. Run both the UI and transport suites on the supported Node versions, retain the temporary-directory cleanup, and fix the official SDK before resynchronizing downstream copies. Changes under `sdk/ts`, including its test scripts, also require regenerating the embedded `sdk/go/bundle/sdk.tar.gz` with `python3 scripts/gen/sdk_bundle.py`; the portable suites alone do not exercise that distribution check.

## Guard SDK synchronization cleanup at the deletion site

SDK synchronization intentionally replaces only the selected SDK subtree (#1471). Keep the destination guard on each recursive deletion (`"${dest:?}/sdk/go"` and `"${dest:?}/sdk/ts"`), even when the entrypoint already rejects an empty destination. Verify that selective synchronization preserves both the unselected SDK and unrelated destination files, and records the source checkout's commit in `sdk/VERSION`. This makes cleanup ownership explicit without changing the official copy contents or release versions.

## Hosted CI path aggregation

- Native workflow-level PR path filters leave individually required checks pending when no run is created. Keep one always-triggered
  orchestrator, put filters in a tested selector, call platform workflows through `workflow_call`, and require only its final aggregate.
  The aggregate must verify that every selected workflow succeeded; accepting all `skipped` results can hide an upstream guard failure.
  Use `--no-renames` when selecting paths so moving a file away from a platform still tests its former consumer. macOS costs require
  path-selection tests proving docs-only edits cannot launch it. A migration is not verified until its PR actually runs on hosted runners.
- Isolating Python suite HOME must unset inherited XDG profile variables, not set them to the outer temporary HOME: fixtures replace HOME
  again and expect XDG defaults to follow it. Otherwise privacy fixtures read the wrong config and service installers write to another
  fixture's directory. Reuse the server test environment cleaner, retain only Go build caches, disable VCS stamping for synthetic Go
  helpers, and allocate free mesh-mock ports instead of colliding with another developer's running mock.

## Screenshots and screen recordings in PRs

- No third-party image hosts, nothing in the repo: since `gh` 2.99, `gh pr create`, `gh pr comment`, `gh issue comment` and others have `--attach` (repeatable, at most 50 at a time),
  which uploads images and videos to GitHub's user-attachments. Write the body with `--body-file x.md` and reference files in it as `![caption](./a.png)`, with paths relative to the body file's directory.
- Only Markdown `![…](./a.png)` is replaced with the uploaded URL; HTML `<img src="./a.png" width=…>` is left as is (2026-11-04, #1691):
  the table was all broken images and the attachments were appended to the end of the comment. To control the size, post Markdown first, then fill the URLs into `<img>` with `gh api -X PATCH repos/<repo>/issues/comments/<id> -F body=@…`.
- The repo is private, so attachments are visible only to members: `curl` without login gives 404, with `gh auth token` gives 200; the upload is not broken.
- Screenshots use only made-up data from the mock hub / demo mode; `scripts/privacy_check.py` cannot read text inside images, so look them over yourself before posting.

## Documentation moves and media cleanup

- Moving root-level guides into module folders must also update documentation discovery and index tests: a `docs/*.md` glob silently stops checking the moved guides. Test discovery with a nested module fixture, rewrite both repository-root and document-relative references, and audit consumers across code, scripts, CI, module READMEs and HTML before deleting assets. A basename match is a lead, not proof of a consumer: verify its resolved directory and distinguish capture outputs from build inputs. Also inspect path constructors that split the directory and filename into separate strings; replacing only literal `docs/...` text misses these consumers. For a wide migration, read staged/commit blobs in one bounded Git batch rather than starting `git show` for every file: slow guard scans let the base branch advance into another conflict before merging. Preserve exact snapshot bytes, special-filename handling and fail-closed parsing; do not relax privacy rules. Keep verification media out of git; only required README, app and website assets remain.

## Temporary test hubs

- Historical note (2026-11-03, #277; superseded by the no-compatibility cutover on 2026-11-06): docs call the product Persona, but historical paths, signing profiles, databases / data volumes, protocol fields, bundle IDs, cache keys and source evidence must not be blindly replaced. When docs show `personad` / `~/.persona`, state that the old `personad` / `~/.persona` remain compatible; describe env var precedence only for new entry points that are already implemented; a standalone wording PR is not proof that the runtime migration is done.

- To verify on a real machine without touching the production hub: on the hub machine, start a copy with a different HOME (`HOME=/tmp/x personad hub`, with another listen port in hub.json
  and a separate database); the `kind: local` machine is a node started from the same binary, and the different HOME keeps it away from the production node's socket and config. Drop the database and delete the directory afterwards.
- Before `ssh -L`, check whether the local port is taken: another lane may already have a tunnel to the production hub on the same port, and requests made before `ExitOnForwardFailure` errors out go to the production hub.
  After connecting, first check that `version` in `/api/v1/health` is your own copy.
- A remote `pkill -f <pattern>` matches the ssh command running it (its command line contains the pattern too), killing your own session so the cleanup after it never runs: get the pid with `pgrep` and kill that, or write it in a form that does not appear on the command line.
- A background process started remotely with `nohup … &` keeps ssh from returning (it inherits the session), so the tool call hangs until timeout: besides redirecting output, add `setsid`, or check on it with a separate ssh afterwards.
- Before committing, confirm with `git var GIT_AUTHOR_IDENT` that the author is the GitHub identity (e.g. the noreply email, set at repo level with `git config --local`): if the global git identity is your real name and company email,
  it stays in the PR's commit history (`refs/pull/<n>/head`, which cannot be changed or deleted after merge; only GitHub support can purge it),
  and on squash merge GitHub writes an author different from the merger into the commit message on `main` as `Co-authored-by` (2026-11-02). Worktrees share the repo-level config.
- `gh pr merge <number> --squash --delete-branch` also deletes the local branch; when that branch is checked out in some worktree, gh 2.99 **deletes the whole worktree directory too** (2026-11-03: stacked PRs rebased in a temporary worktree under `/tmp`, and the directory was gone after the merge).
- **Do not use `git stash` in a worktree to "back out and compare"**: the stash list is shared by the whole repo; when `git stash` has nothing to save (e.g. only new untracked files) it does nothing, and the following `git stash pop` pops the entry **someone else** stashed in another worktree into your directory (2026-11-04: the conflicts and untracked files of a safety snapshot all landed in a freshly opened worktree; because pop conflicted, git kept that stash, so it was not lost). To compare against `main`'s behavior, open another `git worktree add --detach` or use `git show origin/main:<file>`; if you really popped the wrong one, `git reset --hard` only in your own worktree and delete the extra untracked directories; do not touch `git stash list`.
  It also happens when your stash did save something (2026-11-06): two agents stashed in two worktrees seconds apart and each `pop` took the other's entry; both applied cleanly, so `git stash list` was empty and nothing showed the swap. When `git status` lists files you never touched, find the worktree that has yours (`git worktree list` + `git -C <dir> status --short`), move each diff back with `git diff -- <paths> > x.patch` and `git apply x.patch` in its owner's worktree, then `git checkout -- <paths>` where it does not belong, and tell the other agent.
  Before merging, move that worktree off the branch (`git checkout --detach`), or merge from a worktree that does not have the PR's branch checked out; anything uncommitted in it is lost along with it.
- `gh pr merge --delete-branch` can also exit with a failure after GitHub has already merged, when switching back to `main` locally fails because `main` is checked out in another worktree (2026-11-03, #277). Check the PR's `merged` / `merge_commit_sha` first; do not mistake a local cleanup error for a failed merge and retry; run the merge from the main working tree, or handle local branch cleanup separately.

## Merging a long-lived integration branch into `main`

- One big merge (#606, the integration branch was over a hundred commits behind master) was split into PRs by module: resolve the conflicts and `git add` in the merge worktree, export a patch per path with
  `git diff --cached --binary MERGE_HEAD -- <path>`, then open a new worktree on the **latest** `origin/main` and `git apply -3 --index`. `main` keeps moving during the merge,
  and resolving conflicts may also pull in newer `main` code along the way (referencing functions not yet in the merge base), so every module is built and tested on the latest `main`, not in the merge worktree.
- One side refactored a shared lookup while new code on the other side still used the old way: the integration branch replaced `h.byID[…]` / `range h.machines` with `localMachine` / `machinesNow`
  (which include computers handed over by member hubs and computers newly added in the admin UI), while the to-dos, connectors, Lark bot and self-update newly added on master still read `h.byID` directly. grep for it after merging.
- Each side's tests carry assumptions the other side does not know: the source 192.0.2.1 of `httptest.NewRequest` is a public address, and the integration branch's entry point blocks plaintext and the main token (403),
  so master's tests must be changed to come from loopback; the integration branch's errors all carry a `code`; a new pairing pushes "New device paired" to existing devices. Do not change the implementation to make tests pass; first work out whose assumption it is.
- Things two modules share go with the tests that read them: `spec/fixtures` is the server tests' golden data, so it merges with the server module, not the Android module.

## Rootless Docker CI fixtures

Keep Compose data in a run-specific temporary directory and always remove its
containers and synthetic data, including after a failed smoke. A persistent
executor does not get a fresh host after each job. Rootless Docker maps the
container's nonzero PUID into a subordinate host UID, so the smoke verifies the
mapped data-directory owner and checks the JSON owner and mode inside the
container. Keep the existing direct-host checks for rootful executors. Never
set PUID to zero or weaken the JSON mode check to make a runner pass.

Cache upload is an optimization, not a smoke-test prerequisite. Tolerate cache
upload errors (`ignore-error=true`) so a broken cache service cannot fail the
smoke. Build, image-load, and smoke failures remain fatal; do not use
step-level continue-on-error for this.

`timeout=` on a `type=gha` cache export does not bound a slow upload: with
`timeout=2m` a PR's smoke still spent 816 s of a 1037 s Build step in
"sending cache export" (#2674). Pull requests therefore only read the cache
(`cache-from`) and never write it; non-PR runs keep `mode=max`. Why not just
`mode=min` on PRs, measured locally with the docker-container driver and a
`type=local` cache (amd64, same Dockerfile):

| | mode=max | mode=min |
| --- | --- | --- |
| cache size | 1.6 GB, 39 blobs | 116 MB, 11 blobs |
| preparing the export | 165 s | 0 s |
| rebuild, nothing changed | 11 s | 16 s |
| rebuild after a one-line `server/` change | 242 s | 735 s (cold builds here took 615–792 s) |

`mode=min` keeps only the final stage's layers, so the `web` / `build` stages
(`npm ci`, `go mod download`, compiling) start over for any source change. A
PR's cache is also only readable by that PR, and the repository was already
over the 10 GB Actions cache limit (282 entries, all from PR refs), so PR
exports mostly evicted each other. A cache every PR can read comes only from a
run on `main`, and nothing starts one automatically: tag pushes write to the
tag's ref, which PRs cannot read. Without a recent `workflow_dispatch` run on
`main`, PR builds are cold; dispatch one on `main` after a large dependency or
Dockerfile change if PR build time matters. Re-runs of the same PR also no
longer reuse that PR's previous build.

Capture short `docker compose exec` output before checking it with `grep -q`.
An early-closing grep can make Docker's output forwarding fail with exit 255
under pipefail even when the expected text was printed. Command substitution
keeps the command's own failure fatal and consumes its complete output before
applying the same content assertion; so does a grep without `-q` that reads all
of it (`holds` in `scripts/ci/image-smoke.sh`, where `hub pair` writes a QR code
after the link and failed two runs in three).

- In zsh, a glob that matches no files (`rm -rf /tmp/x-*`) makes the whole command fail (nomatch), and nothing after `&&` runs (2026-11-04: the end-to-end test never ran, yet the result was recorded as a failure).
  To clean temp directories use `find /tmp -maxdepth 1 -name 'x-*' -exec rm -rf {} +` or `setopt nullglob` first; before recording a check result, confirm the command actually ran.

## Manual CI dispatch payloads

A `workflow_dispatch` event has no pull-request number or comparison base. Resolve
an explicit base first, otherwise use the PR/merge-group base when present and
`origin/main` for a manual run. A PR's base is the first parent of the merge
commit `actions/checkout` checks out, not the event's `pull_request.base.sha`:
that one stays where the PR was opened, so every commit that later lands on
`main` would count as the PR's (#1962: a PR failed the commit privacy check on a
`main` commit it did not contain). Keep full checkout history and resolve the base to
a commit before the privacy range check. An explicit lane override must reject
unknown names rather than silently skip tests. An `auto` dispatch on unchanged
`main` tests only the paths selected for an empty diff; use `lanes=all` for a full
run. Manual issue-link enforcement takes an explicit PR number, reads metadata
through the API, and runs its write-token job only from the default branch.

## CI wall-clock budgets and test shards

Queue time belongs in a PR budget; a green job shorter than 15 minutes does not
prove push-to-aggregate latency below 15 minutes. Hosted macOS queue time can
already exceed that budget, so keep its full test coverage manual when latency
cannot be bounded. Do not reinterpret a timeout or deliberately unselected lane
as a test pass.

Partition package/module/suite discovery, not a hand-maintained test allowlist.
Assert the union covers every input once and route new inputs to a fallback shard
(or fail closed on an unsupported declaration). Keep real-browser packages on a
separate runner and each Postgres/HOME/mock-port fixture independently owned.
Go's dependency-only primary cache key is immutable: it can keep restoring old
compiled inputs after source changes. Use exact-toolchain, shard, dependency and
source keys with validated Go build-cache fallbacks; still force uncached test
execution. Large cache uploads and repeated runner setup also count as latency.
Measure real test events before replacing sleeps: `testing/synctest` sleeps use
fake time and are not necessarily the slow tests.

A sharded Linux workflow can still miss the budget through repeated queue
boundaries. After the first optimization, a documentation-only PR executed for
about 4m11s but finished in 20m35s: guards, script shards and the aggregate each
waited for another runner. A low-load green run is not proof under shared load.
Keep automatic fast checks and their final result in one runner allocation;
leave full heavy lanes manual when their queue-inclusive latency is not bounded.
Record the failed samples as counterexamples and re-run fresh post-merge PRs.
Do not cancel other work or change repository rules to manufacture a fast run.

Bound manual matrix fan-out as well as automatic PR fan-out. A stale manual
main/lane dispatch can consume capacity long after its source is obsolete.
Group by workflow-file identity, qualified branch and lane, with cancellation
of older work in the same group. The repository qualifier prevents unrelated
forks with the same branch name cancelling each other. A reusable workflow's
`github.workflow` is the caller's name: distinct file prefixes keep Apple or
another lane from accidentally cancelling the caller, while allowing its direct
and reusable invocations to deduplicate the same branch/lane. Keep every shard,
set `max-parallel`, and measure normal-load PRs separately from peak queues.
