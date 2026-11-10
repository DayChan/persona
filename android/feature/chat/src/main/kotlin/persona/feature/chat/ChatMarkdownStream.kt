package persona.feature.chat

import com.mikepenz.markdown.model.ReferenceLinkHandlerImpl
import com.mikepenz.markdown.utils.getUnescapedTextInNode
import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.MarkdownTokenTypes
import org.intellij.markdown.ast.ASTNode
import org.intellij.markdown.ast.CompositeASTNode
import org.intellij.markdown.ast.findChildOfType
import org.intellij.markdown.flavours.gfm.GFMFlavourDescriptor
import org.intellij.markdown.parser.CancellationToken
import org.intellij.markdown.parser.MarkdownParser
import com.mikepenz.markdown.model.State as MarkdownResult

/**
 * A streaming reply's Markdown, parsed from its last settled block on (docs/android/android.md "Reply Markdown parsing").
 * The hub sends a growing reply's whole text with each delta, so parsing all of it each time costs O(n²) over
 * a reply. A top-level block that a blank line closes, and that nothing after it can still change, is settled: the
 * blocks up to it parse the same whatever follows. Each step keeps the blocks before the last settled boundary, parses
 * only the text from there with its offsets, and builds what [whole] (mikepenz's parseMarkdown) builds for the whole
 * text: the same tree (each node's type and offsets), content and links, which ChatMarkdownStreamTest checks for every
 * split of its corpus.
 *
 * A step parses the whole text, and starts over from that parse, when:
 * - the text before the boundary changed (an edit, not an append: a bare link that grew into the boundary, a formula
 *   that closed, a resend);
 * - the text holds a reference-link definition anywhere, since one resolves links in blocks before it (it then stays
 *   whole for the rest of the stream);
 * - the tail fails to parse.
 * Only blocks of the kinds in [settles] close at a blank line: a list, a block quote, an HTML block, indented code or an
 * open fence can still take in what follows, or change how a later line parses, so the boundary does not pass them.
 * The caller ([ChatMarkdownCache]) also parses whole when the reply is not streaming and once more when it ends.
 */
internal class ChatMarkdownStream(private val whole: (String) -> MarkdownResult) {
    /** Where a stream's last step left off: its Markdown, the settled boundary, the top-level nodes before it, and the
     * links in them (label to destination, in the order parseMarkdown's lookup finds them). */
    private class Tail(val markdown: String, val boundary: Int, val settled: List<ASTNode>, val links: LinkedHashMap<String, String?>)

