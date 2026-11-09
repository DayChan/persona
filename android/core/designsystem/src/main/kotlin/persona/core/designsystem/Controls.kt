package persona.core.designsystem

import androidx.compose.ui.res.stringResource
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.*
import androidx.compose.ui.draw.drawBehind
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsFocusedAsState
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Icon
import androidx.compose.material3.LocalContentColor
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.draw.scale
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.autofill.ContentDataType
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDataType
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.toggleableState
import androidx.compose.ui.state.ToggleableState
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons

// ------------------------------------------------------------------ icons

/**
 * A Phosphor icon in the weight the style uses: regular in glass and brief, bold on the board, filled when
 * [selected]. [decorative] icons (leading icons of settings rows) use the duotone weight in glass.
 */
@Composable
fun JIcon(
    icon: PhIcon,
    contentDescription: String? = null,
    modifier: Modifier = Modifier,
    size: Dp = 22.dp,
    tint: Color = LocalContentColor.current,
    selected: Boolean = false,
    decorative: Boolean = false,
) {
    val vector = when {
        selected -> icon.fill
        PersonaTheme.style == AppStyle.Board -> icon.bold
        decorative && PersonaTheme.style == AppStyle.Glass -> icon.duotone
        else -> icon.regular
    }
    Icon(vector, contentDescription, modifier.size(size), tint = tint)
}

// ------------------------------------------------------------------ buttons

/**
 * Draws the rest of the chain at [alpha]: the dimmed content of a disabled control. Use it instead of
 * `Modifier.alpha` wherever the value changes. `Modifier.alpha(1f)` drops its graphicsLayer and any other value
 * adds one, so turning a control enabled removed a layer from the middle of its chain; behind `clip` and
 * `background` (a button's fill, a field's frame) Compose then stopped drawing that background until it changed
 * again, and the button showed only its label (Compose UI 1.12.1, every style). This layer always stays and only
 * its alpha changes.
 */
fun Modifier.stableAlpha(alpha: Float): Modifier = graphicsLayer { this.alpha = alpha }

enum class JButtonKind { Primary, Secondary, Quiet, Destructive }

enum class JButtonSize(val height: Dp, val padding: Dp) { Large(48.dp, 20.dp), Medium(40.dp, 16.dp), Small(32.dp, 12.dp) }

/** Labels are always one line (project rule); long ones ellipsize rather than wrap. */
@Composable
fun JButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    kind: JButtonKind = JButtonKind.Primary,
    size: JButtonSize = JButtonSize.Large,
    icon: PhIcon? = null,
    enabled: Boolean = true,
    loading: Boolean = false,
    loadingText: String? = null,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shapes = PersonaTheme.shapes
    val shape = RoundedCornerShape(if (style == AppStyle.Board) shapes.button else size.height / 2)
    val interaction = remember { MutableInteractionSource() }
    val pressed by interaction.collectIsPressedAsState()
    val scale by animateFloatAsState(if (pressed && style == AppStyle.Glass && !PersonaTheme.reduceMotion) 0.96f else 1f, label = "press")
    val (fill, content) = when (kind) {
        JButtonKind.Primary -> palette.action to palette.onAction
        JButtonKind.Secondary -> (if (style == AppStyle.Board) palette.surface else palette.surfaceStrong) to palette.ink
        JButtonKind.Quiet -> Color.Transparent to (if (style == AppStyle.Board) palette.accent else palette.ink2)
        JButtonKind.Destructive -> palette.statusSurface to palette.danger.text
    }
    val frame = when {
        kind == JButtonKind.Secondary && style == AppStyle.Glass ->
            Modifier.clip(shape).background(fill).border(1.dp, Brush.verticalGradient(listOf(palette.strokeTop, palette.strokeBottom)), shape)
        kind == JButtonKind.Secondary && style == AppStyle.Board -> Modifier.clip(shape).background(fill).border(1.dp, palette.line, shape)
        else -> Modifier.clip(shape).background(fill)
    }
    val busy = loading
    Row(
        modifier.heightIn(min = size.height).scale(scale).then(frame)
            .clickable(interaction, indication = androidx.compose.material3.ripple(), enabled = enabled && !busy, role = Role.Button, onClick = onClick)
            .stableAlpha(if (enabled || busy) 1f else 0.4f)
            .padding(horizontal = if (kind == JButtonKind.Quiet) 8.dp else size.padding),
        horizontalArrangement = Arrangement.Center, verticalAlignment = Alignment.CenterVertically,
    ) {
        if (busy) {
            JSpinner(Modifier.size(16.dp), color = content)
            Spacer(Modifier.width(8.dp))
        } else if (icon != null) {
            JIcon(icon, null, size = if (size == JButtonSize.Small) 15.dp else 18.dp, tint = content, selected = kind == JButtonKind.Primary)
            Spacer(Modifier.width(6.dp))
        }
        Text(
            if (busy) loadingText ?: text else text,
            style = if (size == JButtonSize.Large) PersonaTheme.type.label else PersonaTheme.type.labelSmall,
            color = content, maxLines = 1, softWrap = false, overflow = TextOverflow.Ellipsis,
        )
    }
}

