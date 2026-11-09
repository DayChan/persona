package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.snapping.rememberSnapFlingBehavior
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import coil3.ImageLoader
import coil3.compose.AsyncImage
import coil3.decode.DataSource
import coil3.decode.ImageSource
import coil3.fetch.FetchResult
import coil3.fetch.Fetcher
import coil3.fetch.SourceFetchResult
import coil3.key.Keyer
import coil3.request.ImageRequest
import coil3.request.Options
import coil3.toBitmap
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext
import okio.Buffer
import okio.FileSystem
import persona.core.contracts.api.ExtensionImageSource
import persona.core.contracts.api.ExtensionRowAction
import persona.core.contracts.api.ExtensionRowControl
import persona.core.contracts.api.ExtensionTile
import persona.core.contracts.api.ExtensionViewBadge
import persona.core.contracts.api.ExtensionViewItem
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons

// The UI version 6 parts of an extension's view (#5464, docs/extensions.md "UI version 6"): pictures, a row's control and
// extra actions, tiles in grids and carousels, badges in named colours. Drawn with the design system only.

/**
 * Loads pictures the extension serves through the hub ([ExtensionHubPicture]) with [fetch] (the device's credentials go
 * to the hub's own signed path, nowhere else); https urls are left to the app's ordinary loader, without credentials.
 */
internal fun extensionPictureLoader(context: android.content.Context, fetch: suspend (String) -> ByteArray): ImageLoader =
    ImageLoader.Builder(context).components {
        add(Keyer<ExtensionHubPicture> { data, _ -> data.path })
        add(object : Fetcher.Factory<ExtensionHubPicture> {
            override fun create(data: ExtensionHubPicture, options: Options, imageLoader: ImageLoader): Fetcher = object : Fetcher {
                override suspend fun fetch(): FetchResult {
                    val bytes = fetch(data.path)
                    return SourceFetchResult(ImageSource(Buffer().write(bytes), FileSystem.SYSTEM), null, DataSource.NETWORK)
                }
            }
        })
    }.build()

/** A badge in its named colour (UI version 6) or its tone. */
@Composable
internal fun ExtensionBadge(badge: ExtensionViewBadge, modifier: Modifier = Modifier) {
    val hue = ExtensionViewText.hue(badge.color)
    if (hue != null) JTag(badge.text, hue, modifier, animated = true) else JTag(badge.text, ExtensionViewText.tone(badge.tone), modifier, animated = true)
}

/**
 * A picture from [src] in a frame of its own: the well shows while it loads, and with [alt] and a picture icon when it
 * never does (a ref the hub did not sign, a url that fails). [fit] shows all of it, else it is cropped to fill.
 */
