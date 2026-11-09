# Working with other people and Agents: claim the issue before you start

Several people work in this repository, each alongside their own Agents. Two people have fixed the same bug, and someone has found halfway through a fix that it was already fixed. So:
**claim the issue before you start, and every PR must link an issue**. The overall flow (branching, English PRs, merging after local verification passes) is in `CLAUDE.md` ("Development workflow"); this page has the claiming details, comment templates and commands you can copy.

## Rules

1. **Check for duplicates before starting**: search for an open issue and open PR about the same thing, and glance at recently closed issues and merged PRs (don't redo finished work).
2. **Someone has claimed it and the claim hasn't expired**: don't work on it. You can add information, repro steps, logs or leads under the issue (start the comment with "Note:"). The same goes when there is already an open PR: add to the PR / issue.
3. **No issue? Create one**. When you start, post a claim comment on the issue: what you're doing, how you plan to change it, how long you expect it to take. If an Agent is doing the work, say so.
4. **24 hours with no update counts as abandoned**: someone else can post a comment taking it over (with the reason). If you're still working on it, post a progress comment to renew.
5. **Every PR must link an issue**: write `Fixes #N` / `Closes #N` / `Refs #N` in the description. PRs with no linked issue get a comment from the explicitly dispatched metadata Action and are closed by it.

Don't use GitHub's assign (many people don't have triage permission, and it isn't needed), don't use commands like `/claim`, and expiry isn't handled automatically: the comments in the issue are the only source of truth.

## Who is working on it: the comment and its GitHub account decide

- **The owner is the GitHub account that wrote the comment**. An Agent posts comments with the gh account of the person it belongs to (no separate bot account), and that person is responsible for the Agent's claim; the comment body doesn't need a name.
- When an Agent is doing the work, the claim comment gets an extra line `Agent: yes (<machine id> / <branch or session>)`: several Agents of the same person share one account and are told apart by this line.
  Use the machine id given to the machine in the config, not its hostname.
- **Only what's written in the issue counts**: mentioning it in a group chat, agreeing in a DM, the issue's assignee or labels are not claims.
- Comments are classified by the prefix on their first line, so people and Agents can filter: `Claim:`, `Progress:`, `Take over:`, `Abandon:`, `Note:`.

### What counts as a valid claim

- The current owner is the author of the latest "Claim:" or "Take over:" comment, provided that account hasn't posted "Abandon:" since.
- **Update**: any new comment by the owner on this issue, or a new push or comment on a linked PR (a PR whose description has `Fixes` / `Closes` / `Refs` for this issue).
- The claim is valid for 24 hours from the owner's **last update**; after 24 hours with no update, it counts as abandoned.
- Two people claim at almost the same time: the earlier one wins. After posting your claim, read the comments again; if yours wasn't first, delete it (`gh issue comment <number> --delete-last --yes`) and do something else.

## Comment templates

Claim (Agents copy it as is; if a person is doing it, drop the "Agent" line):

```text
Claim: <what you're doing, one sentence>
Plan: <how you'll change it, which places>
ETA: <how long, e.g. "2 hours", "today">
Agent: yes (<machine id> / <branch or session>)
```

Renew (still working; at least one within 24 hours; a push to a linked PR also counts as an update):

```text
Progress: <how far you've got, next step, new ETA>
```

Take over (the original owner has had no update for more than 24 hours):

```text
Take over: claimed <time>, last update <time>, more than 24 hours ago
Plan: <how you'll change it; if the original branch / PR has usable progress, say you'll continue from it>
ETA: <how long>
Agent: yes (<machine id> / <branch or session>)
```

Abandon (you can't finish or are switching to something else; say so, so others don't wait 24 hours):

```text
Abandon: <how far you got, what's left, where the branch / PR is>
```

Note (someone else has claimed it and you have information for the owner):

```text
Note: <repro steps, logs, leads, relevant code locations>
```

## Commands (Agents can copy these directly)

Run them in the repository directory; `gh` uses the current repository automatically, and gh fills in `{owner}/{repo}` in `gh api` paths. All times are UTC.

### 1. Check for duplicates

```sh
# Open issues / open PRs about the same thing (searches titles, bodies and comments)
gh issue list --state open --search "<keywords>"
gh pr list --state open --search "<keywords>"
# Search issues and PRs at once
gh search issues "<keywords>" --repo "$(gh repo view --json nameWithOwner -q .nameWithOwner)" --state open --include-prs
# Recently closed issues, merged PRs, commits on main: don't redo finished work
gh issue list --state closed --search "<keywords>" --limit 10
gh pr list --state merged --search "<keywords>" --limit 10
git fetch origin && git log origin/main --oneline -i --grep "<keywords>" | head
```

If you find an issue that means the same thing (whoever created it, however the title is worded), claim it there instead of creating another; if someone else has claimed it, follow the "Take over" rules. Create a new one only if there really is none.

### 2. Check whether an issue is claimed and whether the claim has expired

```sh
gh issue view <number> --comments
# Only Claim / Progress / Take over / Abandon (time, account, first line)
gh issue view <number> --json comments --jq '.comments[] | select(.body | test("^(Claim|Progress|Take over|Abandon):")) | "\(.createdAt)  \(.author.login)  \(.body | split("\n")[0])"'
# PRs linked to this issue and when they were last updated
gh api 'repos/{owner}/{repo}/issues/<number>/timeline' --paginate --jq '.[] | select(.event == "cross-referenced" and .source.issue.pull_request != null) | .source.issue | "#\(.number)  \(.state)  \(.updated_at)  \(.user.login)  \(.title)"'
```

If the author of the latest "Claim:" / "Take over:" hasn't posted "Abandon:" since, and the owner's last comment or the linked PR's last update is within 24 hours: don't work on it; add a note instead.

### 3. Create an issue

```sh
gh issue create --title "<one sentence saying what to do>" --label bug --body-file - <<'EOF'
## Symptom / requirement
<what it is, why>

## Repro
<steps, logs>
EOF
```

Requirements get `enhancement`, fixes get `bug`, docs get `documentation`. The command prints the issue link at the end; the trailing number is the issue number.

When creating an issue on the web, choose the `Bug report` or `Feature request` form (`.github/ISSUE_TEMPLATE/`) to add the `bug` / `enhancement` label automatically. Field names and issue bodies are in English. You can also use a blank issue or the command above.

#### TestFlight feedback

Issues created from TestFlight feedback (crash reports in App Store Connect, testers' text / screenshot feedback) must be filterable by source:

- **Always add the `testflight` label** (together with `bug` / `enhancement`); if you don't have permission to add labels, prefix the title with `[TestFlight]`, and someone with permission adds the label when they see it.
- **Check for duplicates first**: one issue per piece of feedback. End the body with the dedup marker `<!-- testflight-intake:<sha256(feedback submission id)> -->`,
  and before creating, check with `gh search issues --repo <owner/repo> "testflight-intake:<hash>"` whether it already exists; if an issue for the same crash cause exists, add a "Note:" on it.
- **Redact**: include only the technical summary, a symbol-only call stack (addresses removed), App version, OS version and device model. Leave out the tester's email and identity,
  crash / install / device identifiers of any kind, Bundle ID, memory addresses and image UUIDs, container and personal paths, network / battery / storage, language and time zone, exact times,
  the specific build number and raw attachments; raw logs stay only in a 0600 file on your machine. End the body with a "Privacy handling" section saying what was removed.
  Examples: #752, #1144.

```sh
gh issue create --label bug --label testflight --title "<one sentence describing the symptom>" --body-file <redacted body>
```

### 4. Claim, renew, take over, abandon

```sh
gh issue comment <number> --body-file - <<'EOF'
Claim: <what you're doing>
Plan: <how you'll change it>
ETA: <how long>
Agent: yes (<machine id> / <branch or session>)
EOF

# Read again after posting to confirm the earliest valid claim is yours (if not, delete yours with --delete-last --yes)
gh issue view <number> --json comments --jq '.comments[] | select(.body | test("^(Claim|Take over|Abandon):")) | "\(.createdAt)  \(.author.login)  \(.body | split("\n")[0])"'

gh issue comment <number> --body "Progress: <how far you've got, next step, new ETA>"
gh issue comment <number> --body "Abandon: <how far you got, what's left, where the branch / PR is>"
```

Take over uses the template above, also posted with `--body-file -`.

### 5. Link the issue when opening a PR

```sh
# Check once more before opening: the issue's comments, and whether someone already fixed it on main
gh issue view <number> --comments
git fetch origin && git merge origin/main   # prefer merge to rebase, see below

gh pr create --title "fix(ios): <English title>" --body-file - <<'EOF'
<English: what changed and why>

Fixes #<number>
EOF
```

- Updating a PR branch with `main`: prefer `git merge origin/main` on the branch to a rebase. PRs are squash-merged, so the merge commit never reaches `main`;
  conflicts are resolved once instead of once per commit, and the push is a plain push with no force while other Agents work in the repository (#5988).
  In a merge "ours" is your branch and "theirs" is `main`, the reverse of a rebase: mind it with `git checkout --ours/--theirs` and `scripts/i18n/apple_l10n.py merge`.
  A rebase is still allowed, for example for a single-commit branch.
- Merging: once the PR's relevant local verification passes, `scripts/hooks/merge-pr.sh <number>`, not `gh pr merge` directly. It first scans the PR's changed files, commit messages, title and body with this machine's private values, and only squash-merges and deletes the branch if that passes (`docs/operations/testing.md`, "Privacy scan"). PRs do not automatically start hosted CI.
- `Fixes #N` / `Closes #N`: GitHub closes the issue automatically after merging. `Refs #N`: links without closing (an issue split into several PRs, or only partly fixed).
- PR titles and bodies are in English (after a squash merge they are the commit on `main`); issues and comments are also in English.

## Checking PRs with no linked issue

`.github/workflows/pr-needs-issue.yml` runs only when an operator explicitly dispatches it from the default branch with `pr_number=<number>`; opening, editing or reopening a PR starts no workflow:

- **Counts as linked** (case-insensitive): `fix` / `fixes` / `fixed` / `close` / `closes` / `closed` / `resolve` / `resolves` / `resolved` / `ref` / `refs`
  followed by `#number` (a colon after the keyword is allowed, e.g. `Fixes: #12`), or `owner/repo#number` for this repository; or a link to an issue in this repository (`https://github.com/<owner>/<repo>/issues/<number>`).
- **Doesn't count**: anything inside code blocks, inline code or HTML comments (the examples in the PR template); a bare `#12` with no keyword; issues in other repositories; PR links; links written only in the title,
  or linked by hand only in the "Development" sidebar of the PR page.
- No link: it leaves a comment explaining the rule, then closes the PR. **Once the author edits the description to add a link, the PR is reopened automatically** and the comment changes to "Reopened";
  if automatic reopening fails (for example the branch was deleted), it posts another comment, and after adding the link you reopen it by hand (`gh pr reopen <PR number>`).
- It only reopens PRs the Action itself closed; a PR closed by a person is not reopened even after adding a link. Editing an already closed PR that still has no link doesn't produce a duplicate comment.
- PRs from bot accounts (dependabot and the like) are exempt.
- Editing the description: `gh pr edit <PR number> --body-file <file>`, or edit it on the web.

### Maintenance

- Logic and flow tests: `node scripts/test/pr_needs_issue_test.mjs` (extracts the github-script script from the workflow and runs dozens of description samples and the close / reopen flow against a fake GitHub client);
  after changing it, also run `actionlint .github/workflows/pr-needs-issue.yml`.
- It uses a manually supplied PR number and the default branch's workflow with a token that has write permission, so it **never checks out or runs the PR's code**;
  the PR's title and description are read only as data via the API and never put into `${{ }}` (that would inject them into the script). Permissions are limited to `pull-requests: write` and `issues: write`.
- Also because it runs the default branch's version, a PR that changes this workflow doesn't use the new logic itself; it takes effect only after merging. Before merging, rely on the tests above.
