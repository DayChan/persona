package persona.feature.inbox

import android.content.res.Resources
import androidx.annotation.StringRes
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import persona.core.contracts.parseTime
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.inbox.InboxItem

// Inbox policies (docs/assistant/inbox.md "Policies"), as the web's PolicyDialog: "Rule for this conversation / sender /
// repository" in an item's menu opens InboxPolicySheet (where its items go, what the AI twin does with them); "Rules" in
// the Inbox's ⋯ menu lists them (InboxRulesSheet) with Edit and Remove. The hub decides in code; this only sets and lists.

/** A rule's tier: [None] leaves it to triage. */
enum class InboxPolicyTier(val wire: String, @get:StringRes val title: Int) {
    None("", R.string.inbox_policy_tier_none), Action("action", R.string.inbox_policy_tier_action),
    Fyi("fyi", R.string.inbox_policy_tier_fyi), Noise("noise", R.string.inbox_policy_tier_noise);

    companion object { fun of(wire: String?): InboxPolicyTier = entries.firstOrNull { it.wire == wire } ?: None }
}

/** What a rule does with the AI twin: [First] hands its items over first (even when not Needs you), [Never] keeps them away; [None] as set. */
enum class InboxPolicyTwin(val wire: String, @get:StringRes val title: Int) {
    None("", R.string.inbox_policy_twin_none), First("first", R.string.inbox_policy_twin_first), Never("never", R.string.inbox_policy_twin_never);

    companion object { fun of(wire: String?): InboxPolicyTwin = entries.firstOrNull { it.wire == wire } ?: None }
}

/** A rule the user set (`GET /api/v1/inbox/policies`): items of [source] with [value] under [tag] get its tier and twin. */
data class InboxPolicy(
    val id: String,
    val source: String,
    val sourceName: String? = null,
    val tag: String,
    val value: String,
    val name: String,
    val tier: InboxPolicyTier = InboxPolicyTier.None,
    val twin: InboxPolicyTwin = InboxPolicyTwin.None,
    val createdAt: String? = null,
    val updatedAt: String? = null,
) {
    val option: InboxMuteOption get() = InboxMuteOption(tag, value, name)

    fun covers(source: String, option: InboxMuteOption) = this.source == source && tag == option.tag && value == option.value

    /** What it does, as the ids of its words: "Always Needs you", "AI twin first"; only the parts it sets. */
    val summary: List<Int> get() = listOfNotNull(
        tier.takeIf { it != InboxPolicyTier.None }?.title, twin.takeIf { it != InboxPolicyTwin.None }?.title)
}

/**
 * What the rule sheet is about: a new rule from one of [record]'s options, or an existing [policy] from "Rules". A rule
 * from an item is its manager's, as its mute is; one from "Rules" is the default manager's.
 */
data class InboxPolicyTarget(
    val source: String,
    val option: InboxMuteOption,
    val record: InboxRecord? = null,
    val policy: InboxPolicy? = null,
) {
    /** The source's name for people: the item's account, else the rule's source name, else the source itself. */
    val sourceName: String get() = record?.sourceLabel?.takeIf { it.isNotBlank() } ?: policy?.sourceName?.takeIf { it.isNotBlank() } ?: source

    companion object {
        fun of(record: InboxRecord, option: InboxMuteOption) = InboxPolicyTarget(record.source, option, record)
        fun of(policy: InboxPolicy) = InboxPolicyTarget(policy.source, policy.option, policy = policy)
    }
}

/** The menu's words for setting a rule on an option: "Rule for this repository / conversation / sender". */
@get:StringRes val InboxMuteOption.policyTitle: Int get() = when (tag) {
    "repo" -> R.string.inbox_policy_repo
    "chat" -> R.string.inbox_policy_chat
    else -> R.string.inbox_policy_sender
}

/** The hub's rules, newest first (by time: RFC 3339 drops trailing zeros, so the text does not sort). */
fun inboxPolicies(body: JsonObject): List<InboxPolicy> =
    (body["policies"] as? JsonArray).orEmpty().mapNotNull { (it as? JsonObject)?.let(::inboxPolicy) }.newestFirst()

private fun List<InboxPolicy>.newestFirst() = sortedByDescending { parseTime(it.createdAt)?.toEpochMilli() ?: 0L }

