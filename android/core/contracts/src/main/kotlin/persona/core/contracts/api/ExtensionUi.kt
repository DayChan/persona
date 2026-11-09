@file:OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)

package persona.core.contracts.api

import kotlinx.serialization.KSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.json.JsonNames
import kotlinx.serialization.Serializable
import kotlinx.serialization.SerializationException
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.encoding.Decoder
import kotlinx.serialization.encoding.Encoder
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonDecoder

/**
 * An extension's declarative UI (docs/extensions/extensions.md "Declarative UI"): the extension never runs code in the app; it answers with a
 * View of a few fixed kinds of item that each app draws with its own design system, and its buttons come back to it.
 */
interface ExtensionUi {
    /** Selected authenticated connection scope. A page pins it for its entire navigation lifetime. */
    val nativeStream: kotlinx.coroutines.flow.Flow<ExtensionStreamBinding?> get() = kotlinx.coroutines.flow.flowOf(null)
    /**
     * `GET /api/v1/extensions/-/ui?placement=…`: what the enabled, running extensions draw at [placement]
     * (`extension_page`, `launcher_section`, `inbox_item`); a launcher's section names the launcher (and the machine it is
     * on). An empty [placement] lists every slot but launcher sections at once (`nav_page`, `overview_card`,
     * `machine_section`, with their badges; docs/extensions/extensions.md "UI slots"), only on a hub that says `extension_ui_slots`. A hub
     * that does not know a placement lists nothing there.
     */
    suspend fun uiPlaces(placement: String, launcher: String? = null, machine: String? = null): List<ExtensionUiPlace>

    /** `POST /api/v1/extensions/{id}/-/ui/{ui}` without an action: draws the view. */
    suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext? = null, state: String? = null): ExtensionView

    /**
     * The same with [action] (a button was pressed): a new view, a toast, a link to open, or nothing (draw again).
     * [values] are the page's fields when the action submits them.
     */
    suspend fun uiAction(
        extension: String, ui: String, action: String, context: ExtensionUiContext? = null, state: String? = null,
        values: Map<String, String>? = null,
    ): ExtensionUiResult

    /**
     * `GET /api/v1/extensions/{id}/-/image/{token}` (UI version 6): a picture an extension serves itself (`ui.image`), at
     * the [path] the hub wrote into the view for its `ref`. Only that hub-signed path is fetched with the device's
     * credentials; an extension's `url` never is. A path that has expired (410) throws [ExtensionImageExpired].
     */
    suspend fun uiImage(path: String): ByteArray = throw UnsupportedOperationException("uiImage")
}

/**
 * Where an extension draws something (`{extension, extension_name, id, title, placement, size, icon?, opens?, badge?, connector?}`).
 * The slots of docs/extensions/extensions.md "UI slots": a `nav_page` has an [icon] and maybe a [badge]; an `overview_card` [opens] one
 * of the extension's pages; an `inbox_item` draws the page of its [connector]'s inbox items.
 */
@Serializable
data class ExtensionUiPlace(
    @SerialName("extension") @JsonNames("plugin") val extension: String = "",
    val id: String = "",
    val title: String = "",
    val placement: String = "",
    @SerialName("extension_name") @JsonNames("plugin_name") val extensionName: String? = null,
    /** `full` or `compact` (a card). */
    val size: String? = null,
    /** A `nav_page`'s: `extension`, `chart`, `gauge`, `list`, `bell`, `doc`, `calendar`, `money`; anything else is `extension`. */
    val icon: String? = null,
    /** An `overview_card`'s: the id of the extension's page tapping the card opens; none, the card does not open anything. */
    val opens: String? = null,
    /** A `nav_page`'s badge, set by the extension (and changed by `extension_ui` events). */
    val badge: ExtensionViewBadge? = null,
    /** An `inbox_item`'s: the connector whose inbox items it draws (`lark`), the items' `source`. */
    val connector: String? = null,
    /**
     * Whose extension draws it, when the place says (docs/assistant/managers.md); null: the default's. `GET /api/v1/extensions/-/ui`
     * is the default manager's alone (not a merged list), so its places carry none today; a page they open keeps one if
     * they ever do.
     */
    val manager: String? = null,
)

/**
 * Where a view is drawn and for whom (docs/extensions/extensions.md "UI slots" "Context"): a launcher's page names the launcher and its
 * machine, a machine's section the machine, an inbox item's page the [item]; [size] is `compact` for a card. The time
 * zone, language, platform and UI version are the app's: the HTTP client fills in what is left empty.
 */
