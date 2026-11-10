package persona.feature.inbox

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Test
import java.io.File

/** Hand to AI remembers, per source, where an item was last handed (#2121): spec/fixtures/inbox/handoff_memory.json. */
class InboxHandOffMemoryTest {
    private val fixture: JsonObject by lazy {
        val root = System.getProperty("persona.repo.root")?.let(::File) ?: generateSequence(File(".").canonicalFile) { it.parentFile }
            .first { File(it, ".git").exists() && File(it, "android").isDirectory }
        Json.parseToJsonElement(File(root, "spec/fixtures/inbox/handoff_memory.json").readText()).jsonObject
    }

    private fun JsonObject.text(name: String): String? = this[name]?.takeIf { it !is JsonNull }?.jsonPrimitive?.content

    @Test fun sourceKeysFollowTheSharedCases() {
        assertEquals(1, fixture["schema_version"]!!.jsonPrimitive.int)
        assertEquals(InboxHandOffMemory.LIMIT, fixture["limit"]!!.jsonPrimitive.int)
        for (case in fixture["keys"]!!.jsonArray.map { it.jsonObject }) {
            val mutes = (case["mutes"] as? JsonArray)?.map { m ->
                val o = m.jsonObject
                InboxMuteOption(o.text("tag")!!, o.text("value")!!, o.text("name")!!)
            }
            assertEquals(case.text("name"), case.text("key"), InboxHandOffMemory.key(case.text("source")!!, mutes))
        }
    }

    @Test fun resolvingFollowsTheSharedCases() {
        for (case in fixture["resolve"]!!.jsonArray.map { it.jsonObject }) {
            if (case["needs_new_chat"]?.jsonPrimitive?.boolean == true) continue // no New Chat target on Android
            val listed = case["listed"]!!.jsonArray.map { it.jsonPrimitive.content }
            val r = InboxHandOffMemory.resolve(case.text("remembered")) { it in listed }
            val name = case.text("name")
            assertEquals(name, case.text("target"), r?.target)
            assertEquals(name, case["last_time"]!!.jsonPrimitive.boolean, r?.lastTime ?: false)
            assertEquals(name, case["gone"]!!.jsonPrimitive.boolean, r?.gone ?: false)
        }
    }

    @Test fun oneEntryPerSourceLatestFirst() {
        val recency = fixture["recency"]!!.jsonObject
        var entries = emptyList<HandOffMemoryEntry>()
        for (pair in recency["remember"]!!.jsonArray.map { it.jsonArray }) {
            entries = InboxHandOffMemory.remember(entries, pair[0].jsonPrimitive.content, pair[1].jsonPrimitive.content)
        }
        assertEquals(recency["order"]!!.jsonArray.map { it.jsonPrimitive.content }, entries.map { it.source })
        for ((key, target) in recency["recall"]!!.jsonObject) {
            assertEquals(key, target.takeIf { it !is JsonNull }?.jsonPrimitive?.content, InboxHandOffMemory.recall(entries, key))
        }
    }

    @Test fun keepsTheMostRecentSources() {
        var entries = emptyList<HandOffMemoryEntry>()
        repeat(InboxHandOffMemory.LIMIT + 5) { entries = InboxHandOffMemory.remember(entries, "github|repo:a/$it", "main") }
        assertEquals(InboxHandOffMemory.LIMIT, entries.size)
        assertEquals(null, InboxHandOffMemory.recall(entries, "github|repo:a/0"))
        assertEquals("main", InboxHandOffMemory.recall(entries, "github|repo:a/${InboxHandOffMemory.LIMIT + 4}"))
    }
}