/** One rule as the hub sends it (in the list, and as `policy` after a save); null without an id. */
fun inboxPolicy(p: JsonObject): InboxPolicy? {
    val id = p.text("id").takeIf { it.isNotBlank() } ?: return null
    return InboxPolicy(id, p.text("source"), p.cursor("source_name"), p.text("tag"), p.text("value"), p.text("name").ifBlank { p.text("value") },
        InboxPolicyTier.of(p.cursor("tier")), InboxPolicyTwin.of(p.cursor("twin")), p.cursor("created_at"), p.cursor("updated_at"))
}

/** The tags a rule can name, the most specific first: a sender's rule beats its chat's (the hub's rule). */
private val POLICY_TAGS = listOf("sender", "chat", "repo")

/** The rule that applies to an item, as the hub picks it: the same source, one of its mute options, sender before chat before repository. */
fun policyFor(record: InboxRecord, policies: List<InboxPolicy>): InboxPolicy? =
    record.mutes.filter { it.tag in POLICY_TAGS }.sortedBy { POLICY_TAGS.indexOf(it.tag) }
        .firstNotNullOfOrNull { o -> policies.firstOrNull { it.covers(record.source, o) } }

/** Whether a rule's tier reaches an item at once (the hub's rule): the same thing, open, triaged, not being handled. */
internal fun policyReaches(source: String, option: InboxMuteOption, record: InboxRecord): Boolean =
    record.source == source && !record.item.isDone && record.item.isTriaged && !record.item.isWorking &&
        record.mutes.any { it.tag == option.tag && it.value == option.value }

/**
 * The records with a rule's [tier] applied at once, as the hub applies it: the ones it reaches ([mine]: the rule's
 * manager's) take the tier, a section that is not "working" follows it, and a tier other than Needs you clears urgent.
 * [InboxPolicyTier.None] leaves them all (the next triage of new content decides). Records left alone are the same objects.
 */
fun applyPolicyTier(
    records: List<InboxRecord>, source: String, option: InboxMuteOption, tier: InboxPolicyTier,
    mine: (InboxRecord) -> Boolean = { true },
): List<InboxRecord> {
    if (tier == InboxPolicyTier.None) return records
    return records.map { r ->
        if (!policyReaches(source, option, r) || !mine(r) || r.item.tier == tier.wire) return@map r
        fun section(status: String?) = status?.let { if (it != InboxItem.WORKING) tier.wire else it }
        val urgent = tier == InboxPolicyTier.Action && r.item.urgent
        r.copy(item = r.item.copy(tier = tier.wire, status = section(r.item.status), urgent = urgent),
            event = r.event.copy(status = section(r.event.status), urgent = tier == InboxPolicyTier.Action && r.event.urgent))
    }
}

/**
 * An item's menu entries for its mute options, then a rule for each, each group under a line (the row's long press),
 * with the name after the words ("Mute this repository · example-repo").
 */
internal fun muteAndRuleActions(
    res: Resources, record: InboxRecord, onMute: (InboxMuteOption) -> Unit, onRule: (InboxMuteOption) -> Unit,
): List<JAction> =
    record.mutes.mapIndexed { i, option ->
        JAction(res.getString(R.string.inbox_mute_option, res.getString(option.title), option.shortName), PhIcons.Prohibit,
            separatorBefore = i == 0) { onMute(option) }
    } + record.mutes.mapIndexed { i, option ->
        JAction(res.getString(R.string.inbox_mute_option, res.getString(option.policyTitle), option.shortName), PhIcons.Target,
            separatorBefore = i == 0) { onRule(option) }
    }

/** The rule sheet's two buttons that send something. */
enum class InboxPolicySend { Save, Remove }

/** The rule sheet's state (the web's PolicyDialog): the rule the hub has, prefilled; the choice; what is being sent. */
@Stable
class InboxPolicyEditor(private val repository: InboxRepository, val target: InboxPolicyTarget) {
    var policy by mutableStateOf(target.policy); private set
    var loaded by mutableStateOf(target.policy != null); private set
    var tier by mutableStateOf(target.policy?.tier ?: InboxPolicyTier.None)
    var twin by mutableStateOf(target.policy?.twin ?: InboxPolicyTwin.None)
    var sending by mutableStateOf<InboxPolicySend?>(null); private set
    val busy: Boolean get() = sending != null