@Serializable
data class ExtensionUiContext(
    val launcher: String? = null,
    val machine: String? = null,
    /** `inbox_item`: the inbox item's id; the hub hands the extension that row. */
    val item: String? = null,
    val size: String? = null,
    val tz: String? = null,
    val locale: String? = null,
    val platform: String? = null,
    @SerialName("ui_version") val uiVersion: Int? = null,
)

/**
 * An extension's view: an optional title, sections of items, the extension's own state, sent back as it is, and how many
 * seconds after which it wants to be drawn again while on screen ([refreshAfter]; 0 or none: never).
 */
@Serializable
data class ExtensionView(
    val title: String? = null,
    val sections: List<ExtensionViewSection> = emptyList(),
    val state: String? = null,
    @SerialName("refresh_after") val refreshAfter: Int? = null,
    /** UI version 3: a search box on top; typing (after a pause) sends its action with `{q}`. */
    val search: ExtensionViewSearch? = null,
    /** UI version 3: one of a few beside the search box; choosing sends its action with `{name: value}` (and `q`). */
    val filter: ExtensionViewFilter? = null,
    /** UI version 4: tabs on top; choosing one sends its action with `{name: value}` (and `q`). */
    val tabs: ExtensionViewTabs? = null,
    /** UI version 4: `split` puts the sections of the `side` pane beside the rest where there is room, above them folded where not. */
    val layout: String? = null,
    /** UI version 5: the pending review's bar at the page's foot. */
    val review: ExtensionViewReview? = null,
)

/** The pending review (UI version 5): [count] drafts; a summary sent under [name] with the button pressed; [discard]. */
@Serializable
data class ExtensionViewReview(
    val count: Int = 0,
    val name: String = "",
    val placeholder: String? = null,
    val buttons: List<ExtensionComposerButton> = emptyList(),
    val discard: ExtensionViewAction? = null,
)

/** A view's tabs (UI version 4): 2 to 8 [items], the chosen [value], sent under [name]. */
@Serializable
data class ExtensionViewTabs(
    val name: String = "",
    val value: String = "",
    val items: List<ExtensionViewTab> = emptyList(),
    val action: ExtensionViewAction = ExtensionViewAction(),
)

/** One tab: its [value], its [title] (the value when none) and a short [badge] (a count). */
@Serializable
data class ExtensionViewTab(val value: String = "", val title: String? = null, val badge: String? = null) {
    val label: String get() = title?.takeIf { it.isNotBlank() } ?: value
}

/** A view's search box (UI version 3): [value] is what it holds now (the extension keeps it in its state). */
@Serializable
data class ExtensionViewSearch(val placeholder: String? = null, val value: String? = null, val action: ExtensionViewAction = ExtensionViewAction())

/** A view's filter (UI version 3): [choices] (2 to 20), the chosen [value], sent under [name]. */
@Serializable
data class ExtensionViewFilter(
    val name: String = "",
    val title: String? = null,
    val choices: List<ExtensionViewChoice> = emptyList(),
    val value: String? = null,
    val action: ExtensionViewAction = ExtensionViewAction(),
)

/** A section's "Load more" (UI version 3): reaching the end, or tapping it, sends [action] once and waits. */
@Serializable
data class ExtensionViewMore(val title: String? = null, val action: ExtensionViewAction = ExtensionViewAction())

/** A section of a view. An item this app cannot even read (a newer hub's) is left out, never the whole view. */
@Serializable
data class ExtensionViewSection(
    val title: String? = null,
    val footer: String? = null,
    /** UI version 3: more items to load at the section's end. */
    val more: ExtensionViewMore? = null,
    /** UI version 4: `side` in a split view (the file tree); else the main pane. */
    val pane: String? = null,
    @Serializable(with = LenientItemsSerializer::class) val items: List<ExtensionViewItem> = emptyList(),
)

/** Reads a section's items one by one: an item that does not decode is skipped and the rest are drawn. */
internal object LenientItemsSerializer : KSerializer<List<ExtensionViewItem>> {
    private val list = ListSerializer(ExtensionViewItem.serializer())
    override val descriptor = list.descriptor

