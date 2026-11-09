package persona.feature.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.input.KeyboardType
import coil3.compose.AsyncImage
import kotlinx.coroutines.delay
import kotlinx.serialization.json.*
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.stream.NativeStreamState
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import java.math.BigDecimal

internal typealias NativePress = (String, ExtensionViewItem, ExtensionViewAction, Map<String, String>, String) -> Unit
private fun JsonObject.text(name: String) = (this[name] as? JsonPrimitive)?.content
private fun JsonObject.action() = JsonCodec.decodeFromJsonElement(ExtensionViewAction.serializer(), this)

/** Fixed native components missing from the legacy display projection. Never decodes or executes extension code. */
@Composable
internal fun NativeStreamItem(item: ExtensionViewItem, display: ExtensionViewState, state: NativeStreamState,
                              local: NativeExtensionLocal, modifier: Modifier, handle: (String) -> ExtensionViewHandle,
                              press: NativePress, edit: (String, String) -> Unit): Boolean {
    val node = state.tree?.nodes?.get(item.key) ?: return false
    when (item.type) {
        "row" -> NativeRow(node, item, state, local, modifier, handle, press, edit)
        "image" -> NativeImage(node, local, modifier)
        "grid", "carousel" -> NativeTiles(node, state, local, modifier, press, edit)
        "field" -> if (item.kind in setOf("text", "number", "multiline") || item.detail != null) {
            Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                if (item.kind in setOf("text", "number", "multiline")) JTextField(display.values[item.name].orEmpty(),
                    { value -> if (item.kind != "number" || ExtensionViewText.isNumber(value)) edit(item.key.orEmpty(), value) },
                    Modifier.fillMaxWidth().nativeInputFocus(local, item.key.orEmpty()), label = item.label, placeholder = item.placeholder, singleLine = item.kind != "multiline", enabled = !display.busy,
                    keyboardOptions = KeyboardOptions(keyboardType = if (item.kind == "number") KeyboardType.Decimal else KeyboardType.Text))
                else GroupedItem(item.copy(detail = null), display, handle(item.key.orEmpty()).withoutNative())
                item.detail?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3) }
            }
        } else return false
        else -> return false
    }
    return true
}

/** Only used to draw one already-supported legacy input inside the opt-in wrapper. */
private fun ExtensionViewHandle.withoutNative() = ExtensionViewHandle(press, edit, choose, open, segment)

@Composable
private fun NativeRow(node: JsonObject, item: ExtensionViewItem, state: NativeStreamState, local: NativeExtensionLocal,
                      modifier: Modifier, handle: (String) -> ExtensionViewHandle, press: NativePress, edit: (String, String) -> Unit) {
    val key = node.getValue("key").jsonPrimitive.content
    val control = node["control"] as? JsonObject
    Column(modifier.fillMaxWidth()) {
        NativeActionContainer(node, local, press) {
            JRow(item.title.orEmpty(), subtitle = item.subtitle, value = item.value,
                leadingIcon = item.icon?.let(ExtensionIcons::of), leading = rowLeading(item, handle(key)),
                trailing = {
                    item.badge?.let { JTag(it.text, ExtensionViewText.tone(it.tone)) }
                    if (control?.text("kind") == "switch") NativeControl(key, item.title.orEmpty(), control, state, local, press, edit)
                    NativeMenu(node, local, press)
                }, chevron = item.action != null, onClick = item.action?.let { action -> { press(key, item, action, emptyMap(), "press") } })
        }
        if (control != null && control.text("kind") != "switch") Box(Modifier.padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.s)) {
            NativeControl(key, item.title.orEmpty(), control, state, local, press, edit)
        }
    }
}

@Composable
private fun NativeActionContainer(node: JsonObject, local: NativeExtensionLocal, press: NativePress,
                                  modifier: Modifier = Modifier, content: @Composable () -> Unit) {
    val actions = (node["actions"] as? JsonArray).orEmpty().map { it.jsonObject }.filter { it["swipe"] == JsonPrimitive(true) }
    val key = node.getValue("key").jsonPrimitive.content
    val label = stringResource(R.string.settings_ext_native_actions)
    var open by local.slot(key, "swipe-menu") { false }
    val swipe = actions.firstOrNull()?.let { first -> SwipeAction(if (actions.size > 1) label else first.text("title").orEmpty(),
        first.text("icon")?.let(ExtensionIcons::of) ?: PhIcons.DotsThree, Status.Idle,
        onTrigger = {
            if (actions.size > 1) open = true else press(key, ExtensionViewItem(type = "button", title = first.text("title"), role = first.text("role")),
                first.getValue("action").jsonObject.action(), emptyMap(), "press")
        }, destructive = actions.size == 1 && first.text("role") == "destructive") }
    JSwipeActions(modifier = modifier, end = swipe, content = content)
    if (open) JActionSheet(actions.map { action -> nativeMenuAction(key, action, press) }, { open = false }, title = label)
}

