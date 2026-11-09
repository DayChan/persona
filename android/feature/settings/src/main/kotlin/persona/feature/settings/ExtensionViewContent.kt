package persona.feature.settings

import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyItemScope
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.animation.core.tween
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.api.ExtensionViewPush
import persona.core.contracts.api.ExtensionViewSection
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.launch

// Draws an extension's View (docs/extensions/extensions.md "Declarative UI" "UI slots") with this app's design system: each kind of item as
// a component of the style in use (charts and number grids from the design system's), tones as role colours. The
// extension gives no colour, size or icon. A kind this app does not know is skipped.

/**
 * What the page does for a view: press a button (after its confirm), choose for a choice field, open a link, switch a
 * segmented control (its action goes at once), and since UI version 3 search, filter and load more.
 */
internal class ExtensionViewHandle(
    val press: (ExtensionViewItem, ExtensionViewAction) -> Unit,
    val edit: (String, String) -> Unit,
    val choose: (ExtensionViewItem) -> Unit,
    val open: (String) -> Unit,
    val segment: (ExtensionViewItem, String) -> Unit = { _, _ -> },
    val search: (String) -> Unit = {},
    val filter: (String) -> Unit = {},
    val more: (Int, ExtensionViewSection) -> Unit = { _, _ -> },
    /** UI version 4: a code hosting component's action, with its own values (a path, a line, the text typed). */
    val act: (ExtensionViewAction, Map<String, String>) -> Unit = { _, _ -> },
    /** UI version 4: a tab was chosen. */
    val tab: (String) -> Unit = {},
    /** UI version 4: a file tree without an action shows that file's diff on the page. */
    val reveal: (String) -> Unit = {},
    /** UI version 5: a page of diffs' viewed marks, whitespace, lines and quiet actions; none where nothing acts. */
    val review: CodeReviewState? = null,
    /** Stream-only callback locality; legacy rendering has no node-scoped adapter. */
    val forNode: ((String) -> ExtensionViewHandle)? = null,
    /** Opt-in fixed native stream parts only; absent keeps every legacy pixel/callback unchanged. */
    val nativeItem: (@Composable (ExtensionViewItem, ExtensionViewState, Modifier) -> Boolean)? = null,
    val nativeLocal: NativeExtensionLocal? = null,
    val nodeKey: String = "",
    /** UI version 6: a row's or a tile's control was changed (after its confirm); shown at once, sent without holding the page. */
    val change: (persona.core.contracts.api.ExtensionRowControl, String) -> Unit = { _, _ -> },
    /** UI version 6: shows a control's new value at once (a slider let go), before [change]. */
    val hold: (String, String) -> Unit = { _, _ -> },
    /** UI version 6: loads the pictures the extension serves through the hub; none where there is no hub (an assistant's card). */
    val pictures: coil3.ImageLoader? = null,
    /** UI version 6: a picture's bytes from the hub, for the full-screen viewer. */
    val picture: suspend (String) -> ByteArray = { throw UnsupportedOperationException("picture") },
    /** UI version 6: a date, time or datetime field was tapped: its picker opens. */
    val pick: (ExtensionViewItem) -> Unit = {},
)

/**
 * The view's dialogs (a button's confirm, a choice field's choices) and the way its buttons go: an action's toast
 * shows as the app's notice, its link opens in the app, a new view replaces the page. [appLink] takes the app's own
 * links a page may open (an inbox item's source) before https ones go to the app's link handling. A push (UI version
 * 3) goes to [onPush] (a page of the extension drawn on top), a pop to [onPop] (back).
 */
