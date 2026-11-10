package persona.core.hub.tunnel

import com.google.crypto.tink.subtle.Ed25519Sign
import com.google.crypto.tink.subtle.Ed25519Verify
import com.google.crypto.tink.subtle.X25519
import com.jcraft.jsch.JSch
import java.io.ByteArrayOutputStream
import java.nio.ByteBuffer
import java.security.GeneralSecurityException
import java.security.MessageDigest
import java.util.Base64
import persona.core.contracts.api.SshHostKey

/**
 * JSch's `curve25519-sha256` and `ssh-ed25519` through Tink. JSch's own need Java 11 / 15 classes or BouncyCastle, and
 * Android has neither: without these both would drop out of the handshake. JSch looks them up by class name.
 */
internal object SshAlgorithms {
    private var installed = false

    /** Before the first session; a second caller waits until both are in. */
    @Synchronized fun install() {
        if (installed) return
        JSch.setConfig("xdh", TinkX25519::class.java.name)
        JSch.setConfig("ssh-ed25519", TinkEd25519::class.java.name)
        installed = true
    }
}

class TinkX25519 : com.jcraft.jsch.XDH {
    private var privateKey = ByteArray(0)
    private var publicKey = ByteArray(0)

    override fun init(name: String, keylen: Int) {
        if (name != "X25519" || keylen != 32) throw GeneralSecurityException("Unsupported curve $name")
        privateKey = X25519.generatePrivateKey()
        publicKey = X25519.publicFromPrivate(privateKey)
    }

    override fun getQ(): ByteArray = publicKey

    override fun getSecret(u: ByteArray): ByteArray = X25519.computeSharedSecret(privateKey, u)

    override fun validate(u: ByteArray): Boolean = u.size == 32
}

class TinkEd25519 : com.jcraft.jsch.SignatureEdDSA {
    private var publicKey: ByteArray? = null
    private var seed: ByteArray? = null
    private val data = ByteArrayOutputStream()

    override fun init() = data.reset()

    override fun setPubKey(y_arr: ByteArray) { publicKey = y_arr }

    override fun setPrvKey(bytes: ByteArray) { seed = bytes }

    override fun update(H: ByteArray) = data.write(H)

    override fun verify(sig: ByteArray): Boolean {
        val key = publicKey ?: return false
        val raw = SshWire.signatureBody(sig, ED25519) ?: return false
        return try {
            Ed25519Verify(key).verify(raw, data.toByteArray())
            true
        } catch (_: GeneralSecurityException) {
            false
        }
    }

    override fun sign(): ByteArray = Ed25519Sign(checkNotNull(seed)).sign(data.toByteArray())
}

internal const val ED25519 = "ssh-ed25519"

/** The SSH wire format's strings (RFC 4251 section 5) and OpenSSH's key fingerprints. */
internal object SshWire {
    fun string(bytes: ByteArray): ByteArray = ByteBuffer.allocate(4 + bytes.size).putInt(bytes.size).put(bytes).array()

    fun string(text: String): ByteArray = string(text.toByteArray(Charsets.UTF_8))

    /** The strings in [blob], in order; null when it does not parse. */
    fun strings(blob: ByteArray): List<ByteArray>? {
        val out = mutableListOf<ByteArray>()
        val buffer = ByteBuffer.wrap(blob)
        while (buffer.hasRemaining()) {
            if (buffer.remaining() < 4) return null
            val length = buffer.int
            if (length < 0 || length > buffer.remaining()) return null
            out += ByteArray(length).also(buffer::get)
        }
        return out
    }

    /** The raw signature: `string(algorithm) string(signature)` unwrapped, or [sig] itself when it is already raw. */
    fun signatureBody(sig: ByteArray, algorithm: String): ByteArray? {
        val parts = strings(sig)
        if (parts != null && parts.size == 2 && String(parts[0], Charsets.UTF_8) == algorithm) return parts[1]
        return sig.takeIf { it.size == 64 }
    }

    /** `SHA256:…` (unpadded base64) and the algorithm as `ssh-keygen -lf` prints them. */
    fun hostKey(blob: ByteArray): SshHostKey {
        val digest = MessageDigest.getInstance("SHA-256").digest(blob)
        val fingerprint = "SHA256:" + Base64.getEncoder().withoutPadding().encodeToString(digest)
        val type = strings(blob)?.firstOrNull()?.toString(Charsets.UTF_8).orEmpty()
        return SshHostKey(fingerprint, displayAlgorithm(type))
    }

    fun displayAlgorithm(type: String): String = when {
        type == ED25519 -> "ED25519"
        type.startsWith("ecdsa-") -> "ECDSA"
        type == "ssh-rsa" -> "RSA"
        else -> type.uppercase()
    }

    /** The server key algorithms to offer, the pinned key's first so the server shows the key that was confirmed. */
    fun hostKeyAlgorithms(pinned: String?): String {
        val all = listOf(ED25519, "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "rsa-sha2-512", "rsa-sha2-256")
        val first = all.filter { pinned != null && displayAlgorithm(if (it.startsWith("rsa-")) "ssh-rsa" else it) == pinned }
        return (first + (all - first.toSet())).joinToString(",")
    }
}