@Composable
internal fun ExtensionPicture(src: ExtensionImageSource?, alt: String, handle: ExtensionViewHandle, modifier: Modifier = Modifier,
                              fit: Boolean = false, showAlt: Boolean = true, onLoaded: (Boolean) -> Unit = {}) {
    val palette = PersonaTheme.palette
    val model = remember(src) { ExtensionViewText.pictureModel(src?.url, src?.path) }
    var failed by remember(model) { mutableStateOf(model == null) }
    Box(modifier.background(palette.sunken), contentAlignment = Alignment.Center) {
        if (model != null) {
            val scale = if (fit) ContentScale.Fit else ContentScale.Crop
            val loaded = { ok: Boolean -> failed = !ok; onLoaded(ok) }
            if (model is ExtensionHubPicture && handle.pictures != null) AsyncImage(model, alt, handle.pictures, Modifier.fillMaxSize(), contentScale = scale,
                onSuccess = { loaded(true) }, onError = { loaded(false) })
            else if (model is String) AsyncImage(model, alt, Modifier.fillMaxSize(), contentScale = scale, onSuccess = { loaded(true) }, onError = { loaded(false) })
            else failed = true
        }
        if (failed) Column(Modifier.padding(Tokens.Space.s), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            JIcon(PhIcons.Image, null, size = Tokens.Space.xl, tint = palette.ink3, decorative = true)
            if (showAlt) Text(alt, style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
    }
}

/** A picture's encoded bytes for the full-screen viewer: the hub's (with credentials) or an https url's (without). */
private suspend fun pictureBytes(context: android.content.Context, src: ExtensionImageSource, handle: ExtensionViewHandle): ByteArray =
    when (val model = ExtensionViewText.pictureModel(src.url, src.path)) {
        is ExtensionHubPicture -> handle.picture(model.path)
        is String -> withContext(Dispatchers.IO) {
            val result = coil3.SingletonImageLoader.get(context).execute(ImageRequest.Builder(context).data(model).build())
            val bitmap = checkNotNull(result.image) { "no picture" }.toBitmap()
            java.io.ByteArrayOutputStream().use { out -> bitmap.compress(Bitmap.CompressFormat.PNG, 100, out); out.toByteArray() }
        }
        else -> error("no picture")
    }

/**
 * An image component (UI version 6): the picture in its aspect (4:3 when none) with rounded corners, its caption under
 * it; tapping it opens it full screen (pinch to zoom) unless the extension says no_zoom.
 */
@Composable
internal fun ExtensionImage(item: ExtensionViewItem, handle: ExtensionViewHandle, modifier: Modifier = Modifier, compact: Boolean = false) {
    val src = item.src ?: return
    val alt = item.alt.orEmpty()
    val context = LocalContext.current
    var loaded by remember(src) { mutableStateOf(false) }
    var viewing by remember { mutableStateOf(false) }
    val zoom = item.noZoom != true && loaded
    val open = stringResource(R.string.settings_ext_open_image)
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        ExtensionPicture(src, alt, handle,
            Modifier.fillMaxWidth().aspectRatio(ExtensionViewText.aspect(item)).clip(PersonaTheme.shapes.cardShape)
                .then(if (zoom) Modifier.clickable(role = Role.Image, onClickLabel = open) { viewing = true } else Modifier),
            fit = item.fit == "fit", onLoaded = { loaded = it })
        item.caption?.takeIf { it.isNotBlank() }?.let {
            Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = if (compact) 2 else Int.MAX_VALUE, overflow = TextOverflow.Ellipsis)
        }
    }
    if (viewing) ImageViewer(listOf(ViewerImage(src.path ?: src.url ?: alt, alt, src.width, src.height) { pictureBytes(context, src, handle) }), 0, { viewing = false })
}

/**
 * A row's or a tile's extra actions (UI version 6): a menu (the ⋮ button, a long press, a right click) and, for
 * TalkBack, the same as the element's custom actions. [open] / [onOpen] are the menu's state, held by the row so its long
 * press opens it too.
 */
@Composable
internal fun RowActionsMenu(actions: List<ExtensionRowAction>, open: Boolean, onOpen: (Boolean) -> Unit, handle: ExtensionViewHandle, enabled: Boolean, button: Boolean = true) {
    if (actions.isEmpty()) return
    Box {
        if (button) JIconButton(PhIcons.DotsThreeVertical, stringResource(R.string.settings_ext_more_actions), { onOpen(true) }, enabled = enabled,
            raised = false, tint = PersonaTheme.palette.ink3)
        JMenu(open, { onOpen(false) }, menuActions(actions, handle))
    }
}

/** The menu's lines: in the extension's order, destructive ones last and in the danger colour. */
internal fun menuActions(actions: List<ExtensionRowAction>, handle: ExtensionViewHandle): List<JAction> =
    actions.sortedBy { it.role == "destructive" }.map { a ->
        JAction(a.title, icon = a.icon?.takeIf { it.isNotBlank() }?.let(ExtensionIcons::of), destructive = a.role == "destructive",
            onClick = { pressRowAction(a, handle) })
    }

/** A row action goes as a button would (its confirm asked first, a destructive one with the danger button). */
internal fun pressRowAction(a: ExtensionRowAction, handle: ExtensionViewHandle) =
    handle.press(ExtensionViewItem(type = "button", title = a.title, role = a.role), a.action)

/** The same actions as TalkBack's custom actions on the element (a long press it cannot find). */
internal fun Modifier.rowActionSemantics(actions: List<ExtensionRowAction>, handle: ExtensionViewHandle): Modifier =
    if (actions.isEmpty()) this else semantics {
        customActions = actions.map { a -> CustomAccessibilityAction(a.title) { pressRowAction(a, handle); true } }
    }