private fun nativeMenuAction(key: String, action: JsonObject, press: NativePress) = JAction(action.text("title").orEmpty(),
    icon = action.text("icon")?.let(ExtensionIcons::of), destructive = action.text("role") == "destructive",
    onClick = { press(key, ExtensionViewItem(type = "button", title = action.text("title"), role = action.text("role")),
        action.getValue("action").jsonObject.action(), emptyMap(), "press") })

@Composable
private fun NativeMenu(node: JsonObject, local: NativeExtensionLocal, press: NativePress) {
    val actions = (node["actions"] as? JsonArray).orEmpty().map { it.jsonObject }
    if (actions.isEmpty()) return
    val key = node.getValue("key").jsonPrimitive.content
    var open by local.slot(key, "menu") { false }
    val label = stringResource(R.string.settings_ext_native_actions)
    JIconButton(PhIcons.DotsThree, label, { open = true }, raised = false)
    if (open) JActionSheet(actions.map { action -> nativeMenuAction(key, action, press) }, { open = false }, title = node.text("title"))
}

private data class StepperPending(val value: String, val control: JsonObject)
@Composable
private fun NativeControl(key: String, title: String, control: JsonObject, state: NativeStreamState, local: NativeExtensionLocal,
                          press: NativePress, edit: (String, String) -> Unit) {
    val kind = control.text("kind")
    val enabled = control["disabled"] != JsonPrimitive(true) && state.phase == persona.core.contracts.stream.NativeStreamPhase.Ready
    val action = control.getValue("action").jsonObject.action()
    val name = control.text("name").orEmpty()
    val needsConfirmation = !action.confirm.isNullOrBlank()
    // Preview confirmed changes locally. Cancel never creates a session draft or overwrites an earlier one.
    var preview by local.slot<String?>(key, "control-preview") { null }
    val value = preview ?: nativeControlValue(control, state.drafts[key]?.value)
    fun update(next: String) { if (needsConfirmation) preview = next else edit(key, next) }
    fun send(next: String) {
        preview = null
        press(key, ExtensionViewItem(type = "row", title = title), action, mapOf(name to next), "change")
    }
    if (kind == "switch") {
        JSwitch(value == "true", { checked -> if (enabled) { val next = checked.toString(); update(next); send(next) } },
            Modifier.semantics { contentDescription = title }, enabled = enabled)
        return
    }
    val min = control.text("min")?.toBigDecimalOrNull() ?: BigDecimal.ZERO
    val max = control.text("max")?.toBigDecimalOrNull() ?: BigDecimal(100)
    val step = control.text("step")?.toBigDecimalOrNull() ?: if (kind == "slider") (max - min).divide(BigDecimal(100)) else BigDecimal.ONE
    val number = (value.toBigDecimalOrNull() ?: min).coerceIn(min, max)
    val label = control.text("display")?.takeIf { key !in state.drafts } ?: (number.stripTrailingZeros().toPlainString() + control.text("unit").orEmpty())
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        Text(label, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
        if (kind == "slider") {
            var dragging by local.slot(key, "slider") { number.toFloat() }
            JSlider(number.toFloat(), { next -> dragging = next; update(next.toString()) },
                valueRange = min.toFloat()..max.toFloat(), step = step.toFloat(), enabled = enabled, contentDescription = title,
                onValueChangeFinished = { if (enabled) send(dragging.toString()) })
        } else {
            var pending by local.slot<StepperPending?>(key, "stepper") { null }
            pending?.let { saved -> LaunchedEffect(saved, enabled, control) {
                if (!enabled) return@LaunchedEffect
                delay(NativeStreamLayout.stepperPauseMillis)
                // Retained data never retains a discarded renderer scope/tree closure.
                pending = null
                if (saved.control == control) {
                    preview = null
                    local.controlPress?.invoke(key, ExtensionViewItem(type = "row", title = title), action, mapOf(name to saved.value), "change")
                        ?: run { local.rejected = true; local.discardStepper(key, state.drafts[key]) }
                }
                else {
                    local.rejected = true
                    local.discardStepper(key, state.drafts[key])
                }
            } }
            val down = stringResource(R.string.settings_ext_native_decrease, title)
            val up = stringResource(R.string.settings_ext_native_increase, title)
            fun change(next: BigDecimal) {
                val text = next.coerceIn(min, max).stripTrailingZeros().toPlainString()
                update(text)
                pending = StepperPending(text, control)
            }
            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JIconButton(PhIcons.CaretDown, down, { change(number - step) }, enabled = enabled && number > min)
                JIconButton(PhIcons.CaretUp, up, { change(number + step) }, enabled = enabled && number < max)
            }
        }
    }
}

