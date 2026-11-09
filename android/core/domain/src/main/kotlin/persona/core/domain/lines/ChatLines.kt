package persona.core.domain.lines

import java.time.Instant

/** Pure transcript folding, in reading order. Live state is supplied by the session owner. */
fun chatLines(items: List<LineItem>, options: LineOptions = LineOptions()): ChatSnapshot {
    val frozen = options.frozen
    val source = items.sortedWith(itemOrder).let { ordered ->
        if (frozen == null) ordered else ordered.takeWhile {
            positionCompare(it.seq, it.part, frozen.seq, frozen.part) <= 0
        }
    }
    val blocks = lineBlocks(source)
    val live = liveTurnId(blocks, options)
    val builder = LineBuilder(live, options)
    val lines = blocks.flatMap {
        when (it) {
            is Block.Row -> listOf(ChatLine.Row(it.row))
            is Block.Turn -> builder.turnLines(it.turn)
        }
    }.let {
        // Working with nothing of this turn yet (the last block is a recap): the work line still shows, as on the web.
        if (live == null && options.live && frozen == null && options.liveTurnId == null)
            it + ChatLine.Work(LiveWorkId, emptyList(), true, null, null, workOpen(LiveWorkId, true, options))
        else it
    }
    // As iOS does: API errors become cards and recaps in a row one line, before open work lines unfold.
    return ChatSnapshot(builder.unfold(foldRecaps(foldApiErrors(lines))), blocks.turns(),
        TaskIndex.build(source), options.pendingInteractionIds?.let(::ChatFooter), live)
}

/**
 * [chatLines]' turns alone, built by the same code: for callers that need where turns begin (the items cache keeps the
 * last two) and not the lines drawn from them, the task index or the folds.
 */
fun chatTurns(items: List<LineItem>): List<ChatTurn> = lineBlocks(items.sortedWith(itemOrder)).turns()

private fun lineBlocks(source: List<LineItem>): List<Block> = buildBlocks(buildLineRows(foldCompactions(source)))

private fun List<Block>.turns(): List<ChatTurn> = filterIsInstance<Block.Turn>().map { it.turn }

/** Whether a work line is open: a running one by default where [LineOptions.openLive] says so, else only when opened. */
fun workOpen(id: String, live: Boolean, options: LineOptions): Boolean =
    if (live && options.openLive) id !in options.liveFolded else id in options.openWork

/** A session status in which the last turn is still running: working, or blocked on you (iOS `isActive`). */
fun isLiveStatus(status: String?): Boolean = status == "working" || status == "blocked"

/** The id of the work line drawn while the agent works and no turn of its own is in the transcript yet. */
const val LiveWorkId = "w-live"

/**
 * The running turn. A frozen window and an explicit id keep the turn they name; otherwise, while the agent is at work,
 * the last block is the running turn if it is one (iOS `liveTurnID`, the web's `chatLines`). Guessing it from the last
 * user message misses a turn that took a queued message in (its id is its first message's), one a notice opened, and
 * a recap at the end.
 */
private fun liveTurnId(blocks: List<Block>, options: LineOptions): String? = when {
    options.frozen != null -> options.frozen.liveTurnId
    options.liveTurnId != null -> options.liveTurnId
    options.live -> (blocks.lastOrNull() as? Block.Turn)?.turn?.id
    else -> null
}

internal fun buildLineRows(items: List<LineItem>): List<ChatRow> {
    val results = items.filter { it.kind == "tool_result" && it.item.toolUseId != null }.associateBy { it.item.toolUseId }
    val images = items.filter { it.kind == "image" && it.item.toolUseId != null && it.item.image != null }
        .groupBy { it.item.toolUseId }
    val rows = mutableListOf<ChatRow>()
    val run = mutableListOf<WorkItem>()
    val thoughts = mutableListOf<LineItem>()
    fun flushThoughts(asRows: Boolean) {
        if (asRows) rows += thoughts.map { ChatRow("thinking", it) }
        else run += thoughts.map { WorkItem.Thought(it) }
        thoughts.clear()
    }
    fun flushRun() {
        if (run.isNotEmpty()) rows += ChatRow("tools", steps = run.toList())
        run.clear()
    }
    for (it in items) {
        when (it.kind) {
            "tool_use" -> {
                flushThoughts(run.isEmpty())
                val callId = it.item.toolUseId
                run += WorkItem.Tool(ToolCall(it, results[callId], images[callId].orEmpty()))
            }
            "thinking" -> thoughts += it
            "fold" -> {
                flushThoughts(run.isEmpty())
                run += WorkItem.Folded(it)
            }
            "tool_result" -> Unit
            else -> {
                if (it.kind == "image" && it.item.toolUseId != null) continue
                flushThoughts(run.isEmpty())
                flushRun()
                val type = when (it.kind) {
                    "text" -> if (it.item.role == "user") "user" else "reply"
                    "notice", "recap", "image" -> it.kind
                    else -> continue
                }
                rows += ChatRow(type, it)
            }
        }
    }
    flushThoughts(run.isEmpty())
    flushRun()
    return rows
}

