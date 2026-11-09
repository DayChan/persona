# Plan: ego lite browser plugin (#1304)

Let Persona's assistant and the Agents on your computers drive a browser through [ego lite](https://lite.ego.app/): open pages, click, fill forms, take screenshots,
watch what it is doing from the phone and take over when needed, and import login state (cookies) into it. Built the same way as the Cua plugin ([computer-use.md](../extensions/computer-use.md)),
reusing its per-machine multi-select switch (#1285) and live view (#1184). **This document is a proposal awaiting the user's decision** ("To decide" at the end).

ego lite: a Chromium-based "agent browser" from Citro Labs. The App is a free download; the `ego-browser` CLI and skill live in
github.com/citrolabs/ego-lite (MIT; external code, read only, not vendored into the repo). The facts below come from its repo, the official docs, and the 0.6.2 (build 7) installed on this machine (researched 2026-11-04).

## What ego looks like

- **Installation**:
  - The official DMG is at `https://cdn.ego.app/setup/macos/{arm64|x64}/egolite-<referral code>.dmg`. The URL has no version number and always serves the latest; no checksums are published.
  - The App is signed with the Developer ID "CITRO LABS PTE. LIMITED" (Team `JGQLC6YQYJ`) and notarized. Tested locally:
    - `spctl -a -vv -t exec` reports "accepted, Notarized Developer ID";
    - `stapler validate` passes;
    - `codesign --verify --deep` passes, but with `--strict` it fails because of a FinderInfo attribute.
  - The official install script removes quarantine; we don't do that.
- **First-run setup (onboarding)**: on first launch it asks whether to import data from Chrome and other browsers (login state, cookies, bookmarks, passwords, extensions); macOS may ask for the keychain password.
  - When done, it registers `~/.local/bin/ego-browser` and writes the skill into `~/.agents/skills/ego-browser` and `~/.claude/skills`.
  - **It can only be done in that Mac's GUI**; there is no unattended way. Calling the CLI before it is done pops up the setup window instead of running.
  - It does not need Accessibility or Screen Recording permission.
- **`ego-browser`**: not a set of individual commands; it **runs a piece of JS** inside ego: `ego-browser nodejs`, with the script from stdin or `-e`. Available in the script:
  - Task spaces: `taskSpace(name)` creates or reuses one by name (a workspace inside the browser, **not a macOS desktop Space**);
  - Tabs: `task.page("p1")`;
  - Snapshots: `page.snapshot()`, accessibility-tree text with nodes tagged `[ref=N]`;
  - Actions: `click(@21)`, `fill`, `press`;
  - Waiting: `waitFor…`;
  - Screenshots: `page.screenshot({path})`, PNG.
- **Across calls**: every call is a new Node process, but task spaces, tabs, and refs persist (`~/.ego-browser/state/space-<id>.json`), so refs from the previous snapshot still work in the next call. The output is whatever the script `console.log`s; if the script throws, earlier output is lost.
- **CDP**: only from inside a script: `page.cdp(method, params)` (Page, Network, Input…) and `task.cdp()` (Target, Browser). There is no external remote debugging port.
- **Import from other browsers**: `ego-browser import --browser chrome [--profile Default] --no-default`, without the GUI (it may prompt for the keychain password).
- **Data directory**: `~/Library/Application Support/Citro Labs/ego lite`. Cookies within one profile are shared: the user's own tabs and every task space use them together.
- **Privacy**: ships Sentry, PostHog, and other analytics. The privacy policy says the optional "usage analytics" may include visited URLs, task input, and page content; it doesn't say whether it is on by default.
- Supports macOS 13 and later, with separate DMGs for Apple silicon and Intel.

## 1. Plugin, discovery, installation

- **Plugin**: built-in plugin `ego`, purely declarative, contributing a new extension point `browser`, **off by default**. Config:
  - `machines`: `format: machine`, `x-os: ["darwin"]`, one switch per Mac;
  - `confirm`: confirm on the phone before acting.
- **node probe** (`browser.status`, only asked of enabled machines); the machine page shows each item:

  | What | How |
  | --- | --- |
  | Installed or not | `/Applications/ego lite.app` (bundle id `com.citrolabs.ego.lite`) and its version |
  | Signature correct | `codesign` Team is `JGQLC6YQYJ` |
  | First-run setup done | Whether `~/.local/bin/ego-browser` exists, and whether the data directory exists. **Don't probe by calling the CLI**: before setup is done, calling it pops up a window on that Mac. This check needs to be confirmed by testing after completing first-run setup locally |
  | Version | `ego-browser --version` |

- **"Install ego lite"** (`browser.install`, a background task reporting progress, like the Cua install):
  1. Download the DMG for the chip into a temp directory and mount it with `hdiutil attach -readonly -nobrowse`.
  2. **Verify**: `codesign --verify --deep` (without `--strict`), Team ID must be `JGQLC6YQYJ`, `spctl -a -t exec` must report notarized. Otherwise don't install.
  3. Install into `/Applications` with `ditto` and verify again afterwards. Files the node downloads itself carry no quarantine attribute (same as with `curl`), so we don't rely on the system checking at first launch: the `spctl` above is the same Gatekeeper check, done once before and once after installing.
  4. Unmount the disk and delete the temp files.
  - Use the link from the official README (its referral code is public in the README, not anyone's account); in the first step it lives in the node (`egoDMG`); move it into the hub's config if it ever needs to change.
  - What we can't do: on **first launch**, both macOS's "App downloaded from the internet" confirmation and ego's first-run setup need a person to click on that Mac. So after installing, offer "Finish first-run setup":
    - the node runs `open -a "ego lite"` to open the setup window on that Mac;
    - the machine page shows step-by-step instructions; with screen sharing on, you can click through remotely via "VNC" (#1184).
- **Analytics**: turn off whatever can be turned off during first-run setup, and remind the user in the guide to turn off "usage analytics". Whether it can be turned off by command needs testing after first-run setup.

## 2. The assistant's tools; division of work with Agents

Tools for the **assistant** (via the hub); descriptions live in `prompts/mcp/browser/`:
- `browser_run {machine, script, timeout_ms?}`: run a piece of ego JS on that Mac.
  - The node wraps the script before handing it to `ego-browser nodejs` (via stdin):
    - first `taskSpace("persona-<first 8 chars of the chat id>")`, so the same chat always lands in the same task space and refs stay valid across calls;
    - then the user's script, and finally a JSON result;
    - on a throw, the output so far is returned together with the error, so nothing is lost to ego's "throw and lose the output" behavior.
- `browser_look {machine, page?, full?}`: snapshot text of the current tab, plus a screenshot (`page.screenshot` writes a temp file, the node reads it back and converts to JPEG). Images are handled as with Cua: passed to the model as an image block, and the App draws it in the tool details.
- **Write operations** (`browser_run`): allowed by default; the hub log only records the script's byte count and duration. With the plugin's "confirm on the phone before acting" on, an approval card pops up first.
- **Cookies are not exposed as a tool**: the model never sees raw cookies and cannot import or export them.

**Agents on the machine** (Claude Code, Codex): use the skill installed by ego's first-run setup (`~/.claude/skills/ego-browser`) directly, without the hub, and unaffected by the plugin switch. The plugin only governs the assistant's path.

## 3. Watching the browser

| | Approach | What you can see | Take over | Status |
| --- | --- | --- | --- | --- |
| a) Live view (#1184) | macOS screen sharing, full screen | Visible when the ego window is in front; **not visible when the agent's task space is in the background** (it is in another workspace inside the browser) | Already there | Exists, nothing new to build |
| b) Periodic screenshots | The node screenshots the agent's tab with `page.screenshot` every second and pushes it to the phone via the hub (a page like the live view) | The agent's tab, whether or not ego is in front | Tap: phone coordinates become page coordinates and run `Input.dispatchMouseEvent`; or `takeOverTaskSpace` first and then act | ego has all the APIs, **needs testing** |
| c) CDP screencast | Run `Page.startScreencast` in a long-lived `ego-browser nodejs`, streaming frames to the phone via the hub | Same as b, higher frame rate | Same as b | Not mentioned in the docs, **needs testing** whether it works |

**Recommendation**:
- Do b first: it is sure to work, cheap, and 1–2 frames per second is enough to see what the agent is doing.
- Switch to c if testing shows it works.
- a stays available, for when you need the whole desktop or to click system windows.
- Entry points as with Cua: both the machine page and the tool details in the chat offer "Watch browser".

## 4. Importing cookies (the key part)

Cookies are login credentials: having them means being logged in. Every step below treats them as credentials.

### Sources

| Source | How | Good for |
| --- | --- | --- |
| a) A browser already on that Mac | Imported from Chrome during ego's first-run setup; or later triggered from the machine page's "Import from Chrome" as `ego-browser import --browser chrome --profile <chosen> --no-default` (may prompt for the keychain password on that Mac). Cookies never pass through the hub or the phone | Least effort: sites the user is already logged into in Chrome on this Mac |
| b) Upload from the phone or the web | Pick a file: Netscape `cookies.txt`, or JSON exported by a browser extension (the array format of Cookie-Editor / EditThisCookie); you can tick only some domains | When the login is on another computer or the phone |
| c) A browser on another computer | If that one has ego too: export the chosen domains from its task space with CDP `Network.getAllCookies` and relay them via the hub to the target Mac. Without ego, export a file for b on that machine | Syncing a site's login across computers |
| d) Only selected domains | a, b, and c all list domains and counts first; the user ticks them (none ticked by default) and only the ticked ones are imported | Applies to all |

