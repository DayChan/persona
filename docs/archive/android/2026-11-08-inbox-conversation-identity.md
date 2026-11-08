# Android inbox conversation identity regression (#1583)

The grouped row introduced by `0eed796ec07aa7ee4bf1448d72eefb0af604ee13`
passed only `sender = row.title` and `title = row.preview` to `MessageItem`.
It omitted the sender identity and chat-kind tags retained by the individual row.
The historical individual renderer at `7679a0c9210ca90e75dc1d24c18704f1f8de1a05`
kept event/conversation context separate from sender identity and localized kind labels.
Restoring the swipe wrapper alone did not restore those arguments.

The repair reuses `MessageItem.heading` and `tags`: current conversation title,
latest represented item's sender, message preview, and localized Group/Direct label.
Explicit conversation metadata wins over event kind; legacy group/dm kinds still
work. Unknown kinds receive no invented label, absent senders receive no invented
identity, and cached group names cannot overwrite the current conversation title.
No swipe callbacks, action scopes, execution authorization, or shared components change.

## Verification

Before the renderer repair, the three new Group, Direct and sender assertions
all failed against the production grouped row (3 tests, 3 failures).
After repair, `InboxConversationIdentityTest` passes six tests, including native
Compose layout bounds and 24 captures: group/direct, single/multiple records,
Glass/Board/Brief, light/dark. The captures use synthetic data, not real messages.
They are generated under `feature/inbox/build/reports/tests/inbox-identity-shots`.
They are native Robolectric renderer captures, not physical-device screenshots.

The complete focused run passed 43 tests with zero failures/errors:
identity 6, conversation swipe 11, existing group-name row 1, conversation model 11,
bulk 6, handoff 6, shared swipe state 2. Inbox lint also passed.

```sh
JAVA_HOME=/opt/tiger/jdk/jdk21 android/gradlew -p android \
  :feature:inbox:testDebugUnitTest \
  --tests '*InboxConversationIdentityTest' --tests '*InboxConversationSwipeTest' \
  --tests '*InboxGroupNameRowTest' --tests '*InboxConversationsTest' \
  --tests '*InboxBulkTest' --tests '*InboxHandOffTest' \
  :core:designsystem:testDebugUnitTest --tests '*SwipeActionsStateTest' \
  :feature:inbox:lintDebug --no-watch-fs --console=plain
# BUILD SUCCESSFUL in 38s
```

The user confirmed installed-latest swipe behavior at 22:26 on 2026-11-08;
no version number was supplied. This is recorded separately from automated tests
and does not establish installation of the subsequent identity repair.
