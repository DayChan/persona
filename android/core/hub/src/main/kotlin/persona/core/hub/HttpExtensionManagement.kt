package persona.core.hub


import persona.core.contracts.JsonCodec
import persona.core.contracts.api.HubExtension
import persona.core.contracts.api.ExtensionCatalog
import persona.core.contracts.api.ExtensionConfigRejected
import persona.core.contracts.api.ExtensionDependent
import persona.core.contracts.api.ExtensionHasDependents
import persona.core.contracts.api.ExtensionDetail
import persona.core.contracts.api.ExtensionListing
import persona.core.contracts.api.ExtensionManagement
import persona.core.contracts.api.ExtensionMarket
import persona.core.contracts.api.ExtensionOperation
import persona.core.contracts.api.ExtensionAuth
import persona.core.contracts.api.BindPrompt
import persona.core.contracts.api.ExtensionPreview
import persona.core.contracts.api.ExtensionUiContext
import persona.core.contracts.api.ExtensionUiForbidden
import persona.core.contracts.api.ExtensionUiNotFound
import persona.core.contracts.api.ExtensionUiPlace
import persona.core.contracts.api.ExtensionUiResult
import persona.core.contracts.api.ExtensionUiUnsupported
import persona.core.contracts.api.ExtensionView
import persona.core.contracts.api.ExtensionQuickPrompt
import persona.core.contracts.api.ExtensionTheme
import persona.core.contracts.api.ExtensionTriageTemplate
import kotlinx.serialization.KSerializer
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.map
import kotlinx.serialization.SerializationException
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonObject
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response

/**
 * Settings › Extensions over the pairing's connection (docs/extensions/extensions.md "Hub API"). The hub's own routes are under the
 * reserved `-` segment of an extension (`/api/v1/extensions/{id}/-/…`); everything else there goes to the extension. The hub-wide
 * ones (markets, the catalog, where extensions draw) are under `/api/v1/extensions/-/…`.
 *
 * A hub from before #1844 has these routes only under their old name, `/api/v1/plugins/…`, and answers with the old keys
 * (`plugins`, `plugin`, `plugin_name`): [legacy] is that hub (its hello has no `extensions` feature). The types read both.
 */