    override fun deserialize(decoder: Decoder): List<ExtensionViewItem> {
        val input = decoder as? JsonDecoder ?: return list.deserialize(decoder)
        val array = input.decodeJsonElement() as? JsonArray ?: throw SerializationException("items: expected an array")
        return array.mapNotNull { element ->
            try { input.json.decodeFromJsonElement(ExtensionViewItem.serializer(), element) }
            catch (_: SerializationException) { null }
            catch (_: IllegalArgumentException) { null }
        }
    }

    override fun serialize(encoder: Encoder, value: List<ExtensionViewItem>) = list.serialize(encoder, value)
}

/**
 * One item of a view, by [type]: `text`, `metric`, `progress`, `row`, `button`, `link`, `field` or `divider`, since UI
 * version 2 `chart`, `stat_grid` and `segmented`, and since 3 `markdown`, `table`, `empty` and `loading`; each uses a
 * few of the fields. `tone` is `neutral | positive | caution | danger`. A type this app does not know is skipped when drawn.
 */
@Serializable
data class ExtensionViewItem(
    val type: String = "",
    val text: String? = null,
    /** text: `body | secondary | caption | code`. */
    val style: String? = null,
    val label: String? = null,
    val value: String? = null,
    val detail: String? = null,
    val tone: String? = null,
    /** progress: 0…1. */
    val ratio: Double? = null,
    val title: String? = null,
    val subtitle: String? = null,
    val badge: ExtensionViewBadge? = null,
    val action: ExtensionViewAction? = null,
    /** button: `normal | destructive`. */
    val role: String? = null,
    /** link: https only. */
    val url: String? = null,
    /** field: the name its value goes back under. */
    val name: String? = null,
    /** field: `text | secret | number | toggle | choice`; chart: `bar | line`. */
    val kind: String? = null,
    /** A choice field's or a segmented's choices. */
    val choices: List<ExtensionViewChoice>? = null,
    val placeholder: String? = null,
    /** chart: 1 to 4 series with the same labels in the same order. */
    val series: List<ExtensionChartSeries>? = null,
    /** chart: the unit of its values, written after a value without a `display`. */
    val unit: String? = null,
    /** chart: bars of the series stacked (else side by side); lines ignore it. */
    val stacked: Boolean? = null,
    /** stat_grid: 1 to 6 numbers side by side. */
    val stats: List<ExtensionStat>? = null,
    /** Any item (UI version 3): stays the same across drawings, so a list keeps its place and what is open. */
    val key: String? = null,
    /** row, empty (UI version 3): one of the fixed icons (docs/extensions/extensions.md); one this app does not know is the default. */
    val icon: String? = null,
    /** row (UI version 3): a picture (https) or one or two letters standing in for one. */
    val avatar: ExtensionViewAvatar? = null,
    /** row (UI version 3): a small picture (https) on the leading side. */
    val thumbnail: ExtensionViewThumbnail? = null,
    /** table (UI version 3): 1 to 8 columns. */
    val columns: List<ExtensionTableColumn>? = null,
    /** table (UI version 3): up to 100 rows, as many cells as columns. */
    val rows: List<List<String>>? = null,
    /** table (UI version 3): a line under it. */
    val caption: String? = null,
    /** empty (UI version 3): at most one button. */
    val button: ExtensionEmptyButton? = null,
    /** html (an assistant's card only, never an extension's): a self-contained page, drawn only where it may run. */
    val html: String? = null,
    /** html: its height, 160 to 640 (320 when none). */
    val height: Int? = null,
    // UI version 4's code hosting components (#2951): header, breadcrumb, file_tree, code, diff, comment_thread, composer.
    /** header: its state badges and a line of facts. */
    val badges: List<ExtensionViewBadge>? = null,
    val facts: List<ExtensionViewFact>? = null,
    /** breadcrumb: the path's parts, every one but the last may be tapped. */
    val crumbs: List<ExtensionViewCrumb>? = null,
    /** file_tree: files (and directories) by their whole paths; [selected] is the one open. */
    val entries: List<ExtensionTreeEntry>? = null,
    val selected: String? = null,
    /** file_tree: loads a directory given with nothing under it (`{path}`); diff: shows hidden lines (`{path, hunk, direction}`). */
    val expand: ExtensionViewAction? = null,
    /** code, diff: the file. */
    val path: String? = null,
    val language: String? = null,
    /** code: the first line's number (1 when none), the lines to mark, and the rest to load (`{path, from}`) when [truncated]. */
    @SerialName("start_line") val startLine: Int? = null,
    val highlight: ExtensionLineRange? = null,
    val truncated: Boolean? = null,
    val load: ExtensionViewAction? = null,
    /** diff */
    @SerialName("old_path") val oldPath: String? = null,
    /** diff: `added | modified | deleted | renamed`. */
    val status: String? = null,
    val additions: Int? = null,
    val deletions: Int? = null,
    val binary: Boolean? = null,
    val collapsed: Boolean? = null,
    val hunks: List<ExtensionDiffHunk>? = null,
    val threads: List<ExtensionDiffThread>? = null,
    /** diff: a new comment at a line (`{path, side, line, body}`). */
    val comment: ExtensionViewAction? = null,
    /** comment_thread */
    val comments: List<ExtensionViewComment>? = null,
    val resolved: Boolean? = null,
    /** comment_thread: a reply (`{body}`). */
    val reply: ExtensionViewAction? = null,
    /** composer: 1 to 4 buttons, each sending the text typed under [name]. */
    val buttons: List<ExtensionComposerButton>? = null,
    // UI version 5's code review (#3136)
    /** diff: hidden lines asked for (`{path, side, from, to}`), answered with [ExtensionUiResult.lines]. */
    val context: ExtensionViewAction? = null,
    /** diff: marked viewed; [markViewed] changes it (`{path, viewed}`); without it the device keeps the mark by [revision]. */
    val viewed: Boolean? = null,
    @SerialName("mark_viewed") val markViewed: ExtensionViewAction? = null,
    val revision: String? = null,
    /** diff: a generated file or a lockfile (folded at first). */
    val generated: Boolean? = null,
    /** diff: how alike a renamed file is to its old one, percent. */
    val similarity: Int? = null,
    /** diff: a comment kept as a draft of the pending review (values as [comment]'s). */
    val draft: ExtensionViewAction? = null,
    /** diff: the extension takes suggestion blocks. */
    val suggestions: Boolean? = null,
    /** code: an https link to lines on the site, `{start}` and `{end}` filled in. */
    @SerialName("line_url") val lineUrl: String? = null,
    // UI version 6 (#5464): pictures, controls, more charts, a row's actions, grids and carousels, more fields, colours
    /** image: where the picture comes from. */
    val src: ExtensionImageSource? = null,
    /** image: read out, and shown when the picture does not load. */
    val alt: String? = null,
    /** image: `square | landscape | wide | portrait | auto` (landscape when none). */
    val aspect: String? = null,
    /** image: `fill` (cropped, the default) or `fit` (all of it). */
    val fit: String? = null,
    /** image: tapping does nothing (else the picture opens full screen). */
    @SerialName("no_zoom") val noZoom: Boolean? = null,
    /** row: a switch, a slider or a stepper. */
    val control: ExtensionRowControl? = null,
    /** row: extra actions, in its menu and (some) under a swipe. */
    val actions: List<ExtensionRowAction>? = null,
    /** row, metric: a small trend line. */
    val spark: ExtensionSpark? = null,
    /** row, metric, empty: the icon's named colour. */
    @SerialName("icon_color") val iconColor: String? = null,
    /** grid, carousel */
    val tiles: List<ExtensionTile>? = null,
    @SerialName("max_columns") val maxColumns: Int? = null,
    /** carousel: `narrow | regular | wide`. */
    val width: String? = null,
    /** field (date, time, datetime): the first and last allowed, written like the value. */
    val earliest: String? = null,
    val latest: String? = null,
    /** chart (donut): the words in the hole. */
    val center: String? = null,
    /** chart (candlestick): `green` (default) or `red`, a rising candle's colour. */
    val rising: String? = null,
)

