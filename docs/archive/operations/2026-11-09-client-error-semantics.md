# Client error semantics audit — 2026-11-09

Scope: #1915, including misleading gateway-status fallbacks on Web, Apple and Android.
The reviewed upstream base was `62c6cc85cab847d93996a67d8b598083149a51f7`.
Before delivery, upstream `6e4bb7476` was merged. The API conflict was resolved by
retaining upstream's bounded read retries and this change's code-first presentation.
Production-mode regression tests confirm that business refusals do not enter those retries.
This is source and synthetic-test evidence, not deployment or real-device acceptance.

## Responsibility chain

The manager's `server/internal/assistant/kb_routes.go` already emits `kb_disabled` (503),
`kb_unavailable` (503), and `kb_read_only` (403). Read-only checks precede writes;
successful empty lists remain successful. `server/internal/assistantwire/kb.go` documents
these codes. The hub's `server/internal/hub/assistant_link.go` registers the relay,
and `server/internal/hub/bus_forward.go` copies the manager's HTTP status, headers and
body unchanged. A disconnected manager instead yields `assistant_offline`.
`server/internal/apierr/apierr.go` keeps stable codes and separates localized summaries
from diagnostic detail. No new server implementation is needed.

Client boundaries:

- Web: `web/src/lib/api.ts` → shared `errorText` → knowledge list/detail/search,
  Inbox dialogs and other consumers. Business codes now precede status fallbacks;
  unknown gateway statuses no longer claim that the hub or computer is down.
  `web/src/lib/feedback.ts` no longer infers a network outage from an HTTP status alone.
- Apple: `apple/Shared/APIClient.swift` → `APIError.errorDescription` / `Error.shown`
  → knowledge screens and other callers. `APIErrorInfo` in PersonaKit parses model
  response errors, not these HTTP failures. Known configuration refusals are not hidden
  merely because their incidental message contains “timeout”.
- Android: `android/core/hub/src/main/kotlin/persona/core/hub/HubFailure.kt` → knowledge repository and shared
  domain/design-system feedback → `KnowledgeViewModel` and screens. Existing disabled/
  starting states are retained; read-only, manager, authentication, permission,
  missing-content and timeout states are distinguished.

## Module × client audit

“Fixed” describes source changes. Native runtime validation remains unavailable here.
“No reproduced issue” is bounded source review, not a comprehensive runtime pass.

| Module | Web | Apple | Android |
| --- | --- | --- | --- |
| Knowledge categories, detail, search | Fixed shared code-first handling and legacy decoding; rendered-list regressions | Fixed shared HTTP presentation; list/detail/search already use `error.shown`; native UI unverified | Existing disabled/starting handling retained; fixed legacy decoding and additional failure kinds; native UI unverified |
| Recordings | Knowledge kind inherits fix; no device recorder | Fixed retry pause and durable pending-delete explanation; no DELETE 404 acknowledgement; sync tests added but not executable here | Knowledge recording list/detail inherit fix; no phone recording-upload worker found |
| Inbox / sent ledger | Fixed inherited manager/gateway wording; existing real-REST dialog test proves no send/navigation on failed lookup | Fixed pre-send item lookup treating arbitrary gateway responses as “Can't reach the hub”; sent ledger no longer calls a timeout a hub outage; uncertain-send receipts remain unchanged | Existing typed errors and shared feedback retained; no same-class list-empty conversion reproduced in reviewed connector-thread model |
| Extensions / connectors / channels | Shared fallback fixed; status-specific extension states retained | `ExtensionsModel` preserves failure and unsupported states; shared presentation fixed; no additional reproduced issue | `ExtensionsState`, `ExtensionMarketState`, `ExtensionUiState` retain explicit states; shared feedback fixed |
| Model configuration / availability | `providers.ts` keeps import-specific offline/outdated cases; shared safe internal fallback fixed; no model probe executed | `ModelConfigurationModel` / view keep 404 compatibility and failed-list states; existing availability regressions retained | `ModelConfigurationState` preserves failed catalog vs unsupported; shared feedback fixed; no model probe executed |
| Agents / execution nodes | Existing `node_unreachable`, delivery and timeout classification retained; operation retry excludes coded refusals | Shared node wording distinguishes confirmed offline from dropped connection; existing quiet timeout behavior retained | Existing coded computer/readiness feedback retained; no additional reproduced issue |

