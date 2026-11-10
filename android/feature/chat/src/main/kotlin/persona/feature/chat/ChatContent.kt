package persona.feature.chat

import androidx.compose.ui.platform.LocalResources
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.layout.FirstBaseline
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import coil3.ImageLoader
import coil3.compose.AsyncImage
import coil3.decode.DataSource
import coil3.decode.ImageSource
import coil3.fetch.Fetcher
import coil3.fetch.SourceFetchResult
import coil3.key.Keyer
import coil3.request.Options
import com.mikepenz.markdown.compose.components.markdownComponents
import com.mikepenz.markdown.compose.elements.MarkdownBulletList
import com.mikepenz.markdown.compose.elements.MarkdownOrderedList
import com.mikepenz.markdown.compose.elements.listDepth
import com.mikepenz.markdown.m3.markdownColor
import com.mikepenz.markdown.m3.markdownTypography
import com.mikepenz.markdown.m3.Markdown
import com.mikepenz.markdown.model.State as MarkdownResult
import com.mikepenz.markdown.model.parseMarkdown
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.designsystem.*
import persona.core.designsystem.WorkStep as TimelineStep
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.lines.*
import androidx.compose.runtime.key
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.withContext
import okio.FileSystem
import okio.Path.Companion.toOkioPath

/** What a reply's text parses as: its formulas as images (MathMarkdown) and its bare links as autolinks (ChatLinks). */
internal fun chatMarkdown(text: String) = ChatLinks.markdown(MathMarkdown.rewrite(text))

/**
 * The page's parsed Markdown (docs/android/android.md "Reply Markdown parsing"): one entry per id, the text it
 * was parsed from and its parse.
 *
 * What it keeps: every id the page's prewarm last asked to [keep], and the most recently used [OTHERS] ids besides
 * (thinking, plans, recaps, replies older than the prewarm's). So it holds at most the prewarm's set (its 80 replies'
 * text parts) plus [OTHERS]: about 112 entries, which at the ~19 bytes of parse per byte of reply measured on the JVM
 * (ChatMarkdownBench.retainedHeapPerEntry: 38 KB for a 2 KB reply, 294 KB for 16 KB, 1.15 MB for 64 KB) is about 4 MB
 * of 2 KB replies and 33 MB of 16 KB ones. A plain LRU as large as the prewarm's set lost all of it to the first other
 * text: the prewarm, walking its set in order, then evicted each entry just before it came back to it.
 *
 * One parse per id and text: a caller that asks while a parse of the same id and text is under way (the page's prewarm
 * and the row it warms, say) waits for that one. The parse runs in the cache's own [scope], so a caller that leaves (a
 * row scrolled away, a prewarm restarted) does not cancel it for the others or lose its work.
 *
 * A streaming reply parses from its last settled block on ([ChatMarkdownStream]); once it ends, its final text is parsed
 * whole and kept under the same id.
 */
