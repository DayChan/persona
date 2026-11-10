package persona.core.data

import kotlinx.serialization.Serializable
import persona.core.contracts.api.LocalProxyRoute
import persona.core.contracts.api.SshRoute

/** tunnels.enc in the pairing's no-backup directory: erased with the rest of the pairing. */
@Serializable
internal data class TunnelsDocument(
    val version: Int = 1,
    val ssh: SshRoute? = null,
    val proxy: LocalProxyRoute? = null,
)

/**
 * A paired hub's SSH tunnel and local SOCKS5 proxy (docs/operations/network.md "Connecting to hub through SSH" and
 * "Connecting to hub through a local SOCKS5 proxy"); a missing file is neither.
 */
data class PairedTunnels(val ssh: SshRoute? = null, val proxy: LocalProxyRoute? = null)

internal fun TunnelsDocument.routes() = PairedTunnels(ssh, proxy)

internal fun PairedTunnels.document() = TunnelsDocument(ssh = ssh, proxy = proxy)

/** ssh-key.enc next to the registry: this device's SSH key, kept when pairings come and go. */
@Serializable
internal data class SshKeyDocument(
    val version: Int = 1,
    /** The Ed25519 key's 32-byte seed, base64. */
    val seed: String? = null,
) {
    override fun toString(): String = "SshKeyDocument(<redacted>)"
}
