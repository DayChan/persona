package persona.feature.inbox

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.yield
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.core.contracts.InboxEventFields
import persona.core.domain.inbox.InboxItem

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], qualifiers = "en")
class InboxPoliciesTest {
    private fun json(text: String) = Json.parseToJsonElement(text).jsonObject

    private val repo = InboxMuteOption("repo", "o/r", "o/r")
    private val chat = InboxMuteOption("chat", "c1", "Design")
    private val sender = InboxMuteOption("sender", "u1", "Alex")

    private fun policy(id: String, tag: String, value: String, source: String = "github", tier: InboxPolicyTier = InboxPolicyTier.Fyi,
        twin: InboxPolicyTwin = InboxPolicyTwin.None) = InboxPolicy(id, source, tag = tag, value = value, name = value, tier = tier, twin = twin)

    @Test fun policiesComeFromTheHubNewestFirstByTime() {
        val policies = inboxPolicies(json("""{"policies":[
            {"id":"a","source":"github","source_name":"GitHub","tag":"repo","value":"o/r","name":"o/r","tier":"noise","created_at":"2026-03-01T10:00:00Z"},
            {"id":"b","source":"lark","tag":"chat","value":"c1","twin":"first","created_at":"2026-03-01T10:00:00.5Z","updated_at":"2026-03-02T00:00:00Z"},
            {"id":"c","source":"lark","tag":"sender","value":"u1","name":"Alex","tier":"action","twin":"never","created_at":"2026-03-01T11:30:00+02:00"},
            {"id":"d","source":"mail","tag":"sender","value":"x","tier":"later"},
            {"source":"github","tag":"repo","value":"none"}]}"""))
        // Text order would put "10:00:00Z" before "10:00:00.5Z", and the +02:00 one (09:30Z) first.
        assertEquals(listOf("b", "a", "c", "d"), policies.map { it.id })
        val (b, a, c, d) = policies
        assertEquals(InboxPolicy("a", "github", "GitHub", "repo", "o/r", "o/r", InboxPolicyTier.Noise, InboxPolicyTwin.None, "2026-03-01T10:00:00Z", null), a)
        assertEquals("c1", b.name)
        assertEquals(InboxPolicyTier.None, b.tier)
        assertEquals(InboxPolicyTwin.First, b.twin)
        assertEquals("2026-03-02T00:00:00Z", b.updatedAt)
        assertEquals(InboxPolicyTier.Action to InboxPolicyTwin.Never, c.tier to c.twin)
        assertEquals(InboxPolicyTier.None, d.tier)
        assertEquals(listOf(R.string.inbox_policy_tier_action, R.string.inbox_policy_twin_never), c.summary)
        assertEquals(listOf(R.string.inbox_policy_twin_first), b.summary)
        assertEquals(InboxMuteOption("chat", "c1", "c1"), b.option)
        assertTrue(inboxPolicies(json("{}")).isEmpty())
        assertNull(inboxPolicy(json("""{"id":"","tag":"repo"}""")))
    }

    @Test fun theMenuNamesARuleForEachKind() {
        assertEquals(R.string.inbox_policy_repo, repo.policyTitle)
        assertEquals(R.string.inbox_policy_chat, chat.policyTitle)
        assertEquals(R.string.inbox_policy_sender, sender.policyTitle)
    }

    @Test fun theSendersRuleBeatsTheChatsAndTheChatsTheRepositorys() {
        val record = InboxRecord(InboxItem("1"), source = "github", mutes = listOf(repo, chat, sender))
        val forRepo = policy("r", "repo", "o/r")
        val forChat = policy("c", "chat", "c1")
        val forSender = policy("s", "sender", "u1")
        assertEquals(forSender, policyFor(record, listOf(forRepo, forChat, forSender)))
        assertEquals(forChat, policyFor(record, listOf(forRepo, forChat)))
        assertEquals(forRepo, policyFor(record, listOf(forRepo)))
        // Another source's, or another value's, is not this item's.
        assertNull(policyFor(record, listOf(policy("x", "sender", "u1", source = "lark"), policy("y", "repo", "o/other"))))
        assertNull(policyFor(record.copy(mutes = emptyList()), listOf(forSender)))
    }