@Composable
fun JTextButton(text: String, onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true, icon: PhIcon? = null) =
    JButton(text, onClick, modifier, kind = JButtonKind.Quiet, size = JButtonSize.Medium, icon = icon, enabled = enabled)

/** Plain icons sit in bars; [raised] ones are standalone controls (a glass circle, a wash disc, a bordered square). */
@Composable
fun JIconButton(
    icon: PhIcon,
    contentDescription: String?,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    raised: Boolean = LocalIconsRaised.current,
    tint: Color? = null,
    badge: Boolean = false,
    selected: Boolean = false,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val color = tint ?: palette.ink
    val surface = when {
        !raised -> Modifier
        style == AppStyle.Glass -> Modifier.size(44.dp).floatingSurface(CircleShape)
        style == AppStyle.Brief -> Modifier.size(42.dp).clip(CircleShape).background(palette.surface)
        else -> Modifier.size(40.dp).clip(RoundedCornerShape(8.dp)).background(palette.surface).border(1.dp, palette.line, RoundedCornerShape(8.dp))
    }
    Box(
        modifier.size(48.dp).clip(CircleShape).clickable(enabled = enabled, role = Role.Button, onClick = onClick)
            .semantics { if (contentDescription != null) this.contentDescription = contentDescription }
            .stableAlpha(if (enabled) 1f else 0.4f),
        contentAlignment = Alignment.Center,
    ) {
        Box(surface, contentAlignment = Alignment.Center) {
            JIcon(icon, null, size = if (raised) 20.dp else 24.dp, tint = color, selected = selected)
        }
        if (badge) Box(
            Modifier.align(Alignment.TopEnd).padding(top = 10.dp, end = 10.dp).size(9.dp).clip(CircleShape)
                .background(palette.needsYou.dot).border(1.5.dp, palette.background, CircleShape),
        )
    }
}

// ------------------------------------------------------------------ text fields

/**
 * The text field of every style. [label] sits above the field; [supportingText] below it. Secrets pass a
 * [visualTransformation] as with Material fields.
 */
