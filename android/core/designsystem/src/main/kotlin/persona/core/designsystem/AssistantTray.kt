package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.unit.dp

/** The composer trays are bounded independently of the transcript and the keyboard. */
object AssistantTrayTokens {
    val maximumHeight = 330.dp
    const val maximumHeightFraction = 0.45f
    val markSize = JChipTokens.dotSize
    /** An entry is a tappable filter chip, so it is exactly as tall as one. */
    val entryMinimumHeight = JChipTokens.height
    const val expandedRotation = 180f
}

enum class AssistantTrayTone { Pending, Progress, Agents }

@Composable
fun assistantTrayColor(tone: AssistantTrayTone): Color = when (tone) {
    AssistantTrayTone.Pending -> PersonaTheme.palette.caution
    AssistantTrayTone.Progress -> PersonaTheme.palette.sourceFeishu.dot
    AssistantTrayTone.Agents -> PersonaTheme.palette.positive
}

/**
 * One entry above the composer, drawn as the filter chip ([JChip]) the old All / task chips used: the same height,
 * capsule radius, padding, type and selected state ([JChipTokens]); [expanded] is the chip's selected state. An empty
 * Pending is quiet (muted ink, grey mark) but can still show its empty state.
 */
@Composable
fun AssistantTrayEntry(label: String, count: Int, tone: AssistantTrayTone, expanded: Boolean,
    description: String, onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true) {
    val active = tone != AssistantTrayTone.Pending || count > 0
    JChip(label, modifier.semantics { stateDescription = description }, selected = expanded,
        onClick = if (enabled) onClick else null, count = count.takeIf { it > 0 }, muted = !active,
        dot = if (active) assistantTrayColor(tone) else PersonaTheme.palette.ink3)
}

/** Row status marks share the same deliberately named palette as the tray controls. */
@Composable
fun AssistantTrayMark(tone: AssistantTrayTone?, modifier: Modifier = Modifier) {
    Box(modifier.size(AssistantTrayTokens.markSize).clip(CircleShape)
        .background(tone?.let { assistantTrayColor(it) } ?: PersonaTheme.palette.ink3))
}
