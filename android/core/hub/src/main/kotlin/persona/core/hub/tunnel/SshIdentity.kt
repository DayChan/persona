package persona.core.hub.tunnel

import com.google.crypto.tink.subtle.Ed25519Sign
import java.util.Base64
import persona.core.contracts.api.SshDeviceKey

/**
 * This device's SSH key (docs/operations/network.md "Connecting to hub through SSH"): one Ed25519 key per device, its
 * private half ([seed]) only in this device's storage, never synced or logged.
 */
class SshIdentity(seed: ByteArray, private val comment: String) {
    private val seed = seed.copyOf()
    private val signer = Ed25519Sign(this.seed)
    private val blob: ByteArray

    init {
        require(seed.size == 32) { "An Ed25519 seed is 32 bytes" }
        val publicKey = Ed25519Sign.KeyPair.newKeyPairFromSeed(this.seed).publicKey
        blob = SshWire.string(ED25519) + SshWire.string(publicKey)
    }

    /** `ssh-ed25519 AAAA… persona-<device>`. */
    val deviceKey: SshDeviceKey get() = SshDeviceKey("$ED25519 ${Base64.getEncoder().encodeToString(blob)} $comment")

    /** The key as JSch signs in with it. */
    internal val jsch: com.jcraft.jsch.Identity get() = object : com.jcraft.jsch.Identity {
        override fun setPassphrase(passphrase: ByteArray?): Boolean = true
        override fun getPublicKeyBlob(): ByteArray = blob.copyOf()
        override fun getSignature(data: ByteArray): ByteArray = SshWire.string(ED25519) + SshWire.string(signer.sign(data))
        override fun getSignature(data: ByteArray, alg: String?): ByteArray = getSignature(data)
        override fun getAlgName(): String = ED25519
        override fun getName(): String = comment
        override fun isEncrypted(): Boolean = false
        override fun clear() = Unit
        override fun toString(): String = "SshIdentity(<redacted>)"
    }

    override fun toString(): String = "SshIdentity(<redacted>)"

    companion object {
        /** A new key's 32-byte seed. */
        fun generateSeed(): ByteArray = Ed25519Sign.KeyPair.newKeyPair().privateKey

        /** `persona-<device name>` on one line: the comment that tells whose key it is in authorized_keys. */
        fun comment(deviceName: String): String =
            "persona-" + deviceName.trim().replace(Regex("\\s+"), "-").ifEmpty { "android" }
    }
}
