package persona.core.designsystem

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons

// Tiles (an extension's grid and carousel, UI version 6), a stepper, and the sizes they share. Nothing here knows about
// extensions.

/** The sizes of tiles: a grid's narrowest, and a carousel's three widths (the widest is a share of the page). */
object TileSize {
    /** A grid has as many columns as tiles of at least this width fit (two on a phone). */
    val min = 150.dp
    val narrow = 140.dp
    val regular = 200.dp
    /** A wide carousel tile is this share of the width, so the next one peeks in. */
    const val wideShare = 0.8f
    /** The well of a tile's icon. */
    val icon = 36.dp
}

/** How many columns a grid of [count] tiles has in [width] dp: as many [TileSize.min] ones as fit, at least one, at most [max]. */
fun tileColumns(width: Float, max: Int, count: Int): Int =
    (width / TileSize.min.value).toInt().coerceAtLeast(1).coerceAtMost(max.coerceIn(1, 6)).coerceAtMost(count.coerceAtLeast(1))

/**
 * A tile: an [icon] in a well (or a [picture] across its top), a [title], a [subtitle] (its state) and a [value], all on
 * one line each; a [badge] and a [trailing] control (a switch) at the top end, a [spark] at the foot. [active] (a light
 * on, a scene running) washes it in its [hue] and draws its edge and icon in it. The whole tile is the tap; a long
 * press opens [onLongClick] (its menu). [state] is read after the words ("On").
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun JTile(
    title: String,
    modifier: Modifier = Modifier,
    subtitle: String? = null,
    value: String? = null,
    icon: PhIcon? = null,
    picture: (@Composable BoxScope.() -> Unit)? = null,
    hue: JHue? = null,
    active: Boolean = false,
    badge: (@Composable () -> Unit)? = null,
    trailing: (@Composable () -> Unit)? = null,
    spark: JSpark? = null,
    enabled: Boolean = true,
    state: String? = null,
    onClick: (() -> Unit)? = null,
    onLongClick: (() -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val shape = PersonaTheme.shapes.cardShape
    val colors = hue?.let { palette.hue(it) }
    val edge = if (active) colors?.dot ?: palette.accent else palette.line
    val wash = if (active) colors?.tint ?: palette.accent.copy(alpha = HueTintAlpha) else null
    val click = if (onClick != null || onLongClick != null)
        Modifier.combinedClickable(enabled = enabled, role = Role.Button, onClick = onClick ?: {}, onLongClick = onLongClick) else Modifier
    Column(
        modifier.clip(shape).background(palette.surface).then(if (wash != null) Modifier.background(wash) else Modifier)
            .border(if (active) 1.5.dp else 1.dp, edge, shape).onSecondaryClick(onLongClick).then(click)
            .semantics(mergeDescendants = true) { if (state != null) stateDescription = state }
            .stableAlpha(if (enabled) 1f else 0.45f),
    ) {
        if (picture != null) Box(Modifier.fillMaxWidth().aspectRatio(16f / 9f).background(palette.sunken), content = picture)
        Column(Modifier.fillMaxWidth().padding(Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
            if (picture == null && (icon != null || badge != null || trailing != null)) {
                Row(Modifier.fillMaxWidth().heightIn(min = TileSize.icon), verticalAlignment = Alignment.CenterVertically) {
                    if (icon != null) Box(
                        Modifier.size(TileSize.icon).clip(if (PersonaTheme.style == AppStyle.Board) RoundedCornerShape(Tokens.Radius.badge) else CircleShape)
                            .background(if (active) colors?.dot ?: palette.accent else colors?.tint ?: palette.surfaceStrong),
                        contentAlignment = Alignment.Center,
                    ) {
                        JIcon(icon, null, size = Tokens.Space.xl, decorative = true, selected = active,
                            tint = if (active) colors?.onDot ?: ExtensionThemeColors.on(palette.accent) else colors?.dot ?: palette.ink2)
                    }
                    Spacer(Modifier.weight(1f))
                    badge?.invoke()
                    trailing?.let { Spacer(Modifier.width(Tokens.Space.xs)); it() }
                }
                Spacer(Modifier.size(Tokens.Space.xs))
            } else if (badge != null || trailing != null) Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                Spacer(Modifier.weight(1f)); badge?.invoke(); trailing?.let { Spacer(Modifier.width(Tokens.Space.xs)); it() }
            }
            Text(title, style = type.bodyStrong, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            subtitle?.let { Text(it, style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis) }
            value?.let { JChangingText(it, style = type.callout.copy(fontWeight = FontWeight.SemiBold), color = palette.ink) }
            spark?.let { Spacer(Modifier.size(Tokens.Space.xxs)); JSparkline(it, Modifier.fillMaxWidth().heightIn(min = SparkSize.height).size(SparkSize.width, SparkSize.height)) }
        }
    }
}

/**
 * A value stepped down and up: − [value] +, the buttons the platform's touch size, [canDown] / [canUp] at the ends of
 * the range. The value is read out with the buttons.
 */
@Composable
fun JStepper(value: String, onDown: () -> Unit, onUp: () -> Unit, modifier: Modifier = Modifier, canDown: Boolean = true, canUp: Boolean = true, enabled: Boolean = true) {
    val palette = PersonaTheme.palette
    Row(modifier, verticalAlignment = Alignment.CenterVertically) {
        JIconButton(PhIcons.Minus, stringResource(R.string.designsystem_stepper_minus), onDown, enabled = enabled && canDown, raised = false, tint = palette.ink2)
        JChangingText(value, style = PersonaTheme.type.callout.copy(fontWeight = FontWeight.SemiBold), color = palette.ink)
        JIconButton(PhIcons.Plus, stringResource(R.string.designsystem_stepper_plus), onUp, enabled = enabled && canUp, raised = false, tint = palette.ink2)
    }
}