/**
 * A picture's source (UI version 6): an https [url] loaded without credentials, or the extension's own [ref], for which
 * the hub writes [path] (its own address, loaded with the device's credentials). [width] and [height] shape `auto`.
 */
@Serializable
data class ExtensionImageSource(
    val url: String? = null,
    val ref: String? = null,
    val path: String? = null,
    val width: Int? = null,
    val height: Int? = null,
)

/**
 * A control in a row or a tile (UI version 6): [kind] `switch` ([on]), `slider` or `stepper` ([value] from [min] to
 * [max] by [step]); changing it sends [action] with `{name: the new value}`.
 */
@Serializable
data class ExtensionRowControl(
    val kind: String = "",
    val name: String = "",
    val on: Boolean? = null,
    val value: Double? = null,
    val min: Double? = null,
    val max: Double? = null,
    val step: Double? = null,
    val unit: String? = null,
    val display: String? = null,
    val disabled: Boolean? = null,
    val action: ExtensionViewAction = ExtensionViewAction(),
)

/** One of a row's or a tile's extra actions (UI version 6); [swipe] ones also go under a swipe. */
@Serializable
data class ExtensionRowAction(
    val title: String = "",
    val icon: String? = null,
    val role: String? = null,
    val swipe: Boolean? = null,
    val action: ExtensionViewAction = ExtensionViewAction(),
)