@Composable
internal fun rememberExtensionView(model: ExtensionViewModel, appLink: (String) -> Boolean = { false },
                                   onPush: (ExtensionViewPush) -> Unit = {}, onPop: () -> Unit = {}): ExtensionViewHandle {
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val uri = LocalUriHandler.current
    val res = LocalResources.current
    val state by model.state.collectAsState()
    var confirming by remember(model) { mutableStateOf<Pair<ExtensionViewItem, ExtensionViewAction>?>(null) }
    var confirmingValues by remember(model) { mutableStateOf<Map<String, String>?>(null) }
    var choosing by remember(model) { mutableStateOf<ExtensionViewItem?>(null) }
    var picking by remember(model) { mutableStateOf<ExtensionViewItem?>(null) }
    var confirmingControl by remember(model) { mutableStateOf<Pair<persona.core.contracts.api.ExtensionRowControl, String>?>(null) }
    val context = LocalContext.current
    val routes by rememberUpdatedState(appLink)
    val push by rememberUpdatedState(onPush)
    val pop by rememberUpdatedState(onPop)
    val open: (String) -> Unit = { url -> if (!routes(url) && ExtensionViewText.isHttps(url)) runCatching { uri.openUri(url) } }
    fun show(outcome: ExtensionActionOutcome) {
        outcome.toast?.let { feedback?.notice(it) }
        outcome.error?.let { feedback?.notice(it.text(res)) }
        outcome.openUrl?.let(open)
        outcome.push?.let(push)
        if (outcome.pop) pop()
    }
    fun run(action: ExtensionViewAction, values: Map<String, String>? = null) = scope.launch { show(model.press(action, values)) }
    val prefs = LocalContext.current.getSharedPreferences(CodeReviewState.Prefs, android.content.Context.MODE_PRIVATE)
    val review = remember(model) {
        CodeReviewState(prefs) { action, values, done ->
            scope.launch {
                val result = model.fetch(action, values)
                if (result == null) feedback?.notice(res.getString(R.string.settings_ext_review_quiet_failed))
                else result.toast?.takeIf { it.isNotBlank() }?.let { feedback?.notice(it) }
                done(result)
            }
        }
    }
    val handle = remember(model) {
        ExtensionViewHandle(
            press = { item, action -> if (!action.confirm.isNullOrBlank()) confirming = item to action else run(action) },
            edit = model::edit,
            choose = { choosing = it },
            open = open,
            segment = { item, value -> scope.launch { show(model.choose(item, value)) } },
            search = { query -> scope.launch { show(model.search(query)) } },
            filter = { value -> scope.launch { show(model.filter(value)) } },
            more = { index, section -> scope.launch { show(model.more(index, section)) } },
            act = { action, values ->
                if (!action.confirm.isNullOrBlank()) { confirmingValues = values; confirming = ExtensionViewItem(type = "button") to action } else run(action, values)
            },
            tab = { value -> scope.launch { show(model.tab(value)) } },
            review = review,
            change = { control, value ->
                if (!control.action.confirm.isNullOrBlank()) confirmingControl = control to value
                else { model.hold(control.name, value); scope.launch { show(model.change(control, value)) } }
            },
            hold = model::hold,
            pictures = extensionPictureLoader(context.applicationContext, model::picture),
            picture = model::picture,
            pick = { picking = it },
        )
    }
    confirmingControl?.let { (control, value) ->
        // cancelled, the control stays as the extension drew it (nothing was held)
        JDialog({ confirmingControl = null }, title = res.getString(R.string.settings_ext_confirm_title), text = control.action.confirm,
            confirm = res.getString(R.string.settings_ext_confirm_ok) to {
                confirmingControl = null
                model.hold(control.name, value)
                scope.launch { show(model.change(control, value)) }
            })
    }
    picking?.let { item -> ExtensionWhenPicker(item, state.values[item.name.orEmpty()].orEmpty(), { model.edit(item.name.orEmpty(), it) }, { picking = null }) }
    confirming?.let { (item, action) ->
        JDialog({ confirming = null }, title = ExtensionViewText.confirmTitle(res, item), text = action.confirm,
            confirm = ExtensionViewText.confirmLabel(res, item) to { confirming = null; run(action, confirmingValues); confirmingValues = null },
            destructive = item.role == "destructive")
    }
    choosing?.takeIf { it.kind == "multichoice" }?.let { item ->
        val name = item.name.orEmpty()
        ExtensionMultiChoiceSheet(item, ExtensionViewText.multi(state.values[name]), { model.edit(name, ExtensionViewText.multiValue(it)) }, { choosing = null })
    }
    choosing?.takeIf { it.kind != "multichoice" }?.let { item ->
        val name = item.name.orEmpty()
        val current = state.values[name]
        JActionSheet(item.choices.orEmpty().map { choice ->
            JAction(choice.label, checked = current == choice.value, onClick = { model.edit(name, choice.value) })
        }, { choosing = null }, title = item.label ?: name)
    }
    return handle
}

