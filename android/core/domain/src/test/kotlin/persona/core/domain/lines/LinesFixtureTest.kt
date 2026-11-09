package persona.core.domain.lines

import persona.core.domain.zh
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.domain.FixtureReader
import java.io.File
import java.nio.file.Files
import java.time.Instant
import kotlinx.serialization.json.*
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized

@RunWith(Parameterized::class)
class LinesFixtureTest(private val name: String, private val fixture: FixtureReader.Case) {
    @Test fun replay() {
        val input = fixture.value.getValue("input").jsonObject
        val expected = fixture.value.getValue("expected")
        val operations = input["operations"]?.jsonArray
        val initial = input.items()
        val actual = if (operations == null) snapshot(initial, input) else {
            val store = LineStore()
            store.merge(initial)
            val checkpoints = operations.map { raw ->
                val op = raw.jsonObject
                when (op.text("op")) {
                    "merge" -> store.merge(op.items())
                    "replace" -> store.replace(op.items())
                    "unfold" -> store.unfold(op.text("fold_id")!!, op.items())
                    "burst_replace" -> {
                        op.getValue("batches").jsonArray.forEach { batch ->
                            store.merge(batch.jsonArray.map { decodeItem(it.jsonObject) })
                            assertEquals(chatLines(store.items), store.snapshot())
                        }
                        store.replace(op.items())
                    }
                    else -> error("Unknown operation: $op")
                }
                val options = JsonObject(input + op.filterKeys { it in setOf("open_work", "live_turn_id") })
                JsonObject(snapshot(store.items, options) + ("item_ids" to json(store.items.map { it.id })))
            }
            obj("checkpoints" to checkpoints)
        }
        compareJson("${fixture.file}/$name", expected, actual)
    }

    private fun snapshot(items: List<LineItem>, input: JsonObject): JsonObject {
        val frozen = (input["frozen"] as? JsonObject)?.let {
            Frozen(it.number("seq")!!, it.number("part")!!, it.text("live_turn_id"))
        }
        val options = LineOptions(input.text("live_turn_id"), input.strings("open_work").toSet(), frozen,
            input["pending_interaction_ids"]?.let { input.strings("pending_interaction_ids") })
        if (input.flag("check_prefixes")) {
            val store = LineStore()
            assertEquals(chatLines(emptyList(), options), store.snapshot(options))
            items.forEachIndexed { i, item ->
                store.merge(listOf(item))
                assertEquals("$name prefix ${i + 1}", chatLines(items.take(i + 1), options), store.snapshot(options))
            }
        }
        val result = chatLines(items, options)
        // The items cache finds its last two turns with chatTurns: the same turns, every fixture.
        if (frozen == null) assertEquals("$name chatTurns", result.turns, chatTurns(items))
        val projection = Projection(result.tasks, input.stringsOrNulls("menu_queries"),
            Instant.parse(input.text("now") ?: "2026-01-01T00:01:00Z"))
        fun checkIds(lines: List<ChatLine>) {
            assertEquals("$name unique line IDs", lines.size, lines.map { it.id }.distinct().size)
            lines.filterIsInstance<ChatLine.Wakes>().forEach { checkIds(it.drawer) }
        }
        checkIds(result.lines)
        return buildJsonObject {
            put("lines", json(result.lines.map(projection::line)))
            put("turns", json(result.turns.map(projection::turn)))
            val source = if (frozen == null) items else items.filter {
                positionCompare(it.seq, it.part, frozen.seq, frozen.part) <= 0
            }
            if (source.any { it.task != null }) put("task_index", projection.taskIndex())
            result.footer?.let {
                put("footer", obj("interaction_ids" to it.interactionIds, "placement" to it.placement,
                    "part_of_transcript" to it.partOfTranscript))
            }
        }
    }

    companion object {
        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> = readLineFixtures().map {
            arrayOf(it.value.getValue("name").jsonPrimitive.content, it)
        }
    }
}