@Composable
fun JTextField(
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    placeholder: String? = null,
    label: String? = null,
    supportingText: String? = null,
    singleLine: Boolean = false,
    minLines: Int = 1,
    maxLines: Int = if (singleLine) 1 else 6,
    enabled: Boolean = true,
    isError: Boolean = false,
    leading: PhIcon? = null,
    trailing: (@Composable () -> Unit)? = null,
    keyboardOptions: KeyboardOptions = KeyboardOptions.Default,
    keyboardActions: KeyboardActions = KeyboardActions.Default,
    visualTransformation: VisualTransformation = VisualTransformation.None,
    /** False keeps the field out of Autofill: nothing is filled into it, nothing typed is offered to be saved. */
    autofill: Boolean = true,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val interaction = remember { MutableInteractionSource() }
    val focused by interaction.collectIsFocusedAsState()
    val shape = PersonaTheme.shapes.controlShape
    val (fill, border) = when (style) {
        AppStyle.Glass -> palette.sunken to if (focused) palette.accent else palette.line
        AppStyle.Brief -> palette.surface to if (focused) palette.ink else Color.Transparent
        AppStyle.Board -> palette.surface to if (focused) palette.accent else palette.line
    }
    Column(modifier) {
        label?.let {
            Text(it, style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Spacer(Modifier.height(6.dp))
        }
        BasicTextField(
            value = value, onValueChange = onValueChange, enabled = enabled, singleLine = singleLine,
            minLines = minLines, maxLines = maxLines, interactionSource = interaction,
            textStyle = PersonaTheme.type.body.copy(color = palette.ink), cursorBrush = SolidColor(palette.accent.takeIf { style != AppStyle.Brief } ?: palette.ink),
            keyboardOptions = keyboardOptions, keyboardActions = keyboardActions, visualTransformation = visualTransformation,
            // On the text field's own semantics node: Autofill reads the node it would fill, not its parents.
            modifier = if (autofill) Modifier.fillMaxWidth() else Modifier.fillMaxWidth().semantics { contentDataType = ContentDataType.None },
            decorationBox = { field ->
                Row(
                    Modifier.fillMaxWidth().heightIn(min = 48.dp).clip(shape).background(fill)
                        .border(if (focused || style != AppStyle.Brief) 1.dp else 0.dp, if (isError) palette.danger.dot else border, shape)
                        .stableAlpha(if (enabled) 1f else 0.5f).padding(horizontal = 14.dp, vertical = 12.dp),
                    verticalAlignment = if (singleLine) Alignment.CenterVertically else Alignment.Top,
                ) {
                    leading?.let { JIcon(it, null, size = 20.dp, tint = palette.ink3); Spacer(Modifier.width(10.dp)) }
                    Box(Modifier.weight(1f)) {
                        if (value.isEmpty() && placeholder != null) Text(placeholder, style = PersonaTheme.type.body, color = palette.ink3, maxLines = if (singleLine) 1 else maxLines, overflow = TextOverflow.Ellipsis)
                        field()
                    }
                    trailing?.let { Spacer(Modifier.width(6.dp)); it() }
                }
            },
        )
        supportingText?.let {
            Spacer(Modifier.height(6.dp))
            Text(it, style = PersonaTheme.type.caption, color = if (isError) palette.danger.text else palette.ink3)
        }
    }
}

/**
 * A password the app never keeps (iOS `SecretField`): masked, on the password keyboard without corrections or
 * suggestions, and outside Autofill (`ContentDataType.None`), since it is a password of the user's own computer, not
 * this app's. The keyboard's action key only puts the keyboard away; nothing is sent from the keyboard.
 */
@Composable
fun JSecretField(value: String, onValueChange: (String) -> Unit, label: String, modifier: Modifier = Modifier, enabled: Boolean = true) {
    val focus = LocalFocusManager.current
    JTextField(
        value, onValueChange, modifier, label = label, singleLine = true, enabled = enabled,
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, autoCorrectEnabled = false, imeAction = ImeAction.Done),
        keyboardActions = KeyboardActions(onDone = { focus.clearFocus() }),
        visualTransformation = PasswordVisualTransformation(), autofill = false,
    )
}

@Composable
fun JSearchField(query: String, onQueryChange: (String) -> Unit, placeholder: String, modifier: Modifier = Modifier) {
    val clear = stringResource(R.string.designsystem_search_clear)
    JTextField(
        query, onQueryChange, modifier, placeholder = placeholder, singleLine = true, leading = PhIcons.MagnifyingGlass,
        trailing = if (query.isEmpty()) null else ({
            Box(Modifier.size(24.dp).clip(CircleShape).clickable(role = Role.Button) { onQueryChange("") }.semantics { contentDescription = clear }, contentAlignment = Alignment.Center) {
                JIcon(PhIcons.XCircle, null, size = 18.dp, tint = PersonaTheme.palette.ink3, selected = true)
            }
        }),
    )
}

