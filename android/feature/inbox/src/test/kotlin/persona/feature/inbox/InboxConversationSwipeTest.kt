package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.After
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.*
import java.io.File

/** Real pointer gestures through the grouped production renderer; all records and writes are synthetic. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class InboxConversationSwipeTest {
    @get:Rule val rule = createComposeRule()
    private val owner = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private val opened = mutableListOf<NavKey>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) { opened += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
    @After fun stop() { owner.cancel() }

    private fun record(id: String) = InboxRecord(
        InboxItem(id, account = "demo", title = "Synthetic conversation", preview = "Synthetic preview",
            kind = "dm", tier = "action", version = "version-$id"),
        source = "lark", feedId = "synthetic-room", read = true,
    )

    private class FakeInbox(records: List<InboxRecord>, query: String = "") : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records))
        override val connectionState = MutableStateFlow(ConnectionState.Online)
        override val preferences = flowOf(InboxPreferences(query = query, expanded = listOf("action", "fyi", "read-pending", "done")))
        override val conversationInbox = MutableStateFlow<InboxConversations?>(null)
        val calls = mutableListOf<Pair<List<InboxDoneTarget>, InboxBulkAction>>()
        val handoffs = mutableListOf<InboxHandOffRequest>()
        override suspend fun handOff(request: InboxHandOffRequest) {
            handoffs += request
            throw IllegalStateException("Synthetic handoff failure")
        }
        var response: suspend (List<InboxDoneTarget>, InboxBulkAction) -> InboxBulkResult = { items, action ->
            val ids = items.map { it.id }
            snapshot.value = snapshot.value.copy(records = snapshot.value.records.map { record ->
                if (record.item.id in ids) record.copy(item = record.item.copy(
                    doneAt = if (action == InboxBulkAction.Reopen) null else "2026-11-08T00:00:00Z")) else record
            })
            InboxBulkResult(changed = ids)
        }
        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction): InboxBulkResult {
            calls += items to action
            return response(items, action)
        }
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = error("Grouped rows must use versioned bulk")
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = error("A swipe must never send a reply")
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private fun screen(inbox: FakeInbox) {
        inbox.conversationInbox.value = InboxConversations(inbox, "synthetic-pairing", owner, ReplyComposers(inbox, owner))
        rule.setContent { PersonaTheme(reduceMotion = true) { InboxScreen(inbox, navigator) } }
        rule.waitForIdle()
    }
    private fun row() = rule.onNode(hasText("Synthetic conversation") and hasClickAction())
    private fun swipe(right: Boolean) {
        row().performTouchInput { if (right) swipeRight() else swipeLeft() }
        rule.waitForIdle()
    }

    @Test fun rightSwipeFilesEveryDisplayedVersionThenReopensWithTheSameSemantics() {
        val inbox = FakeInbox(listOf(record("a"), record("b")))
        screen(inbox)
        swipe(true)
        rule.runOnIdle {
            assertEquals(1, inbox.calls.size)
            assertEquals(setOf(InboxDoneTarget("a", "version-a"), InboxDoneTarget("b", "version-b")), inbox.calls[0].first.toSet())
            assertEquals(InboxBulkAction.Done, inbox.calls[0].second)
            assertTrue(inbox.snapshot.value.records.all { it.item.isDone })
            assertEquals(InboxConversationSection.Done, inbox.conversationInbox.value!!.state.value.rows.single().section)
        }
        swipe(true)
        rule.runOnIdle {
            assertEquals(InboxBulkAction.Reopen, inbox.calls.last().second)
            assertTrue(inbox.snapshot.value.records.none { it.item.isDone })
            assertEquals(InboxConversationSection.ReadPending, inbox.conversationInbox.value!!.state.value.rows.single().section)
        }
    }

    @Test fun filteredSwipeCannotFileHiddenItemsInTheSameConversation() {
        val inbox = FakeInbox(listOf(record("a").let { it.copy(item = it.item.copy(preview = "Visible match")) },
            record("b").let { it.copy(item = it.item.copy(preview = "Hidden event")) }), query = "Visible")
        screen(inbox)
        swipe(true)
        rule.runOnIdle {
            assertEquals(listOf(InboxDoneTarget("a", "version-a")), inbox.calls.single().first)
            assertFalse(inbox.snapshot.value.records.single { it.item.id == "b" }.item.isDone)
        }
    }

    @Test fun filingAMixedConversationDoesNotFileAlreadyCompletedEventsAgain() {
        val inbox = FakeInbox(listOf(record("a"), record("b").let { it.copy(item = it.item.copy(doneAt = "2026-11-08T00:00:00Z")) }))
        screen(inbox)
        swipe(true)
        rule.runOnIdle { assertEquals(listOf(InboxDoneTarget("a", "version-a")), inbox.calls.single().first) }
    }

    @Test fun anUpdatedVersionCancelsAnInFlightGestureRatherThanActingOnUnseenContent() {
        var record by mutableStateOf(record("a"))
        var count = 0
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                InboxConversationItemRow(buildInboxConversations("scope", InboxSnapshot(records = listOf(record))).rows.single(),
                    navigator, GroupPosition.of(0, 1), null, false, false, { count++ }, { count++ })
            }
        }
        row().performTouchInput {
            down(center)
            moveTo(center + androidx.compose.ui.geometry.Offset(width * .35f, 0f), 500)
        }
        rule.runOnIdle { record = record.copy(item = record.item.copy(version = "new-version")) }
        row().performTouchInput {
            moveTo(center + androidx.compose.ui.geometry.Offset(width * .9f, 0f), 500)
            up()
        }
        rule.waitForIdle()
        rule.runOnIdle { assertEquals("A changed event needs a new deliberate gesture", 0, count) }
    }

    @Test fun partialFailureKeepsTheFailedEventOpenAndARetryOnlyAsksForThatEvent() {
        val inbox = FakeInbox(listOf(record("a"), record("b")))
        inbox.response = { _, _ ->
            inbox.snapshot.value = inbox.snapshot.value.copy(records = inbox.snapshot.value.records.map {
                if (it.item.id == "a") it.copy(item = it.item.copy(doneAt = "2026-11-08T00:00:00Z")) else it
            })
            InboxBulkResult(changed = listOf("a"), failed = listOf("b"))
        }
        screen(inbox)
        swipe(true)
        rule.onNodeWithText("previous state is back", substring = true).assertExists()
        rule.runOnIdle {
            assertEquals(InboxConversationSection.ReadPending, inbox.conversationInbox.value!!.state.value.rows.single().section)
        }
        swipe(true)
        rule.runOnIdle {
            assertEquals(2, inbox.calls.size)
            assertEquals(listOf(InboxDoneTarget("b", "version-b")), inbox.calls.last().first)
        }
    }

    @Test fun leftSwipeOpensTheExistingConsentSheetWithoutExecutingOrFiling() {
        val inbox = FakeInbox(listOf(record("a")))
        screen(inbox)
        swipe(false)
        rule.onNodeWithText("What should it do").assertExists()
        rule.runOnIdle { assertTrue(inbox.calls.isEmpty()); assertTrue(opened.isEmpty()) }
    }

    @Test fun filteredLeftSwipeUsesTheVisibleItemOnlyAfterConsentAndKeepsTheRetryOperationId() {
        val inbox = FakeInbox(listOf(record("a").let { it.copy(item = it.item.copy(preview = "Hidden event")) },
            record("b").let { it.copy(item = it.item.copy(preview = "Visible match")) }), query = "Visible")
        screen(inbox)
        swipe(false)
        rule.onNodeWithText("What should it do").assertExists()
        rule.runOnIdle { assertTrue(inbox.handoffs.isEmpty()); assertTrue(inbox.calls.isEmpty()) }
        rule.onNodeWithText("Send").performClick()
        rule.onNodeWithText("Couldn't hand off:", substring = true).assertExists()
        rule.runOnIdle {
            assertEquals("b", inbox.handoffs.single().itemId)
            assertTrue(inbox.calls.isEmpty())
        }
        rule.onNodeWithText("Send").performClick()
        rule.runOnIdle {
            assertEquals(2, inbox.handoffs.size)
            assertEquals(inbox.handoffs.first().operationId, inbox.handoffs.last().operationId)
            assertTrue(inbox.snapshot.value.records.none { it.item.isDone })
        }
    }

    @Test fun pendingBlocksBothDirectionsAndAFailureRestoresTheRowAndReportsIt() {
        val inbox = FakeInbox(listOf(record("a")))
        val result = CompletableDeferred<InboxBulkResult>()
        inbox.response = { _, _ -> result.await() }
        screen(inbox)
        swipe(true)
        swipe(true)
        swipe(false)
        rule.runOnIdle { assertEquals(1, inbox.calls.size); assertTrue("Pending swipes must not navigate", opened.isEmpty()) }
        rule.runOnIdle { result.completeExceptionally(IllegalStateException("Synthetic failure")) }
        rule.waitForIdle()
        rule.onNodeWithText("previous state is back", substring = true).assertExists()
        row().assertExists()
        rule.runOnIdle { assertFalse(inbox.snapshot.value.records.single().item.isDone) }
        inbox.response = { items, _ -> InboxBulkResult(failed = items.map { it.id }) }
        swipe(true)
        rule.onNodeWithText("previous state is back", substring = true).assertExists()
        rule.runOnIdle { assertEquals(2, inbox.calls.size) }
        inbox.response = { _, _ -> throw UnsupportedOperationException("Synthetic unsupported operation") }
        swipe(true)
        rule.onNodeWithText("previous state is back", substring = true).assertExists()
        rule.runOnIdle { assertEquals(3, inbox.calls.size); assertFalse(inbox.snapshot.value.records.single().item.isDone) }

    }

    @Test fun tapsShortHorizontalDragsAndVerticalScrollDoNotExecuteActions() {
        val inbox = FakeInbox(listOf(record("a")))
        screen(inbox)
        row().performTouchInput { swipeUp() }
        rule.runOnIdle { assertTrue(inbox.calls.isEmpty()); assertTrue(opened.isEmpty()) }
        row().performTouchInput { swipe(center, center + androidx.compose.ui.geometry.Offset(8f, 0f), 500) }
        rule.runOnIdle { assertTrue(inbox.calls.isEmpty()) }
        row().performClick()
        rule.runOnIdle { assertEquals(1, opened.size); assertTrue(inbox.calls.isEmpty()) }
    }

    private fun drag(fraction: Float, release: Boolean = true) {
        row().performTouchInput {
            val origin = center
            down(origin)
            repeat(60) { step -> moveTo(origin + androidx.compose.ui.geometry.Offset(width * fraction * (step + 1) / 60, 0f), 16) }
            if (release) up() else cancel()
        }
        rule.waitForIdle()
    }

    @Test fun slowDragsHonorTheHistoricalThresholdAndCancelSpringsBackInBothDirections() {
        val inbox = FakeInbox(listOf(record("a")))
        inbox.response = { items, _ -> InboxBulkResult(failed = items.map { it.id }) }
        screen(inbox)
        val origin = row().fetchSemanticsNode().boundsInRoot.left
        for (direction in listOf(1f, -1f)) {
            drag(direction * .10f) // 40 dp: below the shared component's 56 dp threshold.
            rule.runOnIdle { assertTrue(inbox.calls.isEmpty()); assertTrue(opened.isEmpty()) }
            rule.onNodeWithText("What should it do").assertDoesNotExist()
            assertEquals(origin, row().fetchSemanticsNode().boundsInRoot.left, 1f)
            drag(direction * .40f, release = false)
            rule.runOnIdle { assertTrue(inbox.calls.isEmpty()); assertTrue(opened.isEmpty()) }
            rule.onNodeWithText("What should it do").assertDoesNotExist()
            assertEquals(origin, row().fetchSemanticsNode().boundsInRoot.left, 1f)
        }
        drag(.30f) // 120 dp: over the threshold, including touch slop.
        rule.runOnIdle { assertEquals(1, inbox.calls.size) }
        assertEquals(origin, row().fetchSemanticsNode().boundsInRoot.left, 1f)
        drag(-.30f)
        rule.onNodeWithText("What should it do").assertExists()
    }

    @Test fun emptyRowsHaveNoGestureOrActionAndEveryStyleDrawsBothDirections() {
        var records by mutableStateOf(emptyList<InboxRecord>())
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var count = 0
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.fillMaxWidth().testTag("shot").background(PersonaTheme.palette.background)) {
                    InboxConversationItemRow(InboxConversationRow(InboxConversationKey("scope", "lark", "demo", "room"),
                        "Synthetic conversation", records, InboxConversationSection.Action, "v1"), navigator,
                        GroupPosition.of(0, 1), null, false, false, { count++ }, { count++ })
                }
            }
        }
        rule.onNode(hasText("Synthetic conversation") and hasClickAction()).assertDoesNotExist()
        records = listOf(record("a"))
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            for (right in listOf(true, false)) {
                row().performTouchInput {
                    down(center)
                    moveTo(center + androidx.compose.ui.geometry.Offset(if (right) width * .08f else -width * .08f, 0f), 100)
                    moveTo(center + androidx.compose.ui.geometry.Offset(if (right) width * .35f else -width * .35f, 0f), 500)
                }
                rule.waitForIdle()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                val dir = File("build/reports/tests/inbox-swipe-shots").also { it.mkdirs() }
                File(dir, "${s.name}-${if (d) "dark" else "light"}-${if (right) "right" else "left"}.png").outputStream().use {
                    assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
                }
                row().performTouchInput { cancel() }
                rule.waitForIdle()
            }
        }
        assertEquals(0, count)
    }
}
