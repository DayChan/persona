// Runs the github-script of .github/workflows/pr-needs-issue.yml against sample PR descriptions and flows,
// with a fake GitHub client. Usage: node scripts/test/pr_needs_issue_test.mjs (exit 1 on any failure).
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const workflow = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '.github', 'workflows', 'pr-needs-issue.yml');

// The `script: |` block scalar, dedented.
function extractScript(yaml) {
  const lines = yaml.split('\n');
  const start = lines.findIndex((l) => /^\s*script: \|\s*$/.test(l));
  if (start < 0) throw new Error('no `script: |` in the workflow');
  const keyIndent = lines[start].search(/\S/);
  const body = [];
  for (const line of lines.slice(start + 1)) {
    if (line.trim() !== '' && line.search(/\S/) <= keyIndent) break;
    body.push(line);
  }
  const indent = Math.min(...body.filter((l) => l.trim()).map((l) => l.search(/\S/)));
  return body.map((l) => l.slice(indent)).join('\n');
}

const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const script = new AsyncFunction('github', 'context', 'core', extractScript(readFileSync(workflow, 'utf8')));

const OWNER = 'acme';
const REPO = 'widgets';
const BOT = 'github-actions[bot]';
const MARKER = '<!-- pr-needs-issue -->';

// Runs the script once for a PR in `state`; returns what it did.
async function run(state) {
  const pr = { number: 7, state: 'open', merged: false, user: { login: 'someone', type: 'User' }, ...state.pr };
  const comments = (state.comments ?? []).map((c, i) => ({ id: 100 + i, ...c }));
  const did = [];
  const logs = [];
  const github = {
    paginate: async (fn, params) => (await fn(params)).data,
    rest: {
      pulls: {
        get: async () => ({ data: structuredClone(pr) }),
        update: async ({ state: to }) => {
          if (to === 'open' && state.reopenFails) throw new Error('head branch was deleted');
          pr.state = to;
          did.push(to === 'open' ? 'reopen' : 'close');
          return { data: {} };
        },
      },
      issues: {
        get: async () => ({ data: { closed_by: state.closedBy ? { login: state.closedBy } : null } }),
        listComments: async () => ({ data: comments }),
        createComment: async ({ body }) => {
          comments.push({ id: 100 + comments.length, user: { login: BOT }, body });
          did.push('comment');
          return { data: {} };
        },
        updateComment: async ({ comment_id }) => {
          if (!comments.some((c) => c.id === comment_id)) throw new Error(`no comment ${comment_id}`);
          did.push('edit-comment');
          return { data: {} };
        },
      },
    },
  };
  const context = {
    repo: { owner: OWNER, repo: REPO },
    serverUrl: 'https://github.com',
    payload: { inputs: { pr_number: String(pr.number) }, repository: { default_branch: 'main' } },
  };
  const core = { info: (m) => logs.push(m), warning: (m) => logs.push(`warning: ${m}`) };
  await script(github, context, core);
  const linked = logs.map((l) => l.match(/links (.+)$/)?.[1]).find(Boolean) ?? '-';
  return { did: did.join('+') || 'nothing', linked, logs, comments };
}