private sealed interface Block {
    data class Row(val row: ChatRow) : Block
    data class Turn(val turn: ChatTurn) : Block
}

private class RawTurn(val id: String) {
    val users = mutableListOf<ChatRow>()
    val process = mutableListOf<ChatRow>()
    val trailing = mutableListOf<ChatRow>()
    val wakes = mutableListOf<LineItem>()
    val ordered = mutableListOf<ChatRow>()

    fun finish(): ChatTurn {
        var order = ordered.toList()
        var work = process.toList()
        var afterWake = emptyList<ChatRow>()
        var tail = emptyList<ChatRow>()
        if (wakes.isNotEmpty()) {
            val candidates = mutableListOf<Int>()
            var last: Int? = null
            for ((i, row) in order.withIndex()) {
                if (row.type == "notice" && row.item?.item?.wake == true) {
                    last?.let { candidates += it }
                    last = null
                } else if (row.type == "reply") last = i
            }
            last?.let { candidates += it }
            val firstWake = order.indexOfFirst { it.type == "notice" && it.item?.item?.wake == true }
            if (firstWake >= 0) afterWake = order.drop(firstWake).filterNot { it.isUserInput }
            // The latest run's words are the answer, whatever their length (iOS pickAnswerAcrossWakes, the web's
            // collectWakeRuns): the longest used to be, and a short reply after a wake went into the drawer.
            val pick = candidates.lastOrNull()
            if (pick != null) {
                tail = order.drop(pick + 1).filterNot { it.isUserInput }
                order = order.take(pick + 1) + order.drop(pick + 1).filter { it.isUserInput }
                work = order.filterNot { it.type == "user" }
            }
        }
        val steps = work
        val answerIndex = work.indexOfLast { it.type == "reply" }
        val answer = work.getOrNull(answerIndex)
        work = work.filterIndexed { i, _ -> i != answerIndex }
        return ChatTurn(id, users.toList(), work, answer, trailing.toList(), wakes.toList(), afterWake, tail, order,
            (users + steps).firstNotNullOfOrNull { it.start },
            (steps + trailing).asReversed().firstNotNullOfOrNull { it.end })
    }
}

private fun buildBlocks(rows: List<ChatRow>): List<Block> {
    val blocks = mutableListOf<Block>()
    var current: RawTurn? = null
    var lastRaw: RawTurn? = null
    var lastIndex = 0
    var reopenedAt: Int? = null
    fun close() {
        val raw = current ?: return
        lastRaw = raw
        val turn = Block.Turn(raw.finish())
        val index = reopenedAt ?: blocks.size
        blocks.add(index, turn)
        lastIndex = index
        reopenedAt = null
        current = null
    }
    for (row in rows) {
        when (row.type) {
            "user" -> {
                if (row.item?.item?.queued != true || current == null) {
                    close()
                    current = RawTurn(row.id)
                }
                current!!.users += row
                current!!.ordered += row
            }
            "recap" -> {
                close()
                blocks += Block.Row(row)
            }
            "notice" -> {
                val item = row.item!!
                val wake = item.item.wake == true
                if (wake && current == null && lastRaw != null &&
                    (blocks.getOrNull(lastIndex) as? Block.Turn)?.turn?.id == lastRaw!!.id) {
                    blocks.removeAt(lastIndex)
                    reopenedAt = lastIndex
                    current = lastRaw
                }
                if (wake && current != null) {
                    current!!.wakes += item
                    current!!.process += row
                    current!!.ordered += row
                } else if (current == null) {
                    current = RawTurn(row.id).also { it.process += row; it.ordered += row }
                } else if (item.task == null && item.text.orEmpty().contains("中断")) { // i18n-ignore: the hub's interrupt notice
                    current!!.trailing += row
                } else {
                    current!!.process += row
                    current!!.ordered += row
                }
            }
            else -> {
                if (current == null) current = RawTurn(row.id)
                current!!.process += row
                current!!.ordered += row
            }
        }
    }
    close()
    return blocks
}

private data class Segment(val user: ChatRow?, val rows: List<ChatRow>)
private fun segments(ordered: List<ChatRow>): List<Segment> {
    val out = mutableListOf<Segment>()
    var user: ChatRow? = null
    var rows = mutableListOf<ChatRow>()
    var started = false
    for (row in ordered) {
        if (row.type == "user") {
            if (started) out += Segment(user, rows)
            user = row
            rows = mutableListOf()
        } else rows += row
        started = true
    }
    if (started) out += Segment(user, rows)
    return out
}

private val ChatRow.isCompaction get() = type == "notice" && item?.compaction != null

private fun photos(rows: List<ChatRow>): List<LineItem> = rows.filter {
    it.type == "image" && it.isUserInput && it.item?.item?.image != null
}.map { it.item!! }

private fun workItems(rows: List<ChatRow>): List<WorkItem> = rows.flatMap {
    when (it.type) {
        "thinking" -> if (it.item?.text.isNullOrBlank()) emptyList() else listOf(WorkItem.Thought(it.item!!))
        "tools" -> it.steps
        "notice" -> listOf(WorkItem.Thought(it.item!!))
        "reply" -> listOf(WorkItem.Narration(it.item!!))
        else -> emptyList()
    }
}