    @Test fun aRulesTierReachesTheOpenTriagedItemsItCoversAtOnce() {
        fun rec(id: String, source: String = "github", tier: String? = "action", status: String? = "action", done: Boolean = false,
            urgent: Boolean = false, mutes: List<InboxMuteOption> = listOf(repo), manager: String? = null) =
            InboxRecord(InboxItem(id, tier = tier, status = status, urgent = urgent, doneAt = if (done) "2026-01-01T00:00:00Z" else null),
                source = source, mutes = mutes, event = InboxEventFields(status = status, urgent = urgent), manager = manager)
        val records = listOf(
            rec("open", urgent = true),
            rec("no-status", status = null),
            rec("working", status = InboxItem.WORKING),
            rec("untriaged", tier = null, status = null),
            rec("done", done = true),
            rec("other-source", source = "lark"),
            rec("other-value", mutes = listOf(InboxMuteOption("repo", "o/other", "o/other"))),
            rec("already", tier = "noise", status = "noise"),
            rec("theirs", manager = "m2"),
        )
        val after = applyPolicyTier(records, "github", repo, InboxPolicyTier.Noise) { it.manager == null }
        val byId = after.associateBy { it.item.id }
        assertEquals("noise", byId.getValue("open").item.tier)
        assertEquals("noise", byId.getValue("open").item.status)
        assertEquals("noise", byId.getValue("open").event.status)
        assertFalse(byId.getValue("open").item.urgent)
        assertFalse(byId.getValue("open").event.urgent)
        assertEquals("noise", byId.getValue("no-status").item.tier)
        assertNull(byId.getValue("no-status").item.status)
        records.drop(2).forEach { assertSame(it.item.id, it, byId.getValue(it.item.id)) }

        // Needs you keeps urgent; a section of "working" stays while the tier moves.
        val action = applyPolicyTier(listOf(rec("u", tier = "fyi", status = "fyi", urgent = true)), "github", repo, InboxPolicyTier.Action).single()
        assertEquals("action", action.item.status)
        assertTrue(action.item.urgent)
        assertTrue(action.event.urgent)
        val event = rec("e", tier = "fyi", status = "fyi").let { it.copy(event = it.event.copy(status = InboxItem.WORKING)) }
        assertEquals(InboxItem.WORKING, applyPolicyTier(listOf(event), "github", repo, InboxPolicyTier.Action).single().event.status)
        assertSame(records, applyPolicyTier(records, "github", repo, InboxPolicyTier.None))
    }

    @Test fun theMenuOffersARuleForEachMuteOptionAfterTheMutes() {
        val res = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources
        val record = InboxRecord(InboxItem("1"), source = "github", mutes = listOf(repo, sender))
        val muted = mutableListOf<InboxMuteOption>()
        val ruled = mutableListOf<InboxMuteOption>()
        val actions = muteAndRuleActions(res, record, { muted += it }, { ruled += it })
        assertEquals(listOf("Mute this repository · r", "Mute this sender · Alex", "Rule for this repository · r", "Rule for this sender · Alex"),
            actions.map { it.label })
        assertEquals(listOf(true, false, true, false), actions.map { it.separatorBefore })
        actions.forEach { it.onClick() }
        assertEquals(listOf(repo, sender), muted)
        assertEquals(listOf(repo, sender), ruled)
        assertTrue(muteAndRuleActions(res, record.copy(mutes = emptyList()), {}, {}).isEmpty())
    }

    private class PolicyRepository(var policies: List<InboxPolicy> = emptyList()) : InboxRepository by FakeInboxRepository() {
        var failRead: Exception? = null
        var failRemove: Exception? = null
        var removeGate: CompletableDeferred<Unit>? = null
        val read = mutableListOf<InboxRecord?>()
        val saved = mutableListOf<Triple<InboxPolicyTarget, InboxPolicyTier, InboxPolicyTwin>>()
        val removed = mutableListOf<String>()
        override suspend fun inboxPolicies(record: InboxRecord?): List<InboxPolicy> {
            read += record
            failRead?.let { throw it }
            return policies
        }
        override suspend fun savePolicy(target: InboxPolicyTarget, tier: InboxPolicyTier, twin: InboxPolicyTwin): InboxPolicy? {
            saved += Triple(target, tier, twin)
            if (tier == InboxPolicyTier.None && twin == InboxPolicyTwin.None) return null
            return InboxPolicy(target.policy?.id ?: "new", target.source, tag = target.option.tag, value = target.option.value,
                name = target.option.name, tier = tier, twin = twin)
        }
        override suspend fun removePolicy(id: String) {
            removed += id
            removeGate?.await()
            failRemove?.let { throw it }
        }
    }

    @Test fun theRuleSheetPrefillsTheHubsRuleAndSendsTheChoice() = runBlocking {
        val record = InboxRecord(InboxItem("1"), source = "github", mutes = listOf(repo, sender), manager = "m2")
        val existing = policy("p1", "sender", "u1", tier = InboxPolicyTier.Action, twin = InboxPolicyTwin.First)
        val repository = PolicyRepository(listOf(policy("p0", "repo", "o/r", source = "lark"), existing))
        val editor = InboxPolicyEditor(repository, InboxPolicyTarget.of(record, sender))
        assertFalse(editor.loaded)
        assertFalse(editor.canSave)
        editor.load()
        assertEquals(listOf<InboxRecord?>(record), repository.read)
        assertEquals(existing, editor.policy)
        assertEquals(InboxPolicyTier.Action, editor.tier)
        assertEquals(InboxPolicyTwin.First, editor.twin)

        // An existing rule is updated (its id goes with it), even when both parts are cleared.
        editor.tier = InboxPolicyTier.Noise
        assertFalse(editor.send(InboxPolicySend.Save))
        val (target, tier, twin) = repository.saved.single()
        assertEquals(existing, target.policy)
        assertSame(record, target.record)
        assertEquals(InboxPolicyTier.Noise to InboxPolicyTwin.First, tier to twin)
        assertEquals(InboxPolicyTier.Noise, editor.policy?.tier)
        assertFalse(editor.busy)

        assertTrue(editor.send(InboxPolicySend.Remove))
        assertEquals(Triple("p1", InboxPolicyTier.None, InboxPolicyTwin.None), repository.saved.last().let { Triple(it.first.policy?.id, it.second, it.third) })
        assertNull(editor.policy)
    }