class HttpExtensionManagement internal constructor(
    private val request: suspend (String, String, RequestBody?) -> Response,
    override val available: Flow<Boolean> = flowOf(true),
    /** The app's side of every view's context: time zone, language, platform, UI version (read on each call). */
    private val device: () -> ExtensionUiContext = { deviceContext() },
    /** The hub answers only under `/api/v1/plugins` (before #1844). */
    private val legacy: Boolean = false,
    private val streamConnection: OkHttpHubConnection? = null,
) : ExtensionManagement {
    override val nativeStream = streamConnection?.let { connection ->
        extensionStreamBindings(connection) { "paired" to "primary" }
    } ?: flowOf(null)
    constructor(connection: OkHttpHubConnection, available: Flow<Boolean>, legacy: Boolean = false) : this({ path, method, body ->
        connection.request(path, method, body)
    }, available, legacy = legacy, streamConnection = connection)

    private val base = if (legacy) "api/v1/plugins" else "api/v1/extensions"

    override suspend fun extensions(): List<HubExtension> = listing().extensions

    override suspend fun listing(): ExtensionListing {
        val body = parse(call(path(), "GET")) as? JsonObject ?: throw HubFailure(HubError.Protocol)
        val list = body[if (legacy) "plugins" else "extensions"] as? JsonArray ?: throw HubFailure(HubError.Protocol)
        fun flag(name: String) = (body[name] as? JsonPrimitive)?.booleanOrNull == true
        val extensions = decode { JsonCodec.decodeFromJsonElement(ListSerializer(HubExtension.serializer()), list) }
        return ExtensionListing(extensions, canInstall = flag("can_install"), markets = flag("markets"))
    }

    override suspend fun extension(id: String) = detail(call(path(id, "-", ""), "GET"))

    override suspend fun setEnabled(id: String, enabled: Boolean, cascade: Boolean) =
        detail(call(path(id, "-", "enabled"), "PUT", buildJsonObject { put("enabled", enabled); if (cascade) put("cascade", true) }))

    override suspend fun setContribution(id: String, point: String, contribution: String, enabled: Boolean) =
        detail(call(path(id, "-", "contributions", point.also { require(it.matches(ContributionPoint)) }, id(contribution)),
            "PUT", buildJsonObject { put("enabled", enabled) }))

    override suspend fun quickPrompts() = declared("prompts", "prompts", ExtensionQuickPrompt.serializer())
    override suspend fun themes() = declared("themes", "themes", ExtensionTheme.serializer())
    override suspend fun triageTemplates() = declared("triage-templates", "templates", ExtensionTriageTemplate.serializer())

    private suspend fun <T> declared(route: String, key: String, serializer: KSerializer<T>): List<T> = try {
        val list = (parse(call(hubPath(route), "GET")) as? JsonObject)?.get(key) as? JsonArray
            ?: throw HubFailure(HubError.Protocol)
        decode { JsonCodec.decodeFromJsonElement(ListSerializer(serializer), list) }
    } catch (failure: HubFailure) {
        if (failure.status == 404) emptyList() else throw failure
    }

    override suspend fun setConfig(id: String, config: JsonObject) =
        detail(call(path(id, "-", "config"), "PUT", buildJsonObject { put("config", config) }))

    override suspend fun setSecret(id: String, name: String, value: String) =
        detail(call(path(id, "-", "secrets", secret(name)), "PUT", buildJsonObject { put("value", value) }))

    override suspend fun clearSecret(id: String, name: String) =
        detail(call(path(id, "-", "secrets", secret(name)), "DELETE"))

    // ---- declarative UI (docs/extensions/extensions.md "Declarative UI")

    override suspend fun uiPlaces(placement: String, launcher: String?, machine: String?): List<ExtensionUiPlace> {
        // No placement: every slot but launcher sections (docs/extensions/extensions.md "UI slots").
        val url = hubUrl("ui").apply {
            placement.takeIf { it.isNotEmpty() }?.let { addQueryParameter("placement", it) }
            launcher?.takeIf { it.isNotEmpty() }?.let { addQueryParameter("launcher", it) }
            machine?.takeIf { it.isNotEmpty() }?.let { addQueryParameter("machine", it) }
        }.build()
        val list = (parse(call(url.encodedPath + (url.encodedQuery?.let { "?$it" } ?: ""), "GET")) as? JsonObject)?.get("ui") as? JsonArray
            ?: throw HubFailure(HubError.Protocol)
        return decode { JsonCodec.decodeFromJsonElement(ListSerializer(ExtensionUiPlace.serializer()), list) }
    }

    override suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext?, state: String?): ExtensionView {
        val body = parse(uiCall(extension, ui, uiBody(context, state, null, null))) as? JsonObject ?: throw HubFailure(HubError.Protocol)
        val view = body["view"] as? JsonObject ?: throw HubFailure(HubError.Protocol)
        return decode { JsonCodec.decodeFromJsonElement(ExtensionView.serializer(), view) }
    }

    override suspend fun uiAction(
        extension: String, ui: String, action: String, context: ExtensionUiContext?, state: String?, values: Map<String, String>?,
    ): ExtensionUiResult = read(uiCall(extension, ui, uiBody(context, state, action, values)), ExtensionUiResult.serializer())

    /**
     * Every call says where and for whom it draws (docs/extensions/extensions.md "UI slots" "Context"): the caller's launcher, machine,
     * inbox item and size, and this app's time zone, language, platform and UI version ([UI_VERSION]). An older hub
     * ignores what it does not know, and takes the lower of its version and the app's.
     */
    private fun uiBody(context: ExtensionUiContext?, state: String?, action: String?, values: Map<String, String>?) = buildJsonObject {
        val app = device()
        putJsonObject("context") {
            context?.launcher?.takeIf { it.isNotEmpty() }?.let { put("launcher", it) }
            context?.machine?.takeIf { it.isNotEmpty() }?.let { put("machine", it) }
            context?.item?.takeIf { it.isNotEmpty() }?.let { put("item", it) }
            context?.size?.takeIf { it.isNotEmpty() }?.let { put("size", it) }
            (context?.tz ?: app.tz)?.takeIf { it.isNotEmpty() }?.let { put("tz", it) }
            (context?.locale ?: app.locale)?.takeIf { it.isNotEmpty() }?.let { put("locale", it) }
            (context?.platform ?: app.platform)?.takeIf { it.isNotEmpty() }?.let { put("platform", it) }
            (context?.uiVersion ?: app.uiVersion)?.let { put("ui_version", it) }
        }
        state?.takeIf { it.isNotEmpty() }?.let { put("state", it) }
        action?.let { put("action", it) }
        values?.let { v -> putJsonObject("values") { v.forEach { (key, value) -> put(key, value) } } }
    }

    /**
     * An extension that says the launcher's gateway is not its own answers 404 `unsupported`: the section is hidden. 404
     * `not_found` is a machine (or extension) the hub does not know: [ExtensionUiNotFound]. 403 is a device that may only read
     * and answer: [ExtensionUiForbidden].
     */
    private suspend fun uiCall(extension: String, ui: String, body: JsonObject): String = try {
        call(path(extension, "-", "ui", id(ui)), "POST", body)
    } catch (failure: HubFailure) {
        if (failure.status == 404 && failure.code == "unsupported") throw ExtensionUiUnsupported()
        if (failure.status == 404 && failure.code == "not_found") throw ExtensionUiNotFound(failure.message)
        if (failure.status == 403) throw ExtensionUiForbidden(failure.message)
        throw failure
    }

    // ---- extension markets (docs/extensions/extensions.md "Extension market")

    override suspend fun markets(): List<ExtensionMarket> {
        val list = (parse(call(hubPath("markets"), "GET")) as? JsonObject)?.get("markets") as? JsonArray
            ?: throw HubFailure(HubError.Protocol)
        return decode { JsonCodec.decodeFromJsonElement(ListSerializer(ExtensionMarket.serializer()), list) }
    }

    override suspend fun addMarket(spec: String, token: String?): ExtensionMarket =
        read(call(hubPath("markets"), "POST", buildJsonObject {
            put("spec", spec)
            token?.takeIf { it.isNotEmpty() }?.let { put("token", it) }
        }), ExtensionMarket.serializer())

    override suspend fun removeMarket(id: String) { call(hubPath("markets", id(id)), "DELETE") }

    override suspend fun refreshMarket(id: String): ExtensionMarket =
        // OkHttp sends no POST without a body: an empty object.
        read(call(hubPath("markets", id(id), "refresh"), "POST", JsonObject(emptyMap())), ExtensionMarket.serializer())

    override suspend fun setMarketToken(id: String, token: String): ExtensionMarket =
        read(call(hubPath("markets", id(id), "token"), "PUT", buildJsonObject { put("token", token) }), ExtensionMarket.serializer())

    override suspend fun catalog(query: String, market: String?, refresh: Boolean): ExtensionCatalog {
        val url = hubUrl("catalog").apply {
            query.trim().takeIf { it.isNotEmpty() }?.let { addQueryParameter("q", it) }
            market?.takeIf { it.isNotEmpty() }?.let { addQueryParameter("market", it) }
            if (refresh) addQueryParameter("refresh", "1")
        }.build()
        val path = url.encodedPath + (url.encodedQuery?.let { "?$it" } ?: "")
        return read(call(path, "GET"), ExtensionCatalog.serializer())
    }

    override suspend fun previewFromMarket(market: String, extension: String): ExtensionPreview =
        read(call(hubPath("catalog", "preview"), "POST", buildJsonObject { put("market", market); put(if (legacy) "plugin" else "extension", extension) }),
            ExtensionPreview.serializer())

    override suspend fun install(preview: ExtensionPreview): ExtensionDetail =
        detail(call(hubPath("install"), "POST", agreed(preview)))

    override suspend fun upgrade(id: String, preview: ExtensionPreview): ExtensionDetail =
        detail(call(path(id, "-", "upgrade"), "POST", agreed(preview)))

    override suspend fun uninstall(id: String, keepData: Boolean, cascade: Boolean) {
        val query = listOfNotNull("keep_data=1".takeIf { keepData }, "cascade=1".takeIf { cascade }).joinToString("&")
        call(path(id, "-", "") + if (query.isNotEmpty()) "?$query" else "", "DELETE")
    }

    // ---- signing in (#4507): the extension's own sign-in, a step at a time; the values go to the extension only.

    override suspend fun auth(id: String): ExtensionAuth = read(call(path(id, "-", "auth"), "GET"), ExtensionAuth.serializer())

    override suspend fun authStep(id: String, step: String, values: Map<String, String>): BindPrompt =
        read(call(path(id, "-", "auth", "step"), "POST", buildJsonObject {
            put("step", step)
            put("values", buildJsonObject { values.forEach { (k, v) -> put(k, v) } })
        }), BindPrompt.serializer())

    override suspend fun authLogout(id: String): ExtensionAuth =
        read(call(path(id, "-", "auth", "logout"), "POST", JsonObject(emptyMap())), ExtensionAuth.serializer())

    // ---- in steps (docs/protocol/protocol.md "Extension install, upgrade and uninstall progress"): 202 `{operation}`

    override suspend fun startFromMarket(market: String, extension: String): ExtensionOperation =
        accepted(call(hubPath("catalog", "preview"), "POST", buildJsonObject {
            put("market", market); put("extension", extension); put("async", true)
        }))

    override suspend fun agree(preview: ExtensionPreview, upgradeOf: String?): ExtensionOperation {
        val body = JsonObject(agreed(preview) + ("async" to JsonPrimitive(true)))
        return accepted(call(if (upgradeOf != null) path(upgradeOf, "-", "upgrade") else hubPath("install"), "POST", body))
    }

    override suspend fun startUninstall(id: String, keepData: Boolean, cascade: Boolean): ExtensionOperation {
        val query = listOfNotNull("async=1", "keep_data=1".takeIf { keepData }, "cascade=1".takeIf { cascade }).joinToString("&")
        return accepted(call(path(id, "-", "") + "?$query", "DELETE"))
    }

    override suspend fun operations(): List<ExtensionOperation> {
        val list = (parse(call(hubPath("operations"), "GET")) as? JsonObject)?.get("operations") as? JsonArray ?: return emptyList()
        return decode { JsonCodec.decodeFromJsonElement(ListSerializer(ExtensionOperation.serializer()), list) }
    }

    override suspend fun operation(id: String): ExtensionOperation = read(call(hubPath("operations", opId(id)), "GET"), ExtensionOperation.serializer())

    override suspend fun retryOperation(id: String): ExtensionOperation =
        accepted(call(hubPath("operations", opId(id), "retry"), "POST", JsonObject(emptyMap())))

    override suspend fun cancelOperation(id: String) { call(hubPath("operations", opId(id)), "DELETE") }

    private fun accepted(body: String): ExtensionOperation {
        val op = (parse(body) as? JsonObject)?.get("operation") ?: throw HubFailure(HubError.Protocol)
        return decode { JsonCodec.decodeFromJsonElement(ExtensionOperation.serializer(), op) }
    }

    // ---- auto-upgrades (docs/extensions/extensions.md "Auto-upgrade")

    override suspend fun setAutoUpgrade(id: String, auto: Boolean) =
        detail(call(path(id, "-", "auto-upgrade"), "PUT", buildJsonObject { put("auto", auto) }))

    /** A hub without auto-upgrades has no route (404): null, and the switch is not shown. */
    override suspend fun autoUpgrades(): Boolean? = try {
        autoOf(call(hubPath("upgrades"), "GET"))
    } catch (failure: HubFailure) {
        if (failure.status == 404) null else throw failure
    }

    override suspend fun setAutoUpgrades(auto: Boolean): Boolean =
        autoOf(call(hubPath("upgrades"), "PUT", buildJsonObject { put("auto", auto) })) ?: throw HubFailure(HubError.Protocol)

    private fun autoOf(body: String): Boolean? = ((parse(body) as? JsonObject)?.get("auto") as? JsonPrimitive)?.booleanOrNull

    /** The preview and the permissions the user saw and agreed to. */
    private fun agreed(preview: ExtensionPreview) = buildJsonObject {
        require(preview.dependenciesReady) { "Unresolved extension dependencies" }
        put("preview_id", preview.previewId)
        put("permissions_digest", preview.permissionsDigest)
        if (preview.bundle.isNotEmpty()) put("bundle", JsonArray(preview.bundle.map { p -> buildJsonObject {
            put("preview_id", p.previewId)
            put("permissions_digest", p.permissionsDigest)
        } }))
    }

    private fun <T> read(body: String, serializer: KSerializer<T>): T = decode { JsonCodec.decodeFromJsonElement(serializer, parse(body)) }

    private fun detail(body: String): ExtensionDetail = decode { ExtensionDetail.decode(parse(body)) }

    private fun parse(body: String): JsonElement = try { JsonCodec.parseToJsonElement(body) }
    catch (error: SerializationException) { throw HubFailure(HubError.Protocol, cause = error) }

    /** Decoding errors are the hub's protocol, not the network's. */
    private inline fun <T> decode(block: () -> T): T = try { block() }
    catch (error: IllegalArgumentException) { throw HubFailure(HubError.Protocol, cause = error) }

    // The pairing's connection throws a HubFailure for a status that is not 2xx; a bare request returns it.
    private suspend fun call(path: String, method: String, body: JsonObject? = null): String = try {
        request(path, method, body?.toString()?.toRequestBody(json)).use { response ->
            if (!response.isSuccessful) throw HubFailure.from(response)
            response.body?.string().orEmpty()
        }
    } catch (failure: HubFailure) {
        throw rejected(failure) ?: failure
    }

    /**
     * 400 `{code: "invalid_config", field, message}`: the field the hub refused and why. 409 `{code: "has_dependents",
     * dependents: [{id, name}]}`: the extensions that would be switched off too (#5192).
     */
    private fun rejected(failure: HubFailure): Exception? = when {
        failure.status == 400 && failure.code == "invalid_config" ->
            ExtensionConfigRejected(failure.field.orEmpty(), failure.detail?.takeIf { it.isNotBlank() } ?: failure.message)
        failure.status == 409 && failure.code == "has_dependents" -> {
            val list = runCatching { (JsonCodec.parseToJsonElement(failure.body.orEmpty()) as? JsonObject)?.get("dependents") as? JsonArray }.getOrNull()
            ExtensionHasDependents(list?.let { runCatching { JsonCodec.decodeFromJsonElement(ListSerializer(ExtensionDependent.serializer()), it) }.getOrNull() }.orEmpty(),
                failure.message)
        }
        else -> null
    }

    /** `/api/v1/extensions[/id/…]` (or `/api/v1/plugins`); an empty last segment keeps the trailing slash the detail route has. */
    private fun path(id: String? = null, vararg rest: String): String {
        if (id != null) require(id.matches(ExtensionId)) { "Invalid extension id" }
        return HttpUrl.Builder().scheme("https").host("example.com").addPathSegments(base).apply {
            if (id != null) addPathSegment(id)
            rest.forEach(::addPathSegment)
        }.build().encodedPath
    }

    /** `/api/v1/extensions/-/…`: the hub's own routes for all extensions. */
    private fun hubUrl(vararg rest: String) = HttpUrl.Builder().scheme("https").host("example.com")
        .addPathSegments(base).addPathSegment("-").apply { rest.forEach(::addPathSegment) }

    private fun hubPath(vararg rest: String): String = hubUrl(*rest).build().encodedPath

    // Market and UI ids come from the hub; anything else must not become a path.
    private fun id(value: String): String = value.also { require(it.matches(ExtensionId)) { "Invalid id" } }

    // Operation ids come from the hub (`op-…`); anything else must not become a path.
    private fun opId(value: String): String = value.also { require(it.matches(OperationId)) { "Invalid operation id" } }

    // Secret names come from the extension's manifest; anything else must not become a path ("..").
    private fun secret(name: String): String = name.also { require(it.matches(SecretName)) { "Invalid secret name" } }

    companion object {
        /**
         * The UI version this app draws: 2 has charts, stat grids, segmented controls and `refresh_after`; 3 markdown,
         * tables, empty and loading states, a row's picture, search, filter, "Load more", push / pop and `ui.invalidate`; 4 the
         * code hosting components (header, breadcrumb, file tree, code, diff, comments, composer), tabs, split views and
         * an action's values; 5 code review (a diff's hidden lines, viewed marks, drafts and the review's bar, #3136).
         */
        const val UI_VERSION = 5

        /** This phone's time zone (IANA) and language (a BCP 47 tag), the platform and the UI version it draws. */
        fun deviceContext() = ExtensionUiContext(tz = java.util.TimeZone.getDefault().id, locale = java.util.Locale.getDefault().toLanguageTag(),
            platform = "android", uiVersion = UI_VERSION)

        private val json = "application/json; charset=utf-8".toMediaType()
        private val ExtensionId = Regex("[A-Za-z0-9_-]{1,64}")
        // A hub point, or another extension's exported one as the hub checks it: <extension id>.<point> (#5193).
        private val ContributionPoint = Regex("[a-z][a-z0-9_-]{0,63}|[a-z0-9][a-z0-9_-]{0,31}\\.[a-z][a-z0-9_]{0,31}")
        private val OperationId = Regex("[A-Za-z0-9_.-]{1,128}")
        private val SecretName = Regex("[A-Za-z][A-Za-z0-9_]{0,63}")
    }
}