internal class ChatMarkdownCache(
    /** Parses Markdown whole: mikepenz's parseMarkdown, or one that counts in tests. */
    private val parseWhole: (String) -> MarkdownResult = { parseMarkdown(it) },
    private val scope: CoroutineScope = CoroutineScope(SupervisorJob() + Dispatchers.Default),
) {
    /** [result] parsed from [text]; [whole]: by [parseWhole], not from a stream's tail. */
    private class Entry(val text: String, val result: MarkdownResult, val whole: Boolean)
    private data class Parse(val id: String, val text: String, val whole: Boolean)

    /** Least recently used first. */
    private val entries = LinkedHashMap<String, Entry>(128, 0.75f, true)
    private var kept: Set<String> = emptySet()
    private val running = HashMap<Parse, Deferred<MarkdownResult>>()
    private val streams = ChatMarkdownStream(parseWhole)

    @Synchronized fun cached(id: String, text: String) = entries[id]?.takeIf { it.text == text }?.result

    /** The ids the page's prewarm parses, kept however many other ids are parsed between its runs. */
    @Synchronized fun keep(ids: Collection<String>) {
        kept = ids.toHashSet()
        trim()
    }

    private fun trim() {
        var others = entries.keys.count { it !in kept }
        val ids = entries.keys.iterator()
        while (others > OTHERS && ids.hasNext()) {
            if (ids.next() !in kept) {
                ids.remove()
                others--
            }
        }
    }

    /**
     * [text]'s Markdown under [id]. [streaming] is what the row knows of the reply (MessageContent): true while it
     * streams, false once it is whole; null from a caller that does not know (the page's prewarm, a plan or a recap),
     * which goes with what the row last said.
     */
    suspend fun parse(id: String, text: String, streaming: Boolean? = null): MarkdownResult {
        val parse = synchronized(this) {
            when (streaming) {
                true -> streams.start(id)
                false -> streams.end(id)
                null -> {}
            }
            val whole = !streams.streaming(id)
            entries[id]?.takeIf { it.text == text && (it.whole || !whole) }?.let { return it.result }
            val key = Parse(id, text, whole)
            running[key] ?: scope.async(start = CoroutineStart.LAZY) { compute(key) }.also { job ->
                running[key] = job
                job.invokeOnCompletion { synchronized(this) { running.remove(key, job) } }
                job.start()
            }
        }
        return parse.await()
    }

    private fun compute(parse: Parse): MarkdownResult {
        val markdown = chatMarkdown(parse.text)
        val (result, whole) = if (parse.whole) parseWhole(markdown) to true else streams.parse(parse.id, markdown)
        synchronized(this) {
            entries[parse.id] = Entry(parse.text, result, whole)
            trim()
        }
        return result
    }

    companion object {
        const val OTHERS = 32
    }
}

internal val LocalChatMarkdownCache = staticCompositionLocalOf<ChatMarkdownCache?> { null }

/**
 * Markdown in a conversation. A message's words pass [selectable] false: a long press on a message opens its menu (whose
 * "Select" shows the text to select from), and a SelectionContainer would take the press (docs/android/android.md, the old App's
 * lessons). Text without that menu (thinking, a plan, a recap) stays selectable. [streaming]: a reply's row says whether
 * it is still streaming ([ChatMarkdownCache.parse]).
 */
@Composable
internal fun ChatMarkdown(text: String, id: String = text,
    bodyStyle: TextStyle = PersonaTheme.type.body, color: Color = PersonaTheme.palette.ink, selectable: Boolean = true,
    viewKeys: ((Int) -> String)? = null, streaming: Boolean? = null) {
    if (selectable) SelectionContainer { ChatMarkdownBody(text, id, bodyStyle, color, viewKeys, streaming) }
    else ChatMarkdownBody(text, id, bodyStyle, color, viewKeys, streaming)
}

/**
 * [text] with its card blocks (docs/assistant/assistant-views.md "Card identity", the hub's fence rules): the Markdown around them as
 * Markdown, each ```persona-view as a card under its key ([viewKeys] maps its number in the text to `<item id>/<n>`;
 * none: a local one, which only draws), each ```persona-card as it always was. A card shown inside another code block
 * is an example and stays code.
 */
@Composable
private fun ChatMarkdownCards(text: String, id: String, fences: List<persona.core.domain.views.AssistantFence>,
    bodyStyle: TextStyle, color: Color, viewKeys: ((Int) -> String)?, streaming: Boolean?) {
    val blocks = LocalViewBlocks.current
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        var at = 0
        fences.forEachIndexed { n, fence ->
            text.substring(at, fence.start).takeIf { it.isNotBlank() }?.let { ChatMarkdownBody(it, "$id/m$n", bodyStyle, color, null, streaming) }
            at = fence.end
            when {
                blocks == null -> CodeText(fence.raw.trimEnd('\n', '\r'), fence.language, syntax = true)
                fence.isView -> key(fence.index) { blocks.block(viewKeys?.invoke(fence.index) ?: "$id/local/${fence.index}", fence.raw, fence.complete) }
                else -> {
                    val card = remember(blocks, fence.raw, fence.complete) { if (fence.complete) blocks.parse(fence.language, fence.raw) else null }
                    if (card != null) blocks.draw(card) else CodeText(fence.raw.trimEnd('\n', '\r'), fence.language, syntax = true)
                }
            }
        }
        text.substring(at).takeIf { it.isNotBlank() }?.let { ChatMarkdownBody(it, "$id/m${fences.size}", bodyStyle, color, null, streaming) }
    }
}

