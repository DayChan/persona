package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.SwipeToDismissBox
import androidx.compose.material3.SwipeToDismissBoxValue
import androidx.compose.material3.rememberSwipeToDismissBoxState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.launch
import persona.core.designsystem.icons.PhIcon

/**
 * A swipe gesture on a row. The row springs back; the data change removes it. [destructive] ones ("Close") are drawn in the
 * danger colour instead of the status's.
 */
@Immutable
data class SwipeAction(val label: String, val icon: PhIcon, val status: Status, val onTrigger: () -> Unit, val destructive: Boolean = false)

@Composable
fun JSwipeActions(
    modifier: Modifier = Modifier,
    start: SwipeAction? = null,
    end: SwipeAction? = null,
    content: @Composable () -> Unit,
) {
    if (start == null && end == null) return Box(modifier) { content() }
    val palette = PersonaTheme.palette
    val state = rememberSwipeToDismissBoxState()
    val scope = rememberCoroutineScope()
    val actions = rememberUpdatedState(start to end)
    // Material keys its dismissal effect by this callback as well as the settled anchor. Keep the callback
    // stable while a data update changes the row and its actions during the return animation.
    val onDismiss: (SwipeToDismissBoxValue) -> Unit = remember(state, scope) {
        { value ->
            val action = when (value) {
                SwipeToDismissBoxValue.StartToEnd -> actions.value.first
                SwipeToDismissBoxValue.EndToStart -> actions.value.second
                SwipeToDismissBoxValue.Settled -> null
            }
            scope.launch {
                try { action?.onTrigger?.invoke() }
                finally { state.reset() }
            }
        }
    }
    SwipeToDismissBox(
        state = state, modifier = modifier,
        enableDismissFromStartToEnd = start != null, enableDismissFromEndToStart = end != null,
        onDismiss = onDismiss,
        backgroundContent = {
            // Only while dragging, and only in the gap the row leaves: rows without a fill of their own (the Agents
            // tree in glass and brief) and translucent ones would otherwise show the action colour under their words.
            val action = when (state.dismissDirection) {
                SwipeToDismissBoxValue.StartToEnd -> start
                SwipeToDismissBoxValue.EndToStart -> end
                SwipeToDismissBoxValue.Settled -> null
            }
            if (action != null) {
                val tone = if (action.destructive) palette.danger else palette.status(action.status)
                val fromEnd = state.dismissDirection == SwipeToDismissBoxValue.EndToStart
                val gap = with(androidx.compose.ui.platform.LocalDensity.current) {
                    kotlin.math.abs(runCatching { state.requireOffset() }.getOrDefault(0f)).toDp()
                }
                Box(Modifier.fillMaxSize(), contentAlignment = if (fromEnd) Alignment.CenterEnd else Alignment.CenterStart) {
                    Box(
                        Modifier.fillMaxHeight().width(gap).clip(PersonaTheme.shapes.cardShape).background(palette.statusSurface),
                        contentAlignment = if (fromEnd) Alignment.CenterEnd else Alignment.CenterStart,
                    ) {
                        // The words keep their size and show as the gap opens.
                        Row(
                            Modifier.wrapContentWidth(if (fromEnd) Alignment.End else Alignment.Start, unbounded = true)
                                .padding(horizontal = 24.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            JIcon(action.icon, null, size = 22.dp, tint = tone.text, selected = true)
                            Spacer(Modifier.width(8.dp))
                            Text(action.label, style = PersonaTheme.type.label, color = tone.text, maxLines = 1)
                        }
                    }
                }
            }
        },
    ) { content() }
}

