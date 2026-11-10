package persona.feature.chat

import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.Item
import kotlin.random.Random

/** markdownPrewarm as it was before it kept its split from one call to the next (0453b49bc). */
internal fun splitAfresh(items: List<Item>, limit: Int = 80): Pair<List<MarkdownText>, List<MarkdownText>> {
    val replies = items.filter { it.kind == "text" && it.role == "assistant" }.takeLast(limit)
    val lastUser = items.lastOrNull { it.role == "user" && it.kind == "text" }
    fun texts(item: Item) = messageParts(item.text.orEmpty().let { if (item.fullSize != null) cutText(it) else it }, item.from)
        .mapIndexedNotNull { index, part -> (part as? MessagePart.Text)?.let { MarkdownText("${item.id}/$index", it.text) } }
    val (latest, earlier) = replies.partition { lastUser == null || chatItemOrder.compare(it, lastUser) > 0 }
    return latest.flatMap(::texts) to earlier.asReversed().flatMap(::texts)
}

class MarkdownPrewarmTest {
    private val texts = listOf("## Done", "", "   ", "a\n\nb", "before <cross-session-message from-name=\"x\">theirs</cross-session-message> after",
        "<cross-session-message from-name='y'>only theirs</cross-session-message>", "- one\n- two\n…（已截断，共 9000 字节）")

    private fun item(r: Random, n: Int) = Item(
        id = "i$n#${r.nextInt(3)}", seq = r.nextInt(0, 40),
        role = listOf("assistant", "assistant", "user", "system").random(r),
        kind = listOf("text", "text", "text", "tool_use", "thinking").random(r),
        text = texts.random(r).takeIf { r.nextInt(8) > 0 }, fullSize = if (r.nextInt(5) == 0) 9000 else null,
        from = if (r.nextInt(6) == 0) "other" else null,
    )

    @Test fun keptSplitsMatchSplittingAfreshThroughStreamingAndOtherChanges() {
        for (seed in 1..60) {
            val r = Random(seed)
            val limit = if (seed % 3 == 0) r.nextInt(0, 4) else 80
            val prewarm = MarkdownPrewarm(limit)
            var next = 0
            var items = List(r.nextInt(0, 12)) { item(r, next++) }
            repeat(80) { step ->
                items = when (r.nextInt(10)) {
                    // The last item streams on: mostly what happens.
                    0, 1, 2, 3 -> items.dropLast(1) + listOfNotNull(items.lastOrNull()?.let { it.copy(text = it.text.orEmpty() + texts.random(r)) })
                    4 -> items + item(r, next++)
                    5 -> if (items.isEmpty()) items else items.toMutableList().apply { removeAt(r.nextInt(size)) }
                    6 -> items.shuffled(r)
                    // The last item becomes another row under the same id, or someone else's.
                    7 -> items.dropLast(1) + listOfNotNull(items.lastOrNull()?.let {
                        it.copy(role = listOf("user", "assistant").random(r), kind = "text", seq = it.seq + r.nextInt(2))
                    })
                    8 -> items.mapIndexed { i, it -> if (i == r.nextInt(items.size)) it.copy(text = "changed") else it }
                    else -> items.map { it.copy() }
                }
                assertEquals("seed $seed, step $step", splitAfresh(items, limit), prewarm.split(items))
            }
        }
    }
}