The Mac reuses `KnowledgeView` through `apple/Mac/App/MacKnowledgePage.swift`.
The iOS, macOS, watchOS and visionOS targets compile `Shared` according to
`apple/project.yml`; the change introduces no PersonaKit dependency or newer watch API.
The watch records/transfers audio to the phone rather than running `RecordingSync`.
All four Apple build targets still require verification on a Mac.

Additional source-review anchors:

- Knowledge: `web/src/views/knowledge/KBList.tsx`, `web/src/views/knowledge/KBDetail.tsx`,
  `apple/Persona/Views/Knowledge/KnowledgeView.swift`,
  `android/core/hub/src/main/kotlin/persona/core/hub/HttpKnowledgeRepository.kt`,
  `android/feature/knowledge/src/main/kotlin/persona/feature/knowledge/KnowledgeViewModel.kt`.
- Inbox: `web/src/views/inbox/InboxDialogs.tsx`,
  `apple/Persona/Views/Inbox/InboxHandOffModel.swift`, `apple/Persona/Views/Settings/SentModel.swift`,
  `android/feature/inbox/src/main/kotlin/persona/feature/inbox/ConnectorThreadModel.kt`.
- Extensions: `apple/Persona/Views/Settings/ExtensionsModel.swift`,
  `android/feature/settings/src/main/kotlin/persona/feature/settings/ExtensionsState.kt`,
  `android/feature/settings/src/main/kotlin/persona/feature/settings/ExtensionMarketState.kt`,
  `android/feature/settings/src/main/kotlin/persona/feature/settings/ExtensionUiState.kt`.
- Model configuration: `web/src/lib/providers.ts`,
  `apple/Persona/Views/Settings/ModelConfigurationModel.swift`,
  `android/feature/settings/src/main/kotlin/persona/feature/settings/ModelConfigurationState.kt`.
- Agent recovery: `web/src/lib/ops.ts`, `apple/Shared/Delegated.swift`,
  `android/core/domain/src/main/kotlin/persona/core/domain/OperationFailure.kt`.

## Compatibility and privacy boundaries

All clients recognize the older refusal only when all of these match:

1. The requested route is in `/api/v1/kb/`, `/api/v1/kb-search` or `/api/v1/recordings`
   (Web passes the equivalent path without `/api/v1`).
2. HTTP status is 503 and the original code is exactly `internal`.
3. `detail` is exactly `the knowledge base is off`.

No substring or generic 503 matching is used. Different detail, status, code or
route does not infer disabled. Remove this bridge only when those old hubs are
outside the supported rolling-upgrade window. Older replies without sufficient
evidence cannot be reliably classified; they retain a generic failure.

Stable business messages do not interpolate arbitrary diagnostics. Unknown `internal`
server errors use a generic presentation, not their exception text. Original error
objects retain status/body for existing diagnostic consumers; request IDs remain
available (Android now retains the response ID too). Configuration refusal never
enables knowledge, imports data, probes a model or triggers a retry timer.

Recording uploads and deletions stay pending through configuration refusal.
Ordinary saves cannot resume a paused worker; foreground/manual retry can recheck
after configuration is changed. A local deletion is not a claim that its knowledge
copy was deleted. Unsupported deletion remains queued.

## Verification

The initial rendered Web list reproduced the defect for stable codes and the exact
legacy response: it showed the hub-outage text (or raw read-only summary), not the
business state. The focused fixed suite passed 57 tests before the final hardening.
The full Web lane initially exposed one old Inbox expectation for raw manager text;
that expectation was updated while retaining its no-send/no-navigation assertions.

Original delivery checks after upstream integration (Node 24 for the Web lane):