/** A row's swipes (UI version 6): its first two `swipe` actions, the first from the trailing edge, the second from the leading one. */
internal fun rowSwipes(actions: List<ExtensionRowAction>, handle: ExtensionViewHandle): Pair<SwipeAction?, SwipeAction?> {
    val swipes = actions.filter { it.swipe == true }.take(2).map { a ->
        SwipeAction(a.title, a.icon?.takeIf { it.isNotBlank() }?.let(ExtensionIcons::of) ?: PhIcons.DotsThree, Status.Working,
            { pressRowAction(a, handle) }, destructive = a.role == "destructive")
    }
    return swipes.getOrNull(1) to swipes.getOrNull(0)
}

/**
 * A row's control at its end (UI version 6): a switch, or a stepper (− value +) that sends 0.6 s after the last tap. A
 * slider is drawn under the row ([RowSlider]). A change on its way for more than a second shows a small spinner.
 */
@Composable
internal fun RowControlEnd(control: ExtensionRowControl, state: ExtensionViewState, handle: ExtensionViewHandle) {
    val enabled = control.disabled != true
    Row(verticalAlignment = Alignment.CenterVertically) {
        ControlSpinner(control, state)
        when (control.kind) {
            "switch" -> {
                val on = ExtensionViewText.controlValue(control, state.controls) == "true"
                JSwitch(on, { handle.change(control, (!on).toString()) }, enabled = enabled)
            }
            "stepper" -> RowStepper(control, state, handle, enabled)
        }
    }
}

@Composable
private fun ControlSpinner(control: ExtensionRowControl, state: ExtensionViewState) {
    val sending = control.name in state.sending
    var slow by remember(control.name) { mutableStateOf(false) }
    LaunchedEffect(sending) { slow = false; if (sending) { delay(1000); slow = true } }
    if (sending && slow) { JSpinner(); Spacer(Modifier.width(Tokens.Space.xs)) }
}

@Composable
private fun RowStepper(control: ExtensionRowControl, state: ExtensionViewState, handle: ExtensionViewHandle, enabled: Boolean) {
    // taps add up here; 0.6 s after the last the value goes
    var tapped by remember(control.name) { mutableStateOf<Double?>(null) }
    val current = tapped ?: ExtensionViewText.controlValue(control, state.controls).toDoubleOrNull() ?: control.value ?: 0.0
    LaunchedEffect(tapped) {
        val value = tapped ?: return@LaunchedEffect
        delay(600)
        handle.change(control, ExtensionViewText.number(value, ExtensionViewText.step(control)))
        tapped = null
    }
    val shown = if (tapped != null) ExtensionViewText.controlDisplay(control, mapOf(control.name to ExtensionViewText.number(current, control.step)))
    else ExtensionViewText.controlDisplay(control, state.controls)
    JStepper(shown, { tapped = ExtensionViewText.stepped(control, current, -1) }, { tapped = ExtensionViewText.stepped(control, current, 1) },
        canDown = control.min == null || current > control.min!!, canUp = control.max == null || current < control.max!!, enabled = enabled)
}

/** A row's slider (UI version 6): the row's title and value above, the slider on its own line; it sends when let go. */
@Composable
internal fun RowSlider(control: ExtensionRowControl, label: String, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier = Modifier,
                       header: @Composable (value: String) -> Unit) {
    val min = control.min ?: 0.0
    val max = (control.max ?: 100.0).takeIf { it > min } ?: (min + 1)
    val step = ExtensionViewText.step(control)
    var dragging by remember(control.name) { mutableStateOf<Float?>(null) }
    val value = dragging ?: ExtensionViewText.controlValue(control, state.controls).toFloatOrNull() ?: min.toFloat()
    val steps = ((max - min) / step).takeIf { it in 1.0..10_000.0 }
    val pending = dragging?.let { mapOf(control.name to ExtensionViewText.number(it.toDouble(), step)) } ?: state.controls
    Column(modifier) {
    header(ExtensionViewText.controlDisplay(control, pending))
    JSlider(value, { dragging = it }, Modifier.padding(start = rowPadding, end = rowPadding, bottom = Tokens.Space.s),
        valueRange = min.toFloat()..max.toFloat(), step = if (steps != null && steps <= 200) step.toFloat() else null,
        enabled = control.disabled != true,
        onValueChangeFinished = {
            dragging?.let { v ->
                val text = ExtensionViewText.number(v.toDouble(), step)
                handle.hold(control.name, text)
                handle.change(control, text)
            }
            dragging = null
        }, contentDescription = label)
    }
}

