package persona.core.designsystem

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.painter.Painter
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons

/** Paints a board holder down the left edge without asking the content for its intrinsic height. */
internal fun Modifier.holder(color: androidx.compose.ui.graphics.Color, width: Dp): Modifier = drawBehind {
    drawRect(color, size = androidx.compose.ui.geometry.Size(width.toPx(), size.height))
}

/** Horizontal page padding of the active style; list items assume they sit inside it. */
/** Side padding of page content, plus the room that keeps it to [ReadingWidth] in wide panes. */
val pagePadding: Dp @Composable get() = when (PersonaTheme.style) { AppStyle.Glass -> 16.dp; AppStyle.Brief -> 20.dp; AppStyle.Board -> 12.dp } + LocalReadingInset.current

/** Where an item sits in a group, so lazy lists can draw one continuous container out of separate items. */
@Immutable
data class GroupPosition(val first: Boolean, val last: Boolean) {
    companion object {
        val Single = GroupPosition(first = true, last = true)
        fun of(index: Int, count: Int) = GroupPosition(first = index == 0, last = index == count - 1)
    }
}

/** A section heading inside a page: plain text in every style, with an optional count and trailing action. */
@Composable
fun JSectionHeader(
    title: String,
    modifier: Modifier = Modifier,
    count: Int? = null,
    status: Status? = null,
    trailing: String? = null,
    onClick: (() -> Unit)? = null,
    /** Greyed: what it heads is not reachable now (an offline manager's group). */
    muted: Boolean = false,
    action: (@Composable RowScope.() -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    Row(
        modifier.fillMaxWidth().then(if (onClick != null) Modifier.combinedClickable(role = Role.Button, onClick = onClick) else Modifier)
            .padding(top = if (style == AppStyle.Board) 18.dp else 26.dp, bottom = 10.dp,
                start = if (style == AppStyle.Brief) 0.dp else 4.dp, end = if (style == AppStyle.Brief) 0.dp else 4.dp)
            .semantics { heading() },
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (status != null && style == AppStyle.Board) {
            Box(Modifier.size(10.dp).clip(RoundedCornerShape(2.dp)).background(palette.status(status).dot))
            Spacer(Modifier.width(8.dp))
        }
        // The title and its count take the free width, so the trailing text and the action sit at the end of the row
        // (two weights in one row split the space, which left them near the middle and cut long titles at half width).
        Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically) {
            Text(
                title,
                style = if (style == AppStyle.Glass) PersonaTheme.type.caption.copy(fontWeight = FontWeight.SemiBold, fontSize = androidx.compose.ui.unit.TextUnit(14f, androidx.compose.ui.unit.TextUnitType.Sp))
                else PersonaTheme.type.headline.copy(fontSize = androidx.compose.ui.unit.TextUnit(15f, androidx.compose.ui.unit.TextUnitType.Sp)),
                color = if (muted) palette.ink3 else if (style == AppStyle.Glass) palette.ink2 else palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f, fill = false),
            )
            count?.let { Spacer(Modifier.width(6.dp)); Text("$it", style = PersonaTheme.type.micro, color = palette.ink3) }
        }
        if (trailing != null || action != null) Spacer(Modifier.width(8.dp))
        trailing?.let { Text(it, style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1) }
        action?.invoke(this)
    }
}

/**
 * One item of a group. List groups ([separate] false) read as one container (a glass panel, ruled rows, a
 * bordered table); separate items are cards of their own (glass cards, wash cards, board strips).
 * [holder] gives board strips their coloured holder; other styles ignore it.
 */
