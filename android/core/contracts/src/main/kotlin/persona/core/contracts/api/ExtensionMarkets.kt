@file:OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)

package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.json.JsonNames
import kotlinx.serialization.Serializable
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.flow.flowOf
import persona.core.contracts.parseTime
import java.time.Instant

/**
 * Extension market (docs/extensions/extensions.md "Extension market"): indexes of extensions the hub has registered; a full device browses them,
 * installs, upgrades and removes what they offer (always after a preview of what the extension may do), and adds,
 * removes and refreshes the markets themselves. Tokens are write-only.
 */
interface ExtensionMarkets {
    /** `GET /api/v1/extensions/-/markets`. */
    suspend fun markets(): List<ExtensionMarket>

    /** `POST /api/v1/extensions/-/markets`: read once first; one the hub cannot read is refused with the reason. */
    suspend fun addMarket(spec: String, token: String? = null): ExtensionMarket

    /** `DELETE /api/v1/extensions/-/markets/{id}`: extensions installed from it stay. */
    suspend fun removeMarket(id: String)

    /** `POST /api/v1/extensions/-/markets/{id}/refresh`. */
    suspend fun refreshMarket(id: String): ExtensionMarket

    /** `PUT /api/v1/extensions/-/markets/{id}/token`; an empty token removes it. */
    suspend fun setMarketToken(id: String, token: String): ExtensionMarket

    /** `GET /api/v1/extensions/-/catalog?q=&market=&refresh=1`. */
    suspend fun catalog(query: String = "", market: String? = null, refresh: Boolean = false): ExtensionCatalog

    /** `POST /api/v1/extensions/-/catalog/preview`: downloads and checks it; an installed one previews its upgrade. */
    suspend fun previewFromMarket(market: String, extension: String): ExtensionPreview

    /** `POST /api/v1/extensions/-/install` with the preview and the permissions the user agreed to. */
    suspend fun install(preview: ExtensionPreview): ExtensionDetail

    /** `POST /api/v1/extensions/{id}/-/upgrade`. */
    suspend fun upgrade(id: String, preview: ExtensionPreview): ExtensionDetail

    /**
     * `DELETE /api/v1/extensions/{id}/-/` (only those installed from a market, on a phone); its data stays unless told
     * otherwise. With enabled dependents it is a [ExtensionHasDependents] unless [cascade] (`cascade=1`) switches them off.
     */
    suspend fun uninstall(id: String, keepData: Boolean = true, cascade: Boolean = false)

    /** `GET /api/v1/extensions/-/upgrades`: the auto-upgrade switch for all market extensions; null on a hub without it. */
    suspend fun autoUpgrades(): Boolean? = null

    /** `PUT /api/v1/extensions/-/upgrades`: turns it on or off; the hub's answer. */
    suspend fun setAutoUpgrades(auto: Boolean): Boolean =
        throw UnsupportedOperationException("Auto-upgrades unsupported by this hub")

    // ---- in steps (docs/protocol/protocol.md "Extension install, upgrade and uninstall progress"): only with a hub that says `extension_operations`;
    // an older one keeps the calls above.

    /** The selected hub runs installs as operations. */
    val operationsAvailable: Flow<Boolean> get() = flowOf(false)

    /**
     * Each `extension_operation` the hub pushes; null after each hello (what was missed is listed again) and each managers
     * push (a manager that restarted lost its operations).
     */
    val operationUpdates: Flow<ExtensionOperation?> get() = emptyFlow()

    /** `POST …/-/catalog/preview` with `async`: downloads and checks it in the background; the operation, at its first step. */
    suspend fun startFromMarket(market: String, extension: String): ExtensionOperation = unsupported()

    /** The user agreed to an operation's preview: `POST …/-/install` (or `{upgradeOf}/-/upgrade`) with `async`, the same operation goes on. */
    suspend fun agree(preview: ExtensionPreview, upgradeOf: String? = null): ExtensionOperation = unsupported()

    /** `DELETE …/{id}/-/?async=1`; with enabled dependents it fails with `has_dependents` unless [cascade] (`cascade=1`). */
    suspend fun startUninstall(id: String, keepData: Boolean = true, cascade: Boolean = false): ExtensionOperation = unsupported()

    /** `GET …/-/operations`: those going on and those ended in the last minutes. */
    suspend fun operations(): List<ExtensionOperation> = emptyList()

    /** `GET …/-/operations/{id}`: with its preview while it waits for consent. */
    suspend fun operation(id: String): ExtensionOperation = unsupported()

    /** `POST …/-/operations/{id}/retry`: a failed one again, from where it can. */
    suspend fun retryOperation(id: String): ExtensionOperation = unsupported()

    /** `DELETE …/-/operations/{id}`: cancels one that downloads or waits for consent, forgets one that ended. */
    suspend fun cancelOperation(id: String): Unit = unsupported()
}

private fun unsupported(): Nothing = throw UnsupportedOperationException("Extension operations unsupported by this hub")

/** `GET /api/v1/extensions`: the list, and what this device may do with it. */
data class ExtensionListing(
    val extensions: List<HubExtension>,
    val canInstall: Boolean = false,
    /** The hub has extension markets: Settings › Extensions shows "Extension market". */
    val markets: Boolean = false,
)

