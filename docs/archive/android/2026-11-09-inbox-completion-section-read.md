# Android completion and section read controls (#1807)

## Cause and behavior

Conversation grouping in `0eed796ec` chose unread Action/FYI before checking whether
all represented items were done. The pending projection in #1563 also retained an
unread row's old section. Thus a successful versioned completion could remain in
an open section. The original ungrouped domain list already puts done items in
Done regardless of source read flags. An existing conversation test even expected
a completed-but-unread item to stay Action; it is corrected to the intended filing
behavior. The new production pointer/UI negative control fails before repair.

Done now wins in both conversation state and pending projection. Completed items
cannot give a mixed conversation its unread action tier, and loaded source messages
cannot promote a fully completed row back into Action/FYI. Filing does not send a
read receipt. Pending failure rollback, changed-version protection and source read
semantics remain in their existing repository/action paths.

Needs you headers now offer Mark all as read in either fold state. The existing
bounded conversation read implementation accepts the captured section's keys,
including unopened conversations. Keys include assistant ownership; another
assistant's row with an equal source/account/thread cannot join the operation.
Already-read conversations and interaction cards are excluded. Retries retain only
the failed keys, and queued rows are checked against current section and version.
This operation never calls completion, reply or deletion. The feedback string is
now section-neutral in English, Chinese and Japanese.

## Verification

- Historical production UI negative: one unread completion test fails because
  repository records are done but the conversation section remains Action.
- Repaired focused run: 79 tests, zero failures/errors; inbox lint passes.
- Native Compose production UI: unread completion moves to Done, disappears from
  Needs you and stays Done after controller reconstruction; failed filing restores
  the open row with feedback. Existing mixed-event partial failure/retry and action
  version guards also pass.
- Needs you read: folded failure and retry; expanded mixed read/unread rows;
  another assistant remains untouched; no completion calls; no remaining control
  for the emptied unread section. Empty key sets make no request.
- Repository tests cover versioned writeback, partial failures and rollback; a fresh
  server refresh preserves the successful filing and newer-event versions.
- Synthetic HTTP transport test observes distinct POST bulk action=done with item
  versions and POST conversation/read with source position/message IDs; a 503 read
  propagates failure. No real messages were used.
- Twelve native Robolectric full-page captures cover three styles, light/dark,
  folded/expanded. They are renderer captures, not physical-device screenshots.
  Existing identity captures and both gesture regression paths remain passing.

```sh
JAVA_HOME=/opt/tiger/jdk/jdk21 android/gradlew -p android \
  :feature:inbox:testDebugUnitTest --tests '*InboxConversationSwipeTest' \
  --tests '*InboxConversationsTest' --tests '*InboxConversationIdentityTest' \
  --tests '*InboxBulkTest' --tests '*InboxHandOffTest' \
  :core:data:testDebugUnitTest --tests '*InboxRepositoryTest' \
  :core:hub:test --tests '*InboxCompletionReadContractTest' \
  --tests '*InboxAssistantTransportTest' :feature:inbox:lintDebug \
  --no-watch-fs --console=plain
```

A gesture callback passing does not establish list reconciliation. Keep at least
one unread, successfully completed conversation in production-screen regression
coverage; test filing and source read receipts as independent transitions.