/** Reuse A0's reader without changing its version-only schema or any shared fixture files. */
private fun readLineFixtures(): List<FixtureReader.Case> {
    val root = System.getProperty("persona.repo.root")?.let(::File) ?: generateSequence(File(".").canonicalFile) { it.parentFile }
        .first { File(it, "spec/fixtures/lines").isDirectory }
    val source = File(root, "spec/fixtures/lines")
    check(source.isDirectory) { "Missing lines fixture directory" }
    val files = source.listFiles { file -> file.isFile && file.extension == "json" }!!.sortedBy { it.name }
    check(files.isNotEmpty()) { "Missing lines JSON fixtures" }
    val temp = Files.createTempDirectory("lines-fixtures-").toFile()
    val oldRoot = System.getProperty("persona.repo.root")
    try {
        val target = File(temp, "spec/fixtures/lines").also { it.mkdirs() }
        for (file in files) {
            val value = JsonCodec.parseToJsonElement(file.readText()).jsonObject
            check(value.getValue("schema_version").jsonPrimitive.int == 1)
            check(value.text("domain") == "lines")
            File(target, file.name).writeText(JsonObject(value + ("version" to JsonPrimitive(1))).toString())
        }
        System.setProperty("persona.repo.root", temp.absolutePath)
        return FixtureReader.read("lines").also { check(it.isNotEmpty()) }
    } finally {
        if (oldRoot == null) System.clearProperty("persona.repo.root") else System.setProperty("persona.repo.root", oldRoot)
        temp.deleteRecursively()
    }
}

private class Projection(val tasks: TaskIndex, val menu: List<String?>, val now: Instant) {
    private fun note(item: LineItem): JsonObject = tasks.note(item).let {
        obj("label" to it.label, "subject" to it.subject, "outcome" to it.outcome,
            "failed" to it.failed, "call_id" to it.callId)
    }

    private fun row(row: ChatRow): JsonObject {
        val item = row.item
        val type = if (row.type == "notice" && item?.task != null && item.item.wake != true) "task_note" else row.type
        return buildJsonObject {
            put("id", row.id)
            put("type", type)
            if (item != null) put("text", json(item.text))
            if (type == "task_note") put("task_note", note(item!!))
        }
    }

    private fun step(step: WorkItem): JsonObject = when (step) {
        is WorkItem.Narration -> obj("type" to "narration", "id" to step.id, "item_id" to step.item.id, "text" to step.item.text)
        is WorkItem.Tool -> step.call.let { call ->
            val base = obj("type" to "tool", "id" to call.id, "call_id" to call.use.item.toolUseId,
                "name" to call.name, "result_id" to call.result?.id, "result_text" to call.result?.text,
                "running" to call.running, "failed" to call.failed, "image_count" to call.images.size)
            tasks.badge(call.use.item.toolUseId)?.let {
                JsonObject(base + ("background_badge" to obj("text" to it.text, "failed" to it.failed)))
            } ?: base
        }
        is WorkItem.Folded -> step.item.fold.let {
            obj("type" to "fold", "id" to step.id, "tools" to (it?.tools ?: 0), "thoughts" to (it?.thoughts ?: 0),
                "items" to (it?.items ?: 0), "through_seq" to it?.throughSeq, "through_id" to it?.throughId, "until" to it?.until)
        }
        is WorkItem.Thought -> row(ChatRow(if (step.item.kind == "notice") "notice" else "thinking", step.item))
    }

    fun line(line: ChatLine): JsonObject = buildJsonObject {
        put("id", line.id)
        put("is_input", line.isInput)
        if (menu.isNotEmpty()) put("holds", json(menu.map(line::holds)))
        val content = when (line) {
            is ChatLine.Row -> row(line.row)
            is ChatLine.UserImages -> obj("type" to "user_images", "item_ids" to line.images.map { it.id })
            is ChatLine.Work -> obj("type" to "work", "live" to line.live, "open" to line.open,
                "start" to line.start, "end" to line.end, "elapsed_seconds" to line.elapsedSeconds(now),
                "duration_text" to line.durationText(now)?.zh().orEmpty(), "tools" to line.tools, "thoughts" to line.thoughts,
                "known_failed_tools" to line.knownFailedTools, "unresolved_tools" to line.unresolvedTools,
                "contains_fold" to line.containsFold, "summary_full" to line.summary.full,
                "summary_short" to line.summary.short, "steps" to line.steps.map(::step))
            is ChatLine.Step -> obj("type" to "work_step", "group_id" to line.groupId,
                "first" to line.first, "separator_before" to line.separatorBefore, "step" to step(line.step))
            is ChatLine.End -> obj("type" to "work_end", "group_id" to line.groupId,
                "count" to line.count, "separator_before" to line.separatorBefore)
            is ChatLine.Wakes -> obj("type" to "wakes", "notice_ids" to line.turn.wakes.map { it.id },
                "live" to line.live, "drawer_lines" to line.drawer.map(::line))
            is ChatLine.ApiError -> obj("type" to "api_error", "item_id" to line.item.id, "count" to line.count,
                "status" to line.info.status, "reason" to line.info.reason)
            is ChatLine.Recaps -> obj("type" to "recaps", "item_ids" to line.items.map { it.id })
            is ChatLine.Extra -> obj("type" to "extra", "id" to line.id)
        }
        content.forEach { (key, value) -> put(key, value) }
    }

