# iOS idle-energy investigation: 2026-11-09 (#1634)

This is a point-in-time synthetic investigation linked to the canonical server investigation #1633. It is not physical-device battery acceptance.

## Scope and version boundary

The baseline was a Debug app from `13c62ed455e8885aae50fd1491621715e86595ae`. The measured candidate contains the client fix recorded in `01307f6cd`. Both use stock iPhone 17 Pro / iOS 27.0 simulators and Xcode 27.2, isolated loopback hubs, synthetic conversations and the same visible-page history. A new simulator of the same profile/runtime ruled out accumulated test state; the old owned simulator was shut down. The PR subsequently merged current main, so build/unit evidence is separate from these isolated measurements. The latest source merge `82cbabd92` incorporates 14 additional main commits through `3fedbecd4`; changes to Agents filters/counts, status/chat headers and Watch presentation are not validated merely by the older measurement or screenshot results.

No production hub, real conversation, TestFlight upload or private cloud-document directory was used. Raw traces, process paths, simulator identifiers and detailed artifacts remain outside the public checkout.

CPU is process CPU normalized to one core, read through `proc_pid_rusage` with Mach ticks converted by `mach_timebase_info`. Interrupt and package-idle wakeups are distinct process counters, not all hardware wakeups. A short host Instruments Activity Monitor recording cross-checked the accounting. Mock transport counters measure API bytes at the file/socket boundary, not TLS/TCP/IP or Wi-Fi/cellular energy.

## Ordinary-scene comparison

Each window has fixed endpoints independent of trace saving/symbolication. Setup visits the Agents list and then Persona; the background window starts only after a verified background transition. Normal use performs eight swipes and one synthetic send.

| Scene | Window | App CPU before → after | Interrupt wakeups before → after | HTTP before → after | Mock API received / sent bytes, before → after |
| --- | ---: | ---: | ---: | ---: | --- |
| Persona idle | 300 s | 4.2182% → 0.5211% | 39,652 → 2,461 | 0 → 0 | 140 / 84 → 140 / 84 |
| Agents idle | 300 s | 3.9943% → 0.4981% | 39,794 → 2,678 | 87 → 84 | 24,974 / 20,622 → 29,914 / 19,914 |
| Background | 600 s | 0.5660% → 0.0039% | 8,407 → 82 | 151 → 0 | 43,085 / 35,636 → 0 / 0 |
| Eight swipes + one synthetic send | 90 s | 12.3077% → 9.7090% | 11,934 → 8,228 | 1 → 1 | 629 / 621 → 698 / 621 |

Agents GETs are all existing foreground herdr status reads. The difference of one three-machine polling round is a phase/window-boundary difference, not a new refresh quota. The interval remains ten seconds. Added attribution headers increase request bytes; do not present the small GET-count difference as bandwidth optimization.

The background main socket was already disconnected in the baseline: the eliminated HTTP polling was a separate lifecycle path. This establishes activity in a stock simulator, not continuous execution on a physically suspended iPhone.

Both normal-use windows contain eight swipes and one received POST. The candidate send occurs at 44.21 s; the baseline at 57.39 s. Automation/action duration differs, so the CPU comparison is not an isolated estimate for one rendering change.

## Unchanged one-hint/second load

Each input lasts 120 s and emits 119 hints. The todo response contains 520 synthetic todos, about 1.17 MB before compression. Session requests are classified by actual query as full-list reads, not cursor/delta/counts/lookup.

| Scene | GETs before → after | Completed pre-gzip body bytes before = after | Completed gzip body bytes before = after | App CPU before → after | Interrupt wakeups before → after |
| --- | ---: | ---: | ---: | ---: | ---: |
| todos | 119 → 119 | 138,950,718 | 29,521,996 | 10.6602% → 6.8907% | 8,280 → 1,545 |
| sessions | 118 → 118 | 12,955,220 | 800,512 | 5.7536% → 2.5299% | 15,678 → 1,247 |

These body totals belong to GETs started within the fixed window and audited after response completion. The candidate todo endpoint initially had one in-flight receipt at the boundary. Its default zero lengths/identity encoding were not an empty response: all 119 completed bodies negotiated gzip and the totals exactly match the baseline. Transport and CPU endpoints remain the original 120-second boundaries.

**There is no measured GET or completed-body bandwidth reduction in this load.** Single-flight coalesces overlapping reads, not fast non-overlapping requests at 1 Hz. No cooldown, added RTT, smaller inventory or substitute stimulus was introduced. Reducing full todo bodies while preserving freshness depends on a compatible hub delta/conditional protocol in #1633; this PR does not invent that protocol or request a speculative rate limit.

## Ranked workload paths and fixes