/** A registered market. */
@Serializable
data class ExtensionMarket(
    val id: String = "",
    val spec: String = "",
    val name: String = "",
    val description: String? = null,
    val homepage: String? = null,
    @SerialName("has_token") val hasToken: Boolean = false,
    /** How many extensions its index offers. */
    @SerialName("extensions") @JsonNames("plugins") val extensions: Int = 0,
    val warnings: List<String>? = null,
    /** Why it could not be read the last time. */
    val error: String? = null,
    @SerialName("fetched_at") val fetchedAt: String? = null,
    @SerialName("added_at") val addedAt: String? = null,
    /** Whether its index is signed and checks out (#1848); a hub before it says nothing. */
    val signature: MarketSignature? = null,
    /** The official market every hub has (#2351), which the hub lists first; false on others and older hubs. */
    val official: Boolean = false,
) {
    val label: String get() = name.ifBlank { spec }
}

/**
 * A market's signature (docs/extensions/extensions.md "Signatures"): `signed` (signed with the key the hub trusts; `verified` counts too,
 * #4812), `unsigned` (an older market: still usable), or anything else (the signature fails or the key changed): the hub
 * then neither refreshes nor installs from it until its owner trusts the new key on the hub's machine. [detail] says why,
 * for people.
 */
@Serializable
data class MarketSignature(
    val state: String = "",
    @SerialName("key_id") val keyId: String? = null,
    val detail: String? = null,
) {
    val verified: Boolean get() = state == "signed" || state == "verified"
    val unsigned: Boolean get() = state.isEmpty() || state == "unsigned"
    val failed: Boolean get() = !verified && !unsigned
}

/** `GET /api/v1/extensions/-/catalog`. */
@Serializable
data class ExtensionCatalog(
    val markets: List<ExtensionMarket> = emptyList(),
    @SerialName("extensions") @JsonNames("plugins") val extensions: List<CatalogExtension> = emptyList(),
    /** This hub's `GOOS-GOARCH`. */
    val platform: String? = null,
)

/** An extension a market offers, with how it stands on this hub. */
@Serializable
data class CatalogExtension(
    val market: String = "",
    @SerialName("market_name") val marketName: String? = null,
    val id: String = "",
    val name: String = "",
    val version: String = "",
    val description: String? = null,
    val publisher: String? = null,
    val homepage: String? = null,
    val tags: List<String>? = null,
    val contributes: Map<String, List<String>>? = null,
    val permissions: List<ExtensionPermission> = emptyList(),
    /** Why this hub cannot install it (no package for its platform, a newer hub needed). */
    val problem: String? = null,
    /** The installed version, when it is installed (from anywhere). */
    val installed: String? = null,
    @SerialName("installed_source") val installedSource: String? = null,
    @SerialName("from_this_market") val fromThisMarket: Boolean = false,
    val upgradable: Boolean = false,
) {
    val label: String get() = name.ifBlank { id }
}

/** A downloaded, checked package waiting to be installed (10 minutes): what it is and may do. */
@Serializable
data class ExtensionPreview(
    @SerialName("preview_id") val previewId: String = "",
    val manifest: PreviewManifest? = null,
    val permissions: List<ExtensionPermission> = emptyList(),
    @SerialName("permissions_digest") val permissionsDigest: String = "",
    val warnings: List<String>? = null,
    @SerialName("expires_at") val expiresAt: String? = null,
    val upgrade: Boolean = false,
    /** The version an upgrade replaces. */
    val from: String? = null,
    @SerialName("more_permissions") val morePermissions: Boolean = false,
    /** What an upgrade asks for beyond the installed version. */
    @SerialName("added_permissions") val addedPermissions: List<ExtensionPermission>? = null,
    val market: String? = null,
    val depends: List<ExtensionDependency> = emptyList(),
    val bundle: List<ExtensionPreview> = emptyList(),
    val unresolved: List<String> = emptyList(),
) {
    /** Nothing is unresolved, and every dependency is ok or comes in the same bundle. */
    val dependenciesReady: Boolean get() {
        val previews = listOf(this) + bundle
        val bundled = bundle.mapNotNull { it.manifest?.id }.toSet()
        return previews.all { p -> p.unresolved.isEmpty() && p.depends.all { d ->
            d.state == "ok" || d.state in setOf("missing", "old") && d.id in bundled
        } }
    }

    /**
     * The earliest `expires_at` of this preview and the ones in its bundle: the hub refuses the agreement once any of
     * them is gone. Null from a hub that sends none (it never expires here; the hub still decides).
     */
    val expiresBy: Instant? get() = (listOf(this) + bundle).mapNotNull { parseTime(it.expiresAt) }.minOrNull()

    /** Expired at [now], as the hub counts it (`!now.Before(expires_at)`): previewed again, not agreed to. */
    fun expired(now: Instant): Boolean = expiresBy?.let { !now.isBefore(it) } ?: false

    /** It can be agreed to at [now]: its dependencies are ready and it has not expired. */
    fun canConfirm(now: Instant): Boolean = dependenciesReady && !expired(now)
}

/** The few keys of the previewed manifest the app shows. */
@Serializable
data class PreviewManifest(
    val id: String = "",
    val name: String? = null,
    val version: String? = null,
    val description: String? = null,
    val publisher: String? = null,
    @SerialName("extension_pack") val extensionPack: List<ExtensionPackMember> = emptyList(),
)
