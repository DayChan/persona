package persona.feature.chat

import com.mikepenz.markdown.model.ReferenceLinkHandlerImpl
import com.mikepenz.markdown.model.parseMarkdown
import org.intellij.markdown.ast.ASTNode
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import kotlin.random.Random
import com.mikepenz.markdown.model.State as MarkdownResult

/**
 * The gate for parsing a streaming reply from its last settled block (ChatMarkdownStream): at every point of every
 * sample, what the stream builds equals a whole parse of the same text, node for node (type and offsets), with the same
 * content and links.
 */
class ChatMarkdownStreamTest {
    @Test fun everyPrefixOfEverySampleParsesAsAWholeParse() {
        for ((name, text) in corpus) {
            val stream = ChatMarkdownStream { parseMarkdown(it) }
            stream.start("s")
            var whole = 0
            for (end in 0..text.length) {
                val prefix = text.substring(0, end)
                val (parsed, parsedWhole) = stream.parse("s", prefix)
                if (parsedWhole) whole++
                assertParsedAlike(parseMarkdown(prefix), parsed, "$name, the first $end characters")
            }
            // The generated replies without a definition went on from a settled block nearly every step.
            if (name.startsWith("sample") && !text.contains("[r1]:")) assertTrue("$name: $whole whole parses", whole * 20 < text.length)
        }
    }

    @Test fun aStreamCutAnywhereGoesOnToTheWholeText() {
        for ((name, text) in corpus) {
            val whole = parseMarkdown(text)
            for (end in 0..text.length) {
                val stream = ChatMarkdownStream { parseMarkdown(it) }
                stream.start("s")
                stream.parse("s", text.substring(0, end))
                assertParsedAlike(whole, stream.parse("s", text).first, "$name after the first $end characters")
            }
        }
    }

    @Test fun aLongReplyParsesOnlyItsTailWhileItStreams() {
        var whole = 0
        val stream = ChatMarkdownStream { whole++; parseMarkdown(it) }
        stream.start("s")
        val text = reply(16_000)
        for (end in 40..text.length step 40) stream.parse("s", text.substring(0, end))
        assertEquals(1, whole) // the first step, which has nothing to go on from
    }

    @Test fun anEditOrALinkDefinitionParsesTheWholeText() {
        var whole = 0
        val stream = ChatMarkdownStream { whole++; parseMarkdown(it) }
        stream.start("s")
        val text = reply(4_000).substringBeforeLast("## Section") // ends between two blocks
        stream.parse("s", text)
        stream.parse("s", text + "more")
        assertEquals(1, whole)
        // The text before the settled blocks changed: a resend, or a formula that closed.
        stream.parse("s", "## Changed" + text.substring(10) + "more")
        assertEquals(2, whole)
        // A definition resolves links before it, so a text with one is parsed whole at each step.
        val defined = text + "[r]: https://example.com/r\n\n"
        stream.parse("s", defined)
        stream.parse("s", defined + "[a link][r]")
        assertEquals(4, whole)
        assertParsedAlike(parseMarkdown(defined + "[a link][r]"), stream.parse("s", defined + "[a link][r]").first, "a definition")
    }

    @Test fun anEndedStreamKeepsNoState() {
        val stream = ChatMarkdownStream { parseMarkdown(it) }
        stream.start("s")
        assertTrue(stream.streaming("s"))
        stream.end("s")
        assertEquals(false, stream.streaming("s"))
        repeat(20) { stream.start("s$it") }
        assertEquals(false, stream.streaming("s0")) // the eight most recent streams are kept
        assertTrue(stream.streaming("s19"))
    }

