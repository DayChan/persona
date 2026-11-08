# Android grouped inbox swipe regression

## Cause and boundary

Regression: [PR #94](https://github.com/nwverse/persona/pull/94), commit
`0eed796ec07aa7ee4bf1448d72eefb0af604ee13`, introduced the grouped conversation branch in
`InboxScreen.kt` and `InboxConversationItemRow`. That branch rendered `MessageItem` directly,
where the previous individual `InboxItemRow` used `JSwipeActions`.

Compare `git show 0eed796ec^:android/feature/inbox/src/main/kotlin/persona/feature/inbox/InboxScreen.kt`
with `git show 0eed796ec -- android/feature/inbox/src/main/kotlin/persona/feature/inbox/InboxScreen.kt`.
The original LTR directions are start-to-end (right): Done / Move back; end-to-start (left): Hand to AI.
Neither action is delete, archive or a source read receipt.

The connector supplies `extra.lark.feed_id` and the host preserves that metadata. Android decodes it
in `RealInboxRepository.record`, and `inboxConversationKey` selects the grouped renderer.
The two row actions are host-owned operations, not a connector-provided swipe action declaration.
The connector manifest, host bulk mapping and source action authorization require no changes.
An absent or unsupported connector action must not be fabricated to restore these gestures.

## Repair

Restore `JSwipeActions` around the grouped row, keep its click/long-click navigation, and call the
existing versioned bulk Done/Reopen and Hand-to-AI sheet. Block both directions and menu actions
while any displayed item is pending. Also gate clicks and long presses then: a disabled swipe can otherwise
be treated as a click. Empty grouped records render no actions. Hand-to-AI opens the
existing sheet; the gesture alone sends no message and grants no new authorization.
The existing repository retains partial-failure rollback, manager routing and reconciliation.
Extract existing individual-row rendering to keep `InboxScreen.kt` within 500 lines.

## Verification

Synthetic data only; no writes to a real source. Robolectric API 35 with native graphics exercises
production Compose touch handling. `InboxConversationSwipeTest` covers both directions, exact
item versions, Done/Reopen and list section reconciliation, pending duplicate suppression, thrown
and returned failures, unsupported-operation feedback, short drags, vertical drags, clicks and
empty records. Screenshots cover all three styles, light/dark, both directions, under
`android/feature/inbox/build/reports/tests/inbox-swipe-shots/` (included in Android CI reports).

The missing-wrapper negative control fails the right-swipe regression with `expected:<1> but was:<0>`.
The wrapper is restored before the passing run; use `--no-watch-fs` so Gradle observes both changes.
Related suites: `InboxConversationsTest`, `InboxHandOffTest`, `InboxBulkTest` and
`:core:data`'s `InboxRepositoryTest` (rollback, changed-version and manager routing).

Commands, from repository root, with JDK 21 and the Android SDK configured:

```sh
android/gradlew -p android :feature:inbox:testDebugUnitTest --tests '*InboxConversationSwipeTest' --tests '*InboxConversationsTest' --tests '*InboxHandOffTest' --tests '*InboxBulkTest' --no-watch-fs
android/gradlew -p android :core:data:testDebugUnitTest --tests '*InboxRepositoryTest' --no-watch-fs
android/gradlew -p android :feature:inbox:lintDebug --no-watch-fs
```

These are JVM renderer/repository checks, not physical-device or live-source gesture acceptance.
The existing APK release owner handles the merged source through the existing release chain;
merged, published, client validation and installed statuses must be reported separately.