/**
 * A tile (UI version 6) as the design system's: its icon or picture, colour, badge, a switch at the top end, its spark;
 * tapping does its action, a long press (or right click) opens its extra actions.
 */
@Composable
internal fun ExtensionTileView(tile: ExtensionTile, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier = Modifier) {
    val actions = ExtensionViewText.rowActions(tile.actions)
    var menu by remember(tile.key, tile.title) { mutableStateOf(false) }
    val action = tile.action?.takeIf { it.id.isNotEmpty() || it.push != null }
    val control = tile.control?.takeIf { it.kind == "switch" && it.name.isNotEmpty() }
    val on = stringResource(R.string.settings_ext_tile_on)
    val press: (() -> Unit)? = when {
        action != null -> ({ handle.press(ExtensionViewItem(type = "button", title = tile.title), action) })
        // a tile with only its switch flips it when tapped
        control != null && control.disabled != true -> ({
            handle.change(control, (ExtensionViewText.controlValue(control, state.controls) != "true").toString())
        })
        else -> null
    }
    val picture = tile.image?.takeIf { ExtensionViewText.pictureModel(it.url, it.path) != null || !it.ref.isNullOrEmpty() }
    Box(modifier) {
        JTile(tile.title, Modifier.fillMaxSize().rowActionSemantics(actions, handle), subtitle = tile.subtitle?.takeIf { it.isNotBlank() },
            value = tile.value?.takeIf { it.isNotBlank() },
            icon = if (picture == null) tile.icon?.takeIf { it.isNotBlank() }?.let(ExtensionIcons::of) else null,
            picture = picture?.let { p -> { ExtensionPicture(p, tile.title, handle, Modifier.fillMaxSize(), showAlt = false) } },
            hue = ExtensionViewText.hue(tile.color), active = tile.active == true || (control != null && ExtensionViewText.controlValue(control, state.controls) == "true"),
            badge = tile.badge?.takeIf { it.text.isNotBlank() }?.let { b -> { ExtensionBadge(b) } },
            trailing = control?.let { c -> { RowControlEnd(c, state, handle) } },
            spark = ExtensionViewText.spark(tile.spark),
            enabled = action == null || !state.busy,
            state = if (tile.active == true) on else null,
            onClick = press, onLongClick = if (actions.isNotEmpty()) ({ menu = true }) else null)
        RowActionsMenu(actions, menu, { menu = it }, handle, enabled = true, button = false)
    }
}

/**
 * A grid of tiles (UI version 6): as many columns of at least [TileSize.min] as fit (two on a phone), at most
 * `max_columns` (4 when none); the tiles of a row are as tall as its tallest. [limit] cuts a card's.
 */
@Composable
internal fun ExtensionGrid(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier = Modifier, limit: Int = 60) {
    val tiles = item.tiles.orEmpty().filter { it.title.isNotBlank() }.take(limit)
    if (tiles.isEmpty()) return
    BoxWithConstraints(modifier.fillMaxWidth()) {
        val columns = tileColumns(maxWidth.value, item.maxColumns ?: 4, tiles.size)
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            tiles.chunked(columns).forEach { row ->
                Row(Modifier.fillMaxWidth().height(IntrinsicSize.Min), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                    row.forEach { ExtensionTileView(it, state, handle, Modifier.weight(1f).fillMaxHeight()) }
                    repeat(columns - row.size) { Spacer(Modifier.weight(1f)) }
                }
            }
        }
    }
}

/**
 * Tiles in a line that scrolls sideways (UI version 6), snapping to a tile; `narrow` 140, `regular` 200 (the default)
 * or `wide` (80% of the width, so the next one peeks in). Its title above.
 */