// ------------------------------------------------------------------ toggles

@Composable
fun JSwitch(checked: Boolean, onCheckedChange: ((Boolean) -> Unit)?, modifier: Modifier = Modifier, enabled: Boolean = true) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val (width, height, thumb) = when (style) {
        AppStyle.Glass -> Triple(50.dp, 30.dp, 24.dp)
        AppStyle.Brief -> Triple(48.dp, 28.dp, 22.dp)
        AppStyle.Board -> Triple(46.dp, 26.dp, 20.dp)
    }
    val corner = if (style == AppStyle.Board) 6.dp else height / 2
    val track = when {
        checked && style == AppStyle.Brief -> palette.action
        checked -> palette.done.dot
        else -> palette.surfaceStrong.compositeOn(palette.surfaceOpaque)
    }
    val offset by animateDpAsState(if (checked) width - thumb - 6.dp else 0.dp, label = "thumb")
    Box(
        modifier.size(width, height).clip(RoundedCornerShape(corner)).background(track)
            .then(if (!checked) Modifier.border(1.dp, palette.line, RoundedCornerShape(corner)) else Modifier)
            .then(if (onCheckedChange != null) Modifier.clickable(enabled = enabled, role = Role.Switch) { onCheckedChange(!checked) } else Modifier)
            .semantics { toggleableState = ToggleableState(checked) }
            .stableAlpha(if (enabled) 1f else 0.4f).padding(3.dp),
        contentAlignment = Alignment.CenterStart,
    ) {
        val thumbColor = if (checked && style == AppStyle.Brief) palette.onAction else Color.White
        Box(
            Modifier.offset(x = offset).size(thumb)
                .shadow(if (style == AppStyle.Glass) 2.dp else 0.dp, RoundedCornerShape(if (style == AppStyle.Board) 4.dp else thumb / 2))
                .clip(RoundedCornerShape(if (style == AppStyle.Board) 4.dp else thumb / 2)).background(thumbColor),
        )
    }
}

@Composable
fun JCheckbox(checked: Boolean, onCheckedChange: ((Boolean) -> Unit)?, modifier: Modifier = Modifier, enabled: Boolean = true) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shape = when (style) { AppStyle.Glass -> CircleShape; AppStyle.Brief -> RoundedCornerShape(7.dp); AppStyle.Board -> RoundedCornerShape(4.dp) }
    val on = if (style == AppStyle.Brief) palette.action else palette.accent
    Box(
        modifier.size(48.dp).then(if (onCheckedChange != null) Modifier.clickable(enabled = enabled, role = Role.Checkbox) { onCheckedChange(!checked) } else Modifier)
            .semantics { toggleableState = ToggleableState(checked) }.stableAlpha(if (enabled) 1f else 0.4f),
        contentAlignment = Alignment.Center,
    ) {
        Box(
            Modifier.size(22.dp).clip(shape).background(if (checked) on else Color.Transparent)
                .border(if (checked) 0.dp else 1.5.dp, palette.ink3, shape),
            contentAlignment = Alignment.Center,
        ) { if (checked) JIcon(PhIcons.Check, null, size = 15.dp, tint = if (palette.customRoles) ExtensionThemeColors.on(on) else if (style == AppStyle.Brief) palette.onAction else Color.White, selected = false) }
    }
}