/**
 * The view's sections and items, into a page's list: [key] keeps two views on one page apart. Rows, links, switches
 * and choices that follow each other form one group; the rest stand on their own.
 */
internal fun LazyListScope.extensionViewItems(key: String, state: ExtensionViewState, handle: ExtensionViewHandle, showTitles: Boolean = true,
                                             streamSectionKeys: List<String>? = null) {
    val view = state.view ?: return
    view.tabs?.takeIf { it.items.size >= 2 && it.name.isNotEmpty() }?.let { tabs ->
        item("$key-tabs") { ExtensionTabs(tabs, state.values[tabs.name] ?: tabs.value, state, handle, Modifier.padding(bottom = Tokens.Space.s)) }
    }
    // a page of diffs (UI version 5): viewed so far, open threads, whitespace, the pending review
    if (reviewing(view) && handle.review != null) item("$key-review-tools") { ReviewToolbar(view, state, handle, Modifier.padding(bottom = Tokens.Space.s)) }
    if (view.search != null || view.filter != null) item("$key-search") {
        ExtensionSearchBar(view, state, handle, Modifier.padding(bottom = Tokens.Space.s))
    }
    // a split view (UI version 4): a phone has no room beside the code, so the side pane (the file tree) folds into a
    // bar on top that opens in place, and closes once a file is picked
    val split = view.layout == "split"
    val side = if (split) view.sections.filter { it.pane == "side" } else emptyList()
    if (side.isNotEmpty()) item("$key-side") { SidePane(side, state, handle) }
    view.sections.forEachIndexed { s, section ->
        if (section in side) return@forEachIndexed
        val sectionKey = streamSectionKeys?.get(s)?.let { "$key-section-$it" } ?: "$key-s$s"
        val title = section.title?.takeIf { it.isNotBlank() }
        if (title != null && showTitles) item("$sectionKey-title") { JSectionHeader(title) }
        else if (s > 0) item("$sectionKey-gap") { Spacer(Modifier.padding(top = Tokens.Space.l)) }
        val items = if (streamSectionKeys != null) section.items else section.items.filter(ExtensionViewText::drawable)
        // The extension's keys keep a list's place when it is drawn again (a page's "Load more", a search).
        val keys = if (streamSectionKeys != null) items.map { it.key!! } else ExtensionViewText.itemKeys(items)
        var i = 0
        while (i < items.size) {
            val start = i
            if (ExtensionViewText.grouped(items[i])) while (i < items.size && ExtensionViewText.grouped(items[i])) i++ else i++
            val run = items.subList(start, i)
            run.forEachIndexed { index, item ->
                val itemKey = keys[start + index]?.let { if (streamSectionKeys != null) "$key-node-$it" else "$key-s$s-k-$it" }
                    ?: "$key-s$s-i${start + index}"
                item(itemKey) {
                    val itemHandle = handle.forNode?.invoke(item.key.orEmpty()) ?: handle
                    // drawn again, a block whose key is new fades in and one that went fades out (#2811)
                    val moving = Modifier.itemMotion(this, PersonaTheme.reduceMotion, PersonaTheme.motion.standard)
                    if (ExtensionViewText.grouped(item)) {
                        JGroupItem(GroupPosition.of(index, run.size), moving.then(if (index == 0 && start > 0) Modifier.padding(top = Tokens.Space.s) else Modifier)) {
                            GroupedItem(item, state, itemHandle)
                        }
                    } else Box(moving) { StandaloneItem(item, state, itemHandle, first = start == 0) }
                }
            }
        }
        if (section.more?.action?.id?.isNotEmpty() == true) item("$sectionKey-more") {
            val moreHandle = handle.forNode?.invoke(streamSectionKeys?.get(s).orEmpty()) ?: handle
            JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s)) { ExtensionMore(s, section, state, moreHandle) }
        }
        section.footer?.takeIf { it.isNotBlank() }?.let { footer ->
            item("$sectionKey-footer") { SettingsCaption(footer, Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s)) }
        }
    }
    if (reviewing(view) && view.review != null && handle.review != null) item("$key-review-bar") { ReviewBar(view, state, handle, Modifier.padding(top = Tokens.Space.l)) }
}