    @Test fun aNewRuleNeedsAChoiceAndGoesWithoutAnId() = runBlocking {
        val record = InboxRecord(InboxItem("1"), source = "github", mutes = listOf(repo))
        val repository = PolicyRepository(listOf(policy("p0", "repo", "o/r", source = "lark")))
        val editor = InboxPolicyEditor(repository, InboxPolicyTarget.of(record, repo))
        editor.load()
        assertTrue(editor.loaded)
        assertNull(editor.policy)
        assertEquals(InboxPolicyTier.None to InboxPolicyTwin.None, editor.tier to editor.twin)
        assertFalse(editor.canSave)
        editor.twin = InboxPolicyTwin.Never
        assertTrue(editor.canSave)
        assertFalse(editor.send(InboxPolicySend.Save))
        assertNull(repository.saved.single().first.policy)
        assertEquals("new", editor.policy?.id)

        // An older hub without the list: the sheet still opens, from nothing.
        val older = PolicyRepository().apply { failRead = IllegalStateException("404") }
        val fresh = InboxPolicyEditor(older, InboxPolicyTarget.of(record, repo))
        fresh.load()
        assertTrue(fresh.loaded)
        assertNull(fresh.policy)
    }

    @Test fun aRuleFromTheListIsPrefilledWithoutReading() = runBlocking {
        val existing = policy("p1", "chat", "c1", source = "lark", tier = InboxPolicyTier.Noise).copy(sourceName = "Feishu")
        val repository = PolicyRepository()
        val target = InboxPolicyTarget.of(existing)
        val editor = InboxPolicyEditor(repository, target)
        editor.load()
        assertTrue(repository.read.isEmpty())
        assertTrue(editor.canSave)
        assertEquals(InboxPolicyTier.Noise, editor.tier)
        assertEquals("Feishu", target.sourceName)
        assertEquals("lark", target.copy(policy = existing.copy(sourceName = null)).sourceName)
        assertEquals("GitHub", InboxPolicyTarget.of(InboxRecord(InboxItem("1"), sourceLabel = "GitHub", source = "github"), repo).sourceName)
    }

    @Test fun theRulesListRemovesAtOnceAndPutsBackOnlyTheOneThatFailed() = runBlocking {
        val older = policy("a", "repo", "o/r").copy(createdAt = "2026-01-01T00:00:00Z")
        val newer = policy("b", "sender", "u1").copy(createdAt = "2026-02-01T00:00:00Z")
        val repository = PolicyRepository(listOf(newer, older))
        val model = InboxRulesModel(repository)
        model.load()
        assertEquals(listOf(newer, older), model.rules)

        repository.removeGate = CompletableDeferred()
        repository.failRemove = IllegalStateException("offline")
        val removing = async { runCatching { model.remove(newer) } }
        yield()
        assertEquals(listOf(older), model.rules)
        repository.removeGate?.complete(Unit)
        assertEquals("offline", removing.await().exceptionOrNull()?.message)
        assertEquals(listOf(newer, older), model.rules)

        repository.removeGate = null
        repository.failRemove = null
        model.remove(older)
        assertEquals(listOf(newer), model.rules)
        assertEquals(listOf("b", "a"), repository.removed)
    }

    @Test fun theRulesListSaysWhyOnlyWhenItHasNothingToShow() = runBlocking {
        val repository = PolicyRepository().apply { failRead = IllegalStateException("offline") }
        val model = InboxRulesModel(repository)
        model.load()
        assertNull(model.rules)
        assertEquals("offline", model.failed?.message)
        repository.failRead = null
        repository.policies = listOf(policy("a", "repo", "o/r"))
        model.load()
        assertNull(model.failed)
        assertEquals(listOf("a"), model.rules?.map { it.id })
        repository.failRead = IllegalStateException("again")
        try { model.load(); fail("a later failure throws") } catch (e: IllegalStateException) { assertEquals("again", e.message) }
        assertEquals(listOf("a"), model.rules?.map { it.id })
        assertNull(model.failed)
    }
}
