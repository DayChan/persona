package persona.core.contracts.api

import persona.core.contracts.GroupKeys
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.doubleOrNull
import kotlinx.serialization.json.longOrNull
import kotlin.math.abs

/**
 * Settings › Sent as you (#5278, docs/extensions/connectors.md "Sent as the user"): the ledger of what Persona wrote in the user's name
 * through the connectors, newest first, as the selected hub keeps it. Full-access devices only. Bound to the active pairing's
 * selected hub.
 */
interface HubSent {
    /**
     * `GET /api/v1/connectors/sent?before=&limit=&connector=`: [before] is a row id (what [SentPage.next] gave), [limit]
     * defaults to 50 on the hub (at most 200), [connector] keeps one connector's rows. A value that is null, empty or not
     * above zero is not asked for. The hub's refusals come as failures that carry their status and code: 503 `no_database`
     * (no ledger), 403 (a device without full access), 404 with no code (a hub from before the ledger).
     */
    suspend fun page(before: Long? = null, limit: Int? = null, connector: String? = null): SentPage

    /**
     * `POST /api/v1/connectors/sent/{id}/recall` with `{"confirm": true}`: takes back the message row [id] made, once the user
     * agreed in the app's own dialog (the hub shows no card). The row as the ledger has it afterwards, null when the answer
     * has none. A refusal comes as a failure with its status and code (`too_old`, `already_recalled`, `recall_unknown`, …).
     */
    suspend fun recall(id: Long): SentItem?
}

/** The `connector_gate` rule that allowed a send (`let_through.kind` "rule"). */
data class SentRule(
    val connector: String = "",
    /** The tool's name, or for Lark "<METHOD> <path>"; "*" for any. */
    val action: String = "",
    /** The body fields the rule matched. */
    val match: Map<String, JsonElement> = emptyMap(),
    val decision: String = "",
)

/** How the gate let a send through: a rule, "conversation_always", a card or "auto" (a kind the page does not know is shown as it is). */
data class SentLetThrough(
    val kind: String = "",
    val rule: SentRule? = null,
    /** card: the card the user was asked on. */
    val interactionId: String = "",
    /** card: empty when the user answered, "assistant" when the assistant answered it for them, else who did. */
    val answeredBy: String = "",
    /** auto: the extension setting that let it send without asking. */
    val setting: String = "",
)

/** The conversation that asked: both ids when the request named one; [extensionId] for a send an extension recorded. */
data class SentOrigin(
    val machineId: String = "",
    val sessionId: String = "",
    val extensionId: String = "",
)

/**
 * One row of the ledger (db.SentAsUser). Read as leniently as the web page reads it: a field of the wrong type is its empty
 * value, and a row with no number for an id (which paging and the de-duplication need) is no row. Times are RFC 3339.
 */
data class SentItem(
    val id: Long,
    val at: String = "",
    val connector: String = "",
    val action: String = "",
    /** Where it went; the fields depend on the connector (docs/extensions/connectors.md "Sent as the user"). */
    val target: Map<String, JsonElement> = emptyMap(),
    /** What it said, at most [SentWire.TextMost] characters. */
    val text: String = "",
    /** One line: the card's first line, or the action and where it went. */
    val summary: String = "",
    val origin: SentOrigin = SentOrigin(),
    val letThrough: SentLetThrough = SentLetThrough(),
    val messageRef: String = "",
    val recalledAt: String = "",
    /** The hub can take the message back now; false from a hub from before recall. */
    val canRecall: Boolean = false,
)

/** A page of rows; [next] is the `before` of the page after it, null on the last. */
data class SentPage(val items: List<SentItem> = emptyList(), val next: Long? = null) {
    /**
     * The page with its rows' machine ids as the merged lists show them (GroupKeys): the hub speaks its own ids, and a chat
     * a row opens is found by the group's suffixed one. Unchanged for the primary group ([namespace] null).
     */
    fun inGroup(namespace: String?): SentPage =
        if (namespace == null) this
        else copy(items = items.map { it.copy(origin = it.origin.copy(machineId = GroupKeys.machine(it.origin.machineId, namespace))) })
}

/** Reads the hub's JSON for the ledger the way the web page does: what is not as expected is empty or skipped, never an error. */
object SentWire {
    /** The most characters of a send's text the ledger keeps (connector.SentTextMost): a text of exactly this was cut. */
    const val TextMost = 4000

    /** A page; JSON that is not an object is an empty page, an element of `items` that is not a row is skipped. */
    fun page(json: JsonElement): SentPage {
        val root = json as? JsonObject ?: return SentPage()
        val items = (root["items"] as? JsonArray).orEmpty().mapNotNull(::item)
        return SentPage(items, root["next"].wholeNumber())
    }

    /** A row, or null when it has no number for an id. */
    fun item(json: JsonElement): SentItem? {
        val row = json as? JsonObject ?: return null
        val id = row["id"].wholeNumber() ?: return null
        return SentItem(
            id = id,
            at = row.text("at"),
            connector = row.text("connector"),
            action = row.text("action"),
            target = row.objectAt("target"),
            text = row.text("text"),
            summary = row.text("summary"),
            origin = (row["origin"] as? JsonObject)?.let { SentOrigin(it.text("machine_id"), it.text("session_id"), it.text("extension")) } ?: SentOrigin(),
            letThrough = (row["let_through"] as? JsonObject)?.let(::letThrough) ?: SentLetThrough(),
            messageRef = row.text("message_ref"),
            recalledAt = row.text("recalled_at"),
            canRecall = (row["can_recall"] as? JsonPrimitive)?.takeIf { !it.isString }?.content == "true",
        )
    }

    private fun letThrough(json: JsonObject) = SentLetThrough(
        kind = json.text("kind"),
        rule = (json["rule"] as? JsonObject)?.let { SentRule(it.text("connector"), it.text("action"), it.objectAt("match"), it.text("decision")) },
        interactionId = json.text("interaction_id"),
        answeredBy = json.text("answered_by"),
        setting = json.text("setting"),
    )

    /** A string field: empty when it is missing or not a string. */
    private fun JsonObject.text(key: String): String = (this[key] as? JsonPrimitive)?.takeIf { it.isString }?.content.orEmpty()

    private fun JsonObject.objectAt(key: String): Map<String, JsonElement> = (this[key] as? JsonObject).orEmpty()

    /** A whole number (12 and 12.0 are the same row): null for anything else, a string of digits too. */
    private fun JsonElement?.wholeNumber(): Long? {
        val number = this as? JsonPrimitive ?: return null
        if (number.isString) return null
        number.longOrNull?.let { return it }
        val double = number.doubleOrNull ?: return null
        return if (double == Math.rint(double) && abs(double) < 1e15) double.toLong() else null
    }
}