### Storage and security

- **Destination**: only the ego profile on the target Mac.
- **When passing through the hub** (b, c):
  - after parsing, encrypted with AES-GCM using the hub's key and held in memory or the database, **deleted once written or after 10 minutes**, never kept long term;
  - not logged (only "which machine, which domains, how many"), not in the repo, not in the assistant's context, and the App's pushes never carry the raw values;
  - the file content is not cached on the phone.
- **Who can import**: only the user's own full-permission devices (not read-only devices), through an explicit action in the App or on the web. The assistant, Agents, and plugin processes cannot start it; the hub exposes no interface for it to the model.
- **Viewing and revoking**:
  - the machine page's "Browser logins" lists domains with cookies in ego (count, latest expiry, **no values**), aggregated by the node with `Network.getAllCookies`;
  - each domain can be "cleared" (`Network.deleteCookies`), or everything at once;
  - both clearing and importing go into the audit log (who, which machine, which domains, how many).
- **One shared profile**: ego's cookies are shared across the whole profile, so imported logins are also used when the user browses in ego themselves. For the alternative see item 3 of "To decide".

### How cookies are written

1. CDP first:
   - The node runs an `ego-browser nodejs` script that calls `page.cdp("Network.setCookies", {cookies})` inside Persona's task space. The cookie data sits in a 0600 temp file that the script deletes after reading; it never appears in command-line arguments;
   - fields are mapped to the CDP format: `name`, `value`, `domain` (leading dot removed for hostOnly), `path`, `expires` (seconds), `secure`, `httpOnly`, `sameSite`; expired ones are skipped;
   - after writing, `Network.getCookies` checks the count;
   - the docs' examples use `Network.getAllCookies` and `deleteCookies`, so `setCookies` should work too; **this needs testing after first-run setup**. Whether `Storage.setCookies` is available is unclear.
