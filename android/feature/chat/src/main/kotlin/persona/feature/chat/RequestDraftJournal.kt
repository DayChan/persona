package persona.feature.chat

import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.domain.answer.AnswerDraft
import persona.core.domain.answer.ElicitationForm
import kotlinx.serialization.json.*
import java.security.MessageDigest

/** A shared request journal, isolated by the pairing scope and the exact prompt contents. */
internal class RequestDraftJournal(private val read: (String) -> String?, private val write: (String, String?) -> Unit) {
    private fun key(scope: String, request: Interaction) = digest("$scope\u0000${request.machineId}\u0000${request.sessionId}\u0000${request.id}")
    private fun fingerprint(request: Interaction) = digest(JsonCodec.encodeToString(Interaction.serializer(),
        request.copy(expiresAt = null, sessionTitle = null, cwd = null, summary = null, mcpServerName = null, mcpConfigSource = null)))

    fun load(scope: String, request: Interaction): AnswerDraft {
        val key = key(scope, request)
        if (request.secret == true) { write(key, null); return AnswerDraft() }
        val stored = runCatching { read(key)?.let { JsonCodec.parseToJsonElement(it).jsonObject } }.getOrNull()
        if ((stored?.get("prompt") as? JsonPrimitive)?.content != fingerprint(request)) {
            if (stored != null) write(key, null)
            return AnswerDraft(fields = ElicitationForm.defaults(request.elicitation?.requestedSchema),
                selected = checkedAtComputer(request))
        }
        return runCatching {
            val value = stored.getValue("draft").jsonObject
            fun text(key: String) = (value[key] as? JsonPrimitive)?.content.orEmpty()
            AnswerDraft(feedback = text("feedback"), message = text("message"), text = text("text"),
                page = (value["page"] as? JsonPrimitive)?.intOrNull ?: 0,
                selected = (value["selected"] as? JsonArray).orEmpty().map { it.jsonPrimitive.content }.toSet()
                    .ifEmpty { checkedAtComputer(request) },
                picks = (value["picks"] as? JsonObject).orEmpty().mapValues { (_, v) -> v.jsonArray.map { it.jsonPrimitive.content }.toSet() },
                other = (value["other"] as? JsonObject).orEmpty().mapValues { it.value.jsonPrimitive.content },
                fields = value["fields"] as? JsonObject ?: JsonObject(emptyMap()))
        }.getOrDefault(AnswerDraft())
    }
    fun save(scope: String, request: Interaction, draft: AnswerDraft) {
        val safe = draft.forStorage(request)
        if (request.secret == true || safe == AnswerDraft()) { clear(scope, request); return }
        write(key(scope, request), buildJsonObject {
            put("prompt", fingerprint(request))
            put("draft", buildJsonObject {
                put("feedback", safe.feedback); put("message", safe.message); put("text", safe.text); put("page", safe.page)
                if (safe.selected.isNotEmpty()) put("selected", JsonArray(safe.selected.map(::JsonPrimitive)))
                put("picks", JsonObject(safe.picks.mapValues { JsonArray(it.value.map(::JsonPrimitive)) }))
                put("other", JsonObject(safe.other.mapValues { JsonPrimitive(it.value) }))
                put("fields", safe.fields)
            })
        }.toString())
    }
    fun clear(scope: String, request: Interaction) = write(key(scope, request), null)
}

/** What the computer already ticked; the node untickes anything not sent, so an empty start would undo them. */
private fun checkedAtComputer(request: Interaction): Set<String> =
    request.dialog?.options.orEmpty().filter { it.checked }.map { it.key }.toSet()

private fun digest(value: String) = MessageDigest.getInstance("SHA-256").digest(value.toByteArray())
    .joinToString("") { "%02x".format(it) }