/**
 * How a block of a page's list comes and goes when the view is drawn again: fades in where its key is new, out where
 * its key went, the rest slide to their new places; nothing with reduced motion (the list's first draw never moves).
 */
private fun Modifier.itemMotion(scope: LazyItemScope, reduce: Boolean, duration: Int): Modifier =
    if (reduce) this else with(scope) {
        animateItem(fadeInSpec = tween(duration), placementSpec = tween(duration, easing = FastOutSlowInEasing), fadeOutSpec = tween(duration))
    }

/** A row, a link, a switch or a choice: one line of a group. */
@Composable
internal fun GroupedItem(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle) {
    if (handle.nativeItem?.invoke(item, state, Modifier) == true) return
    val enabled = !state.busy
    when (item.type) {
        "row" -> ExtensionRow(item, state, handle)
        "link" -> JRow(item.title.orEmpty(), leadingIcon = PhIcons.ArrowSquareOut, chevron = false, onClick = { handle.open(item.url.orEmpty()) })
        "field" -> {
            val name = item.name.orEmpty()
            val value = state.values[name].orEmpty()
            val detail = item.detail?.takeIf { it.isNotBlank() }
            if (item.kind == "toggle") {
                JSwitchRow(item.label.orEmpty(), value == "true", { on -> handle.edit(name, if (on) "true" else "false") }, subtitle = detail, enabled = enabled)
            } else if (item.kind in setOf("date", "time", "datetime")) {
                JRow(item.label.orEmpty(), subtitle = detail, value = ExtensionViewText.whenText(item.kind, value).ifEmpty { item.placeholder ?: stringResource(R.string.settings_ext_not_chosen) },
                    leadingIcon = if (item.kind == "time") PhIcons.Clock else PhIcons.CalendarBlank, enabled = enabled, onClick = { handle.pick(item) })
            } else if (item.kind == "multichoice") {
                val chosen = ExtensionViewText.multi(value)
                val label = when (chosen.size) {
                    0 -> item.placeholder ?: stringResource(R.string.settings_ext_not_chosen)
                    1 -> item.choices.orEmpty().firstOrNull { it.value == chosen[0] }?.label ?: chosen[0]
                    else -> stringResource(R.string.settings_ext_chosen_count, chosen.size)
                }
                JRow(item.label.orEmpty(), subtitle = detail, value = label, enabled = enabled, onClick = { handle.choose(item) })
            } else {
                val label = item.choices.orEmpty().firstOrNull { it.value == value }?.label ?: value.ifEmpty { item.placeholder ?: stringResource(R.string.settings_ext_not_chosen) }

                JRow(item.label.orEmpty(), subtitle = detail, value = label, enabled = enabled, onClick = { handle.choose(item) })
            }
        }
    }
}

/**
 * A row: its picture or icon (in its named colour, UI version 6), title, subtitle and value, and at its end a spark, a
 * badge, its control (a switch, a stepper; a slider goes under it) and its extra actions' ⋮ (also a long press, and
 * the first `swipe` ones under swipes).
 */