1. **Full todo/session invalidations amplify incoming hints.** Todo reload, manual refresh and write confirmation now share single-flight ownership with one dirty/trailing read, finite freshness tickets and revision/generation publication fences. Sessions retain the existing short hint-merging window, cursor/delta/lookup and completeness-required fallback. Known-critical paths remain immediate; unmarked hints are not guessed or dropped. Todos reuse the existing off-main decoder.
2. **Idle display-rate work survives visibility changes.** The needs-you ring moves from a 30 Hz SwiftUI timeline to Core Animation, keeping its geometry, wall-clock phase, duration and exact easing curve. Avatars retain nominal 15 Hz behavior but use periodic eligibility, and display clocks/custom frames stop when unmounted, scroll-hidden, tab-hidden or scene-inactive. Recording/transfer clocks remain business work, not display work.
3. **Foreground herdr polling survives background entry.** iOS/visionOS `tabActiveTask` requires both active tab and active scene; cancellation is checked before/after each machine request and before publication. Mac focus semantics are unchanged. The sixty-second Lark fallback also returns after cancellation rather than issuing a final refresh.
4. **Shared credentials obscure caller attribution.** REST, main/probe sockets, widget/extension clients, intents and direct health checks carry bounded platform/execution-surface metadata and public version/build values. Auth, operation IDs, ETags, Range and the video identity encoding are preserved. This is not hardware identity or authentication. Headers can participate in server capability matching; Apple does not currently advertise elicitation, so future capability adoption must keep authenticated hello matching coherent. This is not a claim that every raw Apple-to-hub request is tagged.

## Validation and preserved failures

Initial candidate local validation:

- Strict offline result reader: 2,739 cases, zero failed/skipped; Swift Testing reports two pre-existing new-chat harness known issues, not acceptance of those unsupported flows.
- Separate WebKit batches: 13 HTML-card and 10 bounded HTML-security cases, zero failed/skipped. The destructive infinite-loop lab remains a separate watchdog-controlled integration case.
- Related default-style UI matrix: the complete run passed 24 of 26 after fixture repairs; the two remaining cases passed strict narrow reruns. Coverage includes all three render budgets, both retained hidden-chat flows, all seven screen-truth cases, all ten live-update cases and all four herdr cases. This is passing case coverage across runs, not a fabricated all-green original batch.
- Three mock identity-contract tests pass; the original churn function fails all three negative controls. Resolved IDs no longer become new questions in the fixture; production anti-resurrection remains unchanged.
- Document paths, Go-document paths, privacy and diff-format guards pass. Token counts require no baseline regeneration or exemption.

Follow-up at `79b3ba449`: the complete offline unit/design result contains 2,843 passing cases with zero failed/skipped and the same two known issues. UI batches at `4dabfc00f` pass all 26 cases in Liquid Glass and all 26 in Beautiful, with zero failed/skipped; the earlier case failures and narrow reruns remain historical evidence. Downloaded synthetic avatar selection and cold-relaunch persistence also pass one case in each appearance. The first Xcode 26.0 hosted lane compiled the shipping App/embedded Watch graph, then failed an existing `WorktreeStatus` test conformance: its manual `Decodable` extension inherited MainActor despite the type's explicit nonisolated data contract. The extension is now explicitly nonisolated; all six worktree cases pass locally. The [next pinned run](https://github.com/nwverse/persona/actions/runs/42531727242) at `4dabfc00f`, using Xcode 26.0.1 (17A400) / Swift 6.2, passes the unsigned iOS/embedded Watch build but times out after 912.4 seconds booting a stock iOS 26.0 simulator (exit 124), before simulator tests start. This is not a passing minimum-compiler test result; final rerun status is recorded in the PR.