@Composable
private fun NativeImage(node: JsonObject, local: NativeExtensionLocal, modifier: Modifier) {
    val src = node.getValue("src").jsonObject
    val key = node.getValue("key").jsonPrimitive.content
    val alt = node.text("alt").orEmpty()
    val aspect = when (node.text("aspect")) {
        "square" -> NativeStreamLayout.squareAspect
        "wide" -> NativeStreamLayout.wideAspect
        "portrait" -> NativeStreamLayout.portraitAspect
        "auto" -> {
            val width = (src["width"] as? JsonPrimitive)?.floatOrNull
            val height = (src["height"] as? JsonPrimitive)?.floatOrNull
            if (width != null && height != null) width / height else NativeStreamLayout.landscapeAspect
        }
        else -> NativeStreamLayout.landscapeAspect
    }
    var zoom by local.slot(key, "zoom") { false }
    var loaded by remember(src) { mutableStateOf(false) }
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        // Accessible fallback remains visible on loading/failure; URLs are HTTPS without hub/device credentials.
        Box(Modifier.fillMaxWidth().aspectRatio(aspect).background(PersonaTheme.palette.sunken)
            .then(if (node["no_zoom"] != JsonPrimitive(true)) Modifier.clickable { zoom = true } else Modifier), contentAlignment = Alignment.Center) {
            if (!loaded) Text(alt, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
            AsyncImage(src.text("url"), alt, Modifier.fillMaxSize(), contentScale = if (node.text("fit") == "fit") ContentScale.Fit else ContentScale.Crop,
                onSuccess = { loaded = true }, onError = { loaded = false })
        }
        node.text("caption")?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3) }
    }
    if (zoom) JFullScreen({ zoom = false }) {
        AsyncImage(src.text("url"), alt, Modifier.fillMaxSize().clickable { zoom = false }, contentScale = ContentScale.Fit)
    }
}

@Composable
private fun NativeTiles(node: JsonObject, state: NativeStreamState, local: NativeExtensionLocal, modifier: Modifier,
                        press: NativePress, edit: (String, String) -> Unit) {
    val tiles = node.getValue("tiles").jsonArray.map { it.jsonObject }
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        node.text("title")?.let { JSectionHeader(it) }
        BoxWithConstraints(Modifier.fillMaxWidth()) {
            if (node.text("type") == "carousel") {
                val width = when (node.text("width")) {
                    "narrow" -> NativeStreamLayout.tileMinWidth
                    "wide" -> maxWidth * NativeStreamLayout.tileWideFraction
                    else -> NativeStreamLayout.tileRegularWidth
                }
                Row(Modifier.fillMaxWidth().horizontalScroll(local.scroll(node.text("key").orEmpty(), "carousel")), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    tiles.forEach { tile -> key(tile.text("key")) { NativeTile(tile, state, local, Modifier.width(width), press, edit) } }
                }
            } else {
                val cap = node["max_columns"]?.jsonPrimitive?.intOrNull ?: NativeStreamLayout.gridDefaultColumns
                val count = (maxWidth / NativeStreamLayout.tileMinWidth).toInt().coerceIn(1, cap)
                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    tiles.chunked(count).forEach { row -> Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        row.forEach { tile -> key(tile.text("key")) { NativeTile(tile, state, local, Modifier.weight(1f), press, edit) } }
                        repeat(count - row.size) { Spacer(Modifier.weight(1f)) }
                    } }
                }
            }
        }
    }
}

@Composable
private fun NativeTile(tile: JsonObject, state: NativeStreamState, local: NativeExtensionLocal, modifier: Modifier,
                       press: NativePress, edit: (String, String) -> Unit) {
    val key = tile.getValue("key").jsonPrimitive.content
    val item = ExtensionViewItem(type = "row", key = key, title = tile.text("title"))
    NativeActionContainer(tile, local, press, modifier) {
        JCard(Modifier.fillMaxWidth(), onClick = (tile["action"] as? JsonObject)?.let { action -> { press(key, item, action.action(), emptyMap(), "press") } }) {
            Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                tile.text("icon")?.let { JIcon(ExtensionIcons.of(it), null, tint = if (tile["active"] == JsonPrimitive(true)) PersonaTheme.palette.accent else PersonaTheme.palette.ink3) }
                (tile["image"] as? JsonObject)?.let { src -> AsyncImage(src.text("url"), item.title,
                    Modifier.fillMaxWidth().aspectRatio(NativeStreamLayout.landscapeAspect), contentScale = ContentScale.Crop) }
                Text(item.title.orEmpty(), style = PersonaTheme.type.bodyStrong, color = PersonaTheme.palette.ink)
                tile.text("subtitle")?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3) }
                tile.text("value")?.let { JChangingText(it, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2) }
                (tile["badge"] as? JsonObject)?.let { JTag(it.text("text").orEmpty(), ExtensionViewText.tone(it.text("tone"))) }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    (tile["control"] as? JsonObject)?.let { NativeControl(key, item.title.orEmpty(), it, state, local, press, edit) }
                    NativeMenu(tile, local, press)
                }
            }
        }
    }
}
