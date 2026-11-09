package persona.core.domain.lines

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import persona.core.contracts.Injected
import persona.core.contracts.Item
import persona.core.domain.injectedOf
import java.time.Duration
import java.time.Instant

/** Domain-only additions keep the existing wire Item compatible with its current callers. */
data class LineItem(
    val item: Item, val fold: Fold? = null, val task: TaskNote? = null,
    /** Set by [foldCompactions] on the notice that stands for a compaction; the chat draws it as one divider. */
    val compaction: Compaction? = null,
) {
    val id get() = item.id
    val seq get() = item.seq
    val part get() = blockPart(id)
    val kind get() = item.kind
    val text get() = item.text
    val time: Instant? get() = item.ts?.let(Instant::parse)
    /** Persona typed it in for the model (a task's notice, a scheduled run…): the chat shows its line, not a bubble. */
    val injected: Injected? by lazy { injectedOf(item) }
    internal val isWork get() = kind in setOf("thinking", "tool_use", "tool_result", "fold") ||
        (kind == "image" && item.toolUseId != null)

    internal fun covers(other: LineItem): Boolean = fold?.let {
        positionCompare(other.seq, other.part, seq, part) >= 0 &&
            positionCompare(other.seq, other.part, it.throughSeq, blockPart(it.throughId)) <= 0
    } ?: false
}

data class Fold(
    val items: Int, val tools: Int, val thoughts: Int,
    val throughSeq: Int, val throughId: String, val until: Instant? = null,
)

data class TaskNote(
    val kind: String, val title: String, val status: String? = null,
    val toolUseId: String? = null, val taskId: String? = null,
    val detail: String? = null, val exitCode: Int? = null,
    /** Where the task wrote its output on the session's machine ("View output"). */
    val outputFile: String? = null,
) {
    val failed get() = status == "failed" || (exitCode != null && exitCode != 0)
}

internal fun blockPart(id: String): Int = id.substringAfterLast('#', "").toIntOrNull() ?: 0
internal fun positionCompare(seq: Int, part: Int, otherSeq: Int, otherPart: Int): Int =
    if (seq == otherSeq) part.compareTo(otherPart) else seq.compareTo(otherSeq)
internal val itemOrder = compareBy<LineItem> { it.seq }.thenBy { it.part }

data class ToolCall(val use: LineItem, val result: LineItem?, val images: List<LineItem>) {
    val id get() = use.id
    val name get() = use.item.toolName ?: "tool"
    val running get() = result == null
    val failed get() = result?.item?.isError == true
}

sealed interface WorkItem {
    val id: String
    val start: Instant?
    val end: Instant?
    val tools: Int get() = 0
    val thoughts: Int get() = 0

    data class Tool(val call: ToolCall) : WorkItem {
        override val id get() = call.id
        override val start get() = call.use.time
        override val end get() = call.result?.time ?: start
        override val tools get() = 1
    }
    data class Thought(val item: LineItem) : WorkItem {
        override val id get() = item.id
        override val start get() = item.time
        override val end get() = item.time
        override val thoughts get() = if (item.kind == "notice") 0 else 1
    }
    data class Folded(val item: LineItem) : WorkItem {
        override val id get() = item.id
        override val start get() = item.time
        override val end get() = item.fold?.until ?: start
        override val tools get() = item.fold?.tools ?: 0
        override val thoughts get() = item.fold?.thoughts ?: 0
    }
    data class Narration(val item: LineItem) : WorkItem {
        override val id get() = "t-" + item.id
        override val start get() = item.time
        override val end get() = item.time
    }
}

data class ChatRow(val type: String, val item: LineItem? = null, val steps: List<WorkItem> = emptyList()) {
    val id get() = item?.id ?: "tools-" + steps.firstOrNull()?.id.orEmpty()
    val isUserInput get() = type == "user" ||
        (type == "image" && item?.item?.role == "user" && item.item.toolUseId == null)
    val start get() = item?.time ?: steps.firstNotNullOfOrNull { it.start }
    val end get() = item?.time ?: steps.asReversed().firstNotNullOfOrNull { it.end }
}

data class ChatTurn(
    val id: String,
    val users: List<ChatRow>, val process: List<ChatRow>, val answer: ChatRow?,
    val trailing: List<ChatRow>, val wakes: List<LineItem>,
    val afterWake: List<ChatRow>, val tail: List<ChatRow>, val ordered: List<ChatRow>,
    val start: Instant?, val end: Instant?,
) {
    val toolCount get() = process.sumOf { it.steps.sumOf(WorkItem::tools) }
    // Turn statistics intentionally include empty thoughts and notices inside tool groups.
    val thoughtCount get() = process.sumOf { row ->
        if (row.type == "thinking") 1 else row.steps.sumOf {
            if (it is WorkItem.Thought) 1 else it.thoughts
        }
    }
    val interimCount get() = process.count { it.type == "reply" }
}

sealed interface ChatLine {
    val id: String
    val isInput: Boolean get() = false
    fun holds(messageId: String?): Boolean = messageId != null && when (this) {
        is Row -> row.id == messageId
        is ApiError -> item.id == messageId
        else -> false
    }

