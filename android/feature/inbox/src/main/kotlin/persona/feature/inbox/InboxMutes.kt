package persona.feature.inbox

import androidx.annotation.StringRes
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons

/** One thing an item can be muted by: a repository, a chat or a sender of its source (the hub's `mutes`). */
data class InboxMuteOption(val tag: String, val value: String, val name: String) {
    /** The name under the menu's words, on one line: a repository without its owner. */
    val shortName: String get() = if (tag == "repo") name.substringAfterLast('/') else name

    /** The menu's words for it. */
    @get:StringRes val title: Int get() = when (tag) {
        "repo" -> R.string.inbox_mute_repo
        "chat" -> R.string.inbox_mute_chat
        else -> R.string.inbox_mute_sender
    }
}

/** A mute the user set (`GET /api/v1/inbox/mutes`): nothing of [value] under [tag] in [source] comes into the inbox. */
data class InboxMute(
    val id: String,
    val source: String,
    val sourceName: String? = null,
    val tag: String,
    val value: String,
    val name: String,
    val createdAt: String? = null,
) {
    /** What kind of thing it is, for people: "Repository" / "Chat" / "Sender". */
    @get:StringRes val tagName: Int get() = muteTagName(tag)
}

/** What kind of thing a mute or a rule names, for people: "Repository" / "Chat" / "Sender". */
@StringRes internal fun muteTagName(tag: String): Int = when (tag) {
    "repo" -> R.string.inbox_mute_tag_repo
    "chat" -> R.string.inbox_mute_tag_chat
    else -> R.string.inbox_mute_tag_sender
}

fun inboxMuteOptions(item: JsonObject): List<InboxMuteOption> =
    (item["mutes"] as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }.mapNotNull { m ->
        val tag = m.text("tag"); val value = m.text("value")
        if (tag.isBlank() || value.isBlank()) null else InboxMuteOption(tag, value, m.text("name").ifBlank { value })
    }

/** The hub's mutes, newest first. */
fun inboxMutes(body: JsonObject): List<InboxMute> =
    (body["mutes"] as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }.mapNotNull { m ->
        val id = m.text("id").takeIf { it.isNotBlank() } ?: return@mapNotNull null
        InboxMute(id, m.text("source"), m.cursor("source_name"), m.text("tag"), m.text("value"), m.text("name").ifBlank { m.text("value") }, m.cursor("created_at"))
    }.sortedByDescending { it.createdAt.orEmpty() }

/** The records an item's mute covers: open ones of its source with the same tag and value (iOS `InboxStore.mute`). */
fun mutedBy(records: List<InboxRecord>, record: InboxRecord, option: InboxMuteOption): Set<String> =
    records.filter { r -> r.source == record.source && !r.item.isDone && r.mutes.any { it.tag == option.tag && it.value == option.value } }
        .map { it.item.id }.toSet() + record.item.id

/**
 * "Muted" (the inbox's ⋯ menu): the repositories, chats and senders muted from an item's menu, in every source, newest
 * first. A tap takes one back; nothing it covered comes back, only what comes in later (iOS `InboxMutesView`).
 */
@Composable
internal fun InboxMutesSheet(repository: InboxRepository, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    var mutes by remember { mutableStateOf<List<InboxMute>?>(null) }
    var failure by remember { mutableStateOf<String?>(null) }
    fun unmute(m: InboxMute) {
        // Taken off at once, as on iOS; a failure puts it back.
        val before = mutes
        mutes = before?.filter { it.id != m.id }
        scope.launch {
            try { repository.unmute(m.id) }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) {
                // Put back only this one: another unmute that went through meanwhile stays gone.
                mutes = (mutes.orEmpty().filter { it.id != m.id } + m).sortedByDescending { it.createdAt.orEmpty() }
                if (feedback?.failure(e) != true) failure = res.getString(R.string.inbox_unmute_failed) }
        }
    }
    fun load() {
        scope.launch {
            failure = null
            try { mutes = repository.inboxMutes() }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { if (mutes == null) failure = res.getString(R.string.inbox_mutes_failed) else feedback?.failure(e) }
        }
    }
    LaunchedEffect(repository) { load() }
    JSheet(onDismiss, title = stringResource(R.string.inbox_mutes_title), scrollable = true) {
        val list = mutes
        when {
            failure != null -> JNotice(failure.orEmpty(), status = Status.NeedsYou, actionLabel = stringResource(R.string.inbox_retry), onAction = ::load)
            list == null -> JLoading()
            list.isEmpty() -> JEmptyState(stringResource(R.string.inbox_mutes_empty_title), stringResource(R.string.inbox_mutes_empty_text), icon = PhIcons.Prohibit)
            else -> JGroup(rows = list.map { m -> {
                JRow(m.name, subtitle = listOf(m.sourceName ?: m.source, stringResource(m.tagName)).filter { it.isNotBlank() }.joinToString(" · "),
                    leadingIcon = PhIcons.Prohibit, subtitleLines = 1,
                    trailing = { JTextButton(stringResource(R.string.inbox_unmute), { unmute(m) }) })
            } })
        }
    }
}