@Composable
private fun ChatMarkdownBody(text: String, id: String, bodyStyle: TextStyle, color: Color, viewKeys: ((Int) -> String)? = null,
    streaming: Boolean? = null) {
    val fences = remember(text) {
        if (persona.core.domain.views.AssistantFences.mentions(text)) persona.core.domain.views.AssistantFences.blocks(text) else emptyList()
    }
    if (fences.isNotEmpty()) return ChatMarkdownCards(text, id, fences, bodyStyle, color, viewKeys, streaming)
    val cache = LocalChatMarkdownCache.current ?: remember { ChatMarkdownCache() }
    // The paths there on the session's machine become links that open the files pane (PathLinks.kt).
    val actions = LocalChatRowActions.current
    val presence = actions?.paths
    val open = actions?.openPath
    val asked = remember(text, presence) { if (presence == null) emptyList() else PathLinks.asked(text) }
    if (presence != null && asked.isNotEmpty()) LaunchedEffect(presence, asked) { presence.ask(asked) }
    val linkable = if (presence == null || open == null) emptySet() else asked.filterTo(LinkedHashSet(), presence::linkable)
    val source = remember(text, linkable) { PathLinks.markdown(text, linkable) }
    // produceState retains its previous value across key changes. Never draw a predecessor's Markdown in this slot.
    // [streaming] is a key too: a reply that ends on the text it last streamed still gets its whole parse.
    val parsedSource by produceState(source to cache.cached(id, source), id, source, cache, streaming) {
        value = source to cache.parse(id, source, streaming)
    }
    val parsed = parsedSource.takeIf { it.first == source }?.second
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val outer = LocalUriHandler.current
    val uriHandler = remember(outer, open) { if (open == null) outer else PathLinks.handler(outer, open) }
    CompositionLocalProvider(LocalUriHandler provides uriHandler) {
        if (parsed == null) ChatLinkText(text, Modifier.fillMaxWidth(), bodyStyle.copy(color = color))
        else Markdown(parsed!!, modifier = Modifier.fillMaxWidth(), imageTransformer = rememberMathImages(color, bodyStyle),
            colors = markdownColor(text = color, codeBackground = palette.sunken,
                inlineCodeBackground = palette.sunken, dividerColor = palette.line,
                tableBackground = palette.surface, darkTheme = palette.dark,
                highlightText = palette.ink, highlightBackground = palette.marker),
            typography = markdownTypography(h1 = type.largeTitle, h2 = type.title, h3 = type.headline,
                h4 = type.bodyStrong, h5 = type.callout, h6 = type.caption,
                text = bodyStyle, paragraph = bodyStyle, code = type.code, inlineCode = type.code,
                quote = type.callout.copy(color = palette.ink2), ordered = bodyStyle,
                bullet = bodyStyle, list = bodyStyle, table = bodyStyle, alertTitle = type.bodyStrong,
                textLink = TextLinkStyles(SpanStyle(color = palette.accent, textDecoration = TextDecoration.Underline))),
            components = markdownComponents(
                orderedList = { model -> MarkdownOrderedList(model.content, model.node, depth = model.listDepth,
                    markerModifier = { Modifier.alignBy(FirstBaseline).padding(end = 8.dp) },
                    listModifier = { Modifier.weight(1f).alignBy(FirstBaseline) }) },
                unorderedList = { model -> MarkdownBulletList(model.content, model.node, depth = model.listDepth,
                    markerModifier = { Modifier.alignBy(FirstBaseline).padding(end = 8.dp) },
                    listModifier = { Modifier.weight(1f).alignBy(FirstBaseline) }) },
                codeFence = { model ->
                    MarkdownFence(model.content.substring(model.node.startOffset, model.node.endOffset), color, bodyStyle) {
                        // A card fence the hub's rules do not take as standalone (in a list or a quote): an example, drawn as
                        // code. Standalone ones never get here (ChatMarkdownCards).
                        CodeText(it.code, it.language, syntax = true)
                    }
                },
                codeBlock = { model -> CodeText(model.content.substring(model.node.startOffset, model.node.endOffset).trimIndent()) },
            ))
    }
}

