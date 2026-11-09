package persona.core.designsystem

import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/** Shared content geometry, kept identical while pages migrate away from numeric literals. */
object ContentTokens {
    val hairline = 1.dp
    val optionOutline = 1.5.dp
    val tightGap = 6.dp
    val rowGap = 10.dp
    val statusIcon = 18.dp
    val smallAvatar = 28.dp
    val mediumAvatar = 40.dp
    val fieldHeight = 44.dp
    val taskQuoteMarkHeight = 36.dp
    val mailLabelWidth = 56.dp
    val mailBodyMinHeight = 280.dp
    val profileHeaderHeight = 160.dp
    val profileCoverHeight = 120.dp
    val profileAvatar = 72.dp
    val profileAvatarInset = 3.dp
    val profileLabelWidth = 80.dp
    val mediaWaveWidth = 100.dp
    val mediaWaveGap = 3.dp
    val mediaWaveBarWidth = 3.dp
    val mediaPreviewMinHeight = 160.dp
    val mediaPreviewMaxHeight = 240.dp
    val stickerMaxSize = 160.dp
    val reactionPickerMaxHeight = 380.dp
    val reactionEmoji = 14.sp
    val quickReactionEmoji = 18.sp
    val pickerReactionEmoji = 22.sp
    /** Compact provenance messages show their first few lines until the reader expands them. */
    const val sourcePreviewLines = 4
}