The [PR screenshot gallery](https://github.com/nwverse/persona/pull/1730) embeds 24 inspected synthetic PNGs from the image-only `pr-assets/1730` branch: six baseline views, sixteen four-screen/style/appearance combinations at `4dabfc00f` and two downloaded-avatar views at that same revision. Every raw URL was fetched without credentials and byte-matched to its local PNG. Captures do not freeze animations, and static images are not playback-lifecycle or frame-cadence evidence. All owned local simulators and mock services were stopped after testing.

Latest Apple-source follow-up: both related UI batches pass again (26/26 Liquid Glass and 26/26 Beautiful, zero failed/skipped) against the shipping/selected-test tree at `82cbabd92`. Sixteen additional screenshots from the binary stamped `53473f56` were inspected across the four screens, both styles and both appearances; the shipping tree is unchanged. The image-only branch now preserves all 40 PNGs, with every raw URL byte-verified and all images embedded in the PR. Active scene, style and appearance are asserted without freezing animations. Owned local simulators and mock services are shut down.

A documentation-only commit's required run failed an untouched Web pane-render assertion (`expected 1 to be 0`). #1789 / #1790 independently reproduced and fixed its initial question-rail measurement on main, without weakening the render-budget assertion. The branch merges that existing fix at `7aa021a76`. The only Apple difference from `82cbabd92` is an unrelated New Chat UI test file; shipping code and the 52 selected energy-related cases are unchanged. The [required run at `7aa021a76`](https://github.com/nwverse/persona/actions/runs/42541189658) passes after that merge. The pinned Apple run and subsequent documentation-head check results remain separately recorded in the PR.

Failures were retained and repaired rather than weakened: frame-only windows did not mount the native representable until attached to `UIWindowScene`; profiling snapshots contaminated sync/Watch fixture inventories; a retained Agent chat changed the warmed page; automation setup could leave a notification alert; title-based menu selectors hit the wrong control or an offscreen row. The final menu driver was checked against actual simulator screenshots and scrolls inside visible bounds. Background waiting uses a bounded XCTest expectation, not a busy loop blocking event delivery.

An unmatched Persona trial recorded 5.5371% CPU and 39,593 interrupt wakeups, and its Agents setup failed the baseline-visible-session guard. It is not silently discarded or used as a valid comparator. Replaying items alone also regenerated task feeds at a later clock, putting cards after the synthetic message; matching item counts did not mean matching display work. The corrected observer pins the original fixture clock before initialization, restores the full regular/assistant inventory without duplication, and reproduces the inspected baseline message order.

Full hosted scripts initially failed four exact compatibility registrations in unchanged lesson/node carriers on both the branch and main; #1722 / #1723 owns that separate fix. No blanket exemption or repository setting change was made. After merging #1723, the [full hosted scripts run](https://github.com/nwverse/persona/actions/runs/42530492135) passes at `79b3ba449`. Latest-merge CI/build/screenshot details are recorded in the linked PR; local minimum Xcode 26.0 is not verified.

## Final pinned-compiler result

The [Xcode 26.0 lane retry](https://github.com/nwverse/persona/actions/runs/42538201491) passes at `82cbabd92` using Xcode 26.0.1 (17A400) / Swift 6.2. It builds the unsigned iOS app, embedded Watch and extensions, then passes 2,846 offline unit/design cases, 13 HTML-card cases and 10 bounded HTML-security cases with zero failed/skipped. Swift Testing retains the same two pre-existing harness known issues. The later Web-fix merge and documentation commits preserve that shipping/offline-test tree; an unrelated New Chat UI test file is the only subsequent Apple difference. The [required documentation-head check](https://github.com/nwverse/persona/actions/runs/42542088561) at `0bffdfb19` also passes. This closes the pinned-compiler execution gap, not the physical-device or Instruments gaps below; preceding failures remain recorded.

## Instruments status

| Window | Baseline Time Profiler | Candidate Time Profiler |
| --- | --- | --- |
| Persona idle | Save timeout | Process-name lookup failed |
| Agents idle | Saved/exported | Saved |
| Background | Save timeout | Saved |
| Normal use | Saved/exported | Save timeout |
| Todo load | Saved | Save timeout |
| Session load | Save timeout | Saved |

The independent CPU/transport endpoints cover all six full windows, but the table is **not an all-pass Instruments matrix**. Name/PID probing and preserved errors are separate diagnostic evidence. Long recordings can stall while saving, and concurrent recording sessions contend for `kperf`.

Simulator Activity Monitor, Network Connections, Power Profiler and Animation Hitches rejected simulator attribution; Energy Log was absent and the SwiftUI probe yielded no usable data. Host all-process short recordings work, but long recordings repeatedly failed. Unavailable network/GPU/radio/power counters are not reported as zero. The successful Agents traces have 11,149 target samples before and 1,563 after, with Timeline matching 559 before and 10 after; the successful candidate background trace has 11 target samples. These are statistical weights, not CPU execution duration. Baseline StatusDot matches 441 samples, while the candidate has no resolved symbol match: unresolved stacks prevent concluding that any missing symbol means zero work, and inclusive subtree weights cannot be added.

## Remaining acceptance boundaries

- No physical iPhone battery/radio/GPU-energy claim or before/after battery-percentage experiment was performed. It requires an approved controlled device/signing lane without real conversations; TestFlight publishing is excluded from this task.
- The reported production ten-minute sample uses a shared credential and pre-gzip totals; it cannot establish a single phone or be directly compared with this synthetic fixture. Prospective bounded caller tags help separate execution surfaces, not physical devices.
- Complete long-window Instruments-save acceptance remains a tool/trace gap where shown above. A stable toolchain or controlled device lane must supply unavailable power/network/GPU metrics.
- Conditional paths remain separate fixtures: prepared VNC/PiP drawing, paused recording display clocks, queued on-device transcription, Watch transfer/summary work, terminal maintenance and Markdown preprocessing. Their source periods are not established physical-device idle-energy causes.
- The ordinary main connection already has background shutdown, backoff and generation checks. No fixed one-minute reconnect was reproduced. A hello-only extension capability probe can close normally within ten seconds and is not proof of a main-app reconnect loop.
- Default background modes are audio and remote-notification. Location requests are one-shot with a fifteen-second bound; no default continuous-location/fetch/processing/BGTask loop was found. Live Activity is event-driven/deduplicated, widgets use minute-scale schedules, and the separate server fix #1641 is not a client measurement.

Public evidence: [root-cause baseline](https://github.com/nwverse/persona/issues/1634#issuecomment-6725748009), [same-load comparison](https://github.com/nwverse/persona/issues/1634#issuecomment-6726979845), [PR #1730](https://github.com/nwverse/persona/pull/1730), [canonical #1633](https://github.com/nwverse/persona/issues/1633).
