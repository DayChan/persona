package persona.feature.inbox

import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalResources
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.domain.inbox.InboxGroup
import persona.core.domain.inbox.InboxRowType

internal class InboxSectionReader(
    val busy: Boolean,
    val notice: String?,
    val retry: (() -> Unit)?,
    val read: (InboxGroup) -> Unit,
)

/** Capture the actual manager-section rows, including folded ones. Read receipts never file pending items. */
@Composable
internal fun rememberInboxSectionReader(
    controller: InboxConversations?, rows: Map<String, InboxConversationRow>,
): InboxSectionReader {
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    var busy by remember(controller) { mutableStateOf(false) }
    var notice by remember(controller) { mutableStateOf<String?>(null) }
    var retry by remember(controller) { mutableStateOf<(() -> Unit)?>(null) }
    fun read(keys: Set<InboxConversationKey>, section: InboxConversationSection, unsupported: Boolean) {
        if (busy) return
        if (controller == null) { notice = res.getString(R.string.inbox_conversations_read_skipped); return }
        busy = true
        retry = null
        scope.launch {
            try {
                val result = controller.readAll(setOf(section), keys)
                val failed = result.skipped.toSet() + result.failed.map { it.key }
                notice = res.getString(when {
                    failed.isNotEmpty() -> R.string.inbox_conversations_read_failed
                    unsupported -> R.string.inbox_conversations_read_skipped
                    else -> R.string.inbox_conversations_read_success
                })
                if (failed.isNotEmpty()) retry = { read(failed, section, false) }
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) {
                notice = res.getString(R.string.inbox_conversations_read_failed)
                retry = { read(keys, section, unsupported) }
            } finally { busy = false }
        }
    }
    return InboxSectionReader(busy, notice, retry) { group ->
        val items = group.rows.filter { it.type == InboxRowType.Item }
        val captured = items.mapNotNull { rows[it.id]?.key }.toSet()
        read(captured, if (group.id == "action") InboxConversationSection.Action else InboxConversationSection.Fyi,
            items.any { rows[it.id] == null })
    }
}