    companion object {
        private fun dump(node: ASTNode, out: StringBuilder = StringBuilder(), depth: Int = 0): String {
            out.append(" ".repeat(depth)).append(node.type.name).append(' ').append(node.startOffset).append(' ').append(node.endOffset).append('\n')
            node.children.forEach { dump(it, out, depth + 1) }
            return out.toString()
        }

        private fun same(a: ASTNode, b: ASTNode): Boolean = a.type == b.type && a.startOffset == b.startOffset &&
            a.endOffset == b.endOffset && a.children.size == b.children.size && a.children.indices.all { same(a.children[it], b.children[it]) }

        @Suppress("UNCHECKED_CAST")
        private fun links(result: MarkdownResult.Success): Map<String, String?> =
            ReferenceLinkHandlerImpl::class.java.getDeclaredField("stored").apply { isAccessible = true }.get(result.referenceLinkHandler) as Map<String, String?>

        fun assertParsedAlike(expected: MarkdownResult, actual: MarkdownResult, where: String) {
            if (expected !is MarkdownResult.Success || actual !is MarkdownResult.Success) {
                assertEquals(where, expected::class, actual::class)
                return
            }
            if (!same(expected.node, actual.node)) assertEquals(where, dump(expected.node), dump(actual.node))
            assertEquals(where, expected.content, actual.content)
            assertEquals(where, expected.linksLookedUp, actual.linksLookedUp)
            assertEquals(where, links(expected), links(actual))
            if (expected.referenceLinkHandler.javaClass != actual.referenceLinkHandler.javaClass) fail(where)
        }

        /** The bench's reply (ChatMarkdownBench): headings, a paragraph with links and code, a list and a fence, again. */
        fun reply(size: Int) = buildString {
            while (length < size) append("## Section\n\nSome **bold** words, `code`, a [link](https://example.com/x) and more text here.\n\n- item one\n- item two\n\n```kotlin\nval x = 1\nprintln(x)\n```\n\n")
        }.take(size)

        private val words = listOf("alpha", "beta", "gamma", "**bold**", "*em*", "_em_", "`code`", "``a ` b``", "[link](https://example.com/a)",
            "<https://example.com/b>", "https://example.com/c?q=1", "www.example.com", "me@example.com", "中文的字", "snake_case",
            "~~gone~~", "\$x^2\$", "\\*", "&amp;", "<b>tag</b>", "[ref][r1]", "[short]", "![img](https://example.com/i.png)", "1.", "-", "#",
            "|", "*", "**open", "`open", "[open", "](x)", "<!--", "-->")

        /** A reply of about [size] characters from block kinds a reply has, at random but the same each run. */
        fun sample(seed: Int, size: Int, definitions: Boolean): String {
            val r = Random(seed)
            fun line() = (1..r.nextInt(2, 12)).joinToString(" ") { words.random(r) }
            fun lines(n: Int) = (1..n).joinToString("\n") { line() }
            val blocks = mutableListOf<() -> String>(
                { "#".repeat(r.nextInt(1, 7)) + " " + line() },
                { line() + "\n" + "=".repeat(r.nextInt(1, 6)) },
                { line() + "\n---" },
                { lines(r.nextInt(1, 4)) },
                { lines(r.nextInt(1, 3)) + "  \nhard break\\\nline" },
                { "```" + listOf("", "kotlin", "go", "json").random(r) + "\n" + lines(r.nextInt(1, 4)) + "\n```" },
                { "~~~\n" + lines(2) + "\n~~~" },
                { "````md\n```\ninner\n```\n````" },
                { "- " + line() + "\n  - " + line() + "\n    1. " + line() + "\n- " + line() + "\n\n  " + line() },
                { "1. " + line() + "\n2. " + line() + "\n   ```\n   code\n   ```\n3. " + line() },
                { "* " + line() + "\n\n* " + line() },
                { "| a | b |\n| - | :-: |\n| " + words.random(r) + " | 2 |\n| x | " + words.random(r) + " |" },
                { "$$\n\\frac{a}{b}\n$$" },
                { "> " + line() + "\n> " + line() + "\n>\n> - " + line() },
                { "> [!NOTE]\n> " + line() },
                { "<div>\n<p>" + line() + "</p>\n</div>" },
                { "<!-- a comment\n\nacross a blank line -->" },
                { "<details><summary>more</summary>\n\n" + line() + "\n\n</details>" },
                { "    indented code\n    " + line() },
                { listOf("***", "- - -", "___").random(r) },
                { "<pre>\n" + line() + "\n\n" + line() + "\n</pre>" },
            )
            if (definitions) blocks += { "[r1]: https://example.com/r1 \"title\"" }
            val gaps = listOf("\n\n", "\n\n", "\n\n", "\n", "\n\n\n", "\n  \n", "\n\t\n")
            return buildString {
                while (length < size) append(blocks.random(r)()).append(gaps.random(r))
            }
        }

        /** Markdown from the chat's tests (PathLinks, ChatLinks, ReceiptMarkdown, MathMarkdown), then generated replies. */
        val corpus: List<Pair<String, String>> = listOf(
            "path links" to "改了 `src/a.ts:12`，见 src/a.ts:40 和 ~/notes/ 和 /tmp/other/x.md。\n\n```\ncat src/a.ts\n```\n\n" +
                "[链接](https://example.com/src/a.ts) <https://example.com/src/a.ts>",
            "path links, linked" to "改了 [`src/a.ts:12`](persona-path:src%2Fa.ts#L12)，见 [src/a.ts:40](persona-path:src%2Fa.ts#L40) 和 " +
                "[~/notes/](persona-path:%7E%2Fnotes%2F) 和 /tmp/other/x.md。\n\n```\ncat src/a.ts\n```\n\n" +
                "[链接](https://example.com/src/a.ts) <https://example.com/src/a.ts>",
            "indented code" to "说明\n\n    cat src/a.ts\n",
            "chat links" to "[网页](https://example.com/a) `https://example.com/code`\n```\nhttps://example.com/fence\n```\n<https://example.com/plain>。",
            "receipt" to "## 根因\nhub 和 node 不同步\n\n**网页的改动**\n- **底部的「收起」**：改成 `Collapse`\n  - 调 `POST /api/v1/sessions/{mid}/{sid}/interrupt`\n1. 第二条",
            "receipt quote" to "> 见 [PR](https://github.com/o/r/pull/7) 和 <https://example.com>，~~旧的~~ *新的*\n```go\nx := 1\n```\n---\n| a | b |\n| - | - |\n| 1 | 2 |",
            "math" to "前\n```math\na^2 + b^2 = c^2\n```\n后\n\n面积是 ![\\pi r^2](persona-math:%5Cpi%20r%5E2)，周长。\n\n用 `\$x\$` 写公式\n```\n$$\nnot math\n$$\n```\n    \$y$ 缩进的代码",
            "reply" to reply(2_400),
        ) + (1..4).map { "sample $it" to sample(it, 2_000 + 500 * it, definitions = false) } +
            (5..6).map { "sample $it" to sample(it, 2_000, definitions = true) }
    }
}