/**
 * A code block with its caption ([language]) and "Copy". [syntax]: [language] is a fence's language, so the code is
 * coloured by it (web highlights fenced blocks that name one, no guessing); tool output and inputs stay plain.
 */
@Composable
internal fun CodeText(text: String, language: String = "", syntax: Boolean = false) {
    var expanded by remember(text) { mutableStateOf(false) }
    val preview = remember(text, expanded) { if (expanded) text else text.lineSequence().take(40).joinToString("\n") }
    val clipboard = LocalClipboardManager.current
    val palette = PersonaTheme.palette
    val colour = syntax && language.isNotBlank()
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Text(language, Modifier.weight(1f), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            JTextButton(stringResource(R.string.chat_content_copy), { clipboard.setText(AnnotatedString(text)) }, icon = PhIcons.Copy)
        }
        if (remember(preview) { ChatLinks.matches(preview).isNotEmpty() }) {
            SelectionContainer {
                val linked = chatLinkAnnotations(preview)
                Text(if (colour) remember(linked, language, palette) { CodeSyntax.highlight(linked, language, palette) } else linked, style = PersonaTheme.type.code, color = PersonaTheme.palette.ink,
                    softWrap = false, modifier = Modifier.fillMaxWidth().clip(PersonaTheme.shapes.controlShape)
                        .background(PersonaTheme.palette.sunken).horizontalScroll(rememberScrollState()).padding(Tokens.Space.m))
            }
        } else if (colour) JCodeBlock(remember(preview, language, palette) { CodeSyntax.highlight(AnnotatedString(preview), language, palette) })
        else JCodeBlock(preview)
        Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (preview.length < text.length) JTextButton(stringResource(R.string.chat_content_show_all), { expanded = true })
            ReaderEntry(text, language.ifBlank { stringResource(R.string.chat_content_full_text) })
        }
    }
}

internal data class ChatImage(val session: SessionKey, val item: Item)

internal fun chatImageLoader(context: android.content.Context, repository: ChatRepository): ImageLoader =
    ImageLoader.Builder(context).components {
        add(Keyer<ChatImage> { data, _ -> "${(repository as? ChatLocalStorage)?.localStorage?.value?.scope}/${data.session.path}/${data.item.id}" })
        add(object : Fetcher.Factory<ChatImage> {
            override fun create(data: ChatImage, options: Options, imageLoader: ImageLoader): Fetcher = object : Fetcher {
                override suspend fun fetch() = withContext(Dispatchers.IO) {
                    val file = checkNotNull(repository as? ChatLocalStorage) { "Scoped image storage is unavailable" }
                        .imageFile(data.session, data.item,
                            (options.size.width as? coil3.size.Dimension.Pixels)?.px ?: context.resources.displayMetrics.widthPixels,
                            (options.size.height as? coil3.size.Dimension.Pixels)?.px ?: context.resources.displayMetrics.heightPixels)
                    SourceFetchResult(ImageSource(file.toOkioPath(), FileSystem.SYSTEM), "image/png",
                        DataSource.DISK)
                }
            }
        })
    }.build()

/**
 * A picture of the conversation (R5): it keeps its shape while it loads, at most [ChatPictureMaxHeight] tall; a tap opens
 * the image viewer on all the conversation's pictures, a failed one retries.
 */
@Composable
internal fun ChatPicture(session: SessionKey, item: Item, loader: ImageLoader, alignEnd: Boolean = false) {
    val image = item.image ?: return
    val actions = LocalChatRowActions.current
    val ratio = if ((image.width ?: 0) > 0 && (image.height ?: 0) > 0) image.width!!.toFloat() / image.height!! else null
    var failed by remember(item.id) { mutableStateOf(false) }
    var attempt by remember(item.id) { mutableIntStateOf(0) }
    val description = stringResource(R.string.chat_content_image)
    ChatPictureFrame(ratio, failed, onClick = actions?.let { rowActions -> { rowActions.openPicture(item) } },
        onRetry = { failed = false; attempt++ }, description = description, alignEnd = alignEnd) {
        key(attempt) {
            AsyncImage(ChatImage(session, item), description, imageLoader = loader,
                modifier = Modifier.fillMaxSize(), onError = { failed = true }, onSuccess = { failed = false })
        }
    }
}