@Composable
private fun ExtensionRow(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle) {
    val enabled = !state.busy
    val palette = PersonaTheme.palette
    val action = item.action?.takeIf { it.id.isNotEmpty() || it.push != null }
    val badge = item.badge?.takeIf { it.text.isNotBlank() }
    val control = item.drawnControl()
    val actions = ExtensionViewText.rowActions(item.actions)
    var menu by remember(item.key, item.title) { mutableStateOf(false) }
    val spark = ExtensionViewText.spark(item.spark)
    val iconHue = ExtensionViewText.hue(item.iconColor)
    val icon = item.icon?.takeIf { it.isNotBlank() }?.let(ExtensionIcons::of)
    val leading = rowLeading(item, handle)
    val busy = action != null && state.acting == action.id
    val end = control?.takeIf { !it.under() }
    val flip = end?.takeIf { it.kind == "switch" && it.disabled != true }
    val trailing: (@Composable () -> Unit)? = if (busy || badge != null || end != null || spark != null || actions.isNotEmpty()) ({
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            spark?.let { JSparkline(it) }
            if (busy) JSpinner() else badge?.let { ExtensionBadge(it) }
            end?.let { RowControlEnd(it, state, handle) }
            RowActionsMenu(actions, menu, { menu = it }, handle, enabled = true)
        }
    }) else null
    val (start, swipeEnd) = rowSwipes(actions, handle)
    val row: @Composable (String?) -> Unit = { value ->
        JRow(item.title.orEmpty(), Modifier.rowActionSemantics(actions, handle), subtitle = item.subtitle?.takeIf { it.isNotBlank() }, value = value,
            leadingIcon = if (leading == null) icon else null, leading = leading, trailing = trailing,
            leadingIconTint = iconHue?.let { palette.hue(it).dot },
            chevron = action != null, enabled = enabled || action == null, changingValue = true,
            // a row with a switch and nothing to open flips its switch, as a settings switch row does
            onClick = action?.let { a -> { handle.press(item, a) } } ?: flip?.let { c -> {
                handle.change(c, (ExtensionViewText.controlValue(c, state.controls) != "true").toString())
            } },
            onLongClick = if (actions.isNotEmpty()) ({ menu = true }) else null,
            role = if (action == null && flip != null) androidx.compose.ui.semantics.Role.Switch else androidx.compose.ui.semantics.Role.Button)
    }
    JSwipeActions(start = start, end = swipeEnd) {
        val slider = control?.takeIf { it.under() }
        if (slider != null) RowSlider(slider, item.title.orEmpty(), state, handle) { value -> row(value) }
        else row(item.value?.takeIf { it.isNotBlank() })
    }
}