@Composable
fun JRadio(selected: Boolean, onClick: (() -> Unit)?, modifier: Modifier = Modifier, enabled: Boolean = true) {
    val palette = PersonaTheme.palette
    val on = if (PersonaTheme.style == AppStyle.Brief) palette.action else palette.accent
    Box(
        modifier.size(48.dp).then(if (onClick != null) Modifier.clickable(enabled = enabled, role = Role.RadioButton, onClick = onClick) else Modifier)
            .semantics { this.selected = selected }.stableAlpha(if (enabled) 1f else 0.4f),
        contentAlignment = Alignment.Center,
    ) {
        Box(Modifier.size(22.dp).clip(CircleShape).border(if (selected) 6.5.dp else 1.5.dp, if (selected) on else palette.ink3, CircleShape))
    }
}

// ------------------------------------------------------------------ segmented filters and chips

data class JSegment(val label: String, val count: Int? = null, val status: Status? = null)

/**
 * A small set of mutually exclusive filters: a sliding pill in glass, underlined text tabs in brief, a ruled
 * strip of cells on the board.
 */
@Composable
fun JSegmented(options: List<JSegment>, selected: Int, onSelect: (Int) -> Unit, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    fun text(segment: JSegment) = segment.label + (segment.count?.let { " $it" } ?: "")
    when (PersonaTheme.style) {
        AppStyle.Glass -> Row(
            modifier.clip(CircleShape).background(palette.surface)
                .border(1.dp, Brush.verticalGradient(listOf(palette.strokeTop, palette.strokeBottom)), CircleShape).padding(4.dp),
        ) {
            options.forEachIndexed { index, segment ->
                val on = index == selected
                Box(
                    Modifier.clip(CircleShape).background(if (on) palette.action else Color.Transparent)
                        .clickable(role = Role.Tab) { onSelect(index) }.semantics { this.selected = on }
                        .padding(horizontal = 16.dp, vertical = 9.dp),
                    contentAlignment = Alignment.Center,
                ) { Text(text(segment), style = type.labelSmall, color = if (on) palette.onAction else palette.ink2, maxLines = 1) }
            }
        }
        AppStyle.Brief -> Row(modifier, horizontalArrangement = Arrangement.spacedBy(22.dp)) {
            options.forEachIndexed { index, segment ->
                val on = index == selected
                Column(
                    Modifier.clickable(role = Role.Tab) { onSelect(index) }.semantics { this.selected = on }.padding(vertical = 6.dp),
                    horizontalAlignment = Alignment.CenterHorizontally,
                ) {
                    Text(text(segment), style = type.label, color = if (on) palette.ink else palette.ink3, maxLines = 1)
                    Spacer(Modifier.height(6.dp))
                    Box(Modifier.size(20.dp, 2.5.dp).clip(CircleShape).background(if (on) palette.ink else Color.Transparent))
                }
            }
        }
        AppStyle.Board -> Row(
            modifier.height(IntrinsicSize.Min).clip(RoundedCornerShape(8.dp)).background(palette.surface).border(1.dp, palette.line, RoundedCornerShape(8.dp)),
        ) {
            options.forEachIndexed { index, segment ->
                val on = index == selected
                if (index > 0) Box(Modifier.width(1.dp).fillMaxHeight().background(palette.line))
                val accent = palette.accent
                Box(
                    Modifier.weight(1f, fill = false)
                        // The selected one's underline is drawn, not laid out: a fillMaxWidth line widened that segment to
                        // its whole share of the row (the image editor's "Pen" took the row's width, "Box" stayed small).
                        .drawWithContent {
                            drawContent()
                            if (on) drawRect(accent, Offset(0f, size.height - 2.dp.toPx()), Size(size.width, 2.dp.toPx()))
                        }
                        .clickable(role = Role.Tab) { onSelect(index) }.semantics { this.selected = on },
                ) {
                    Text(
                        text(segment), style = type.labelSmall.copy(fontWeight = if (on) FontWeight.Bold else FontWeight.Medium),
                        color = if (on) palette.ink else palette.ink2, maxLines = 1, modifier = Modifier.padding(horizontal = 14.dp, vertical = 10.dp),
                    )
                }
            }
        }
    }
}

/**
 * A filter or a fact. With [onClick] it toggles; without, it is a quiet label. [onLongClick] (with [onClick]) is a long
 * press for more (a menu of other ways to use it); the chip looks the same.
 */
