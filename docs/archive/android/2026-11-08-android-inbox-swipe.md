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

## Follow-up after the first repair

The first wrapper repair and APK update-path checks did not establish full gesture correctness.
The follow-up compares the complete pre-grouping behavior and narrows implementation changes to reproduced defects.

| Historical baseline | Behavior and evidence |
| --- | --- |
| `e3ef98cbd` (initial inbox) | Left-only Done; completed rows had an explicit Move back button. Not the later two-direction contract. |
| `3a7e164d8` | Left Done/Move back at 85% distance, vetoed dismissal to spring back; per-item pending state and error restoration. |
| `4f5f208cc`, `30486e160` | Shared component intentionally replaces that contract: right Done/Move back, left AI handoff, default Material threshold, reset after execution. No persistent reveal-button state. Each item owns its callback and action scope. |
| `8ce5368ce` | Action background appears only during a drag; idle translucent rows must not retain it. |
| `0eed796ec` | Conversation grouping omits the wrapper and admits a whole feed when any item matches a filter. |
| `548da7dfd` (#1499) | Restores the wrapper but sends every grouped record, including hidden matches and already-completed records. The AI test checks background text, not sheet content. |

The dependency actually resolved by this build is Material 3 1.4.0, Foundation 1.12.1.
Material's default positional threshold is 56 dp, with velocity-based flings retained. The older 85% threshold
belongs to the superseded one-direction implementation; it is not restored. Slow pointer samples test below
and above the current threshold, cancellation, and return-to-origin bounds, separately from full flings.

Reproductions before repair:

- Search admits event `a`, hides `b` in the same feed: expected request `[a@version-a]`, actual `[a@version-a,b@version-b]`.
- Mixed open `a` and completed `b`: the same unexpected extra target. A partial-failure retry also includes successful events again.
- A shared swipe action changes its rendered action after invocation: the old callback causes
  `AppNotIdleException: Compose did not get idle ... in 60 SECONDS`. Material's `SwipeToDismissBox`
  keys its dismissal effect by both `state.settledValue` and `onDismiss`; the changing callback restarts it
  before reset finishes. The independent `SwipeActionsStateTest` reproduces this with synthetic data.

Repair: derive the displayed conversation records from the same filtered item set as the list. Both the
representative AI target and Done targets now use that view. Done skips records already at the requested
state; Reopen still addresses all completed records in the displayed conversation. Project pending writes
into already-read conversation sections so failure rolls them back immediately. Unread source receipts
remain independent of completion. Keep the shared dismissal callback stable, read current actions through
`rememberUpdatedState`, and reset in `finally`. Thresholds, directions, navigation, and the existing
repository/consent/idempotency paths remain in place. The swipe renderer is extracted without redesigning
its visuals. No connector or Hub protocol changes are needed.

Verification additions:

- Real synthetic Compose gestures test filtered Done and filtered AI consent, mixed completion, partial-failure
  retry, exact item versions, both slow-drag directions, below-threshold return, cancellation return, and sheet content.
- Shared component tests check one callback per deliberate gesture, subsequent reuse, and saved-state restoration.
  Saved-state replay and content replacement during a drag did not reproduce a defect; no speculative implementation
  change was retained for either path. Vertical/click, pending suppression and empty-row coverage remain.
- `InboxHandOffTest` retains operation-id semantics; the filtered sheet test checks failed consent retries reuse
  the same operation id and never complete an item after a failed handoff. Repository tests retain rollback,
  changed-version checks and manager routing. No real messages are used.
- Screenshots still cover the production renderer in Glass/Board/Brief, light/dark, both directions. They show
  drag visuals; sheet-internal and callback assertions establish behavior rather than relying on matching labels.

Run the commands above, plus `:core:designsystem:testDebugUnitTest --tests '*SwipeActionsStateTest'` and
`:core:designsystem:lintDebug`. These remain native-graphics JVM gesture checks, not installed-device acceptance.
APK download/update success is tracked separately by the existing release owner.