    /** Save needs a choice: a new rule one of its two parts (an existing one may clear both, which removes it). */
    val canSave: Boolean get() = loaded && (policy != null || tier != InboxPolicyTier.None || twin != InboxPolicyTwin.None)

    /** Prefills from the hub's list when opened from an item; an older hub without the list starts from nothing. */
    suspend fun load() {
        if (loaded) return
        val same = try { repository.inboxPolicies(target.record).firstOrNull { it.covers(target.source, target.option) } }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) { null }
        policy = same
        tier = same?.tier ?: InboxPolicyTier.None
        twin = same?.twin ?: InboxPolicyTwin.None
        loaded = true
    }

    /** Sends the choice ([InboxPolicySend.Remove]: neither part); true when that removed the rule. Throws what the hub said. */
    suspend fun send(what: InboxPolicySend): Boolean {
        val (tier, twin) = if (what == InboxPolicySend.Remove) InboxPolicyTier.None to InboxPolicyTwin.None else tier to twin
        sending = what
        try { policy = repository.savePolicy(target.copy(policy = policy), tier, twin) }
        finally { sending = null }
        return tier == InboxPolicyTier.None && twin == InboxPolicyTwin.None
    }
}

/** "Rule for this conversation / sender / repository": where its items go and what the AI twin does with them. */
@Composable
internal fun InboxPolicySheet(repository: InboxRepository, target: InboxPolicyTarget, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    val palette = PersonaTheme.palette
    val editor = remember(repository, target) { InboxPolicyEditor(repository, target) }
    // The sheet stays open on a failure, over the page where a notice would show: it says so itself.
    var failure by remember(editor) { mutableStateOf<String?>(null) }
    LaunchedEffect(editor) { editor.load() }
    fun send(what: InboxPolicySend) {
        failure = null
        scope.launch {
            try {
                val removed = editor.send(what)
                feedback?.notice(res.getString(if (removed) R.string.inbox_policy_removed else R.string.inbox_policy_saved, target.option.name))
                onDismiss()
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { failure = res.getString(R.string.inbox_policy_failed, failureReasonText(e).resolve(res)) }
        }
    }
    JSheet(onDismiss, title = stringResource(R.string.inbox_policy_title, target.option.shortName), dismissible = !editor.busy, scrollable = true) {
        if (!editor.loaded) { JLoading(); return@JSheet }
        val kind = stringResource(muteTagName(target.option.tag))
        Text(stringResource(R.string.inbox_policy_intro, "${target.sourceName} · $kind"), style = PersonaTheme.type.callout, color = palette.ink2)
        JSectionHeader(stringResource(R.string.inbox_policy_tier))
        InboxPolicyTier.entries.forEachIndexed { i, t ->
            JGroupItem(GroupPosition.of(i, InboxPolicyTier.entries.size)) {
                PolicyChoice(stringResource(t.title), editor.tier == t, enabled = !editor.busy) { editor.tier = t }
            }
        }
        JSectionHeader(stringResource(R.string.inbox_policy_twin))
        InboxPolicyTwin.entries.forEachIndexed { i, t ->
            JGroupItem(GroupPosition.of(i, InboxPolicyTwin.entries.size)) {
                PolicyChoice(stringResource(t.title), editor.twin == t, enabled = !editor.busy) { editor.twin = t }
            }
        }
        Spacer(Modifier.height(Tokens.Space.s))
        Text(stringResource(R.string.inbox_policy_twin_note), Modifier.padding(horizontal = rowPadding),
            style = PersonaTheme.type.caption, color = palette.ink3)
        failure?.let { JNotice(it, Modifier.padding(top = Tokens.Space.l), status = Status.NeedsYou) }
        Spacer(Modifier.height(Tokens.Space.xl))
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            if (editor.policy != null) JButton(stringResource(R.string.inbox_policy_remove), { send(InboxPolicySend.Remove) },
                kind = JButtonKind.Quiet, size = JButtonSize.Medium, enabled = !editor.busy, loading = editor.sending == InboxPolicySend.Remove)
            Spacer(Modifier.weight(1f))
            JButton(stringResource(R.string.inbox_policy_cancel), onDismiss, kind = JButtonKind.Secondary, size = JButtonSize.Medium, enabled = !editor.busy)
            JButton(stringResource(R.string.inbox_policy_save), { send(InboxPolicySend.Save) }, size = JButtonSize.Medium,
                enabled = editor.canSave && !editor.busy, loading = editor.sending == InboxPolicySend.Save)
        }
    }
}