/** A small trend line (UI version 6): [values] only, in a named [color] or a [tone], [fill] shading under it. */
@Serializable
data class ExtensionSpark(
    val values: List<Double> = emptyList(),
    val color: String? = null,
    val tone: String? = null,
    val fill: Boolean? = null,
)

/** A tile of a grid or a carousel (UI version 6). */
@Serializable
data class ExtensionTile(
    val key: String? = null,
    val title: String = "",
    val subtitle: String? = null,
    val value: String? = null,
    val icon: String? = null,
    val image: ExtensionImageSource? = null,
    val color: String? = null,
    val badge: ExtensionViewBadge? = null,
    val active: Boolean? = null,
    val spark: ExtensionSpark? = null,
    val control: ExtensionRowControl? = null,
    val actions: List<ExtensionRowAction>? = null,
    val action: ExtensionViewAction? = null,
)

@Serializable
data class ExtensionViewFact(val label: String? = null, val value: String = "", val icon: String? = null)

@Serializable
data class ExtensionViewCrumb(val title: String = "", val action: ExtensionViewAction? = null)

/** A file of a tree (a directory with [kind] `dir`): its whole path, its [status] in a change, its lines added and removed. */
@Serializable
data class ExtensionTreeEntry(
    val path: String = "",
    val kind: String? = null,
    val status: String? = null,
    val additions: Int? = null,
    val deletions: Int? = null,
    val comments: Int? = null,
    val badge: ExtensionViewBadge? = null,
    /** UI version 5 */
    val viewed: Boolean? = null,
    val generated: Boolean? = null,
)

@Serializable
data class ExtensionLineRange(val start: Int = 1, val end: Int = 1)

/** A run of a diff: where it starts on each side, its lines each starting with ' ', '+', '-' or '\\'. */
@Serializable
data class ExtensionDiffHunk(
    val header: String? = null,
    @SerialName("old_start") val oldStart: Int = 0,
    @SerialName("new_start") val newStart: Int = 0,
    val lines: List<String> = emptyList(),
)

/** Comments at a line of a diff ([side] `old` or `new`). */
@Serializable
data class ExtensionDiffThread(
    val id: String? = null,
    val side: String = "new",
    val line: Int = 0,
    @SerialName("start_line") val startLine: Int? = null,
    val resolved: Boolean = false,
    val outdated: Boolean = false,
    val comments: List<ExtensionViewComment> = emptyList(),
    val reply: ExtensionViewAction? = null,
    /** UI version 5: a draft of the pending review; resolving it (`{thread, resolved}`). */
    val pending: Boolean = false,
    val resolve: ExtensionViewAction? = null,
)

/** A comment: [at] is RFC 3339 (drawn relative), [body] the markdown subset; an [event] is a quiet line. */
@Serializable
data class ExtensionViewComment(
    val id: String? = null,
    val author: String = "",
    val avatar: ExtensionViewAvatar? = null,
    val at: String? = null,
    val body: String? = null,
    val badge: ExtensionViewBadge? = null,
    val event: Boolean = false,
)

@Serializable
data class ExtensionComposerButton(val title: String = "", val action: ExtensionViewAction = ExtensionViewAction(), val tone: String? = null)

@Serializable
data class ExtensionEmptyButton(val title: String = "", val action: ExtensionViewAction = ExtensionViewAction())

@Serializable
data class ExtensionViewAvatar(val url: String? = null, val text: String? = null,
                               /** UI version 6: the extension's own picture; the hub writes [path] for it. */
                               val ref: String? = null, val path: String? = null)

/** [aspect]: `square` (default) or `wide`; since UI version 6 a [ref] (with the hub's [path]) in place of [url]. */
@Serializable
data class ExtensionViewThumbnail(val url: String? = null, val aspect: String? = null, val ref: String? = null, val path: String? = null)