/** Text, a number, a progress bar, a button, a text field, a divider: each on its own. */
@Composable
internal fun StandaloneItem(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle, first: Boolean) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val top = if (first) Modifier else Modifier.padding(top = Tokens.Space.s)
    if (handle.nativeItem?.invoke(item, state, top) == true) return
    when (item.type) {
        "text" -> {
            val text = item.text.orEmpty()
            when (item.style) {
                "code" -> JCodeBlock(text, top, small = true)
                "caption" -> Text(text, top.padding(horizontal = rowPadding), style = type.caption, color = palette.ink3)
                "secondary" -> Text(text, top.padding(horizontal = rowPadding), style = type.callout, color = palette.ink2)
                // UI version 6: a section's big title and a small heading
                "title" -> Text(text, top.padding(horizontal = rowPadding), style = type.title, color = palette.ink)
                "headline" -> Text(text, top.padding(horizontal = rowPadding), style = type.headline, color = palette.ink)
                else -> Text(text, top.padding(horizontal = rowPadding), style = type.body, color = palette.ink)
            }
        }
        "metric" -> JGroupItem(GroupPosition.Single, top) {
            Column(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    item.icon?.takeIf { it.isNotBlank() }?.let {
                        JIcon(ExtensionIcons.of(it), null, size = Tokens.Space.l, tint = palette.hueOr(ExtensionViewText.hue(item.iconColor)), decorative = true)
                        Spacer(Modifier.width(Tokens.Space.xs))
                    }
                    Text(item.label.orEmpty(), style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                val tone = ExtensionViewText.tone(item.tone)
                JChangingText(item.value.orEmpty(), style = type.number, color = if (tone == Tone.Neutral) palette.ink else palette.tone(tone).text)
                ExtensionViewText.spark(item.spark)?.let { JSparkline(it, Modifier.fillMaxWidth().height(SparkSize.height)) }
                item.detail?.takeIf { it.isNotBlank() }?.let { Text(it, style = type.caption, color = palette.ink3) }
            }
        }
        "progress" -> JGroupItem(GroupPosition.Single, top) {
            Column(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(item.label.orEmpty(), Modifier.weight(1f), style = type.body, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Spacer(Modifier.width(Tokens.Space.s))
                    Text(ExtensionViewText.percent(item), style = type.micro, color = palette.ink2, maxLines = 1)
                }
                JLinearProgress(ExtensionViewText.ratio(item), tone = ExtensionViewText.tone(item.tone))
                item.detail?.takeIf { it.isNotBlank() }?.let { Text(it, style = type.caption, color = palette.ink3) }
            }
        }
        "button" -> {
            val action = item.action ?: return
            JButton(item.title.orEmpty(), { handle.press(item, action) }, top.fillMaxWidth(),
                kind = when {
                    item.role == "destructive" -> JButtonKind.Destructive
                    action.submit -> JButtonKind.Primary
                    else -> JButtonKind.Secondary
                },
                loading = state.acting == action.id, enabled = !state.busy)
        }
        "field" -> FieldBox(item, state, handle, top)
        "divider" -> JDivider(top.padding(vertical = Tokens.Space.s))
        "chart" -> JGroupItem(GroupPosition.Single, top) { ChartItem(item, Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.m), compact = false) }
        "stat_grid" -> JGroupItem(GroupPosition.Single, top) {
            JStatGrid(ExtensionViewText.stats(item), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.m))
        }
        "segmented" -> SegmentedItem(item, state, handle, top)
        "markdown" -> ExtensionMarkdown(item.text.orEmpty(), top.padding(horizontal = rowPadding))
        "table" -> JGroupItem(GroupPosition.Single, top) { ExtensionTable(item, Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.m), local = handle.nativeLocal) }
        "empty" -> JEmptyState(item.title.orEmpty(), item.detail.orEmpty(), top, icon = item.icon?.takeIf { it.isNotBlank() }?.let(ExtensionIcons::of),
            iconHue = ExtensionViewText.hue(item.iconColor),
            action = item.button?.takeIf { it.title.isNotBlank() && it.action.id.isNotEmpty() }?.let { b ->
                { JButton(b.title, { handle.press(item, b.action) }, kind = JButtonKind.Secondary, loading = state.acting == b.action.id,
                    enabled = !state.busy) }
            })
        "loading" -> JLoading(top, item.title?.takeIf { it.isNotBlank() })
        "html" -> LocalViewHtml.current?.invoke(item, top)
        "header" -> CodeHeader(item, top)
        "breadcrumb" -> CodeBreadcrumb(item, state, handle, top)
        "file_tree" -> CodeFileTree(item, state, handle, top)
        "code" -> CodeFile(item, state, handle, top)
        "diff" -> CodeDiff(item, state, handle, top)
        "comment_thread" -> CodeCommentThread(item, state, handle, top)
        "composer" -> CodeComposer(item, state, handle, top)
        // UI version 6
        "image" -> ExtensionImage(item, handle, top)
        "grid" -> ExtensionGrid(item, state, handle, top)
        "carousel" -> ExtensionCarousel(item, state, handle, top)
    }
}

/**
 * How an `html` item is drawn: only an assistant's card provides it (AssistantViewCard); elsewhere the item is not
 * drawn at all (the hub never lets an extension's view have one).
 */
internal val LocalViewHtml = staticCompositionLocalOf<(@Composable (ExtensionViewItem, Modifier) -> Unit)?> { null }

/**
 * A view drawn in place, not in a lazy list (an assistant's ```persona-view in a reply): the same items in the same
 * groups as a page's, one section under another.
 */
@Composable
internal fun ExtensionViewColumn(state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier = Modifier) {
    val view = state.view ?: return
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        view.title?.takeIf { it.isNotBlank() }?.let { Text(it, style = type.headline, color = palette.ink) }
        view.sections.forEach { section ->
            section.title?.takeIf { it.isNotBlank() }?.let { Text(it, style = type.caption, color = palette.ink2) }
            val items = section.items.filter(ExtensionViewText::drawable)
            var i = 0
            while (i < items.size) {
                val start = i
                if (ExtensionViewText.grouped(items[i])) while (i < items.size && ExtensionViewText.grouped(items[i])) i++ else i++
                val run = items.subList(start, i)
                if (ExtensionViewText.grouped(run.first())) JGroup(rows = run.map { item -> { GroupedItem(item, state, handle) } })
                else run.forEach { StandaloneItem(it, state, handle, first = true) }
            }
            section.footer?.takeIf { it.isNotBlank() }?.let { Text(it, style = type.caption, color = palette.ink3) }
        }
    }
}

