package persona.core.hub.tunnel

import java.security.KeyPair
import java.security.KeyPairGenerator
import java.util.concurrent.atomic.AtomicInteger
import org.apache.sshd.common.config.keys.KeyUtils
import org.apache.sshd.common.config.keys.PublicKeyEntry
import org.apache.sshd.common.digest.BuiltinDigests
import org.apache.sshd.common.keyprovider.KeyPairProvider
import org.apache.sshd.common.session.Session
import org.apache.sshd.common.session.SessionListener
import org.apache.sshd.server.SshServer
import org.apache.sshd.server.forward.AcceptAllForwardingFilter
import org.apache.sshd.server.forward.RejectAllForwardingFilter
import persona.core.contracts.api.SshRoute

/**
 * An SSH server on loopback that lets [user] in with [allowed]'s key only (none while [accepting] is off) and forwards
 * wherever it is asked.
 */
internal class TestSshServer(
    private val allowed: SshIdentity,
    private val user: String = "persona",
    forwarding: Boolean = true,
    val hostKey: KeyPair = ed25519(),
) : AutoCloseable {
    val logins = AtomicInteger()
    /** Every connection that got as far as the SSH handshake, logged in or not. */
    val connections = AtomicInteger()
    @Volatile var accepting = true
    private val server = SshServer.setUpDefaultServer().apply {
        host = "127.0.0.1"
        port = 0
        keyPairProvider = KeyPairProvider.wrap(hostKey)
        publickeyAuthenticator = org.apache.sshd.server.auth.pubkey.PublickeyAuthenticator { name, key, _ ->
            (accepting && name == user && PublicKeyEntry.toString(key) == allowed.deviceKey.publicKey.substringBeforeLast(' '))
                .also { if (it) logins.incrementAndGet() }
        }
        addSessionListener(object : SessionListener {
            override fun sessionCreated(session: Session) { connections.incrementAndGet() }
        })
        forwardingFilter = if (forwarding) AcceptAllForwardingFilter.INSTANCE else RejectAllForwardingFilter.INSTANCE
        start()
    }
    val port: Int get() = server.port

    /** `SHA256:…` as `ssh-keygen -lf` prints the server's key. */
    val fingerprint: String = KeyUtils.getFingerPrint(BuiltinDigests.sha256, hostKey.public)

    fun route(targetPort: Int, hostKey: String? = fingerprint, user: String = this.user) =
        SshRoute("127.0.0.1", port, user, "127.0.0.1", targetPort, hostKey = hostKey)

    /** Drops every session: a tunnel has to dial again. */
    fun dropSessions() = server.activeSessions.forEach { it.close(true) }

    override fun close() = server.stop(true)

    companion object {
        fun ed25519(): KeyPair = net.i2p.crypto.eddsa.KeyPairGenerator().generateKeyPair()
        fun ecdsa(): KeyPair = KeyPairGenerator.getInstance("EC").apply { initialize(256) }.generateKeyPair()
    }
}
