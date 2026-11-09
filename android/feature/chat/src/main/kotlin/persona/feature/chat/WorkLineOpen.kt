package persona.feature.chat

import androidx.compose.runtime.saveable.Saver
import persona.core.contracts.Item
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.LiveWorkId
import persona.core.domain.lines.WorkItem

/**
 * Which work lines are open (#1614). [open] holds the lines the user opened, as in every chat. Where running lines open
 * on their own (Persona's own chat, [ChatRenderStyle.openLiveWork]) a tap on a running line goes to [liveFolded]
 * instead: the running lines the user folded. Neither set gets a running line's id there, so when the turn ends its
 * line folds back to its summary whatever was tapped during the run; [settleLiveFolds] then forgets those folds.
 */
internal data class WorkOpenState(val open: Set<String> = emptySet(), val liveFolded: Set<String> = emptySet())

/** The running work lines among [lines], those in a wake drawer too. */
internal fun liveWorkIds(lines: List<ChatLine>): Set<String> = buildSet {
    fun visit(list: List<ChatLine>) {
        for (line in list) when (line) {
            is ChatLine.Work -> if (line.live) add(line.id)
            is ChatLine.Wakes -> visit(line.drawer)
            else -> {}
        }
    }
    visit(lines)
}

/** A tap on work line [id], running ([live]) or not: the new state, and whether the line is open now. */
internal fun toggleWork(state: WorkOpenState, id: String, live: Boolean, openLive: Boolean): Pair<WorkOpenState, Boolean> =
    if (live && openLive) {
        val folding = id !in state.liveFolded
        state.copy(liveFolded = if (folding) state.liveFolded + id else state.liveFolded - id) to !folding
    } else {
        val opening = id !in state.open
        state.copy(open = if (opening) state.open + id else state.open - id) to opening
    }

/**
 * The folds of running lines that still run ([live]): a turn that ended drops its fold, so the next turn's line opens
 * again. A fold of the placeholder line drawn before the turn has items of its own ([LiveWorkId]) moves to the line
 * that takes its place.
 */
internal fun settleLiveFolds(folded: Set<String>, live: Set<String>): Set<String> {
    if (folded.isEmpty()) return folded
    val kept = folded intersect live
    return if (LiveWorkId in folded && LiveWorkId !in live) kept + live else kept
}

/**
 * The hub folds to fetch on their own: those in running work lines that are open by default ([openLive], not folded by
 * the user), so a chat opened mid-run lists every call of the turn as rows, not "Did 12 steps" folds, as a tap on the
 * line does (#1614). Folds already loading ([folding]) or that failed ([failed]: a tap retries) are left alone.
 */
internal fun liveFoldsToFetch(lines: List<ChatLine>, openLive: Boolean, folding: Set<String>, failed: Set<String>): List<Item> {
    if (!openLive) return emptyList()
    // A wake drawer (ChatLine.Wakes) is one notice on the phone: its steps are never drawn, so they are not fetched.
    return lines.filterIsInstance<ChatLine.Work>().filter { it.live && it.open }.flatMap { line ->
        line.steps.mapNotNull { step -> (step as? WorkItem.Folded)?.item?.item?.takeIf { it.id !in folding && it.id !in failed } }
    }
}

/** Saves the running work lines the user folded ([settleLiveFolds]) across a configuration change. */
internal val LiveFoldedSaver = Saver<Set<String>, ArrayList<String>>(
    save = { ArrayList(it) },
    restore = { it.toSet() },
)