    fun turn(turn: ChatTurn): JsonObject = obj("id" to turn.id,
        "user_ids" to turn.users.map { it.id }, "process_ids" to turn.process.map { it.id },
        "answer_id" to turn.answer?.id, "trailing_ids" to turn.trailing.map { it.id },
        "wake_ids" to turn.wakes.map { it.id }, "after_wake_ids" to turn.afterWake.map { it.id },
        "tail_ids" to turn.tail.map { it.id }, "ordered_ids" to turn.ordered.map { it.id },
        "tool_count" to turn.toolCount, "thought_count" to turn.thoughtCount, "interim_count" to turn.interimCount,
        "start" to turn.start, "end" to turn.end)

    fun taskIndex(): JsonObject = obj("started" to tasks.started.sorted(), "calls" to tasks.calls,
        "descriptions" to tasks.descriptions, "started_at" to tasks.startedAt,
        "statuses" to tasks.notes.mapValues { (_, notes) -> notes.map { it.status } })
}

private fun decodeItem(raw: JsonObject): LineItem = LineItem(JsonCodec.decodeFromJsonElement<Item>(raw),
    (raw["fold"] as? JsonObject)?.let {
        Fold(it.number("items")!!, it.number("tools")!!, it.number("thoughts")!!,
            it.number("through_seq")!!, it.text("through_id")!!, it.text("until")?.let(Instant::parse))
    },
    (raw["task"] as? JsonObject)?.let {
        TaskNote(it.text("kind")!!, it.text("title")!!, it.text("status"), it.text("tool_use_id"),
            it.text("task_id"), it.text("detail"), it.number("exit_code"))
    })

private fun JsonObject.text(key: String): String? = (get(key) as? JsonPrimitive)?.contentOrNull
private fun JsonObject.number(key: String): Int? = (get(key) as? JsonPrimitive)?.intOrNull
private fun JsonObject.flag(key: String): Boolean = (get(key) as? JsonPrimitive)?.booleanOrNull == true
private fun JsonObject.stringsOrNulls(key: String): List<String?> = get(key)?.jsonArray?.map { it.jsonPrimitive.contentOrNull }.orEmpty()
private fun JsonObject.strings(key: String): List<String> = stringsOrNulls(key).map { requireNotNull(it) }
private fun JsonObject.items(): List<LineItem> = get("items")?.jsonArray?.map { decodeItem(it.jsonObject) }.orEmpty()
private fun obj(vararg fields: Pair<String, Any?>): JsonObject = JsonObject(fields.associate { it.first to json(it.second) })
private fun json(value: Any?): JsonElement = when (value) {
    null -> JsonNull
    is JsonElement -> value
    is persona.core.contracts.CoreText -> JsonPrimitive(value.zh())
    is String -> JsonPrimitive(value)
    is Boolean -> JsonPrimitive(value)
    is Number -> JsonPrimitive(value)
    is Instant -> JsonPrimitive(value.toString())
    is List<*> -> JsonArray(value.map(::json))
    is Map<*, *> -> JsonObject(value.entries.associate { it.key.toString() to json(it.value) })
    else -> error("Unsupported projection value: ${value.javaClass}")
}

private fun compareJson(path: String, expected: JsonElement, actual: JsonElement) {
    when (expected) {
        is JsonObject -> {
            assertTrue("$path object", actual is JsonObject)
            actual as JsonObject
            assertEquals("$path keys", expected.keys, actual.keys)
            expected.forEach { (key, value) -> compareJson("$path.$key", value, actual.getValue(key)) }
        }
        is JsonArray -> {
            assertTrue("$path array", actual is JsonArray)
            actual as JsonArray
            assertEquals("$path size", expected.size, actual.size)
            expected.forEachIndexed { i, value -> compareJson("$path[$i]", value, actual[i]) }
        }
        else -> assertEquals(path, expected, actual)
    }
}
