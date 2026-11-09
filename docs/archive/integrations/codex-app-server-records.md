# Codex app-server: records

Moved unchanged from the [Codex app-server guide](../../integrations/codex-app-server.md).
This is the acceptance record of its first implementation (2026-11-02): what ran, and what could not run that round.

## Acceptance record for the first implementation (2026-11-02)

Validated: full server tests against real test Postgres, repeated Codex/send-related race regressions, protocol driving of an actual node process with independent HOME, and all 16 web tests plus build. The new unknown-event regression covers the old parser overlay and genuinely fails on the old implementation. Synthetic data only; no changes to the shared worktree.

Apple attempted xcodebuild test with an independent source directory/simulator and exited 70: although simctl had iOS 26.0 and the SDK list showed 26.2, Xcode reported the iOS 26.2 platform uninstalled and the destination unavailable. Actual Swift tests executed: 0; no iOS/watchOS light/dark screenshots obtained. Android had no available SDK/Gradle, so no build or tests. No platform installation, signing changes, or TestFlight release.

Real-corpus validation attempted to select Codex snapshots from the user's independent corpus directory, but no eligible files were found. Corpus window/continuity was therefore not run this round; a skip is not a pass. Long history relies on existing fixture/window regressions. UI and real dual-client approval-race limitations remain.