/** [align]: `start` (default) or `end` (numbers). */
@Serializable
data class ExtensionTableColumn(val title: String = "", val align: String? = null)

/** One line, or one colour of bars, of a chart. No [tone]: the app's own colours for series, in order. */
@Serializable
data class ExtensionChartSeries(
    val name: String? = null,
    val tone: String? = null,
    val points: List<ExtensionChartPoint> = emptyList(),
    /** UI version 6: a named colour. */
    val color: String? = null,
)

/** A value at a label (a day, a model); [display] is how to write it ("¥1.20"), else the app writes it with the unit. */
@Serializable
data class ExtensionChartPoint(
    val label: String = "", val value: Double = 0.0, val display: String? = null,
    /** UI version 6: a candlestick's. */
    val open: Double? = null, val high: Double? = null, val low: Double? = null, val close: Double? = null, val volume: Double? = null,
)

/** One number of a stat_grid; [tone] colours its detail. */
@Serializable
data class ExtensionStat(val label: String = "", val value: String = "", val detail: String? = null, val tone: String? = null,
                         /** UI version 6 */ val spark: ExtensionSpark? = null)

@Serializable
data class ExtensionViewBadge(val text: String = "", val tone: String? = null, /** UI version 6: a named colour. */ val color: String? = null)

/**
 * A button's action: [confirm] asks first, [submit] sends the page's fields with it. With [push] (UI version 3) it is not
 * sent at all: a page of the extension is drawn on top, with the push's state.
 */
@Serializable
data class ExtensionViewAction(
    val id: String = "",
    val confirm: String? = null,
    val submit: Boolean = false,
    val push: ExtensionViewPush? = null,
    /** Native stream saved-action navigation; legacy HTTP rendering keeps its existing behavior. */
    val open: String? = null,
    val pop: Boolean = false,
    /** UI version 4: values it carries, sent with the fields' and the component's (these win on a name they share). */
    val values: Map<String, String>? = null,
)

/**
 * A page drawn on top (UI version 3): the extension's UI [ui] (this one when empty, a whole page's), first drawn with
 * [state]; [title] stands until the view brings its own.
 */
@Serializable
data class ExtensionViewPush(val state: String? = null, val title: String? = null, val ui: String? = null)

@Serializable
data class ExtensionViewChoice(val value: String = "", val title: String? = null) {
    val label: String get() = title?.takeIf { it.isNotBlank() } ?: value
}

/** What a pressed button brings back; all empty means: draw the view again. */
@Serializable
data class ExtensionUiResult(
    val view: ExtensionView? = null,
    val toast: String? = null,
    @SerialName("open_url") val openUrl: String? = null,
    /** UI version 3: draw a page on top (after a list's "New", say). */
    val push: ExtensionViewPush? = null,
    /** UI version 3: back to the page underneath (after "Save"), which is drawn again. */
    val pop: Boolean = false,
    /** UI version 5: lines of a file a diff's context asked for (the page is not drawn again). */
    val lines: ExtensionFileLines? = null,
    /** UI version 5: components in place of those with the same key (the rest is not drawn again). */
    @Serializable(with = LenientItemsSerializer::class) val items: List<ExtensionViewItem> = emptyList(),
)

/** Lines of one side of a file from [start] (1 the first); [total] is how many the file has. */
@Serializable
data class ExtensionFileLines(
    val path: String = "",
    val side: String? = null,
    val start: Int = 1,
    val lines: List<String> = emptyList(),
    val total: Int? = null,
)

/**
 * The extension says the launcher's gateway is not its own (404 `{code: "unsupported"}`, a section without `hosts` is
 * listed on every launcher): the section is hidden, not an error.
 */
class ExtensionUiUnsupported : Exception("unsupported")

/**
 * The hub has no such thing to draw for (404 `not_found`): a machine's section for a machine it does not know (one of
 * another hub), an extension that has gone. A machine's page leaves the section out.
 */
class ExtensionUiNotFound(message: String) : Exception(message)

/**
 * This device may not draw extensions' views (403: a device that can only read and answer). An inbox item's page opens
 * the item's own page instead; elsewhere it reads as any failure, in the hub's words.
 */
class ExtensionUiForbidden(message: String) : Exception(message)

/** A picture's hub path has expired (410): the page is drawn again, which signs its pictures anew. */
class ExtensionImageExpired : Exception("expired")