@Composable
fun JChip(
    text: String,
    modifier: Modifier = Modifier,
    selected: Boolean = false,
    onClick: (() -> Unit)? = null,
    icon: PhIcon? = null,
    status: Status? = null,
    count: Int? = null,
    /** Greyed text (not selected): what it stands for is not reachable now, like an offline manager. */
    muted: Boolean = false,
    onLongClick: (() -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shape = RoundedCornerShape(PersonaTheme.shapes.chip)
    val (fill, plainInk, border) = when (style) {
        AppStyle.Glass -> Triple(if (selected) palette.action else palette.surfaceStrong, if (selected) palette.onAction else palette.ink, if (selected) Color.Transparent else palette.strokeBottom)
        AppStyle.Brief -> Triple(if (selected) palette.action else palette.surface, if (selected) palette.onAction else palette.ink, Color.Transparent)
        AppStyle.Board -> Triple(if (selected) palette.statusSurface else palette.surface, palette.ink, if (selected) palette.accent else palette.line)
    }
    val ink = if (muted && (!selected || style == AppStyle.Board)) palette.ink3 else plainInk
    Row(
        modifier.heightIn(min = if (onClick != null) 36.dp else 28.dp).clip(shape).background(fill).border(1.dp, border, shape)
            .then(when {
                onClick != null && onLongClick != null ->
                    Modifier.combinedClickable(role = Role.Checkbox, onClick = onClick, onLongClick = onLongClick).semantics { this.selected = selected }
                onClick != null -> Modifier.clickable(role = Role.Checkbox, onClick = onClick).semantics { this.selected = selected }
                else -> Modifier
            })
            .padding(horizontal = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        status?.let { JStatusDot(it, size = 8.dp); Spacer(Modifier.width(6.dp)) }
        icon?.let { JIcon(it, null, size = 15.dp, tint = ink); Spacer(Modifier.width(5.dp)) }
        Text(text, style = PersonaTheme.type.labelSmall.copy(fontWeight = FontWeight.Medium), color = ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
        count?.let {
            Spacer(Modifier.width(5.dp))
            Text("$it", style = PersonaTheme.type.micro, color = if (selected && style != AppStyle.Board) ink else palette.ink3)
        }
    }
}

/** A count waiting for the user (tab badges, request counts). */
@Composable
fun JBadge(count: Int, modifier: Modifier = Modifier, text: String? = null) {
    val palette = PersonaTheme.palette
    val label = text ?: if (count > 99) "99+" else "$count"
    val shape = if (PersonaTheme.style == AppStyle.Board) RoundedCornerShape(4.dp) else CircleShape
    Box(
        modifier.heightIn(min = 18.dp).widthIn(min = 18.dp).clip(shape).background(palette.needsYou.dot)
            .border(1.5.dp, palette.background, shape).padding(horizontal = 5.dp),
        contentAlignment = Alignment.Center,
    ) { Text(label, style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.Bold, fontSize = androidx.compose.ui.unit.TextUnit(11f, androidx.compose.ui.unit.TextUnitType.Sp), lineHeight = androidx.compose.ui.unit.TextUnit(14f, androidx.compose.ui.unit.TextUnitType.Sp)), color = palette.needsYou.onDot, maxLines = 1) }
}

/** A small static label such as "Draft", "Demo", "Read-only". */
@Composable
fun JTag(text: String, modifier: Modifier = Modifier, status: Status? = null) =
    JTagColored(text, modifier, status?.let { PersonaTheme.palette.status(it) })

/**
 * A small label in a role colour ([Tone]): an extension's badge ("Valid", "Expiring soon"). [animated]: drawn again with another tone
 * its colours go over to the new ones and a new text fades in (an extension's row badge, #2811).
 */
@Composable
fun JTag(text: String, tone: Tone, modifier: Modifier = Modifier, animated: Boolean = false) =
    JTagColored(text, modifier, PersonaTheme.palette.tone(tone).takeIf { tone != Tone.Neutral }, animated)

/** A small label in a named colour ([JHue], UI version 6): a category ("客厅") rather than a meaning. */
@Composable
fun JTag(text: String, hue: JHue, modifier: Modifier = Modifier, animated: Boolean = false) =
    JTagColored(text, modifier, PersonaTheme.palette.hue(hue), animated)

@Composable
internal fun JTagColored(text: String, modifier: Modifier, tone: StatusColors?, animated: Boolean = false) {
    val palette = PersonaTheme.palette
    val shape = RoundedCornerShape(if (PersonaTheme.style == AppStyle.Board) 4.dp else 6.dp)
    val style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.SemiBold)
    var ink = tone?.text ?: palette.ink2
    var fill = palette.statusSurface
    if (animated) {
        val spec: AnimationSpec<Color> = if (PersonaTheme.reduceMotion) snap() else tween(PersonaTheme.motion.standard)
        ink = animateColorAsState(ink, spec, label = "tag ink").value
        fill = animateColorAsState(fill, spec, label = "tag fill").value
        Box(modifier.clip(shape).drawBehind { drawRect(fill) }.padding(TagPadding)) { JChangingText(text, style = style, color = ink) }
    } else Text(text, style = style, color = ink, maxLines = 1, modifier = modifier.clip(shape).background(fill).padding(TagPadding))
}

private val TagPadding = PaddingValues(horizontal = 6.dp, vertical = 2.dp)


// ------------------------------------------------------------------ progress

/** The style's spinner: an ink arc in brief, an accent arc elsewhere. Static when motion is reduced. */
@Composable
fun JSpinner(modifier: Modifier = Modifier, color: Color = if (PersonaTheme.style == AppStyle.Brief) PersonaTheme.palette.ink else PersonaTheme.palette.accent, stroke: Dp = 2.2.dp) {
    val turning = !PersonaTheme.reduceMotion
    val loading = stringResource(R.string.designsystem_loading)
    val angle = if (turning) rememberInfiniteTransition(label = "spin").animateFloat(0f, 360f, infiniteRepeatable(tween(1100, easing = LinearEasing)), label = "angle").value else 0f
    Canvas(modifier.size(20.dp).clearAndSetSemantics { contentDescription = loading }) {
        val width = stroke.toPx()
        val inset = width / 2
        val arcSize = Size(size.width - width, size.height - width)
        drawArc(color.copy(alpha = 0.2f), 0f, 360f, false, Offset(inset, inset), arcSize, style = Stroke(width))
        drawArc(color, angle - 90f, 100f, false, Offset(inset, inset), arcSize, style = Stroke(width, cap = StrokeCap.Round))
    }
}

@Composable
fun JLinearProgress(progress: Float?, modifier: Modifier = Modifier, tone: Tone? = null) {
    val palette = PersonaTheme.palette
    // A toned bar (an extension's progress) takes the role colour; otherwise the style's ink or accent.
    val color = tone?.takeIf { it != Tone.Neutral }?.let { palette.tone(it).dot }
        ?: if (PersonaTheme.style == AppStyle.Brief) palette.ink else palette.accent
    Box(modifier.fillMaxWidth().height(4.dp).clip(CircleShape).background(palette.surfaceStrong)) {
        if (progress != null) Box(Modifier.fillMaxWidth(progress.coerceIn(0f, 1f)).fillMaxHeight().clip(CircleShape).background(color))
        else {
            val shift by rememberInfiniteTransition(label = "bar").animateFloat(0f, 1f, infiniteRepeatable(tween(1200, easing = LinearEasing)), label = "shift")
            BoxWithConstraints(Modifier.fillMaxSize()) {
                Box(Modifier.offset(x = maxWidth * (shift * 1.3f - 0.3f)).width(maxWidth * 0.3f).fillMaxHeight().clip(CircleShape).background(color))
            }
        }
    }
}