/** A chart from the design system; a card's is short, with no scale. */
@Composable
private fun ChartItem(item: ExtensionViewItem, modifier: Modifier, compact: Boolean) {
    val data = remember(item) { ExtensionViewText.chart(item) } ?: return
    JChart(data, modifier, compact = compact, title = item.title?.takeIf { it.isNotBlank() }, detail = item.detail?.takeIf { it.isNotBlank() })
}

/**
 * One of a few, switched at once (a time range): the style's segmented control in the width there is (the board's
 * cells share it out, so it must not sit in a sideways scroll: its cells collapsed there).
 */
@Composable
private fun SegmentedItem(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier) {
    val choices = item.choices.orEmpty()
    val current = state.values[item.name.orEmpty()] ?: item.value
    Box(modifier.fillMaxWidth()) {
        JSegmented(choices.map { JSegment(it.label) }, choices.indexOfFirst { it.value == current },
            { index -> if (!state.busy) choices.getOrNull(index)?.let { handle.segment(item, it.value) } })
    }
}

/**
 * An extension's compact view (a card, `size: compact`), not lazily: its sections one under another, no fields. Rows and
 * links are plain rows, buttons small.
 */
@Composable
internal fun ExtensionCardContent(state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier = Modifier) {
    val view = state.view ?: return
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        view.sections.forEach { section ->
            val items = section.items.filter(ExtensionViewText::drawableOnCard)
            if (items.isEmpty() && section.footer.isNullOrBlank()) return@forEach
            section.title?.takeIf { it.isNotBlank() }?.let { Text(it, style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis) }
            items.forEach { item -> CardItem(item, state, handle) }
            section.footer?.takeIf { it.isNotBlank() }?.let { Text(it, style = type.caption, color = palette.ink3) }
        }
    }
}

@Composable
private fun CardItem(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    when (item.type) {
        "text" -> Text(item.text.orEmpty(), style = when (item.style) {
            "caption" -> type.caption; "secondary" -> type.callout; "code" -> type.code; "title" -> type.headline; "headline" -> type.bodyStrong; else -> type.body },
            color = when (item.style) { "caption" -> palette.ink3; "secondary" -> palette.ink2; else -> palette.ink })
        "metric" -> JStatGrid(listOf(JStat(item.label.orEmpty(), item.value.orEmpty(), item.detail, ExtensionViewText.tone(item.tone), ExtensionViewText.spark(item.spark))))
        "progress" -> Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(item.label.orEmpty(), Modifier.weight(1f), style = type.callout, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(ExtensionViewText.percent(item), style = type.micro, color = palette.ink2, maxLines = 1)
            }
            JLinearProgress(ExtensionViewText.ratio(item), tone = ExtensionViewText.tone(item.tone))
        }
        "row", "link" -> CardLine(item, state, handle)
        "button" -> item.action?.let { action ->
            JButton(item.title.orEmpty(), { handle.press(item, action) }, kind = if (item.role == "destructive") JButtonKind.Destructive else JButtonKind.Secondary,
                size = JButtonSize.Small, loading = state.acting == action.id, enabled = !state.busy)
        }
        "divider" -> JDivider()
        "chart" -> ChartItem(item, Modifier, compact = true)
        "stat_grid" -> JStatGrid(ExtensionViewText.stats(item))
        "segmented" -> SegmentedItem(item, state, handle, Modifier)
        "markdown" -> ExtensionMarkdown(item.text.orEmpty())
        "empty" -> Text(item.title.orEmpty(), style = type.callout, color = palette.ink2)
        "loading" -> Row(verticalAlignment = Alignment.CenterVertically) {
            JSpinner()
            item.title?.takeIf { it.isNotBlank() }?.let { Spacer(Modifier.width(Tokens.Space.s)); Text(it, style = type.caption, color = palette.ink3) }
        }
        // UI version 6: a card's grid has at most four tiles, its carousel six (the hub holds them to it)
        "image" -> ExtensionImage(item, handle, compact = true)
        "grid" -> ExtensionGrid(item, state, handle, limit = 4)
        "carousel" -> ExtensionCarousel(item, state, handle, limit = 6)
    }
}