/** One choice of a group: its words and a radio, the whole row tapping it. */
@Composable
private fun PolicyChoice(title: String, selected: Boolean, enabled: Boolean, onSelect: () -> Unit) =
    JRow(title, onClick = onSelect, chevron = false, enabled = enabled, role = Role.RadioButton, trailing = { JRadio(selected, null) })

/** The "Rules" list's state: every source's rules, newest first; Remove takes one off at once and puts it back on failure. */
@Stable
class InboxRulesModel(private val repository: InboxRepository) {
    var rules by mutableStateOf<List<InboxPolicy>?>(null); private set
    /** Why the first read failed; a later one leaves the shown list and throws. */
    var failed by mutableStateOf<Throwable?>(null); private set

    suspend fun load() {
        failed = null
        try { rules = repository.inboxPolicies() }
        catch (e: CancellationException) { throw e }
        catch (e: Exception) { if (rules == null) failed = e else throw e }
    }

    /** Removes [policy] at once; throws what the hub said, with the row back. */
    suspend fun remove(policy: InboxPolicy) {
        rules = rules?.filter { it.id != policy.id }
        try { repository.removePolicy(policy.id) }
        catch (e: Exception) {
            // Back only this one: another remove that went through meanwhile stays gone.
            rules = rules?.let { list -> (list.filter { it.id != policy.id } + policy).newestFirst() }
            throw e
        }
    }
}

/** "Rules" (the Inbox's ⋯ menu): each rule's name, "source · kind · what it does", Edit and Remove. */
@Composable
internal fun InboxRulesSheet(repository: InboxRepository, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    val model = remember(repository) { InboxRulesModel(repository) }
    var editing by remember { mutableStateOf<InboxPolicyTarget?>(null) }
    var failure by remember { mutableStateOf<String?>(null) }
    fun load() {
        scope.launch {
            try { model.load() }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { feedback?.failure(e) }
        }
    }
    fun remove(p: InboxPolicy) {
        failure = null
        scope.launch {
            try {
                model.remove(p)
                feedback?.notice(res.getString(R.string.inbox_policy_removed, p.name))
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { failure = res.getString(R.string.inbox_rules_remove_failed, failureReasonText(e).resolve(res)) }
        }
    }
    LaunchedEffect(model) { load() }
    // The rule's own sheet takes the list's place; the list reads again when it closes.
    editing?.let { target ->
        InboxPolicySheet(repository, target, onDismiss = { editing = null; load() })
        return
    }
    JSheet(onDismiss, title = stringResource(R.string.inbox_rules_title), scrollable = true) {
        val list = model.rules
        val failed = model.failed
        when {
            failed != null -> JNotice(stringResource(R.string.inbox_rules_failed, failureReasonText(failed).text()), status = Status.NeedsYou,
                actionLabel = stringResource(R.string.inbox_retry), onAction = ::load)
            list == null -> JLoading()
            list.isEmpty() -> JEmptyState(stringResource(R.string.inbox_rules_empty_title), stringResource(R.string.inbox_rules_empty_text), icon = PhIcons.Target)
            else -> JGroup(rows = list.map { p -> {
                JRow(p.name, subtitle = (listOf(p.sourceName ?: p.source, stringResource(muteTagName(p.tag))) + p.summary.map { stringResource(it) })
                    .filter { it.isNotBlank() }.joinToString(" · "),
                    leadingIcon = PhIcons.Target,
                    trailing = {
                        Row {
                            JTextButton(stringResource(R.string.inbox_policy_edit), { editing = InboxPolicyTarget.of(p) })
                            JTextButton(stringResource(R.string.inbox_policy_remove), { remove(p) })
                        }
                    })
            } })
        }
        failure?.let { JNotice(it, Modifier.padding(top = Tokens.Space.l), status = Status.NeedsYou) }
    }
}