    /** Each streaming id's last step: null after a whole parse that cannot go on from a boundary. Few replies stream
     * at once, so the eight most recent are plenty; one evicted parses whole on its next step. */
    private val tails = object : LinkedHashMap<String, Tail?>(8, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, Tail?>) = size > 8
    }

    @Synchronized fun streaming(id: String) = id in tails

    @Synchronized fun start(id: String) {
        if (id !in tails) tails[id] = null
    }

    @Synchronized fun end(id: String) {
        tails.remove(id)
    }

    /** [markdown]'s parse for [id]'s stream, going on from its last step, and whether it parsed the whole text. */
    fun parse(id: String, markdown: String): Pair<MarkdownResult, Boolean> {
        val previous = synchronized(this) { tails[id] }
        // Anything unexpected in a step (a tail that fails to parse among them) parses the whole text instead.
        val stepped = previous?.takeIf { it.boundary <= markdown.length && markdown.regionMatches(0, it.markdown, 0, it.boundary) }
            ?.let { runCatching { step(it, markdown) }.getOrNull() }
        val (result, next) = stepped ?: parseWhole(markdown)
        synchronized(this) { if (id in tails) tails[id] = next }
        return result to (stepped == null)
    }

    private fun parseWhole(markdown: String): Pair<MarkdownResult, Tail?> {
        val result = whole(markdown)
        val root = (result as? MarkdownResult.Success)?.node ?: return result to null
        if (root.children.any(::definesLinks)) return result to null
        return result to settle(Tail(markdown, 0, emptyList(), LinkedHashMap()), markdown, root.children)
    }

    private fun step(from: Tail, markdown: String): Pair<MarkdownResult, Tail?>? {
        // A parser as parseMarkdown makes one: a fresh one each time, since steps for other ids run at once.
        val tail = MarkdownParser(GFMFlavourDescriptor(), cancellationToken = CancellationToken.NonCancellable)
            .buildMarkdownTreeFromString(markdown.substring(from.boundary), from.boundary).children
        if (tail.any(::definesLinks)) return null
        val next = settle(from, markdown, tail)
        val links = LinkedHashMap(next.links)
        for (node in tail) if (node.startOffset >= next.boundary) lookUpLinks(links, node, markdown)
        val handler = ReferenceLinkHandlerImpl().apply { links.forEach { (label, destination) -> store(label, destination) } }
        val root = CompositeASTNode(MarkdownElementTypes.MARKDOWN_FILE, from.settled + tail)
        return MarkdownResult.Success(root, markdown, true, handler) to next
    }

    /** [from] moved past the blocks in [nodes] (the top-level nodes after its boundary) that have settled. */
    private fun settle(from: Tail, markdown: String, nodes: List<ASTNode>): Tail {
        var boundary = from.boundary
        var block: ASTNode? = null
        for (node in nodes) {
            if (node.type == MarkdownTokenTypes.EOL || node.type == MarkdownTokenTypes.WHITE_SPACE) continue
            val before = block
            block = node
            if (before == null || !settles(before)) continue
            val line = markdown.lastIndexOf('\n', node.startOffset - 1) + 1
            if (line > boundary && blank(markdown, before.endOffset, line)) boundary = line
        }
        if (boundary == from.boundary) return from
        val settled = nodes.filter { it.startOffset < boundary }
        val links = LinkedHashMap(from.links)
        settled.forEach { lookUpLinks(links, it, markdown) }
        return Tail(markdown, boundary, from.settled + settled, links)
    }

    private companion object {
        /** Blocks a blank line closes for good: nothing after one can join it or reach back into it. */
        fun settles(node: ASTNode) = when (node.type) {
            MarkdownElementTypes.PARAGRAPH, MarkdownElementTypes.ATX_1, MarkdownElementTypes.ATX_2, MarkdownElementTypes.ATX_3,
            MarkdownElementTypes.ATX_4, MarkdownElementTypes.ATX_5, MarkdownElementTypes.ATX_6, MarkdownElementTypes.SETEXT_1,
            MarkdownElementTypes.SETEXT_2, MarkdownTokenTypes.HORIZONTAL_RULE -> true
            MarkdownElementTypes.CODE_FENCE -> node.children.any { it.type == MarkdownTokenTypes.CODE_FENCE_END }
            else -> false
        }

        /** Whether [from] until [to] holds only spaces, tabs and line feeds, two or more of them line feeds. */
        fun blank(markdown: String, from: Int, to: Int): Boolean {
            var feeds = 0
            for (i in from until to) when (markdown[i]) {
                '\n' -> feeds++
                ' ', '\t' -> {}
                else -> return false
            }
            return feeds >= 2
        }

        fun definesLinks(node: ASTNode): Boolean =
            node.type == MarkdownElementTypes.LINK_DEFINITION || node.children.any(::definesLinks)

        /** mikepenz's lookupLinkDefinition (0.46.0), which parseMarkdown runs over the whole tree to fill the result's
         * referenceLinkHandler. */
        fun lookUpLinks(links: MutableMap<String, String?>, node: ASTNode, content: String) {
            var linkOnly = false
            val label = when (node.type) {
                MarkdownElementTypes.LINK_DEFINITION -> node.findChildOfType(MarkdownElementTypes.LINK_LABEL)?.getUnescapedTextInNode(content)
                MarkdownElementTypes.INLINE_LINK -> node.findChildOfType(MarkdownElementTypes.LINK_TEXT)?.getUnescapedTextInNode(content)
                MarkdownElementTypes.AUTOLINK -> {
                    linkOnly = true
                    (node.children.firstOrNull { it.type.name == MarkdownElementTypes.AUTOLINK.name } ?: node).getUnescapedTextInNode(content)
                }
                else -> null
            }
            if (label != null) {
                links[label] = if (linkOnly) label else node.findChildOfType(MarkdownElementTypes.LINK_DESTINATION)?.getUnescapedTextInNode(content)
            }
            node.children.forEach { lookUpLinks(links, it, content) }
        }
    }
}