/** A text, secret or number field; a secret is never filled in by the extension and never shown. */
@Composable
private fun FieldBox(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier) {
    val name = item.name.orEmpty()
    val value = state.values[name].orEmpty()
    val secret = item.kind == "secret"
    val number = item.kind == "number"
    // UI version 6: several lines (3 to 8, the text keeps its line breaks), and a line of help under any field
    val multiline = item.kind == "multiline"
    JTextField(value, { text -> if (!number || ExtensionViewText.isNumber(text)) handle.edit(name, text) }, modifier.fillMaxWidth(),
        label = item.label, placeholder = item.placeholder?.takeIf { it.isNotBlank() }, singleLine = !multiline,
        minLines = if (multiline) 3 else 1, maxLines = if (multiline) 8 else 1, supportingText = item.detail?.takeIf { it.isNotBlank() }, enabled = !state.busy,
        visualTransformation = if (secret) PasswordVisualTransformation() else androidx.compose.ui.text.input.VisualTransformation.None,
        keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, keyboardType = when {
            secret -> KeyboardType.Password
            number -> KeyboardType.Decimal
            else -> KeyboardType.Text
        }))
}

/** A row or a link on a card: on the card's own text column (a settings row would indent it a second time). */
@Composable
private fun CardLine(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val link = item.type == "link"
    val action = item.action?.takeIf { it.id.isNotEmpty() && !link }
    val actions = if (link) emptyList() else ExtensionViewText.rowActions(item.actions)
    var menu by remember(item.key, item.title) { mutableStateOf(false) }
    val click: (() -> Unit)? = when {
        link -> ({ handle.open(item.url.orEmpty()) })
        action != null -> ({ handle.press(item, action) })
        else -> null
    }
    Row(Modifier.fillMaxWidth().rowActionSemantics(actions, handle)
        .then(if (click != null || actions.isNotEmpty()) Modifier.combinedClickable(enabled = !state.busy || link, onClick = click ?: {},
            onLongClick = if (actions.isNotEmpty()) ({ menu = true }) else null) else Modifier)
        .padding(vertical = Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(item.title.orEmpty(), style = type.body, color = if (link) palette.accent else palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            item.subtitle?.takeIf { it.isNotBlank() && !link }?.let { Text(it, style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis) }
        }
        item.value?.takeIf { it.isNotBlank() && !link }?.let {
            Spacer(Modifier.width(Tokens.Space.s))
            JChangingText(it, style = type.callout, color = palette.ink3)
        }
        ExtensionViewText.spark(item.spark)?.takeIf { !link }?.let { Spacer(Modifier.width(Tokens.Space.s)); JSparkline(it) }
        item.badge?.takeIf { it.text.isNotBlank() && !link }?.let {
            Spacer(Modifier.width(Tokens.Space.s))
            ExtensionBadge(it)
        }
        // a card takes a switch or a stepper (the hub leaves no slider there), and the row's actions only in its menu
        item.drawnControl()?.takeIf { !link && !it.under() }?.let { Spacer(Modifier.width(Tokens.Space.s)); RowControlEnd(it, state, handle) }
        if (actions.isNotEmpty()) RowActionsMenu(actions, menu, { menu = it }, handle, enabled = true)
        if (action != null && state.acting == action.id) { Spacer(Modifier.width(Tokens.Space.s)); JSpinner() }
        else if (click != null && actions.isEmpty()) {
            Spacer(Modifier.width(Tokens.Space.xs))
            JIcon(if (link) PhIcons.ArrowSquareOut else PhIcons.CaretRight, null, size = Tokens.Space.l, tint = palette.ink3)
        }
    }
}