/** [userImages] renders neighboring user-image rows in the same bubble; hide those rows with [imagesInUserBubble].
 * [workRunning] comes from the parent work group's live flag; [lastWorkStep] ends its timeline connector; [plan] is the
 * group's plan ([latestPlan]): its TodoWrite is the full plan card, with earlier updates retained as ordinary steps.
 */
@Composable
internal fun ChatLineContent(
    line: ChatLine, session: SessionKey, loader: ImageLoader, recordedDuration: Long?,
    onToggle: (String) -> Unit, onFold: (Item) -> Unit, folding: Set<String>, foldErrors: Map<String, String>,
    lastUserId: String? = null, onLastLine: (Int) -> Unit = {},
    onFullText: (Item) -> Unit,
    style: ChatRenderStyle = ChatRenderStyle.Agent,
    textUpdates: Map<String, ChatTextUpdate> = emptyMap(),
    userImages: List<Item> = emptyList(),
    imagesInUserBubble: Boolean = false,
    workRunning: Boolean = false,
    lastWorkStep: Boolean = false,
    plan: LinePlan? = null,
    showSourceLabel: Boolean = true,
) {
    val res = LocalResources.current
    when (line) {
        is ChatLine.Row -> when (line.row.type) {
            "user" -> line.row.item?.item?.let { item ->
                val source = persona.core.domain.sourceOf(item)
                when {
                    source != null -> SourceMessage(item, source, showSourceLabel, textUpdates[item.id],
                        images = userPictures(userImages, session, loader))
                    line.row.item?.injected != null -> InjectedLine(line.row.id, line.row.item!!.injected!!)
                    else -> MergedUserBubble(listOf(item), textUpdates, onLastLine.takeIf { line.row.id == lastUserId },
                        images = userPictures(userImages, session, loader))
                }
            }
            "reply" -> line.row.item?.item?.let { MessageContent(it, false, textUpdates[it.id], boxed = true) }
            "thinking" -> if (!style.hideThoughts) ThoughtContent(line.row.item?.text.orEmpty(), line.id, style.collapseThoughts)
            "image" -> line.row.item?.item?.let { ChatPicture(session, it, loader) }
            "recap" -> line.row.item?.item?.let { RecapLine(listOf(it)) }
            else -> line.row.item?.compaction?.let { TimelineMark(it.label.text()) }
                ?: line.row.item?.takeIf { it.task != null }?.let { TaskNoteLine(it, last = true) }
                ?: ChatSystemLine(line.row.item?.text.orEmpty(), line.row.item?.task?.failed == true)
        }
        is ChatLine.Recaps -> RecapLine(line.items.map { it.item })
        is ChatLine.Extra -> AssistantExtraLine(line.payload, LocalAssistantTaskHost.current)
        is ChatLine.ApiError -> ApiErrorLine(line)
        is ChatLine.UserImages -> if (!imagesInUserBubble) UserBubbleFrame {
            line.images.forEach { ChatPicture(session, it.item, loader, alignEnd = true) }
        }
        is ChatLine.Work -> Column(Modifier.fillMaxWidth()) {
            // No live preview: ordinary tools, thoughts and intermediate text stay inside the expandable summary.
            if (style.compactToolSummary) AssistantWorkSummary(line, onToggle)
            else {
                val now by rememberNow(1_000, enabled = line.live)
                WorkGroup(workSummary(res, line, recordedDuration, now.takeIf { line.live }), line.open,
                    { onToggle(line.id) }, running = line.live) {}
            }
        }
        is ChatLine.Step -> WorkStep(line.step, session, loader, onFold, folding, foldErrors, onFullText, style, textUpdates, workRunning, lastWorkStep, plan)
        is ChatLine.End -> TimelineMark(pluralStringResource(R.plurals.chat_content_collapse_steps, line.count, line.count),
            Modifier.heightIn(min = 44.dp).clickable(role = Role.Button) { onToggle(line.groupId) })
        is ChatLine.Wakes -> ChatSystemLine(pluralStringResource(R.plurals.chat_content_wakes, line.turn.wakes.size, line.turn.wakes.size))
    }
}

