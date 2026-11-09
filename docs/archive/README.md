# Archive: point-in-time records

Only retain records with unique measurements, unresolved acceptance boundaries, or design decisions not captured by a maintained guide.
The records below describe their stated date, not the current product. Source filenames, immutable commit references and test counts may be historical;
local documentation links are kept current so readers can reach the maintained contract. Current module guides and lessons take precedence.

Development screenshots and recordings are not stored here. Keep new verification media outside the checkout and attach it to the associated PR.
Place necessary historical text under its module directory and list it below.

| File | Contents |
| --- | --- |
| `android/2026-11-09-inbox-completion-section-read.md` | Completion projection, assistant-section read controls and synthetic UI/API checks (#1807) |
| `android/2026-11-08-inbox-conversation-identity.md` | Grouped inbox chat-kind and sender identity loss, historical negative controls and native render checks (#1583) |
| `android/2026-11-08-android-inbox-swipe.md` | Grouped inbox swipe regression, historical cause, synthetic pointer and reconciliation checks (#1491) |
| `assistant/task-completion-6525.md` | Completion race reproductions, reconnect ordering, and historical evidence limits (#6525) |
| `apple/apple-rolling-compatibility-6192-acceptance.md` | Actual pre-rename hub interoperability, final Apple full-suite counts, inspected GUI evidence, preserved baseline failures and native-system limits (#6192 / #6277) |
| `apple/apple-extension-stream-6176.md` | Apple native extension streams: isolated-hub acceptance, atomic reduction, consent/fences and remaining GUI/unit gates (#6176) |
| `android/android-p0-acceptance.md` | Android P0 round 4 joint acceptance checklist |
| `android/android-p0-status.md` | Android v1 P0 status |
| `apple/agents-grouping-5766.md` | iPhone Agents grouping and initial long-list limits (#5766) |
| `apple/agents-list-5929-local-acceptance.md` | Bounded Agents list API: local acceptance (#5929) |
| `apple/apple-html-mac-normal-20261106.md` | Apple HTML: normal Mac matrix for the latest Coordinator (2026-11-06) |
| `apple/ios-perf-5730-final.md` | iOS 60 FPS optimization closeout and remaining performance limits (#5730) |
| `apple/live-updates.md` | Live updates must not interrupt user actions (2026-10-31) |
| `apple/persona-native-6152-acceptance.md` | Apple native naming migration acceptance (#6152) |
| `apple/persona-native-6152-reversible-acceptance.md` | Apple reversible native-state compatibility acceptance (#6152) |
| `assistant/assistant-rich-ui-predeployment-2026-11-06.md` | Main chat rich UI: pre-deployment acceptance |
| `extensions/plan-telegram.md` | Plan: Telegram connector (the user's own account) |
| `extensions/source-extension-migration-2026-11-07.md` | Server-rendered source extensions: migration and acceptance record |
| `integrations/codex-project-hierarchy-verification.md` | Codex project and task hierarchy verification |
| `integrations/msg-render-plan.md` | Message reading and rendering pipeline: diagnosis and rework plan |
| `integrations/msg-render-verification.md` | Real-data verification of the message pipeline lane (stages 2–4) |
| `integrations/session-verify.md` | Real-data verification of session reading and message sending (2026-10-31) |
| `operations/tab-switch-performance-739.md` | Persona/Agents switching CPU and request measurements, targeted fixes and the separate Mac/TestFlight acceptance boundary (#739) |
| `operations/apple-tab-switch-validation-739.md` | Original Apple 240-sample matrix, cadence/payload observations, hidden HTTP follow-up and open acceptance gates (#739 / #1159) |
| `operations/node-link-update-validation.md` | Persona Link node update acceptance (#1158) |
| `operations/persona-only-277-acceptance.md` | Persona-only naming acceptance (#277) |
| `apple/plan-chat-topbar.md` | Superseded Apple chat-header proposal and decisions; current implementation is in the iOS guide |
| `operations/plan-hub-center.md` | Historical primary-hub design and rollout plan; current contract is in the mesh guide |
| `operations/plan-hub-mesh.md` | Superseded multi-hub topology proposal; current primary/member behavior is in the mesh guide |
| `operations/rolling-upgrade-compatibility-verification.md` | Rolling-upgrade verification (#6223) |
| `redesign/android.md` | Android plan |
| `redesign/flows.md` | Main feature flow diagrams |
| `redesign/hub-changes.md` | Hub change list |
| `redesign/ios-refactor.md` | iOS refactoring steps |
| `redesign/parity.md` | Feature parity matrix (existing iOS features + redesign additions) |
| `redesign/research/android-cn-roms.md` | Persona Android: research on Chinese vendor ROMs and Android-version compatibility |
| `redesign/research/android-tech.md` | Persona Android technology research notes |
| `redesign/research/bh-architecture.md` | Architecture and engineering research on a reference app (for Persona's iOS refactor and new Android version) |
| `redesign/research/bh-ui-qa.md` | Reference app design language, prototype review, and test acceptance (compared with Persona) |
| `redesign/research/hub-network-current.md` | Current hub networking audit (direct public connectivity / IPv6 priority / TLS / pairing) |
| `redesign/research/persona-features.md` | Persona iOS feature inventory (source for the Android parity matrix) |
| `redesign/research/persona-ios-arch.md` | Current Persona iOS architecture and technical debt (redesign baseline) |
| `redesign/research/persona-protocol.md` | Persona hub protocol research (for a new Android client) |
| `redesign/research/public-direct-ipv6.md` | Direct public connectivity, IPv6, TLS, and persistent-connection keepalive: research notes |
| `redesign/ux.md` | Interaction and UI specification (shared by both platforms) |
| `apple/2026-11-09-ios-idle-energy.md` | Six synthetic before/after energy-workload windows, single-flight boundaries, lifecycle regressions and preserved Instruments/device acceptance gaps (#1634 / #1633) |

The [historical redesign index](redesign/README.md) groups the retained interaction drafts and research.
The retained [chat-header prototype](apple/prototypes/chat-topbar.html) is a text-only historical design, not a production screen.