@Composable
fun JGroupItem(
    position: GroupPosition,
    modifier: Modifier = Modifier,
    separate: Boolean = false,
    holder: Status? = null,
    content: @Composable ColumnScope.() -> Unit,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val radius = if (separate) PersonaTheme.shapes.card else PersonaTheme.shapes.panel.coerceAtMost(26.dp).let { if (style == AppStyle.Board) 10.dp else it }
    val shape = if (separate) RoundedCornerShape(radius) else RoundedCornerShape(
        topStart = if (position.first) radius else 0.dp, topEnd = if (position.first) radius else 0.dp,
        bottomStart = if (position.last) radius else 0.dp, bottomEnd = if (position.last) radius else 0.dp,
    )
    val gap = if (separate && !position.last) (if (style == AppStyle.Board) 8.dp else 12.dp) else 0.dp
    Column(modifier.fillMaxWidth().padding(bottom = gap)) {
        when (style) {
            AppStyle.Glass -> Column(
                Modifier.fillMaxWidth().clip(shape).background(palette.surface)
                    .then(if (separate) Modifier.border(1.dp, Brush.verticalGradient(listOf(palette.strokeTop, palette.strokeBottom)), shape) else Modifier)
                    .drawBehind {
                        if (!separate && position.first) drawLine(palette.strokeTop.copy(alpha = 0.6f), Offset(radius.toPx(), 0f), Offset(size.width - radius.toPx(), 0f), 1.dp.toPx())
                        if (!separate && !position.first) drawLine(palette.line, Offset(16.dp.toPx(), 0f), Offset(size.width - 16.dp.toPx(), 0f), 1.dp.toPx())
                    },
                content = content,
            )
            AppStyle.Brief -> Column(
                Modifier.fillMaxWidth()
                    .then(if (separate) Modifier.clip(shape).background(palette.surface) else Modifier)
                    .drawBehind { if (!separate && !position.first) drawLine(palette.line, Offset(0f, 0f), Offset(size.width, 0f), 1.dp.toPx()) },
                content = content,
            )
            AppStyle.Board -> if (separate) Column(
                Modifier.fillMaxWidth().clip(shape).background(palette.surface)
                    .then(if (holder != null) Modifier.holder(palette.status(holder).dot, Tokens.Space.xxs).padding(start = 6.dp) else Modifier)
                    .border(1.dp, palette.line, shape),
                content = content,
            ) else Column(
                Modifier.fillMaxWidth().clip(shape).background(palette.surface)
                    .drawBehind {
                        val w = 1.dp.toPx()
                        drawLine(palette.line, Offset(0f, 0f), Offset(0f, size.height), w)
                        drawLine(palette.line, Offset(size.width - w, 0f), Offset(size.width - w, size.height), w)
                        if (position.first) drawLine(palette.line, Offset(0f, w / 2), Offset(size.width, w / 2), w)
                        else drawLine(palette.line, Offset(14.dp.toPx(), 0f), Offset(size.width, 0f), w)
                        if (position.last) drawLine(palette.line, Offset(0f, size.height - w / 2), Offset(size.width, size.height - w / 2), w)
                    },
                content = content,
            )
        }
    }
}

/** A small, non-lazy group (settings sections): items in one container. */
@Composable
fun JGroup(modifier: Modifier = Modifier, rows: List<@Composable () -> Unit>) {
    Column(modifier) {
        rows.forEachIndexed { index, row -> JGroupItem(GroupPosition.of(index, rows.size)) { row() } }
    }
}

/**
 * Side padding of a [JRow] in the active style: glass and board inset the row inside its panel, brief rows sit on
 * the paper's text column (none). Content that belongs to a row but is not one (a long value set under its label, a
 * note) pads with this to line up with the rows' text.
 */
val rowPadding: Dp @Composable get() = when (PersonaTheme.style) { AppStyle.Glass -> 16.dp; AppStyle.Brief -> 0.dp; AppStyle.Board -> 14.dp }