const fence = '```';
// [name, description, issues it should find (null = none)]
const bodies = [
  ['Fixes #12', 'Fixes #12', '#12'],
  ['lower case', 'fixes #12', '#12'],
  ['upper case', 'CLOSES #3', '#3'],
  ['colon after keyword', 'Resolved: #45', '#45'],
  ['Refs', 'Refs #7', '#7'],
  ['ref (singular)', 'Part of the rollout, ref #7.', '#7'],
  ['two keywords', 'This fixed #9 and closes #10.', '#9、#10'],
  ['in a list, after Chinese', '- 修复启动崩溃。Fixes #12，顺便清理。', '#12'],
  ['owner/repo#N of this repo', 'Fixes acme/widgets#12', '#12'],
  ['issue link', 'See https://github.com/acme/widgets/issues/12 for context', '#12'],
  ['issue link, mixed case', 'HTTPS://GitHub.com/Acme/Widgets/issues/12', '#12'],
  ['CRLF, code then a real one', `${fence}\r\nFixes #1\r\n${fence}\r\nCloses #2`, '#2'],
  ['fake in code, real outside', `${fence}sh\ngit commit -m "Fixes #1"\n${fence}\n\nRefs #5`, '#5'],
  ['empty description', '', null],
  ['no description (null)', null, null],
  ['keyword without number', 'Fix the crash on launch', null],
  ['bare #N, no keyword', 'Follow-up to #12', null],
  ['keyword inside a word', 'prefix #12, hotfix #13', null],
  ['no space before #', 'Fix#12', null],
  ['#N followed by letters', 'Fixes #12abc', null],
  ['other repo shorthand', 'Fixes other/repo#12', null],
  ['other repo link', 'https://github.com/other/repo/issues/12', null],
  ['PR link, not an issue', 'https://github.com/acme/widgets/pull/12', null],
  ['only in a ``` block', `${fence}\nFixes #1\n${fence}`, null],
  ['only in a ~~~ block', '~~~\nCloses #1\n~~~', null],
  ['unclosed fence', `Notes:\n${fence}\nFixes #1`, null],
  ['only in inline code', 'Write `Fixes #1` in the body', null],
  ['inline code with backticks', '``Fixes `#1` ``', null],
  ['only in an HTML comment (template)', '<!--\nFixes #1\n-->\nSummary only', null],
  ['backtick before a blank line', 'a `b\n\nFixes #3\n\nc` d', '#3'],
];

let failed = 0;
const rows = [];
for (const [name, body, want] of bodies) {
  const got = await run({ pr: { body } });
  const wantDid = want ? 'nothing' : 'comment+close';
  const ok = got.linked === (want ?? '-') && got.did === wantDid;
  if (!ok) failed++;
  rows.push([ok ? 'ok' : 'FAIL', name, JSON.stringify(body), got.linked, got.did]);
}

const ours = { user: { login: BOT }, body: `${MARKER}\n这个 PR 的描述里没有关联 issue…` };
// [name, state, expected actions]
const flows = [
  ['closed by the workflow, link added', { pr: { state: 'closed', body: 'Fixes #4' }, closedBy: BOT, comments: [ours] }, 'reopen+edit-comment'],
  ['closed by the workflow, edited without link', { pr: { state: 'closed', body: 'still nothing' }, closedBy: BOT, comments: [ours] }, 'nothing'],
  ['closed by a person, link added', { pr: { state: 'closed', body: 'Fixes #4' }, closedBy: 'someone', comments: [ours] }, 'nothing'],
  ['closed, no comment of ours', { pr: { state: 'closed', body: 'Fixes #4' }, closedBy: BOT }, 'nothing'],
  ['reopen fails (branch gone)', { pr: { state: 'closed', body: 'Fixes #4' }, closedBy: BOT, comments: [ours], reopenFails: true }, 'comment'],
  ['author reopens without link', { pr: { body: 'please' }, comments: [ours] }, 'comment+close'],
  ['link removed in an edit', { pr: { body: 'removed the reference' } }, 'comment+close'],
  ['bot account (dependabot)', { pr: { body: 'Bumps x from 1 to 2', user: { login: 'dependabot[bot]', type: 'Bot' } } }, 'nothing'],
  ['[bot] login with type User', { pr: { body: '', user: { login: 'renovate[bot]', type: 'User' } } }, 'nothing'],
  ['merged PR edited', { pr: { state: 'closed', merged: true, body: '' } }, 'nothing'],
  ['someone else\'s marker comment ignored', { pr: { state: 'closed', body: 'Fixes #4' }, closedBy: BOT, comments: [{ user: { login: 'someone' }, body: MARKER }] }, 'nothing'],
];
for (const [name, state, want] of flows) {
  const got = await run(state);
  const ok = got.did === want;
  if (!ok) failed++;
  rows.push([ok ? 'ok' : 'FAIL', `flow: ${name}`, '', got.linked, got.did]);
}

const widths = [4, 40, 52, 8, 20];
const fmt = (r) => r.map((c, i) => String(c).slice(0, widths[i]).padEnd(widths[i])).join(' | ').trimEnd();
console.log(fmt(['', 'case', 'description', 'linked', 'action']));
console.log(widths.map((w) => '-'.repeat(w)).join('-|-'));
for (const r of rows) console.log(fmt(r));
console.log(`\n${rows.length - failed}/${rows.length} passed`);
process.exit(failed ? 1 : 0);
