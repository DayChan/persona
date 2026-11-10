package persona.core.hub

import persona.core.contracts.JsonCodec
import persona.core.contracts.api.HubSent
import persona.core.contracts.api.SentItem
import persona.core.contracts.api.SentPage
import persona.core.contracts.api.SentWire
import kotlinx.serialization.SerializationException
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import okhttp3.Response

/** Settings › Sent as you over the pairing's connection (docs/extensions/connectors.md "Sent as the user", `GET /api/v1/connectors/sent`, answered by the hub's manager). */
class HttpHubSent internal constructor(
    private val request: suspend (path: String, method: String, body: RequestBody?) -> Response,
) : HubSent {
    // A recall (the only POST) may wait for one already on its way, about 40 seconds at most: the hub asks for 60 or more.
    constructor(connection: OkHttpHubConnection) : this({ path, method, body ->
        connection.request(path, method, body, readTimeoutSeconds = if (method == "POST") RecallSeconds else null)
    })

    override suspend fun page(before: Long?, limit: Int?, connector: String?): SentPage =
        SentWire.page(call(sentPath(before, limit, connector), "GET", null))

    override suspend fun recall(id: Long): SentItem? =
        (call("$Path/$id/recall", "POST", ConfirmBody.toRequestBody(JsonType)) as? JsonObject)?.get("item")?.let(SentWire::item)

    private suspend fun call(path: String, method: String, body: RequestBody?): JsonElement {
        val text = request(path, method, body).use { response ->
            // The pairing's connection throws a HubFailure for a status that is not 2xx; a bare request returns it.
            if (!response.isSuccessful) throw HubFailure.from(response)
            response.body?.string().orEmpty()
        }
        // Not JSON at all is the hub's protocol gone wrong; JSON that is not an object is an empty page (SentWire.page).
        return try { JsonCodec.parseToJsonElement(text) }
        catch (error: SerializationException) { throw HubFailure(HubError.Protocol, cause = error) }
        catch (error: IllegalArgumentException) { throw HubFailure(HubError.Protocol, cause = error) }
    }

    private companion object {
        const val Path = "/api/v1/connectors/sent"
        const val RecallSeconds = 70L
        const val ConfirmBody = """{"confirm":true}"""
        val JsonType = "application/json".toMediaType()

        /** The path with the query: `before`, `limit` and `connector` only when they say something. */
        fun sentPath(before: Long?, limit: Int?, connector: String?): String {
            val url = "https://example.com$Path".toHttpUrl().newBuilder().apply {
                if (before != null && before > 0) addQueryParameter("before", before.toString())
                if (limit != null && limit > 0) addQueryParameter("limit", limit.toString())
                if (!connector.isNullOrEmpty()) addQueryParameter("connector", connector)
            }.build()
            return url.encodedPath + url.encodedQuery?.let { "?$it" }.orEmpty()
        }
    }
}