/** A settings-style row: optional icon, title and subtitle, a value or trailing control, and a chevron when it navigates. */
@Composable
fun JRow(
    title: String,
    modifier: Modifier = Modifier,
    subtitle: String? = null,
    leadingIcon: PhIcon? = null,
    leading: (@Composable () -> Unit)? = null,
    value: String? = null,
    trailing: (@Composable () -> Unit)? = null,
    onClick: (() -> Unit)? = null,
    onLongClick: (() -> Unit)? = null,
    chevron: Boolean = onClick != null && trailing == null,
    enabled: Boolean = true,
    destructive: Boolean = false,
    subtitleLines: Int = 2,
    selected: Boolean = false,
    /** Drawn right after the title on its line (a small source mark); the title gives way to it when space is short. */
    titleAccessory: (@Composable () -> Unit)? = null,
    /** Drawn again with a new [value], its numbers count to the new ones and other text fades in (#2811). */
    changingValue: Boolean = false,
    /** What accessibility services call the row's click: a switch row says [Role.Switch] so its on / off is read. */
    role: Role = Role.Button,
) {
    val palette = PersonaTheme.palette
    RowFrame(modifier, onClick, onLongClick, null, enabled, selected, value, trailing, chevron, changingValue, role) {
        if (leading != null) { leading(); Spacer(Modifier.width(14.dp)) }
        else if (leadingIcon != null) {
            JIcon(leadingIcon, null, size = 22.dp, tint = if (destructive) palette.danger.dot else palette.ink2, decorative = true)
            Spacer(Modifier.width(16.dp))
        }
        Column(Modifier.weight(1f)) {
            val titleText: @Composable (Modifier) -> Unit = { m ->
                Text(title, m, style = PersonaTheme.type.body, color = if (destructive) palette.danger.text else palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            if (titleAccessory == null) titleText(Modifier)
            else Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                titleText(Modifier.weight(1f, fill = false))
                titleAccessory()
            }
            subtitle?.let { Text(it, style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink2, maxLines = subtitleLines, overflow = TextOverflow.Ellipsis) }
        }
    }
}

/**
 * A [JRow] whose middle is [content] instead of a title and subtitle: lines that wrap, tags, a status mark in front.
 * Everything else is the row's own (the style's side padding, height, click and long press, selection, the dimmed
 * disabled state, [trailing] and the chevron), so it lines up with the rows around it in every style. [content] gets
 * the width left of [trailing], centred vertically like a title; give a mark `Modifier.align(Alignment.Top)` to keep
 * it on the first of several lines. [onClickLabel] names the click for accessibility ("More actions").
 */
@Composable
fun JRow(
    modifier: Modifier = Modifier,
    trailing: (@Composable () -> Unit)? = null,
    onClick: (() -> Unit)? = null,
    onLongClick: (() -> Unit)? = null,
    onClickLabel: String? = null,
    chevron: Boolean = onClick != null && trailing == null,
    enabled: Boolean = true,
    selected: Boolean = false,
    /** What accessibility services call the row's click; a row that toggles a [JSwitch] passes [Role.Switch]. */
    role: Role = Role.Button,
    content: @Composable RowScope.() -> Unit,
) = RowFrame(modifier, onClick, onLongClick, onClickLabel, enabled, selected, value = null, trailing, chevron, role = role) {
    Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically, content = content)
}