    data class Row(val row: ChatRow) : ChatLine {
        override val id get() = row.id
        override val isInput get() = row.type == "user"
    }
    data class UserImages(val images: List<LineItem>) : ChatLine {
        override val id get() = "img-" + images.first().id
    }
    data class Work(
        override val id: String, val steps: List<WorkItem>, val live: Boolean,
        val start: Instant?, val end: Instant?, val open: Boolean,
    ) : ChatLine {
        val tools get() = steps.sumOf(WorkItem::tools)
        val thoughts get() = steps.sumOf(WorkItem::thoughts)
        val knownFailedTools get() = steps.count { it is WorkItem.Tool && it.call.failed }
        val unresolvedTools get() = steps.count { it is WorkItem.Tool && it.call.running }
        val containsFold get() = steps.any { it is WorkItem.Folded }
        val summary get() = WorkSummary(steps)
        fun elapsedSeconds(now: Instant): Long? = start?.let {
            Duration.between(it, if (live) now else end ?: now).seconds.coerceAtLeast(0)
        }
        fun durationText(now: Instant): CoreText? = elapsedSeconds(now)?.let(::workDuration)
    }
    data class Step(val groupId: String, val step: WorkItem, val first: Boolean) : ChatLine {
        override val id get() = "$groupId/${step.id}"
        val separatorBefore get() = !first
    }
    data class End(val groupId: String, val count: Int) : ChatLine {
        override val id get() = "$groupId-end"
        val separatorBefore get() = true
    }
    data class Wakes(val turn: ChatTurn, val live: Boolean, val drawer: List<ChatLine>) : ChatLine {
        override val id get() = "wk-" + turn.id
    }
    /**
     * An answer or a notice that is a model API error, drawn as a card ([foldApiErrors]); [count] is how many times
     * the same error came in a row, the user's "continue" between them folded in. Keeps the first error's id.
     */
    data class ApiError(val item: LineItem, val info: ApiErrorInfo, val count: Int) : ChatLine {
        override val id get() = item.id
    }
    /** Two or more away recaps in a row, as one line ([foldRecaps]). */
    data class Recaps(val items: List<LineItem>) : ChatLine {
        override val id get() = "rc-" + items.first().id
    }
    /** What the assistant's conversation adds to the transcript (docs/assistant/unified-entry.md, `tasks.AssistantLines`): the
     *  hub's feed items, quick results, day lines; [payload] is drawn by the chat's host. */
    data class Extra(override val id: String, val payload: Any) : ChatLine
}

data class WorkSummary(val full: List<CoreText>, val short: List<CoreText>) {
    constructor(steps: List<WorkItem>) : this(summary(steps, false), summary(steps, true))
}

private fun summary(steps: List<WorkItem>, short: Boolean): List<CoreText> {
    val calls = steps.sumOf { it.tools } + steps.count { it is WorkItem.Thought && it.item.item.wake == true }
    val thoughts = steps.sumOf { it.thoughts }
    val texts = steps.count { it is WorkItem.Narration }
    val notices = steps.count { it is WorkItem.Thought && it.item.kind == "notice" && it.item.item.wake != true }
    return buildList {
        if (calls > 0) add(CoreText.of(Key.WorkTools, calls))
        if (!short && thoughts > 0) add(CoreText.of(Key.WorkThoughts, thoughts))
        if (!short && texts > 0) add(CoreText.of(Key.WorkReplies, texts))
        if (isEmpty() && notices > 0) add(CoreText.of(Key.WorkNotices, notices))
    }
}

private fun workDuration(s: Long): CoreText = when {
    s < 60 -> CoreText.of(Key.DurationSeconds, s)
    s < 3600 -> if (s % 60 == 0L) CoreText.of(Key.DurationMinutes, s / 60) else CoreText.of(Key.DurationMinutesSeconds, s / 60, s % 60)
    else -> CoreText.of(Key.DurationHoursMinutes, s / 3600, s % 3600 / 60)
}

data class Frozen(val seq: Int, val part: Int, val liveTurnId: String? = null)
data class LineOptions(
    val liveTurnId: String? = null, val openWork: Set<String> = emptySet(), val frozen: Frozen? = null,
    val pendingInteractionIds: List<String>? = null,
    /** The agent is at work (working or blocked on you): the last turn is the running one, see [chatLines]. */
    val live: Boolean = false,
    /**
     * A running work line is open unless its id is in [liveFolded] (Persona's own chat, #1614): every step shows as it
     * arrives. Off (the Agents chat), a running line is open only when its id is in [openWork], like any other.
     */
    val openLive: Boolean = false,
    val liveFolded: Set<String> = emptySet(),
)
data class ChatFooter(val interactionIds: List<String>) {
    val placement get() = "above_composer"
    val partOfTranscript get() = false
}
data class ChatSnapshot(
    val lines: List<ChatLine>, val turns: List<ChatTurn>, val tasks: TaskIndex, val footer: ChatFooter? = null,
    /** The turn drawn as running; a frozen window keeps it (`Frozen.liveTurnId`). */
    val liveTurnId: String? = null,
)
