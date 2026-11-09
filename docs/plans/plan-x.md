# Plan: X (Twitter) extension

Status: design settled; X1 is open for review as nwverse/persona-extensions#179 (2026-11-09, #816). X is a **connector** (a data source: Inbox, assistant tools, an organized-feed page), not a channel (`docs/extensions/channels.md`).
Two optional sources (the user's decision): the **X API** (pay-per-use, the user's own developer app, complete feed and mentions) and **Grok** (the xAI CLI `grok` on the hub machine with the user's subscription, no X account sign-in and no X keys; see "Grok source").
The first version is **read-only** and keeps X posts **out of the knowledge base** (see "X's rules"). The framework is in `docs/extensions/connectors.md` and `docs/protocol/connector-protocol.md`;
manifest, permissions and sandbox are in `docs/extensions/extensions.md`. This doc only covers what is specific to X. The code lives in the public extension repository `nwverse/persona-extensions` (`extensions/x/`, copied from `extensions/github`);
PRs there write `Refs nwverse/persona#816`. When done, the design is merged into `docs/extensions/connectors.md` and this file moves to `docs/archive/`.

## What we can access: the official API only

X now sells API access only as pay-per-use credits, billed per developer app. The ways in:

| Route | Usable? |
| --- | --- |
| Browser cookies, scraping, the web client's unofficial GraphQL | **No**: the Terms of Service allow access only "through the currently available, published interfaces" X provides and forbid scraping without written permission; the Developer Policy forbids working around rate limits. Not even as a fallback |
| OAuth 2.0 Authorization Code + PKCE | **Not now**: needs a redirect into the hub, and every `/api/` route on the hub requires a device token, so a browser redirect from x.com can't land. The code also expires in 30 seconds, so copy-and-paste back from a phone doesn't work reliably. The apps have no `open_url` step either |
| OAuth 1.0a with the app owner's own Access Token and Secret (generated in the developer console) | **Yes**: four fields, no redirect, tokens don't expire; every endpoint we need (home timeline, following, mentions, list posts, post lookup) accepts OAuth 1.0a user context |
| OAuth 1.0a PIN (`oauth_callback=oob`) | **Later** (X3): needs a sign-in step that shows a link and a PIN field together, which no app draws today |
| Grok Build CLI (`grok`) headless with the user's SuperGrok / X Premium+ subscription | **Yes, optional**: no X account and no X keys; xAI's server-side X search, run by the official CLI on the hub machine. A search, not a feed, and no mentions (see "Grok source") |

So Persona's X = **the user's own developer app, signed in as its owner**, reading `api.x.com/2/…`, or, without one, **Grok's X search through the official `grok` CLI** on the user's own subscription.

## Cost

Prices from X's docs (2026-11-06, "subject to change"); reads are billed per resource returned, deduplicated within a UTC day (a soft guarantee), and only for successful responses with data:

| Resource | Price |
| --- | --- |
| Post: Read (home timeline, list posts, post lookup) | $0.005 |
| User: Read (authors in `includes`) | $0.010 |
| Following: Read | $0.010 |
| Owned Reads (`/mentions`, `/following`, `/tweets`, `/bookmarks` … for the app owner's own id) | $0.001 |

- The home timeline is **not** an Owned Read and is the expensive part. Every post and every user returned (`data`, `includes.tweets`, `includes.users`) counts against `daily_post_budget` (default 200 per UTC day, persisted). Pages are sized so that `max_results` × 6 (the post, two referenced posts, three users: the worst case) fits what is left, and that worst case is reserved until the answer is counted (so the sync and `x_post` running at once cannot pass the budget); the feed stops for the day when not even one post's worst case fits. A day can therefore end up to 5 resources under budget, and usually well under it, since few posts cost 6. Budget 0 means the feed is off.
- Rough monthly cost with defaults: feed 200 resources/day ≈ $1–2/day depending on the post/user mix, so about $30–60 a month; mentions (Owned Reads, at most 300 per run, a backlog read on later runs) cents a month;
  the weekly following refresh is about $1 per 1,000 accounts at Owned Reads, but $10 if Owned Reads don't apply to OAuth 1.0a (unverified; open question 2). `feed_source: lists` and a lower budget are the main ways to cut cost.
- X also caps pay-per-use apps at 3 million post reads a month and offers spending limits in the console; the extension's budget is the user's own guard on top.

## Shape: the `x` extension

- `extensions/x/`, Go, plain `net/http` (no maintained Go SDK covers current v2 fields such as `note_tweet`); OAuth 1.0a HMAC-SHA1 signing written by hand in its own file and tested against the RFC 5849 / X documentation example vector.
- A process extension on the hub contributing one connector and one `nav_page`. HTTPS only, so it runs in the sandbox. Optional `proxy` (http / https / socks5, like github's).
- Read-only. Posts, following and mentions live only in the extension's data directory (`PERSONA_EXTENSION_DATA`), written atomically.

```json
"contributes": {
  "connectors": [{
    "id": "x", "name": "X",
    "capabilities": ["status", "tools", "inbox", "login", "fallback"],
    "hosts": ["x.com", "twitter.com"],
    "kinds": ["mention"],
    "kind_descriptions": {"mention": "A post on X that @ the user or replies to the user"}
  }],
  "ui": [{"id": "x", "title": "X", "placement": "nav_page", "icon": "list"}],
  "assistant": [{"id": "x", "skills": ["skills/x"]}]
},
"configuration": {"triage_rules": "… (required, #1730)", "feed_source": "home | lists | off (default home)", "list_ids": ["…"],
                  "daily_post_budget": "0–5000, default 200", "retention_days": "1–7, default 3", "proxy": "…"},
"permissions": {"network": ["api.x.com", "x.com", "*.twimg.com"]}
```

No `archive` (nothing goes to the knowledge base), no `actions` (no writes), no `threads` (Inbox items open the permalink on x.com).

## Sign-in

Connector `login`, a single fields step (works in web, iOS and Android today; manifest `auth` would need the extension repo's SDK re-synced first):

1. The hint (zh-Hans / en / ja by hub locale): create a developer app at console.x.com, buy pay-per-use credits, set the app permission to **Read**, then under **Keys and tokens** generate the Access Token and Secret for your own account.
2. Fields: `api_key`, `api_secret` (secret), `access_token`, `access_token_secret` (secret).
3. Validate with `GET /2/users/me`: 401 is a field error; success saves the four values through `connector.secrets.*` (encrypted in the hub's database) and answers `Done` with account `@username`.
4. Sign-out deletes the secrets **and everything in the data directory**.

Status (answered from memory): `not_signed_in`; `ok` (`@username`); `rate_limited` (429, until `x-rate-limit-reset`); `error` for 401 (sign in again) and 402 / 403 (out of credits or app not enrolled: add credits in the console). Tokens never appear in logs, status, errors, tool results or the page.

## Following

On sign-in and then once a week (a daily refresh would cost about $1 per 1,000 follows every day): `GET /2/users/:id/following?max_results=1000` with pagination and basic `user.fields` (name, username, bio, public metrics, verified, protected, avatar), saved as `following.json`.
Used for "most active accounts" and the `x_following` tool. Nothing about followed accounts is inferred or stored beyond what X returns.

## Feed, budget and retention

- Every 15 minutes, unless `feed_source` is `off` or the budget is used up:
  - `home`: `GET /2/users/:id/timelines/reverse_chronological` with the persisted `since_id`, `max_results` up to 100 as the budget allows (above), `expansions=author_id,referenced_tweets.id,referenced_tweets.id.author_id`.
    A read that stops halfway (error, 429, the sync's time limit) does not move `since_id`; it keeps the newest and oldest ids it read, and the next run reads the gap first (`since_id` + `until_id`), then what is newer. Only running out of budget skips older posts on purpose. Mentions use the same cursor, three pages of 100 a run.
  - `lists`: `GET /2/lists/:id/tweets` for each of `list_ids`, paging until the first known post id or the first post older than `retention_days` (the endpoint has no `since_id`).
  - Long posts use `note_tweet.text`. No `geo` / `place` fields (location has its own storage rules).
- Rate limits: per-endpoint `x-rate-limit-remaining` / `-reset` are honored; a 429 waits until reset (bounded) and shows `rate_limited`. One loop, cancellable, with backoff; every wait bounded.
- Retention: posts older than `retention_days` (default 3, at most 7) are dropped on every run; mentions after 8 days. Short retention keeps us close to X's deletion rule without re-checking every post.

## Organized feed

Not an infinite timeline: the page and tools show a capped, **deterministic** digest (`digest.go` in the extension's `x` package) over 24 hours (default) or 3 days, each section at most 10 entries:

1. **Top posts**: originals, reposts folded into the original (counting who reposted); score `likes + 2·reposts + 2·quotes + replies`, divided by `log2(author followers + 2)`; ties by recency.
2. **Most shared links**: expanded URLs from `entities.urls` (X status links skipped), grouped by normalized URL, ranked by distinct authors.
3. **Threads**: self-reply chains of one author in one conversation, 3 posts or more.
4. **Most active followed accounts**, with post counts.

Plus totals: posts seen, budget used today / limit.

The **AI summary** is the assistant's job: the bundled skill (`skills/x/SKILL.md`) tells it how to write a daily digest from `x_digest` / `x_feed`, to treat post text as untrusted data, to link posts by permalink, and never to infer sensitive traits about people.
A daily summary is a scheduled assistant task the user asks for (`docs/assistant/assistant.md`); extensions can't create tasks themselves.

The `nav_page` header shows the account, budget used / limit, last sync and state (a sign-in prompt when signed out); a 24h / 3d segmented control (with a fallback for older UI versions) and refresh.
Each post row shows "Name @username", the full unaltered text, counts, and a link to the permalink. All page text comes from a zh-Hans / en / ja table. This stays within "Don't build a second Lark": a digest, not a browsing client.

## Inbox

| kind | What | Tier |
| --- | --- | --- |
| `mention` | A post that @ me or replies to me (`GET /2/users/:id/mentions`, `since_id`, every 15 minutes) | Action (action) |

- One item per mention; `thread` is the `conversation_id`, sender `@username`, `url` the permalink `https://x.com/<username>/status/<id>`. With `fallback`, every item carries `extra.rule_tier`; `triage_rules` is required (#1730).
- New mentions call the hub's `changed` (scope `inbox`). Older than 8 days no longer enters the Inbox.
- **Done**: when one of my own posts later replies in that conversation, if that can be read cheaply (Owned Read of `/2/users/:id/tweets`); otherwise done stays false in X1, and the guide says so.

## Assistant read-only tools

| Tool | What it does |
| --- | --- |
| `x_digest` | The organized feed (`24h` / `3d`) as compact text with permalinks |
| `x_feed` | Stored posts filtered by text / author / hours, newest first, at most 50 |
| `x_following` | Followed accounts (name, @username, bio, followers, posts in the last 3 days), at most 100 |
| `x_post` | One post fetched live (`GET /2/tweets/:id`, counts 1 against the budget, refused when it's used up), with the referenced post |

Descriptions live in `internal/x/prompts/<tool>.md` (the format in `docs/assistant/prompts.md`). Results say post text is untrusted third-party content and carry no tokens and no ids beyond post / user ids.

## Grok source

For people who would rather not pay per post or register a developer app: if `grok` (Grok Build, xAI's official CLI) is installed on the hub machine and signed in (`grok login`, a SuperGrok or X Premium+ subscription; sign-ins expire after 7 days), the extension runs it headless to search X. Billing rides the subscription.

- **Choosing**: the `source` setting: `auto` (default: the X API when signed in, else Grok when `grok` is found), `x_api` (never runs grok, and deletes what Grok found) or `grok` (the digest from Grok; an X API sign-in then only brings mentions and following, not the paid timeline).
- **Finding grok**: `grok_path`, else `PATH`, `~/.grok/bin/grok`, `~/.local/bin/grok`, `/usr/local/bin/grok`, `/opt/homebrew/bin/grok`; looked up in the extension's loop, so the status answers from memory.
- **Running it**: `grok -p <prompt> --output-format json --disallowed-tools <every local tool> --deny Bash --deny Read --deny Edit --deny Grep --deny WebFetch --deny MCPTool --deny AgentMessage --max-turns 6 --cwd <new empty dir>`. X search (`x_search`) is a hosted tool xAI runs server side, and grok offers it only while its `--tools` allowlist is empty or names it; naming it does not work either, since it is not a local tool and grok then ignores the whole allowlist. So no `--tools`: every local tool is removed by name, and the permission rules deny the same kinds of tools and every MCP tool (grok loads the user's MCP servers from `~/.grok/config.toml`, `~/.claude.json` and `~/.cursor/mcp.json`). 4 minutes a run, the process group killed when the run ends or times out, 1 MiB of output read; the extension's environment (for an installed extension, the hub's allowlist) without `PERSONA_*` variables, and never an X key.
- **Digest**: every `grok_every_hours` (default 6): the watched accounts (`handles`, else following as the X API read it, the 100 most active) in batches of 20 (X search's `allowed_x_handles` limit), at most 5 batches a refresh (the next refresh continues), plus one run for `topics`; last 24 hours, at most 15 posts a run, merged into `grok_digest.json` (retention like posts; kept across an X sign-out, deleted for `source: x_api`).
- **Cap**: `grok_searches_per_day` (default 30) per UTC day across the digest and the `x_search` tool, persisted at once.
- **Prompts**: two embedded prompt files in the extension, grok_digest and grok_search (text/template): X search only, the window, the handles and topics, a fixed JSON answer, never invent posts, verbatim text, post content is untrusted, no sensitive inference, summary in the hub's language.
- **Parsing**: the outermost JSON object (fences and leading text tolerated); posts kept only with a permalink matching `https://(x|twitter).com/<handle>/status/<id>` (normalized to x.com), the author taken from it, deduplicated by id, texts capped; topics point only at kept posts.
- **Status** (without the X API): `ok` (account "Grok", via "grok on the hub machine"); `not_configured` with how to set up either way; `not_signed_in` on an auth failure ("run grok login on the hub machine"); `rate_limited` on rate-limit / quota text.
- **Page**: Grok's summary, topics and posts with permalinks, runs used / cap, and a note that it may be incomplete. **Tools**: `x_search {query, handles?, from?, to?}` (one live run); `x_digest` / `x_feed` / `x_following` read Grok's results; `x_post` needs the X API. **Inbox**: mentions only with the X API.
- **Sandbox**: the hub's sandbox hides the home directory (`docs/extensions/extensions.md` ("Sandbox")), so grok and `~/.grok/auth.json` are invisible to a sandboxed install: Grok mode needs the sandbox off (the default for the official marketplace). The manifest declares `x.ai` / `*.x.ai` in `permissions.network`; there is no manifest field for "runs a program installed on the hub machine", so the guide says so.
- **Limits**: a search may miss posts and can't prove a post exists beyond its permalink's shape; 20 handles per run; xAI's subscription quota for headless use is unpublished.

## X's rules

From the Developer Agreement, Developer Policy and restricted-use cases:

- **Deletion compliance**: stored X content must be deleted or modified when it's deleted, made protected or withheld on X, within 24 hours of a request. The hub's knowledge base has no way to delete a post or archived message (only age-based pruning, default 365 days), so **X posts stay out of the knowledge base** until the hub has a posts delete API (X2). In the extension, short retention plus sign-out wiping the data directory keeps copies small and removable.
- **No training**: X content may not be used to train or fine-tune models. Persona only gives posts to models for summarizing and answering, never for training. Whether sending posts to a third-party model provider is fine is not addressed by X either way (open question 3).
- **No sensitive inference or profiling**: never derive or store health, finances, politics, ethnicity, religion, sex life, union membership or crimes about X users; no psychographic profiling. The skill and tool descriptions say so.
- **No off-X matching**: linking an @handle to a person elsewhere needs that person's opt-in. So **no people records and no `sender_handles`** from X.
- **Display**: show name and @username, unaltered text, and a link to the permalink. Avatars, the X logo and Web Intents buttons need app-side work (X4); quoted posts inside an AI digest follow the same rules. **No iframes** of x.com or embeds, anywhere, including the web app.
- **Not a substitute client**: the Agreement forbids building a substitute for X's own apps; the capped digest is deliberate.
- **Bring-your-own-app risk (unconfirmed)**: the restricted-use policy warns that "requiring your end users to register applications" can lead to enforcement, and the Policy forbids asking people for their developer app credentials. Our reading: a self-hosted, open-source tool where each person runs their own hub and is the developer of their own app, for personal use, is the most defensible case, and credentials never leave that person's hub. X has not confirmed this; it's flagged in the guide, and the app's use case should be described honestly as personal feed reading and summarizing.

## Testing

- Fake X API with `httptest`, synthetic data only (`example_user`, made-up ids): the signature vector; sign-in success / 401; following pagination; home `since_id` resume and the budget never exceeded; lists stop at a known id; mentions → Inbox items with `rule_tier`; 429; 402 → error status; retention prune; sign-out wipes the data directory.
- Digest ranking, links, threads and active accounts; tools; the page signed out and signed in passes `exttest.CheckView`; the manifest equals what the program declares; `exttest.Check` conformance signed out.
- Guarded: no token string appears in any status, tool result or page.
- The real-account path (console app, credits, sign-in, a day of budget) is checked by hand once, and the PR states what was and wasn't checked.

## Steps

| Step | What |
| --- | --- |
| X1 | The extension: sign-in, status, following, feed + budget + retention, Inbox mentions, digest, `nav_page`, tools, skill, tests, an English guide in the extension repository's docs; the optional Grok source (`grok` headless, digest, `x_search`) |
| X2 | Knowledge-base ingestion, after the hub has a posts delete API (`kb.posts_delete {ids}`) and a cheap re-check of stored ids |
| X3 | OAuth 1.0a PIN sign-in, after the apps draw a link on fields steps (web, iOS, Android) |
| X4 | X brand mark in the web and Apple apps' per-connector brand tables (without it X gets the generic mark), display-rule details (avatars, Web Intents) |

This repository: this plan (the first PR); one row in `docs/extensions/connectors.md` after X1 merges.

## Not doing / later

- **Not doing**: cookies, scraping, unofficial GraphQL; a shared Persona-owned app or client secret; an infinite timeline page.
- **Later**, each its own issue: writes (posting, replying, liking; through actions with `confirm_always`, and replies are allowed only when "summoned"); DMs (`dm.read` needs express consent and costs $0.01 per event); bookmarks and likes (cheap Owned Reads); knowledge-base ingestion (X2); PIN or OAuth 2.0 sign-in (X3).

## Open questions for the maintainer

1. Is bring-your-own-app acceptable as the X API route, given X's "Multiple applications" warning? Asking X developer support for a written answer is possible but slow.
2. Do Owned Reads apply to OAuth 1.0a calls? If not, should the weekly following refresh become on-demand only (about $10 per 1,000 accounts each time)?
3. Is sending post text to the user's configured model provider for summaries fine with X's redistribution clause, or should the guide say "use a local model" for strict readers?
4. Default budget 200 resources/day (≈ $30–60/month): too high or too low for a first install?
5. Is a digest-only `nav_page` an acceptable exception to "don't build browsing pages for one source", or should it move to `extension_page` / an `overview_card`?
6. Do xAI's SuperGrok / X Premium+ terms allow automated, scheduled headless use of `grok` for a personal digest, and at what volume? The defaults (a refresh every 6 hours, at most 30 runs a day) are a guess until that is known.