private class LineBuilder(val liveId: String?, val options: LineOptions) {
    fun unfold(lines: List<ChatLine>): List<ChatLine> = lines.flatMap { line ->
        if (line is ChatLine.Work && line.open && line.steps.isNotEmpty()) {
            listOf(line) + line.steps.mapIndexed { i, step -> ChatLine.Step(line.id, step, i == 0) } +
                ChatLine.End(line.id, line.steps.sumOf {
                    if (it is WorkItem.Folded) it.item.fold?.let { f -> f.tools + f.thoughts } ?: 1 else 1
                })
        } else listOf(line)
    }

    private fun work(id: String, steps: List<WorkItem>, live: Boolean, start: Instant?, end: Instant?) =
        ChatLine.Work(id, steps, live, start, end, workOpen(id, live, options))

    private fun wakes(turn: ChatTurn, live: Boolean): ChatLine.Wakes {
        val drawer = buildList {
            val segments = segments(turn.afterWake)
            for ((k, seg) in segments.withIndex()) {
                seg.user?.let { add(ChatLine.Row(it)) }
                val last = seg.rows.indexOfLast { it.type == "reply" }
                val isLive = live && k == segments.lastIndex
                val steps = workItems(seg.rows.filterIndexed { i, _ -> i != last })
                if (steps.isNotEmpty() || isLive) add(work("dw-" + (seg.rows.firstOrNull()?.id ?: turn.id), steps,
                    isLive, seg.rows.firstOrNull()?.start, seg.rows.lastOrNull()?.end))
                seg.rows.getOrNull(last)?.let { add(ChatLine.Row(it)) }
            }
        }
        return ChatLine.Wakes(turn, live, unfold(drawer))
    }

    fun turnLines(turn: ChatTurn): List<ChatLine> = buildList {
        var ordered = turn.ordered
        var wokeAt: Instant? = null
        val first = ordered.firstOrNull()
        if (first?.type == "notice" && first.item?.item?.wake == true) {
            add(ChatLine.Row(first))
            ordered = ordered.drop(1)
            wokeAt = first.start
        }
        val segments = segments(ordered)
        for ((k, seg) in segments.withIndex()) {
            val live = turn.id == liveId && k == segments.lastIndex && turn.tail.isEmpty()
            val photos = photos(seg.rows)
            if (photos.isNotEmpty()) add(ChatLine.UserImages(photos))
            seg.user?.let { add(ChatLine.Row(it)) }
            val last = seg.rows.indexOfLast { it.type == "reply" }
            // A compaction after the answer (idle, a /compact of its own) is a divider after it, not a step hidden in
            // the turn's work line.
            val after = if (last < 0) emptySet() else
                seg.rows.indices.filter { it > last && seg.rows[it].isCompaction }.toSet()
            val rows = seg.rows.filterIndexed { i, _ -> i != last && i !in after }
            val steps = workItems(rows)
            if (!live && steps.isNotEmpty() && steps.all { it is WorkItem.Thought && it.item.compaction != null }) {
                // Only compactions: dividers across the chat, no work line to open.
                addAll(rows.filter { it.isCompaction }.map { ChatLine.Row(it) })
            } else if (steps.isNotEmpty() || live) {
                val id = "w-" + (seg.user?.id ?: seg.rows.firstOrNull()?.id ?: turn.id)
                add(work(id, steps, live, seg.user?.start ?: (if (k == 0) wokeAt else null) ?:
                    seg.rows.firstOrNull()?.start ?: turn.start, seg.rows.lastOrNull()?.end))
            }
            seg.rows.getOrNull(last)?.let { add(ChatLine.Row(it)) }
            addAll(after.sorted().map { ChatLine.Row(seg.rows[it]) })
        }
        if (turn.wakes.isNotEmpty()) add(wakes(turn, turn.id == liveId && turn.tail.isNotEmpty()))
        addAll(turn.trailing.map { ChatLine.Row(it) })
    }
}

/** Synchronous state operations make each result a complete snapshot, including late tool results. */
class LineStore {
    var items: List<LineItem> = emptyList()
        private set

    fun merge(page: List<LineItem>) {
        val folds = page.filter { f -> f.kind == "fold" && items.none { it.id == f.id && it.fold == f.fold } }
        val retained = items.filterNot { it.isWork && folds.any { fold -> fold.covers(it) } }
        items = (retained + page).distinctBy { it.id }.sortedWith(itemOrder)
    }

    fun replace(page: List<LineItem>) {
        items = page.distinctBy { it.id }.sortedWith(itemOrder)
    }

    fun unfold(foldId: String, page: List<LineItem>) {
        if (items.none { it.id == foldId }) return
        items = items.filterNot { it.id == foldId }
        merge(page.filterNot { it.kind == "fold" })
    }

    fun snapshot(options: LineOptions = LineOptions()): ChatSnapshot = chatLines(items, options)
}