| Command | Result |
| --- | --- |
| `scripts/ci/web.sh` | Passed install, lint, typecheck, tests (7,203 passed, 1 skipped), production build and privacy scan |
| `python3 scripts/test/test_apple_l10n.py` | 60 tests passed; catalog completeness included |
| `python3 scripts/i18n/apple_l10n.py check` | Passed en / zh-Hans / ja completeness and format checks |
| `python3 scripts/app/android_i18n_check.py` | Passed, zero problems |
| `python3 scripts/app/android_token_baseline.py` | Passed without raising the baseline |
| `python3 -B test_android_i18n_check.py` / `python3 -B test_android_token_baseline.py` (from `scripts/app/`) | 10 / 3 checker tests passed |
| `python3 scripts/test/test_doc_paths.py` / `python3 scripts/test/test_doc_go_paths.py` / `python3 scripts/test/test_doc_wire.py` | 12 / 3 / 13 tests passed |
| `python3 scripts/privacy_check.py` and staged scan | Clean |

### Follow-up synchronization — 2026-11-10

The general comment on PR #2614 reported no findings on head `7096810`; it did
not request a code change or constitute formal approval. Upstream `8212b6fb3`
was merged without rebasing or force-pushing. The only textual conflicts were
concurrent additions to `Localizable.xcstrings`: all 6,104 upstream entries and
this PR's 12 entries were retained, while upstream removals stayed removed.
The automatically merged API routing, Inbox hand-off and recording-sync changes
were inspected against the new base.

`scripts/ci/web.sh` with Node 24 passed again after the final synchronization:
install, lint, typecheck, 7,555 tests passed / 1 skipped, production build and
privacy. Apple catalog completeness, Android translations, and the documentation
path / Go-path / section / wire suites passed (12 / 3 / 3 / 13 tests).

### Further upstream synchronization — 2026-11-10

Upstream `4156d9e4a` was merged after main advanced again. The recording-detail
conflict retains upstream's meeting minutes and timestamp navigation, together
with this PR's distinction between local deletion and pending Knowledge removal.
The catalog retains all 6,150 upstream entries and this PR's 12 entries unchanged.
Only the new entries are inserted in the documented key order; existing entries
are not reordered, and entries removed upstream are not restored. No code is
relocated to avoid a merge conflict: shared error classification stays in the
existing client boundaries, and recording retry/deletion state stays in
`RecordingSync`.

The full Web lane with Node 24 passed install, lint, typecheck, 7,585 tests
passed / 1 skipped, production build and privacy. After the catalog resolution,
Apple localization checks passed again, including 60 checker tests. Android
translation/token checks and the documentation path / Go-path / section / wire
suites passed.

Before delivery, upstream `a226ebe11` was also merged. A concurrent addition to
the iOS lessons document retains both sections. All 6,152 upstream catalog entries
and the 12 PR entries were checked unchanged, with upstream ordering preserved.
The final full Web lane passed install, lint, typecheck, 7,665 tests passed /
1 skipped, production build and privacy. Apple catalog, Android translation/token
and documentation checks passed on this integrated tree.

Web is the primary validation for this review. Remaining validation gaps are
disclosed rather than treated as a blocker to marking the PR ready for review:

- Apple compilation, `PersonaTests`, `RecordingSyncTests`, Inbox model tests,
  PersonaKit tests and light/dark simulator screenshots: unavailable on this Linux
  environment (`scripts/ci/apple.sh packages` requires macOS/Xcode; `swift` is absent).
- Android compilation, JVM/Robolectric tests and screenshots across all styles:
  unavailable (`scripts/ci/android.sh --lane test-core` requires JDK 21; SDK is absent).
- Rendered-browser screenshots: unavailable because `browser-use` is absent.
  jsdom component regressions are not screenshot evidence.

No PR merge, deployment, runtime configuration change, restart, knowledge enablement,
private data-source access or paid/live model call was performed. Installed clients
and the running hub have not been replaced by this source change.