@Composable
internal fun ExtensionCarousel(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier = Modifier, limit: Int = 20) {
    val tiles = item.tiles.orEmpty().filter { it.title.isNotBlank() }.take(limit)
    if (tiles.isEmpty()) return
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        item.title?.takeIf { it.isNotBlank() }?.let { Text(it, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis) }
        BoxWithConstraints(Modifier.fillMaxWidth()) {
            val width = when (item.width) { "narrow" -> TileSize.narrow; "wide" -> maxWidth * TileSize.wideShare; else -> TileSize.regular }
            val list = rememberLazyListState()
            LazyRow(Modifier.fillMaxWidth(), state = list, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
                flingBehavior = rememberSnapFlingBehavior(list)) {
                itemsIndexed(tiles, key = { i, t -> t.key?.takeIf { it.isNotBlank() } ?: "t$i" }) { _, tile ->
                    ExtensionTileView(tile, state, handle, Modifier.width(width))
                }
            }
        }
    }
}

/** Whether a row's control goes at its end (a switch, a stepper) or under it (a slider). */
internal fun ExtensionRowControl.under(): Boolean = kind == "slider"

/** A row's control the app draws: one of the three kinds, with a name and an action. */
internal fun ExtensionViewItem.drawnControl(): ExtensionRowControl? =
    control?.takeIf { it.kind in setOf("switch", "slider", "stepper") && it.name.isNotEmpty() && it.action.id.isNotEmpty() }

/**
 * A date, time or datetime field's picker (UI version 6): the style's date picker held to `earliest` / `latest`, its
 * time dial, or the one then the other. The value goes back in the field's format (`YYYY-MM-DD`, `HH:MM`, both with `T`).
 */
@Composable
internal fun ExtensionWhenPicker(item: ExtensionViewItem, value: String, onPick: (String) -> Unit, onDismiss: () -> Unit) {
    val date = ExtensionViewText.date(value)
    val time = ExtensionViewText.time(value)
    // a datetime picks its day first, then its time
    var day by remember(item) { mutableStateOf<java.time.LocalDate?>(null) }
    val title = item.label?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_ext_pick_time)
    when {
        item.kind == "time" -> JTimePickerDialog(time, { onPick(it.toString()) }, onDismiss, title)
        item.kind == "datetime" && day != null -> JTimePickerDialog(time, { t -> onPick("${day}T$t") }, onDismiss, title)
        else -> JDatePickerDialog(date, { d -> if (item.kind == "datetime") day = d else onPick(d.toString()) },
            { if (item.kind != "datetime" || day == null) onDismiss() },
            earliest = ExtensionViewText.date(item.earliest), latest = ExtensionViewText.date(item.latest))
    }
}

/**
 * A multichoice field's sheet (UI version 6): each choice a line with a check box, staying open while they are ticked;
 * more than six get a search box. [onChange] gets the chosen values in the choices' order.
 */
@Composable
internal fun ExtensionMultiChoiceSheet(item: ExtensionViewItem, chosen: List<String>, onChange: (List<String>) -> Unit, onDismiss: () -> Unit) {
    val choices = item.choices.orEmpty().take(50)
    var query by remember { mutableStateOf("") }
    var picked by remember(item) { mutableStateOf(chosen.toSet()) }
    JSheet(onDismiss, title = item.label?.takeIf { it.isNotBlank() }, scrollable = true) {
        if (choices.size > 6) JSearchField(query, { query = it }, stringResource(R.string.settings_ext_search), Modifier.padding(bottom = Tokens.Space.s))
        choices.filter { query.isBlank() || it.label.contains(query.trim(), ignoreCase = true) }.forEach { choice ->
            val on = choice.value in picked
            JRow(choice.label, chevron = false, role = Role.Checkbox, onClick = {
                picked = if (on) picked - choice.value else picked + choice.value
                onChange(choices.map { it.value }.filter { it in picked })
            }, trailing = { JCheckbox(on, null) })
        }
    }
}

/** A row's leading picture, if it has one: its thumbnail or avatar (a picture, or its letters), an https one or the hub's (UI version 6). */
internal fun rowLeading(item: ExtensionViewItem, handle: ExtensionViewHandle): (@Composable () -> Unit)? = when {
    item.thumbnail != null -> ({ RowThumbnail(item, handle) })
    item.avatar != null -> ({ RowAvatar(item, handle) })
    else -> null
}
