package persona.feature.settings

import android.content.Context
import android.content.res.Resources
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.HubSent
import persona.core.contracts.api.SentItem
import persona.core.contracts.api.SentLetThrough
import persona.core.contracts.api.SentOrigin
import persona.core.contracts.api.SentPage
import persona.core.contracts.api.SentRule
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import java.time.Instant
import java.time.ZoneOffset

private fun obj(json: String): Map<String, JsonElement> = JsonCodec.parseToJsonElement(json) as JsonObject

private fun item(
    id: Long = 1, connector: String = "mail", action: String = "mail_send", target: String = "{}", text: String = "",
    summary: String = "", origin: SentOrigin = SentOrigin(), through: SentLetThrough = SentLetThrough(),
) = SentItem(id, connector = connector, action = action, target = obj(target), text = text, summary = summary, origin = origin, letThrough = through)

private fun ids(items: List<SentItem>?): List<Long> = items.orEmpty().map { it.id }

private fun refused(status: Int, code: String? = null, message: String = "refused") = HubFailure(HubError.Server, message, status, code)

/**
 * Settings › Sent as you (#5278, docs/extensions/connectors.md "Sent as the user"), the cases of the web page's `sent.test.ts` and of iOS's
 * `SentModelTests`: where a send went, what it said, how the gate let it through, who asked, paging and refreshing the list,
 * the connector chips, the hub's refusals in plain words, and the model over a fake hub, including the reads that come back
 * late. (Reading the hub's JSON is in `SentWireTest` and `HttpHubSentTest`.) Made-up values; the words are in Simplified Chinese
 * (robolectric.properties).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class SentStateTest {
    private val res: Resources get() = ApplicationProvider.getApplicationContext<Context>().resources

    // ------------------------------------------------------------------ where it went

    @Test fun mailSaysTheRecipientsAndTheSubject() {
        val two = item(target = """{"to":["a@example.com","b@example.com"],"subject":"Hi","cc":["c@example.com"]}""")
        assertEquals("a@example.com, b@example.com · Hi", SentText.targetText(two))
        assertEquals("a@example.com", SentText.targetText(item(target = """{"to":["a@example.com"],"subject":""}""")))
    }

    @Test fun gitHubSaysTheRepoAndNumberAndAReviewKeepsItsEventForTheDetails() {
        val g = item(connector = "github", action = "github_review", target = """{"ref":"example-org/app#12","event":"APPROVE"}""")
        assertEquals("example-org/app#12", SentText.targetText(g))
        assertEquals(listOf(SentField("ref", "example-org/app#12"), SentField("event", "APPROVE")), SentText.targetFields(g.target))
    }

    @Test fun larkSaysTheConversationElseTheCallTheNodesGateSaw() {
        val both = item(connector = "lark", target = """{"conversation":"oc_1","method":"POST","path":"/im/v1/conversations/oc_1/messages"}""")
        assertEquals("oc_1", SentText.targetText(both))
        assertEquals("POST /docs/v1/comments", SentText.targetText(item(connector = "lark", target = """{"method":"POST","path":"/docs/v1/comments"}""")))
        assertEquals("—", SentText.targetText(item(connector = "lark")))
    }

    @Test fun anyOtherConnectorSaysTheCommonFieldsItNamedThreeAtMostInTheHubsOrder() {
        assertEquals("general · t-9", SentText.targetText(item(connector = "chatext", target = """{"thread":"t-9","chat":"general"}""")))
        assertEquals("c · s · r", SentText.targetText(item(connector = "chatext", target = """{"number":4,"repo":"r","subject":"s","chat":"c","extra":"x"}""")))
        assertEquals("ann, bo", SentText.targetText(item(connector = "chatext", target = """{"recipient":["ann","bo"]}""")))
    }

    @Test fun nothingToShowIsADash() {
        assertEquals("—", SentText.targetText(item(connector = "chatext", target = """{"arg_keys":["body","id"]}""")))
        assertEquals("—", SentText.targetText(item(connector = "chatext")))
        assertEquals("only", SentText.targetText(item(target = """{"subject":"only"}""")))
        assertEquals("—", SentText.targetText(item(connector = "github")))
    }

    @Test fun theDetailsListEveryNonEmptyFieldInAFixedOrderWithNumbersAsTheyAre() {
        val fields = SentText.targetFields(obj("""{"zzz":"last","to":["b","a"],"n":3,"big":12.0,"ok":false,"empty":"","none":null,"nested":{"b":1,"a":[1.5,"x"]},"list":[],"subject":" S ","cc":["c"]}"""))
        // where it went first (the order the hub's destination fields have, then the other mail and GitHub ones), then the rest by name
        assertEquals(listOf(
            SentField("to", "b, a"),
            SentField("subject", "S"),
            SentField("cc", "c"),
            SentField("big", "12"),
            SentField("n", "3"),
            SentField("nested", """{"a":[1.5,"x"],"b":1}"""),
            SentField("ok", "false"),
            SentField("zzz", "last"),
        ), fields)
        assertTrue(SentText.targetFields(emptyMap()).isEmpty())
        assertEquals("x", SentText.fieldText(JsonPrimitive("  x ")))
        assertEquals("1.5", SentText.fieldText(JsonPrimitive(1.5)))
        assertEquals("", SentText.fieldText(JsonNull))
        assertEquals("", SentText.fieldText(null))
        // a whole number is written in full as JavaScript writes it below 1e21, and zero has no sign
        assertEquals("12", SentText.fieldText(JsonPrimitive(12.0)))
        assertEquals("1000000000000000000", SentText.fieldText(JsonPrimitive(1e18)))
        assertEquals("0", SentText.fieldText(JsonPrimitive(-0.0)))
        assertEquals("-7", SentText.fieldText(JsonPrimitive(-7)))
        assertEquals("1e+300", SentText.fieldText(JsonPrimitive(1e300)))
        assertEquals("1.5e-7", SentText.fieldText(JsonPrimitive(1.5e-7)))
        assertEquals("0.000001", SentText.fieldText(JsonPrimitive(1e-6)))
        assertEquals("-2.5", SentText.fieldText(JsonPrimitive(-2.5)))
    }

    // ------------------------------------------------------------------ what it said

    @Test fun theRowShowsTheFirstTwoLinesBlankLinesAndCarriageReturnsSkipped() {
        assertEquals(listOf("one", "two"), SentText.previewLines(item(text = "one\r\n\r\ntwo\nthree")))
        assertEquals(listOf("only"), SentText.previewLines(item(text = "  only  ")))
        assertEquals(listOf("a", "b", "c"), SentText.previewLines(item(text = "a\nb\nc"), most = 3))
        // only \n and \r\n are line breaks, as on the web
        assertEquals(listOf("a\rb c"), SentText.previewLines(item(text = "a\rb c")))
    }

    @Test fun theSummaryStandsInWhenThereIsNoTextAndNothingWhenThereIsNeither() {
        assertEquals(listOf("mail_send to a@example.com"), SentText.previewLines(item(text = " \n ", summary = "mail_send to a@example.com")))
        assertTrue(SentText.previewLines(item()).isEmpty())
    }

    @Test fun aTextIsCutOnlyWhenItIsExactlyAsLongAsTheLedgerKeepsCountingCharacters() {
        assertTrue(SentText.textCut("x".repeat(4000)))
        assertFalse(SentText.textCut("x".repeat(3999)))
        // 3999 letters and one emoji are 4000 characters but 4001 UTF-16 units; 2000 emoji are 2000 characters
        assertTrue(SentText.textCut("x".repeat(3999) + "😀"))
        assertFalse(SentText.textCut("😀".repeat(2000)))
        assertFalse(SentText.textCut(""))
        assertEquals("4,000", SentText.textMost(res))
    }

    // ------------------------------------------------------------------ how the gate let it through

    @Test fun aRuleIsSaidAsOneWrittenTheWayConnectorGateHasIt() {
        val rule = SentRule("lark", "POST /im/v1/messages", obj("""{"kind":["a","b"],"chat_id":"oc_1"}"""), "allow")
        val allowed = SentText.allowedBy(res, SentLetThrough("rule", rule))
        assertEquals(SentAllowed("按规则", SentTone.Neutral, "lark · POST /im/v1/messages (chat_id=oc_1, kind=a, b) → allow"), allowed)
        assertEquals("github · github_comment → allow", SentText.ruleText(SentRule("github", "github_comment", decision = "allow")))
        assertEquals("", SentText.ruleText(null))
        // a rule the hub did not give is still a rule
        val bare = SentText.allowedBy(res, SentLetThrough("rule"))
        assertTrue(bare.label == "按规则" && bare.detail == "一条网关规则放行了它。")
    }

    @Test fun alwaysAllowedInThisChat() {
        val a = SentText.allowedBy(res, SentLetThrough("conversation_always"))
        assertTrue(a.label == "本对话始终允许" && a.tone == SentTone.Neutral)
    }

    @Test fun theUsersOwnAnswerOnACardSaysNoMoreThanThatTheyAgreedOnACard() {
        val a = SentText.allowedBy(res, SentLetThrough("card", interactionId = "ix-1"))
        assertEquals(SentAllowed("你在确认卡片上同意", SentTone.Neutral, "弹出了确认卡片，你点了允许。"), a)
        // the ledger can't say which device, so no device is named
        assertFalse((a.label + a.detail).contains("手机"))
    }

    @Test fun aCardTheAssistantAnsweredSaysSoInOrange() {
        val a = SentText.allowedBy(res, SentLetThrough("card", interactionId = "ix-1", answeredBy = "assistant"))
        assertTrue(a.label == "助手代答" && a.tone == SentTone.Orange)
    }

    @Test fun aCardAnyoneElseAnsweredNamesThemInOrangeAndIsNeverTheUsersConfirmation() {
        val other = SentText.allowedBy(res, SentLetThrough("card", answeredBy = "bot-x"))
        assertEquals(SentAllowed("由 bot-x 代答", SentTone.Orange, "确认卡片由 bot-x 作答。"), other)
        assertNotEquals(other.label, SentText.allowedBy(res, SentLetThrough("card")).label)
    }

    @Test fun anExtensionsOwnSendIsOrangeWithTheSettingThatLetIt() {
        val withSetting = SentText.allowedBy(res, SentLetThrough("auto", setting = "auto_reply"))
        assertTrue(withSetting.label == "扩展自行发送 · auto_reply" && withSetting.tone == SentTone.Orange)
        val plain = SentText.allowedBy(res, SentLetThrough("auto"))
        assertTrue(plain.label == "扩展自行发送" && plain.tone == SentTone.Orange)
    }

    @Test fun aKindThePageDoesNotKnowIsShownAsItIsAndNoneAtAllIsADash() {
        assertEquals(SentAllowed("voice_confirm", SentTone.Neutral, ""), SentText.allowedBy(res, SentLetThrough("voice_confirm")))
        assertEquals("—", SentText.allowedBy(res, SentLetThrough()).label)
    }

    // ------------------------------------------------------------------ who asked

    @Test fun aChatToOpenNeedsBothIds() {
        val both = SentText.originInfo(SentOrigin("mac", "s1"))
        assertTrue(both.hasChat && both.machine == "mac" && both.session == "s1" && both.extensionId == "")
        assertFalse(SentText.originInfo(SentOrigin(machineId = "mac")).hasChat)
        assertFalse(SentText.originInfo(SentOrigin(sessionId = "s1")).hasChat)
        assertFalse(SentText.originInfo(SentOrigin(machineId = " ", sessionId = "s1")).hasChat)
    }

    @Test fun anExtensionsOwnRecordSaysSoWithTheChatItNamesWhenItNamesOne() {
        val alone = SentText.originInfo(SentOrigin(extensionId = "examplechat"))
        assertTrue(!alone.hasChat && alone.extensionId == "examplechat")
        val both = SentText.originInfo(SentOrigin("mac", "s1", "examplechat"))
        assertTrue(both.session == "s1" && both.extensionId == "examplechat")
    }

    @Test fun anEmptyOriginIsNoChat() {
        val none = SentText.originInfo(SentOrigin())
        assertTrue(!none.hasChat && none.extensionId == "")
    }

    @Test fun whenItWasSentIsHowLongAgoAndInFullInTheDetails() {
        val now = Instant.parse("2026-11-05T12:30:00Z")
        assertEquals("30 分钟前", SentText.ago(res, "2026-11-05T12:00:00Z", now, ZoneOffset.UTC))
        assertEquals("刚刚", SentText.ago(res, "2026-11-05T12:30:00Z", now, ZoneOffset.UTC))
        assertEquals("5 小时前", SentText.ago(res, "2026-11-05T07:30:00Z", now, ZoneOffset.UTC))
        // no readable time: a dash
        assertEquals("—", SentText.ago(res, "", now))
        assertEquals("—", SentText.full(res, "yesterday"))
        assertTrue(SentText.full(res, "2026-11-05T12:00:05Z", ZoneOffset.UTC).contains("12:00:05"))
    }

    /** As web's `ago`: days by the local calendar up to six, then the date (a send from 80 days back does not say "80 天前"). */
    @Test fun daysAreCountedByTheCalendarUpToSixAndThenItIsTheDate() {
        val now = Instant.parse("2026-11-05T12:30:00Z")
        assertEquals("2 天前", SentText.ago(res, "2026-11-03T12:00:00Z", now, ZoneOffset.UTC))
        assertEquals("6 天前", SentText.ago(res, "2026-10-30T12:00:00Z", now, ZoneOffset.UTC))
        assertEquals("10月29日", SentText.ago(res, "2026-10-29T12:00:00Z", now, ZoneOffset.UTC))
        assertEquals("8月17日", SentText.ago(res, "2026-08-17T12:00:00Z", now, ZoneOffset.UTC))
        // Yesterday 23:00 seen at 23:30 tonight is a day ago; 23:50 is not yet 24 hours back: hours.
        val late = Instant.parse("2026-11-05T23:30:00Z")
        assertEquals("1 天前", SentText.ago(res, "2026-11-04T23:00:00Z", late, ZoneOffset.UTC))
        assertEquals("23 小时前", SentText.ago(res, "2026-11-04T23:50:00Z", late, ZoneOffset.UTC))
        // The calendar is the phone's: 01:00 on the 5th and 01:00 on the 6th in UTC+8 are a day apart though 24 hours is all it is.
        assertEquals("1 天前", SentText.ago(res, "2026-11-04T17:00:00Z", Instant.parse("2026-11-05T17:00:00Z"), ZoneOffset.ofHours(8)))
        // Never "0 天前" for a gap of a day or more.
        assertEquals("1 天前", SentText.ago(res, "2026-11-04T12:00:00Z", Instant.parse("2026-11-05T12:00:00Z"), ZoneOffset.UTC))
    }

    // ------------------------------------------------------------------ the list

    @Test fun mergesByIdOneRowEachTheNewerCopyWinsNewestFirst() {
        val merged = SentText.merge(listOf(item(9), item(5, summary = "old"), item(2)), listOf(item(5, summary = "new"), item(12)))
        assertEquals(listOf(12L, 9L, 5L, 2L), ids(merged))
        assertEquals("new", merged[2].summary)
    }

    @Test fun aRefreshWithNothingOlderLoadedIsThePageAlone() {
        val page = SentPage(listOf(item(6), item(5)), next = 5)
        assertEquals(SentRefresh(page.items, SentRefresh.Cursor.Set(5), false), SentText.refreshed(listOf(item(5), item(4)), page, loadedOlder = false))
    }

    @Test fun aRefreshWithOlderPagesLoadedMergesThePageInAndKeepsTheOlderEndsCursor() {
        val have = listOf(100L, 99L, 51L, 50L, 3L).map { item(it) }
        val out = SentText.refreshed(have, SentPage(listOf(item(102), item(101), item(100)), next = 100), loadedOlder = true)
        assertEquals(listOf(102L, 101L, 100L, 99L, 51L, 50L, 3L), ids(out.items))
        assertTrue(out.next == SentRefresh.Cursor.Keep && out.loadedOlder)
    }

    @Test fun aRefreshWhosePageDoesNotReachTheNewestRowHeldStartsOver() {
        val page = SentPage(listOf(item(160), item(159)), next = 159)
        assertEquals(SentRefresh(page.items, SentRefresh.Cursor.Set(159), false), SentText.refreshed(listOf(item(100), item(99)), page, loadedOlder = true))
    }

    @Test fun aFirstPageThatIsTheWholeLedgerMergesWhateverItsIdsAndAnEmptyOneEmptiesTheList() {
        val have = listOf(item(100), item(50))
        val whole = SentText.refreshed(have, SentPage(listOf(item(200)), next = null), loadedOlder = true)
        assertEquals(listOf(200L, 100L, 50L), ids(whole.items))
        assertEquals(SentRefresh(emptyList(), SentRefresh.Cursor.Set(null), false), SentText.refreshed(have, SentPage(), loadedOlder = true))
    }

    // ------------------------------------------------------------------ the connector chips

    @Test fun theChipsAreLarkThenTheConnectorsRowsShowedThenTheChosenOneNoneTwice() {
        val all = SentText.connectorChoices(listOf("mail", "github", "goneext"), "other")
        assertEquals(listOf("lark", "mail", "github", "goneext", "other"), all)
        assertEquals(listOf("飞书", "邮件", "GitHub", "goneext", "other"), all.map { SentText.connectorName(res, it) })
        assertEquals(listOf("lark", "mail"), SentText.connectorChoices(listOf("lark", "mail"), "mail"))
    }

    @Test fun onlyLarkIsOfferedBeforeAnyRowHasShownSoAConnectorThatOnlyReadsHasNoChip() {
        assertEquals(listOf("lark"), SentText.connectorChoices(emptyList(), null))
    }

    @Test fun theChosenConnectorsChipStaysWhileItIsChosenThoughNoRowHasShownIt() {
        assertEquals(listOf("lark", "mail"), SentText.connectorChoices(emptyList(), "mail"))
    }

    @Test fun mailGitHubAndLarkAreNamedByTheirUsualWordsAnyOtherConnectorByItsId() {
        assertEquals("邮件", SentText.connectorName(res, "mail"))
        assertEquals("GitHub", SentText.connectorName(res, "github"))
        assertEquals("飞书", SentText.connectorName(res, "lark"))
        assertEquals("xext", SentText.connectorName(res, "xext"))
    }

    @Test fun collectsTheConnectorsRowsShowInTheOrderTheyFirstShowed() {
        assertEquals(listOf("mail"), SentText.seenConnectors(listOf("mail"), listOf(item(connector = "mail"))))
        val rows = listOf(item(connector = "github"), item(connector = "mail"), item(connector = "github"), item(connector = ""))
        assertEquals(listOf("mail", "github"), SentText.seenConnectors(listOf("mail"), rows))
    }

    // ------------------------------------------------------------------ the hub's refusals

    private fun said(error: Throwable) = SentText.problem(error).let { it.kind to it.text.resolve(res) }

    @Test fun noDatabaseIsSaidAsThatNotAsTheHubBeingUnavailable() {
        assertEquals(SentProblem.Kind.NoDatabase to "这台 hub 没有数据库，所以不记录以你的名义发出的内容。",
            said(refused(503, "no_database", "no database: there is no ledger")))
    }

    @Test fun a503WithNoSuchCodeIsTheUsualError() {
        assertEquals(SentProblem.Kind.Error to "读取失败：bad gateway", said(refused(503, "node_offline", "bad gateway")))
        assertEquals(SentProblem.Kind.Error, SentText.problem(refused(503)).kind)
    }

    @Test fun a403IsADeviceWithoutFullAccess() {
        val (kind, text) = said(refused(403, "forbidden", "only a device with full rights"))
        assertTrue(kind == SentProblem.Kind.Forbidden && text.contains("完整权限"))
        // a 403 is that whatever its code says
        assertEquals(SentProblem.Kind.Forbidden, SentText.problem(refused(403)).kind)
    }

    @Test fun a404WithNoCodeIsAHubFromBeforeTheLedgerAndOneWithACodeIsAnOrdinaryFailure() {
        val (kind, text) = said(refused(404, null, "404 page not found"))
        assertTrue(kind == SentProblem.Kind.Unsupported && text.contains("升级 hub"))
        assertEquals(SentProblem.Kind.Error, SentText.problem(refused(404, "not_found", "no such thing")).kind)
    }

    @Test fun anythingElseIsTheUsualErrorTextAndNoNetworkSaysItCannotReachTheHub() {
        assertEquals(SentProblem.Kind.Error to "读取失败：boom", said(refused(500, "internal", "boom")))
        assertEquals(SentProblem.Kind.Error to "读取失败：连不上 hub", said(HubFailure(HubError.NoNetwork)))
        // the status is read from a failure wrapped in another
        assertEquals(SentProblem.Kind.NoDatabase, SentText.problem(RuntimeException("wrapped", refused(503, "no_database"))).kind)
    }

    // ------------------------------------------------------------------ the model, over a fake hub

    /**
     * The ledger a fake hub answers from: rows newest first, three to a page, as the hub pages them. The n-th request (1 is the
     * first) can be held: it is answered at once as the hub would then (a failure too), but only returned once the test lets it go.
     */
    private class Hub : HubSent {
        var rows: List<Pair<Long, String>> = emptyList()
        var fail: ((before: Long?, connector: String?) -> Exception?)? = null
        /** A held read that is cancelled ends as OkHttp's does, an IOException ("Canceled"), not a CancellationException. */
        var cancelAsFailure = false
        val asked = mutableListOf<Triple<Long?, Int?, String?>>()
        private var count = 0
        private val holds = mutableSetOf<Int>()
        private val reached = mutableMapOf<Int, CompletableDeferred<Unit>>()
        private val opened = mutableMapOf<Int, CompletableDeferred<Unit>>()

        /** Rows taken back, and what a recall answers instead (a failure, thrown). */
        val recalled = mutableSetOf<Long>()
        var recallFail: Exception? = null

        fun set(vararg ids: Long, connector: String = "mail") { rows = ids.map { it to connector } }
        fun hold(vararg n: Int) { holds += n.toSet() }
        suspend fun waitReached(n: Int) = reached.getOrPut(n) { CompletableDeferred() }.await()
        fun open(n: Int) { opened.getOrPut(n) { CompletableDeferred() }.complete(Unit) }

        private fun answer(before: Long?, connector: String?): SentPage {
            fail?.invoke(before, connector)?.let { throw it }
            val hit = rows.filter { (before == null || it.first < before) && (connector.isNullOrEmpty() || it.second == connector) }
            val got = hit.take(PageSize)
            val items = got.map { row(it.first, it.second) }
            return SentPage(items, next = if (hit.size > PageSize) got.last().first else null)
        }

        private fun row(id: Long, connector: String) = SentItem(id, at = "2026-11-05T12:00:00Z", connector = connector, action = "a", text = "row $id",
            recalledAt = if (id in recalled) "2026-11-05T12:05:00Z" else "", canRecall = id !in recalled)

        override suspend fun recall(id: Long): SentItem? {
            recallFail?.let { throw it }
            recalled += id
            return row(id, rows.first { it.first == id }.second)
        }

        override suspend fun page(before: Long?, limit: Int?, connector: String?): SentPage {
            asked += Triple(before, limit, connector)
            val n = ++count
            val result = runCatching { answer(before, connector) }
            if (n in holds) {
                reached.getOrPut(n) { CompletableDeferred() }.complete(Unit)
                // a read the page gave up on ends as a cancelled request does
                try { opened.getOrPut(n) { CompletableDeferred() }.await() }
                catch (cancelled: CancellationException) { if (cancelAsFailure) throw java.io.IOException("Canceled") else throw cancelled }
            }
            return result.getOrThrow()
        }

        companion object { const val PageSize = 3 }
    }

    @Test fun readsTheFirstPageThenOlderOnesToTheEnd() = runBlocking {
        val hub = Hub().apply { set(7, 6, 5, 4, 3, 2, 1) }
        val page = SentModel(hub)
        assertTrue(page.state.value.items == null && !page.state.value.showsChips)
        page.load()
        assertTrue(ids(page.state.value.items) == listOf(7L, 6L, 5L) && page.state.value.next == 5L)
        page.loadMore()
        assertTrue(ids(page.state.value.items) == listOf(7L, 6L, 5L, 4L, 3L, 2L) && page.state.value.next == 2L)
        page.loadMore()
        assertTrue(ids(page.state.value.items) == listOf(7L, 6L, 5L, 4L, 3L, 2L, 1L) && page.state.value.next == null)
        assertEquals(listOf(null, 5L, 2L), hub.asked.map { it.first })
        // nothing older: Load earlier does nothing
        page.loadMore()
        assertEquals(3, hub.asked.size)
    }

    @Test fun aRowThatComesTwiceShowsOnce() = runBlocking {
        var second = false
        val api = object : HubSent {
            override suspend fun page(before: Long?, limit: Int?, connector: String?) =
                if (second) SentPage(listOf(SentItem(4, text = "again"), SentItem(3)), null)
                else SentPage(listOf(SentItem(6), SentItem(5), SentItem(4, text = "first")), 4)
            override suspend fun recall(id: Long): SentItem? = null
        }
        val page = SentModel(api)
        page.load()
        second = true
        page.loadMore()
        assertEquals(listOf(6L, 5L, 4L, 3L), ids(page.state.value.items))
        // the newer copy of the row that came twice is the one shown
        assertEquals("again", page.state.value.items?.first { it.id == 4L }?.text)
    }

    @Test fun aRecallShowsTheRowTheHubAnswersAndReadsTheListAgain() = runBlocking {
        val hub = Hub().apply { set(3, 2, 1) }
        val page = SentModel(hub)
        page.load()
        assertTrue(page.state.value.items.orEmpty().all { it.canRecall })
        page.recall(2)
        val row = page.state.value.items?.first { it.id == 2L }
        assertTrue(row != null && !row.canRecall && row.recalledAt.isNotEmpty())
        assertTrue(page.state.value.recalling == null && page.state.value.recallError == null)
        assertEquals(2, hub.asked.size) // the first page again
    }

    @Test fun aRefusedRecallSaysWhyForThatRowAndReadsTheListAgain() = runBlocking {
        val hub = Hub().apply { set(3, 2, 1); recallFail = refused(409, "too_old", "too old") }
        val page = SentModel(hub)
        page.load()
        page.recall(2)
        assertEquals(2L, page.state.value.recallError?.id)
        assertEquals("这条消息发出太久，已不能撤回。", page.state.value.recallError?.text?.resolve(res))
        assertTrue(page.state.value.recalling == null && hub.recalled.isEmpty())
        assertEquals(2, hub.asked.size)
        // a new attempt clears the old reason
        hub.recallFail = null
        page.recall(2)
        assertNull(page.state.value.recallError)
    }

    @Test fun aRecallRefusalIsInPlainWordsAndOtherwiseWhatTheHubSaid() {
        fun said(error: Exception) = SentText.recallProblem(error).resolve(res)
        assertEquals("这条消息已经撤回过了。", said(refused(409, "already_recalled")))
        assertEquals("不确定消息是否已被删除。可以再试一次确认。", said(refused(504, "recall_unknown")))
        assertEquals("只有你自己的、拥有完整权限的设备才能撤回消息。", said(refused(403, "own_device_only")))
        assertEquals("这个 Hub 的版本还不能撤回消息。请更新 Hub 后再试。", said(refused(404)))
        assertEquals("消息没有撤回：GitHub said no", said(refused(502, "recall_refused", "GitHub said no")))
        assertEquals("消息没有撤回：a new reason", said(refused(409, "something_new", "a new reason")))
    }

    @Test fun theChipsAreWhatTheLoadedRowsShowedAndChoosingOneReadsItsRowsOnly() = runBlocking {
        val hub = Hub().apply { rows = listOf(6L to "mail", 5L to "github", 4L to "mail", 3L to "github", 2L to "mail") }
        val page = SentModel(hub)
        page.load()
        assertTrue(page.state.value.showsChips && page.state.value.choices == listOf("lark", "mail", "github"))
        page.choose("github")
        assertTrue(page.state.value.filter == "github" && ids(page.state.value.items) == listOf(5L, 3L))
        assertEquals("github", hub.asked.last().third)
        // the chosen one again goes back to All
        page.choose("github")
        assertTrue(page.state.value.filter == null && ids(page.state.value.items) == listOf(6L, 5L, 4L))
        assertNull(hub.asked.last().third)
    }

    @Test fun loadEarlierKeepsTheChosenConnector() = runBlocking {
        val hub = Hub().apply { rows = (9L downTo 1L).map { it to if (it % 2 == 0L) "github" else "mail" } }
        val page = SentModel(hub)
        page.load()
        page.choose("github") // 8, 6, 4 and then 2
        assertTrue(ids(page.state.value.items) == listOf(8L, 6L, 4L) && page.state.value.next == 4L)
        page.loadMore()
        assertTrue(ids(page.state.value.items) == listOf(8L, 6L, 4L, 2L) && page.state.value.next == null)
        assertEquals(4L to "github", hub.asked.last().let { it.first to it.third })
    }

    @Test fun aFilteredListThatIsEmptyKeepsItsChipSoThereIsAWayBack() = runBlocking {
        val hub = Hub().apply { set(3, 2, 1) }
        val page = SentModel(hub)
        page.load()
        page.choose("lark")
        assertTrue(page.state.value.items?.isEmpty() == true && page.state.value.filter == "lark" && page.state.value.showsChips)
        page.choose(null)
        assertEquals(listOf(3L, 2L, 1L), ids(page.state.value.items))
    }

    @Test fun readingAgainKeepsTheOlderPagesAndTheirCursorAndAddsWhatCameIn() = runBlocking {
        val hub = Hub().apply { set(6, 5, 4, 3, 2, 1) }
        val page = SentModel(hub)
        page.load()
        page.loadMore()
        assertTrue(ids(page.state.value.items) == listOf(6L, 5L, 4L, 3L, 2L, 1L) && page.state.value.next == null)
        hub.set(7, 6, 5, 4, 3, 2, 1)
        page.load()
        assertTrue(ids(page.state.value.items) == listOf(7L, 6L, 5L, 4L, 3L, 2L, 1L) && page.state.value.next == null)
    }

    @Test fun readingAgainWithNothingOlderLoadedLetsPrunedRowsGo() = runBlocking {
        val hub = Hub().apply { set(3, 2, 1) }
        val page = SentModel(hub)
        page.load()
        hub.set(3, 2)
        page.load()
        assertEquals(listOf(3L, 2L), ids(page.state.value.items))
    }

    @Test fun theHubsRefusalsAreTheProblemAndAFailedFirstReadShowsNoChipsUnderAll() = runBlocking {
        val hub = Hub().apply { fail = { _, _ -> refused(503, "no_database") } }
        val page = SentModel(hub)
        page.load()
        assertTrue(page.state.value.problem?.kind == SentProblem.Kind.NoDatabase && page.state.value.items == null && !page.state.value.showsChips)
        // under a chip they stay, so a failed read can be left by choosing another one or All
        page.choose("mail")
        assertTrue(page.state.value.problem?.kind == SentProblem.Kind.NoDatabase && page.state.value.showsChips)
        page.choose(null)
        assertNull(page.state.value.filter)
    }

    @Test fun aHubFromBeforeTheLedgerAnd403() = runBlocking {
        val old = SentModel(Hub().apply { fail = { _, _ -> refused(404, null, "404 page not found") } })
        old.load()
        assertEquals(SentProblem.Kind.Unsupported, old.state.value.problem?.kind)

        val page = SentModel(Hub().apply { fail = { _, _ -> refused(403, "forbidden") } })
        page.load()
        assertEquals(SentProblem.Kind.Forbidden, page.state.value.problem?.kind)
    }

    @Test fun aListThatIsThereStaysWhenALaterReadFailsAndRetryReadsAgain() = runBlocking {
        val hub = Hub().apply { set(3, 2, 1) }
        var failing = true
        hub.fail = { _, _ -> if (failing) refused(500, null, "boom") else null }
        val page = SentModel(hub)
        page.load()
        assertTrue(page.state.value.problem?.kind == SentProblem.Kind.Error && page.state.value.items == null)
        failing = false
        page.load() // Retry
        assertTrue(page.state.value.problem == null && ids(page.state.value.items) == listOf(3L, 2L, 1L))
        failing = true
        page.load()
        assertTrue(page.state.value.problem == null && ids(page.state.value.items) == listOf(3L, 2L, 1L))
    }

    @Test fun retryGoesBackToTheSpinnerThenReadsAgain() = runBlocking {
        val hub = Hub().apply { set(3, 2, 1) }
        var failing = true
        hub.fail = { _, _ -> if (failing) refused(500, null, "boom") else null }
        hub.hold(2)
        val page = SentModel(hub)
        page.load()
        assertEquals(SentProblem.Kind.Error, page.state.value.problem?.kind)
        failing = false
        val again = launch { page.retry() } // request 2, held
        hub.waitReached(2)
        assertTrue("the failure stays over the spinner", page.state.value.problem == null && page.state.value.items == null)
        hub.open(2)
        again.join()
        assertTrue(page.state.value.problem == null && ids(page.state.value.items) == listOf(3L, 2L, 1L))
    }

    @Test fun aFailedLoadEarlierSaysWhyAndKeepsTheList() = runBlocking {
        val hub = Hub().apply { set(6, 5, 4, 3) }
        var failing = false
        hub.fail = { before, _ -> if (failing && before != null) refused(500, null, "boom") else null }
        val page = SentModel(hub)
        page.load()
        failing = true
        page.loadMore()
        val state = page.state.value
        assertTrue(state.moreError?.resolve(res) == "读取失败：boom" && ids(state.items) == listOf(6L, 5L, 4L) && state.next == 4L && !state.loadingMore)
        failing = false
        page.loadMore()
        assertTrue(page.state.value.moreError == null && ids(page.state.value.items) == listOf(6L, 5L, 4L, 3L))
    }

    // ------------------------------------------------------------------ reads that come back late

    @Test fun aReadStartedForAnEarlierFilterIsDroppedWhenItComesBack() = runBlocking {
        val hub = Hub().apply { rows = listOf(6L to "mail", 5L to "github", 4L to "mail", 3L to "github"); hold(1) }
        val page = SentModel(hub)
        val slow = launch { page.load() } // the whole ledger, held
        hub.waitReached(1)
        page.choose("github")
        assertTrue(page.state.value.filter == "github" && ids(page.state.value.items) == listOf(5L, 3L))
        hub.open(1)
        slow.join()
        assertTrue(page.state.value.filter == "github" && ids(page.state.value.items) == listOf(5L, 3L) && page.state.value.problem == null)
    }

    /** The same with the chip's own read still on its way when the earlier one comes back: still dropped, not shown under the chip. */
    @Test fun aReadStartedForAnEarlierFilterIsDroppedThoughTheChipsReadIsNotInYet() = runBlocking {
        val hub = Hub().apply { rows = listOf(6L to "mail", 5L to "github", 4L to "mail", 3L to "github"); hold(1, 2) }
        val page = SentModel(hub)
        val slow = launch { page.load() } // the whole ledger, held
        hub.waitReached(1)
        val choosing = launch { page.choose("github") } // request 2, held
        hub.waitReached(2)
        hub.open(1)
        slow.join()
        assertTrue(page.state.value.filter == "github" && page.state.value.items == null && page.state.value.problem == null)
        hub.open(2)
        choosing.join()
        assertEquals(listOf(5L, 3L), ids(page.state.value.items))
    }

    /** A newer read that was cancelled (its task went with the page) must not strand an older one that is good. */
    @Test fun aCancelledNewerReadDoesNotStrandAnOlderGoodOne() = runBlocking {
        val hub = Hub().apply { set(3, 2, 1); hold(1, 2) }
        val page = SentModel(hub)
        val first = launch { page.load() }
        hub.waitReached(1)
        val second = launch { page.load() }
        hub.waitReached(2)
        second.cancel()
        hub.open(2)
        second.join()
        assertTrue("a cancelled read says nothing", page.state.value.items == null && page.state.value.problem == null)
        hub.open(1)
        first.join()
        assertTrue(ids(page.state.value.items) == listOf(3L, 2L, 1L) && page.state.value.problem == null)
    }

    @Test fun aReadOfTheFirstPageThatANewerOneSupersededIsDropped() = runBlocking {
        val hub = Hub().apply { set(3, 2, 1); hold(1) }
        val page = SentModel(hub)
        val slow = launch { page.load() } // answered with [3, 2, 1], held
        hub.waitReached(1)
        hub.set(4, 3, 2, 1)
        page.load()
        assertEquals(listOf(4L, 3L, 2L), ids(page.state.value.items))
        hub.open(1)
        slow.join()
        assertEquals(listOf(4L, 3L, 2L), ids(page.state.value.items))
    }

    @Test fun olderRowsReadForACursorTheListHasMovedOnFromAreDropped() = runBlocking {
        val hub = Hub().apply { set(9, 8, 7, 6, 5, 4); hold(2) }
        val page = SentModel(hub)
        page.load() // request 1: [9, 8, 7], next 7
        assertEquals(7L, page.state.value.next)
        val more = launch { page.loadMore() } // request 2: [6, 5, 4] for before=7, held
        hub.waitReached(2)
        assertTrue(page.state.value.loadingMore)
        hub.set(12, 11, 10, 9, 8, 7, 6, 5, 4)
        page.load() // request 3: [12, 11, 10]: the older page was not in yet, so the list is this page alone
        assertTrue(ids(page.state.value.items) == listOf(12L, 11L, 10L) && page.state.value.next == 10L)
        hub.open(2)
        more.join()
        assertTrue(ids(page.state.value.items) == listOf(12L, 11L, 10L) && page.state.value.next == 10L && !page.state.value.loadingMore)
    }

    /** Two reads of the first page in flight, the older back first: it shows until the newer comes, which then replaces it. */
    @Test fun aNewerReadThatComesBackAfterAnOlderOneIsTaken() = runBlocking {
        val hub = Hub().apply { set(3, 2, 1); hold(1, 2) }
        val page = SentModel(hub)
        val first = launch { page.load() } // answered with [3, 2, 1], held
        hub.waitReached(1)
        hub.set(4, 3, 2, 1)
        val second = launch { page.load() } // answered with [4, 3, 2], held
        hub.waitReached(2)
        hub.open(1)
        first.join()
        assertEquals(listOf(3L, 2L, 1L), ids(page.state.value.items))
        hub.open(2)
        second.join()
        assertTrue(ids(page.state.value.items) == listOf(4L, 3L, 2L) && page.state.value.next == 2L)
    }

    /** Older rows read under All come back after a chip was chosen whose list has the same cursor: not the chip's rows. */
    @Test fun olderRowsReadUnderAnEarlierFilterAreDroppedThoughTheCursorIsTheSame() = runBlocking {
        val hub = Hub().apply {
            rows = listOf(9L to "mail", 8L to "mail", 7L to "mail", 6L to "github", 5L to "github", 4L to "github", 2L to "mail", 1L to "mail")
            hold(2)
        }
        val page = SentModel(hub)
        page.load() // request 1: [9, 8, 7], next 7
        val more = launch { page.loadMore() } // request 2: [6, 5, 4] (github) for before=7, held
        hub.waitReached(2)
        page.choose("mail") // request 3: [9, 8, 7], next 7 again
        assertTrue(ids(page.state.value.items) == listOf(9L, 8L, 7L) && page.state.value.next == 7L)
        hub.open(2)
        more.join()
        val state = page.state.value
        assertTrue(state.filter == "mail" && ids(state.items) == listOf(9L, 8L, 7L) && state.next == 7L && !state.loadingMore)
    }

    /** Choosing a chip starts the list over: the spinner, not the rows of the connector before, until its rows come in. */
    @Test fun choosingAChipEmptiesTheListUntilItsRowsComeIn() = runBlocking {
        val hub = Hub().apply { rows = listOf(4L to "mail", 3L to "github", 2L to "mail"); hold(2) }
        val page = SentModel(hub)
        page.load()
        assertEquals(listOf(4L, 3L, 2L), ids(page.state.value.items))
        val choosing = launch { page.choose("github") } // request 2, held
        hub.waitReached(2)
        assertTrue(page.state.value.filter == "github" && page.state.value.items == null && page.state.value.problem == null)
        hub.open(2)
        choosing.join()
        assertEquals(listOf(3L), ids(page.state.value.items))
    }

    /** The list under All was read; a chip whose read fails says why, not a spinner for good. */
    @Test fun aChipWhoseFirstReadFailsSaysWhyAfterAllWasRead() = runBlocking {
        val hub = Hub().apply {
            rows = listOf(4L to "mail", 3L to "github")
            fail = { _, connector -> if (connector == "github") refused(500, null, "boom") else null }
        }
        val page = SentModel(hub)
        page.load()
        assertEquals(listOf(4L, 3L), ids(page.state.value.items))
        page.choose("github")
        assertTrue(page.state.value.problem?.kind == SentProblem.Kind.Error && page.state.value.items == null && page.state.value.showsChips)
    }

    /** A read under All that fails after a chip was chosen says nothing: the chip's own read is still on its way. */
    @Test fun aFailedReadStartedForAnEarlierFilterSaysNothing() = runBlocking {
        val hub = Hub().apply {
            rows = listOf(4L to "mail", 3L to "github")
            fail = { _, connector -> if (connector == null) refused(500, null, "boom") else null }
            hold(1, 2)
        }
        val page = SentModel(hub)
        val all = launch { page.load() } // request 1: fails, held
        hub.waitReached(1)
        val choosing = launch { page.choose("github") } // request 2, held
        hub.waitReached(2)
        hub.open(1)
        all.join()
        assertTrue(page.state.value.filter == "github" && page.state.value.problem == null && page.state.value.items == null)
        hub.open(2)
        choosing.join()
        assertTrue(page.state.value.problem == null && ids(page.state.value.items) == listOf(3L))
    }


    /** A read cancelled with the page can come back from the transport as an ordinary failure: that says nothing either. */
    @Test fun aReadCancelledAndEndedAsAnOrdinaryFailureSaysNothing() = runBlocking {
        val hub = Hub().apply { set(3, 2, 1); hold(1); cancelAsFailure = true }
        val page = SentModel(hub)
        val first = launch { page.load() }
        hub.waitReached(1)
        first.cancel()
        first.join()
        assertTrue(first.isCancelled)
        assertTrue("no failure is shown for it", page.state.value.problem == null && page.state.value.items == null)
    }

    @Test fun aLoadEarlierCancelledAndEndedAsAnOrdinaryFailureSaysNothingAndIsNotLoadingAnyMore() = runBlocking {
        val hub = Hub().apply { set(6, 5, 4, 3); hold(2); cancelAsFailure = true }
        val page = SentModel(hub)
        page.load()
        val more = launch { page.loadMore() }
        hub.waitReached(2)
        assertTrue(page.state.value.loadingMore)
        more.cancel()
        more.join()
        val state = page.state.value
        assertTrue(more.isCancelled)
        assertTrue("no failure and no spinner", state.moreError == null && !state.loadingMore && ids(state.items) == listOf(6L, 5L, 4L) && state.next == 4L)
    }

    /** A second Load earlier while one is on its way does nothing: one page is asked for, whoever taps. */
    @Test fun aSecondLoadEarlierWhileOneIsOnItsWayAsksForNothing() = runBlocking {
        val hub = Hub().apply { set(9, 8, 7, 6, 5, 4, 3, 2, 1); hold(2) }
        val page = SentModel(hub)
        page.load() // request 1: [9, 8, 7], next 7
        val first = launch { page.loadMore() } // request 2: [6, 5, 4], held
        hub.waitReached(2)
        page.loadMore() // while the first is on its way
        assertEquals(2, hub.asked.size)
        hub.open(2)
        first.join()
        assertEquals(listOf(9L, 8L, 7L, 6L, 5L, 4L), ids(page.state.value.items))
        assertEquals(listOf(null, 7L), hub.asked.map { it.first })
    }

    /** Load earlier is asked with the cursor and the connector, nothing else. */
    @Test fun loadEarlierAsksOnlyForTheCursorAndTheConnector() = runBlocking {
        val hub = Hub().apply { set(7, 6, 5, 4) }
        val page = SentModel(hub)
        page.load()
        page.loadMore()
        assertEquals(listOf(Triple<Long?, Int?, String?>(null, null, null), Triple(5L, null, null)), hub.asked)
    }
}