/** The parts every [JRow] shares; [content] is laid out first, then the value, the trailing control and the chevron. */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun RowFrame(
    modifier: Modifier,
    onClick: (() -> Unit)?,
    onLongClick: (() -> Unit)?,
    onClickLabel: String?,
    enabled: Boolean,
    selected: Boolean,
    value: String?,
    trailing: (@Composable () -> Unit)?,
    chevron: Boolean,
    changingValue: Boolean = false,
    role: Role = Role.Button,
    content: @Composable RowScope.() -> Unit,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    Row(
        modifier.fillMaxWidth().heightIn(min = 56.dp).selectedRow(selected).onSecondaryClick(onLongClick)
            .then(if (onClick != null || onLongClick != null) Modifier.combinedClickable(enabled = enabled, role = role, onClickLabel = onClickLabel, onClick = onClick ?: {}, onLongClick = onLongClick) else Modifier)
            .stableAlpha(if (enabled) 1f else 0.45f).padding(horizontal = rowPadding, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        content()
        value?.let {
            Spacer(Modifier.width(10.dp))
            val valueStyle = if (style == AppStyle.Board) PersonaTheme.type.micro.copy(fontSize = androidx.compose.ui.unit.TextUnit(14f, androidx.compose.ui.unit.TextUnitType.Sp)) else PersonaTheme.type.callout
            if (changingValue) JChangingText(it, style = valueStyle, color = palette.ink3, modifier = Modifier.widthIn(max = 180.dp))
            else Text(it, style = valueStyle, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.widthIn(max = 180.dp))
        }
        trailing?.let { Spacer(Modifier.width(10.dp)); it() }
        if (chevron) { Spacer(Modifier.width(6.dp)); JIcon(PhIcons.CaretRight, null, size = 16.dp, tint = palette.ink3) }
    }
}

@Composable
fun JSwitchRow(
    title: String,
    checked: Boolean,
    onCheckedChange: (Boolean) -> Unit,
    modifier: Modifier = Modifier,
    subtitle: String? = null,
    leadingIcon: PhIcon? = null,
    enabled: Boolean = true,
) = JRow(title, modifier, subtitle, leadingIcon, value = null, onClick = { onCheckedChange(!checked) }, chevron = false, enabled = enabled,
    trailing = { JSwitch(checked, null, enabled = enabled) }, role = Role.Switch)

/**
 * A card: a frosted panel, a wash card, or a bordered strip. [status] tints the glass edge and gives board
 * strips a holder.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun JCard(
    modifier: Modifier = Modifier,
    onClick: (() -> Unit)? = null,
    onLongClick: (() -> Unit)? = null,
    status: Status? = null,
    padding: Dp = if (PersonaTheme.style == AppStyle.Board) 14.dp else 20.dp,
    content: @Composable ColumnScope.() -> Unit,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shape = PersonaTheme.shapes.cardShape
    val click = if (onClick != null || onLongClick != null) Modifier.combinedClickable(role = Role.Button, onClick = onClick ?: {}, onLongClick = onLongClick) else Modifier
    when (style) {
        AppStyle.Glass -> Column(
            modifier.fillMaxWidth().clip(shape).background(palette.surface)
                .border(1.dp, Brush.verticalGradient(listOf(status?.let { palette.status(it).dot.copy(alpha = 0.8f) } ?: palette.strokeTop, palette.strokeBottom)), shape)
                .then(click).padding(padding),
            content = content,
        )
        AppStyle.Brief -> Column(modifier.fillMaxWidth().clip(shape).background(palette.surface).then(click).padding(padding), content = content)
        AppStyle.Board -> Column(
            modifier.fillMaxWidth().clip(shape).background(palette.surface)
                .then(if (status != null) Modifier.holder(palette.status(status).dot, Tokens.Space.xxs) else Modifier)
                .border(1.dp, palette.line, shape).then(click)
                .padding(start = if (status != null) 6.dp + padding else padding, end = padding, top = padding, bottom = padding),
            content = content,
        )
    }
}

/** A notice inside a page (connection lost, permission needed). [status] picks the tone. */
@Composable
fun JNotice(
    text: String,
    modifier: Modifier = Modifier,
    title: String? = null,
    status: Status = Status.Unknown,
    icon: PhIcon = PhIcons.Info,
    actionLabel: String? = null,
    onAction: (() -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val tone = palette.status(status)
    JCard(modifier, status = status, padding = 16.dp) {
        Row(verticalAlignment = Alignment.Top) {
            JIcon(icon, null, size = 20.dp, tint = if (PersonaTheme.style == AppStyle.Brief) palette.ink else tone.dot, modifier = Modifier.padding(top = 2.dp))
            Spacer(Modifier.width(12.dp))
            Column(Modifier.weight(1f)) {
                title?.let { Text(it, style = PersonaTheme.type.headline, color = palette.ink) }
                Text(text, style = PersonaTheme.type.callout, color = palette.ink2)
                if (actionLabel != null && onAction != null) {
                    Spacer(Modifier.height(10.dp))
                    JButton(actionLabel, onAction, kind = JButtonKind.Secondary, size = JButtonSize.Medium)
                }
            }
        }
    }
}

@Composable
fun JEmptyState(
    title: String,
    description: String,
    modifier: Modifier = Modifier,
    icon: PhIcon? = null,
    action: (@Composable () -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    Column(modifier.fillMaxWidth().padding(horizontal = 32.dp, vertical = 40.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        icon?.let {
            Box(
                Modifier.size(64.dp).clip(if (PersonaTheme.style == AppStyle.Board) RoundedCornerShape(12.dp) else CircleShape).background(palette.surfaceStrong),
                contentAlignment = Alignment.Center,
            ) { JIcon(it, null, size = 28.dp, tint = palette.ink2, decorative = true) }
            Spacer(Modifier.height(16.dp))
        }
        Text(title, style = PersonaTheme.type.title, color = palette.ink, textAlign = TextAlign.Center)
        Spacer(Modifier.height(6.dp))
        Text(description, style = PersonaTheme.type.callout, color = palette.ink2, textAlign = TextAlign.Center)
        action?.let { Spacer(Modifier.height(18.dp)); it() }
    }
}

@Composable
fun JLoading(modifier: Modifier = Modifier, text: String? = null) {
    Column(modifier.fillMaxWidth().padding(32.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        JSpinner(Modifier.size(28.dp))
        text?.let { Spacer(Modifier.height(12.dp)); Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
    }
}

@Composable
fun JDivider(modifier: Modifier = Modifier, inset: Dp = 0.dp) {
    Box(modifier.padding(start = inset).fillMaxWidth().height(1.dp).background(PersonaTheme.palette.line))
}

/**
 * A person, group or source. [badge] is a small corner mark (Lark / mail). [image] is their picture, filling the same
 * shape; without one (none, still loading, failed) the initial or [icon] stands in.
 */
@Composable
fun JAvatar(
    text: String,
    modifier: Modifier = Modifier,
    size: Dp = 40.dp,
    icon: PhIcon? = null,
    badge: PhIcon? = null,
    badgeColor: Color? = null,
    image: Painter? = null,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shape = if (style == AppStyle.Board) RoundedCornerShape(size * 0.22f) else CircleShape
    Box(modifier.size(size)) {
        Box(
            Modifier.fillMaxSize().clip(shape).background(palette.surfaceStrong)
                .then(if (style == AppStyle.Glass) Modifier.border(1.dp, Brush.verticalGradient(listOf(palette.strokeTop, palette.strokeBottom)), shape) else Modifier),
            contentAlignment = Alignment.Center,
        ) {
            if (image != null) Image(image, null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
            else if (icon != null) JIcon(icon, null, size = size * 0.5f, tint = palette.ink2)
            else Text(text.trim().take(1), style = PersonaTheme.type.label.copy(fontSize = androidx.compose.ui.unit.TextUnit(size.value * 0.4f, androidx.compose.ui.unit.TextUnitType.Sp)), color = palette.ink2)
        }
        badge?.let {
            val color = badgeColor ?: if (style == AppStyle.Brief) palette.ink else palette.accent
            Box(
                Modifier.align(Alignment.BottomEnd).offset(3.dp, 3.dp).size(size * 0.45f).clip(if (style == AppStyle.Board) RoundedCornerShape(4.dp) else CircleShape)
                    .background(palette.background).padding(2.dp).clip(if (style == AppStyle.Board) RoundedCornerShape(3.dp) else CircleShape).background(color),
                contentAlignment = Alignment.Center,
            ) { JIcon(it, null, size = size * 0.26f, tint = if (style == AppStyle.Brief) palette.background else Color.White, selected = true) }
        }
    }
}

/**
 * Text the reader can long-press to select and copy (a link, an address shown to hand on), with no surface of its own;
 * [style] and [color] come from the theme like any [Text].
 */
@Composable
fun JSelectableText(
    text: String,
    style: androidx.compose.ui.text.TextStyle,
    color: Color,
    modifier: Modifier = Modifier,
    maxLines: Int = Int.MAX_VALUE,
) {
    SelectionContainer(modifier) {
        Text(text, style = style, color = color, maxLines = maxLines, overflow = TextOverflow.Ellipsis)
    }
}

/** Code and command text in the mono face on a sunken surface; selectable. */
@Composable
fun JCodeBlock(code: String, modifier: Modifier = Modifier, maxLines: Int = Int.MAX_VALUE, small: Boolean = false) =
    JCodeBlock(androidx.compose.ui.text.AnnotatedString(code), modifier, maxLines, small)

/** Rich code text (links, search highlights); [onTextLayout] lets readers locate matches. */
@Composable
fun JCodeBlock(
    code: androidx.compose.ui.text.AnnotatedString,
    modifier: Modifier = Modifier,
    maxLines: Int = Int.MAX_VALUE,
    small: Boolean = false,
    onTextLayout: (androidx.compose.ui.text.TextLayoutResult) -> Unit = {},
) {
    val palette = PersonaTheme.palette
    val shape = RoundedCornerShape(if (PersonaTheme.style == AppStyle.Board) 6.dp else 14.dp)
    SelectionContainer {
        Text(
            code, style = if (small) PersonaTheme.type.codeSmall else PersonaTheme.type.code, color = palette.ink, maxLines = maxLines, overflow = TextOverflow.Ellipsis,
            onTextLayout = onTextLayout,
            modifier = modifier.fillMaxWidth().clip(shape).background(if (PersonaTheme.style == AppStyle.Brief) palette.background else palette.sunken)
                .then(if (PersonaTheme.style == AppStyle.Brief) Modifier.border(1.dp, palette.line, shape) else Modifier).padding(14.dp),
        )
    }
}