@Composable
private fun AssistantWorkSummary(line: ChatLine.Work, onToggle: (String) -> Unit) {
    val state = stringResource(if (line.open) R.string.chat_tasks_expanded else R.string.chat_tasks_collapsed)
    val label = pluralStringResource(R.plurals.chat_assistant_checked, line.tools, line.tools)
    val spoken = stringResource(persona.core.designsystem.R.string.designsystem_with_state, label, state)
    Row(Modifier.fillMaxWidth().clickable(role = Role.Button) { onToggle(line.id) }
        .then(Modifier.semantics { stateDescription = state; contentDescription = spoken })
        .padding(vertical = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        JIcon(if (line.open) PhIcons.CaretDown else PhIcons.CaretRight, decorative = true,
            size = Tokens.Space.m, tint = PersonaTheme.palette.ink3)
        Text(label,
            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3,
            maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

@Composable
private fun WorkStep(step: WorkItem, session: SessionKey, loader: ImageLoader, onFold: (Item) -> Unit,
    folding: Set<String>, errors: Map<String, String>, onFullText: (Item) -> Unit,
    style: ChatRenderStyle, textUpdates: Map<String, ChatTextUpdate>, running: Boolean, last: Boolean, plan: LinePlan? = null) {
    when (step) {
        is WorkItem.Folded -> {
            val item = step.item.item
            val loading = item.id in folding
            WorkGroup(
                summary = if (loading) pluralStringResource(R.plurals.chat_content_loading_steps, step.stepCount(), step.stepCount())
                    else if (item.id in errors) stringResource(R.string.chat_content_load_failed)
                    else pluralStringResource(R.plurals.chat_work_done_steps, step.stepCount(), step.stepCount()),
                expanded = false, onToggle = { if (!loading) onFold(item) }, running = loading,
            ) {}

        }
        is WorkItem.Tool -> {
            val call = step.call
            // A Workflow call whose run the session lists draws the run's card (#3435); otherwise the row it always was.
            if (call.name == "Workflow") LocalChatWorkflows.current?.let { workflows ->
                call.use.item.toolUseId?.let(workflows.runs::get)?.let { run ->
                    JWorkflowCallCard(run, folds = workflows.folds, onOpenAgent = workflows.open)
                    return
                }
            }
            var open by remember(call.id) { mutableStateOf(false) }
            val diff = remember(call.use.item) { toolDiff(call.use.item) }
            val todos = remember(call.use.item) { todoEntries(call.use.item) }
            val searchItem = LocalChatSearchItem.current
            LaunchedEffect(searchItem) {
                if (searchItem != null && (searchItem == call.use.id || searchItem == call.result?.id)) {
                    open = true
                }
            }
            if (todos != null && !style.compactToolSummary && plan?.id == call.id) {
                PlanStep(plan, running)
                return
            }
            val presentation = toolPresentation(LocalResources.current, call.use.item)
            Column(Modifier.fillMaxWidth()) {
                // A named tool with nothing to name ("Read agent chat") is its title already: no verb tag saying it twice.
                TimelineStep(toolCategory(call.name), presentation.title, verb = presentation.verb.takeUnless { it == presentation.title },
                    detail = listOfNotNull(presentation.detail, if (call.running) stringResource(if (running) R.string.chat_content_tool_running else R.string.chat_content_tool_no_result) else null)
                        .joinToString(stringResource(R.string.chat_status_clause_separator)).ifBlank { null },
                    state = when {
                        call.failed -> WorkState.Failed
                        running && call.running -> WorkState.Running
                        call.running -> WorkState.Waiting
                        else -> WorkState.Done
                    },
                    last = last, mono = presentation.mono, added = diff?.added ?: 0, removed = diff?.removed ?: 0,
                    onClick = { open = !open })
                if (todos != null) TodoList(todos, call.failed)
                if (call.name == "Task" || call.name == "Agent") SubagentEntry(call.use.item.toolUseId)
                if (call.failed && !open) call.result?.text?.takeIf { it.isNotBlank() }?.let {
                    Text(it, maxLines = 3, overflow = TextOverflow.Ellipsis, color = PersonaTheme.palette.danger.text,
                        style = PersonaTheme.type.caption)
                }
                if (open) Column(Modifier.fillMaxWidth().padding(start = Tokens.Space.m),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    if (diff != null) {
                        toolPath(call.use.item)?.let { DiffHeader(it, diff.added, diff.removed) }
                        DiffView(diff, call.id)
                    } else if (todos == null) ToolInputDetail(call.use.item, call.result?.item)
                    // A truncated output opens in full from "Raw output" below. An edit's own result only says it worked; an
                    // answered ask_user's result repeats the ticked choices above in the model's words (iOS ToolViews).
                    call.result?.item?.let { result -> if (todos == null && (diff == null || call.failed) && (!PersonaTools.isAskUser(call.name) || call.failed)) CodeText(result.text.orEmpty(), stringResource(R.string.chat_content_output)) }
                    // A dev server saying where it listens (docs/operations/ports.md).
                    if (call.name == "Bash") call.result?.text?.let { PortBadges(it) }
                    call.images.forEach { ChatPicture(session, it.item, loader) }
                    ToolReaderActions(call.use.item.toolInput?.toString(), call.result?.text,
                        call.result?.item?.takeIf { it.fullSize != null }?.let { result -> { onFullText(result) } })
                }
            }
        }

        is WorkItem.Narration -> MessageContent(step.item.item, false, textUpdates[step.item.id])
        is WorkItem.Thought -> if (step.item.kind == "thinking") {
            if (!style.hideThoughts) ThoughtContent(textUpdates[step.item.id]?.text ?: step.item.text.orEmpty(), step.item.id, style.collapseThoughts, last)
        }
        else step.item.compaction?.let { TimelineMark(it.label.text()) }
            ?: step.item.takeIf { it.task != null }?.let { TaskNoteLine(it, last) }
            ?: ChatSystemLine(step.item.task?.detail ?: step.item.text.orEmpty(), step.item.task?.failed == true)
    }
}

/** A work line's plan as the plan card (web #1990): the step it is on, Step N/M, the list. [nested]: inside a work group's card. */
@Composable
private fun PlanStep(plan: LinePlan, running: Boolean, nested: Boolean = false) {
    val progress = remember(plan.todos) { planProgress(plan.todos) }
    val items = remember(plan.todos) {
        plan.todos.map { it.content to when (it.status) {
            "completed" -> PlanItemState.Done
            "in_progress" -> PlanItemState.Active
            else -> PlanItemState.Pending
        } }
    }
    PlanCard(items, progress.current, progress.step, live = running, nested = nested, note = if (plan.failed) stringResource(R.string.chat_content_plan_failed) else null)
}

/** The pictures shown inside a user bubble, or null when there are none (a message of only contexts has no bubble). */
private fun userPictures(images: List<Item>, session: SessionKey, loader: ImageLoader): (@Composable () -> Unit)? =
    if (images.isEmpty()) null else ({ images.forEach { image -> ChatPicture(session, image, loader, alignEnd = true) } })

/** A message not in the transcript yet (the outbox): no long-press menu, its own status line has the actions. */
@Composable
internal fun UserBubble(text: String) {
    MergedUserBubble(listOf(Item("pending", role = "user", kind = "text", text = text)), menu = false)
}

@Composable
internal fun ChatSystemLine(text: String, failed: Boolean = false) {
    Row(Modifier.fillMaxWidth().padding(vertical = 4.dp), horizontalArrangement = Arrangement.spacedBy(6.dp),
        verticalAlignment = Alignment.Top) {
        JIcon(if (failed) PhIcons.WarningCircle else PhIcons.Info, null, size = 16.dp,
            tint = if (failed) PersonaTheme.palette.danger.text else PersonaTheme.palette.ink2)
        ChatLinkText(text, style = PersonaTheme.type.caption.copy(color = if (failed) PersonaTheme.palette.danger.text else PersonaTheme.palette.ink2))
    }
}