2. Source a doesn't use this path: ego's own `import` reads Chrome directly.
3. While the user has taken over that task space, ego doesn't let scripts use CDP (`EGO_TASK_SPACE_USER_IN_CONTROL`): ask the user to hand it back first, then import.

## What step one did (#1304, 2026-11-04)

The user decided: go step by step, periodic screenshots first, cookies go into the default profile, uploaded cookies are parsed on the phone and relayed straight through the hub (nothing written to disk on the hub), skip other computers for now, and turn analytics off automatically where possible. Step one built the plugin, discovery, installation, the first-run setup guide, readiness, and two assistant tools; watching the browser and cookies come in later steps.

- **Plugin `ego`**: built in, purely declarative, off by default, contributes the extension point `browser`. Config `machines` (`format: machine`, `x-os: ["darwin"]`, one switch per Mac on the plugin page) and `confirm`. The machine page's "Allow the assistant to use this machine's browser" is the same value.
- **node `browser.status`** (the hub asks every online Mac while the plugin is on):
  - Installed: `/Applications/ego lite.app`, version from Info.plist.
  - First-run setup: `ego.welcome_page_shown` in `Local State`, plus whether `~/.local/bin/ego-browser` exists. Whether data was imported: `ego.onboarding_imported_browser_data`.
  - Analytics: if Chromium's `Consent To Send Stats` file exists, it is on.
  - Ready: once setup is done, run one read-only probe (`listTaskSpaces()`, touching no page) and keep the result for a minute.
  - Finally stamp `checked` (the hub orders by it, see #1387).
- **Install** (`browser.install`): download the official DMG from the README, mount it read-only and verify (`fetchEgo`), install into Applications and verify again. Files the node downloads carry no quarantine attribute, so the Gatekeeper check is done by `spctl` once before and once after installing.
  - Verification: `codesign --verify --deep`; `codesign -dv` TeamIdentifier is `JGQLC6YQYJ`; `spctl` is accepted and Notarized. No `--strict`, which fails because of a FinderInfo attribute.
  - Tested: the 0.6.2 installed on this Mac passes all three.
- **Open** (`browser.open`): `open` the App to start first-run setup on that Mac. The machine page polls every 2 seconds and switches to ready once done.
- **First-run setup guide** (machine page):
  1. Tap "Open ego lite", or use "VNC" to operate it from the phone;
  2. choose to import data from Chrome (or another browser); macOS asks for the keychain password several times, enter it each time; this can be skipped;
  3. finish first-run setup (it installs ego-browser);
  4. "Check again".

  Tested: apart from the keychain, first-run setup needs no other authorization (no Accessibility, no Screen Recording).
- **Ready** is green. Below it, checkmarks list: first-run setup, whether data was imported, usage analytics.
  - When analytics are on, it suggests turning them off: in ego's **Settings › Sync and services**, turn off "Automatically sends usage statistics and crash reports to Citro Labs".
  - **Not automated**: on the settings page this item is hidden inside a collapsed group and is not a standard toggle component, so clicking it from a script is unreliable; editing its preferences file while ego runs gets overwritten. So we fell back to a hint in the guide.
- **Assistant tools** (`prompts/mcp/browser/`):
  - `browser_run {machine, script}`: run a piece of JS in the chat's own task space (`persona-<first 8 chars of the chat id>`), with `task` and `page` (tab p1) at hand; the return value, URL, title, and screenshot come back together.
  - `browser_look {machine, full?}`: snapshot (with `[ref=N]`) plus screenshot.
  - Write operations are allowed by default and the hub log only records byte counts; with "confirm on the phone before acting" on, a card pops up first.
- **Tested** (`TestEgoEndToEnd`, `PERSONA_EGO_E2E=1`, only touching example.com):
  - status ready;
  - open example.com; "Watch browser" returns the snapshot and a 1920×849 screenshot;
  - the task space is closed at the end of the test.
  - Opening the page takes about 0.3 s, snapshot plus screenshot about 0.1 s.
- **Gotcha**: if the script's printed output contains even one NUL character, ego drops that call's entire output. The result line now uses a visible marker, `@@persona-browser@@`.
- **Protocol compatibility (#1224)**: ordinary `browser_look` uses `page.snapshot({scope: "only_within_viewport"})`; `full: true` uses `full_page`. A returned result object with a nonempty `error` becomes a failed tool call even when the node RPC succeeded, while retaining its metadata and screenshot. Both changes require deploying the node to the Mac running ego and the hub respectively. The extension page's **Set Up** opens the existing per-Mac installation and first-run guide; it does not install anything by itself.

## Steps

1. **Plugin and discovery**: plugin `ego`, `browser.status`, machine page status, the "Finish first-run setup" guide.
2. **Install**: download, verify, install into Applications, verify again afterwards.
3. **Assistant tools**: `browser_run`, `browser_look`, the confirm switch, logging.
4. **Watching the browser**: b, periodic screenshots plus tap to take over; entry points on the machine page and in tool details.
5. **Cookies**: list and clear, import from Chrome (a), upload a file (b), selected domains (d); another computer (c) last.

One PR per step; screenshot anything that touches the UI first. From step 3 on, testing needs that Mac to have finished first-run setup.

## To decide

1. Follow the steps above, starting with 1–3?
2. For watching the browser, do b (periodic screenshots) first and switch to c (CDP screencast) once testing shows it works?
3. Where cookies go:
   - **(i) ego's default profile**: shared with the user's own ego browsing; logins imported from Chrome during first-run setup are usable by the agent right away;
   - **(ii) a separate profile just for Persona**: no interference either way, but the agent only has the logins imported into it.

   Recommend (i): less hassle, and it fits "easy to operate".
4. Uploaded cookies held on the hub for at most 10 minutes and deleted once written: OK? Or require parsing only on the phone and relaying straight through the hub to the node, never touching the hub's database?
5. Build import from another computer (c), or start with only a, b, d?
6. ego's "usage analytics": just a hint in the guide to turn it off, or have the node turn it off automatically where possible (needs testing whether there is a switch)?
7. **The user needs to complete ego's first-run setup once on this Mac** (the import step can be skipped for now): only then can I test the completion marker, CLI, screenshots, `Network.setCookies`, and screencasting.

## What cookie import did (#1304, 2026-11-04)

- **Tested** (ego lite on this dev Mac, only touching example.com, deleted after testing): `page.cdp("Network.setCookies", {cookies})` on a task-space page writes (both domain cookies and host-only), `Network.getCookies` reads back the right flags, `Network.deleteCookies` deletes, and `Network.getAllCookies` lists the whole profile. `Storage.*` doesn't work: `task.cdp` only allows `Target.` / `Browser.`, so everything goes through the Network domain.
- **node** (`node/browser_cookies.go`): `browser.cookies.list` / `.import` / `.clear`. Each call runs a script in Persona's own task space "persona cookies" and calls `task.finish` at the end. Cookies reach `ego-browser nodejs` via the script's stdin, never on the command line or in a file; only the site, count, latest expiry, and imported / skipped / deleted counts go back to the hub, and when ego errors its output is not passed back either (it may echo the script). Mapping to the CDP format: host-only drops the leading dot, domain cookies get one added; SameSite=None gets Secure added; expired and malformed ones are skipped. Sites are grouped by the public suffix list (`www.example.com` and `.example.com` both count as example.com). While the user has taken over the task space, it asks them to hand it back first.
- **hub** (`extmgr/browser_cookies.go`): `GET / POST / DELETE /api/v1/machines/{mid}/browser/cookies`. Only accepts the App / web the user is signed into (`byOwnDevice`): the pairing token from hub.json (CLI, the assistant's process), watches, machines, and other hubs all get 403, and the assistant has no matching tool. Imported content goes straight to the node, never into the database, onto disk, or into logs; the log only records "which machine, who, how many, which sites". 409 when the target Mac is offline. Only available when the ego plugin is on and the machine is a Mac.
- **Web** (machine page "Browser logins", `views/machines/BrowserLogins.tsx`, `lib/cookies.ts`): lists sites (count, latest expiry, no content), with "Clear" per site and "Clear all" (both confirm first); "Import cookie file": Netscape `cookies.txt` (including `#HttpOnly_` lines) or JSON from Cookie-Editor / EditThisCookie / `{cookies: [...]}`, parsed in the page, listed by site (none ticked by default, expired ones not imported), and only the ticked ones are sent to the hub; after sending, the page no longer holds the file content.
- **iOS** (machine page "Browser logins", `Views/Machines/BrowserLoginsSection.swift`, `Shared/BrowserCookies.swift`): same as the web: list sites, clear (with confirmation), "Import" picks cookies.txt / JSON with the system file picker, parses it on the phone (`CookieFile`; `BrowserCookiesTests` shares the same examples as the web tests), shows a sheet to tick sites, and sends only the ticked ones.
- Not done yet: the "Import from Chrome" button for source a, and source c (another computer).
