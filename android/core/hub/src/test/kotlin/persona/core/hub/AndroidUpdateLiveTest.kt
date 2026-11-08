package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.HttpUrl.Companion.toHttpUrl
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test
import java.io.IOException
import java.io.OutputStream
import java.net.InetSocketAddress
import java.net.Proxy
import java.net.ProxySelector
import java.net.SocketAddress
import java.net.URI
import java.security.DigestOutputStream
import java.security.MessageDigest

/** Optional read-only probe of the same TLS update client used by the Android app. */
class AndroidUpdateLiveTest {
    @Test fun checksAndDownloadsPublishedApkThroughProductionClient() = runBlocking {
        val url = System.getenv("PERSONA_ANDROID_UPDATE_HUB")
        val token = System.getenv("PERSONA_ANDROID_UPDATE_TOKEN")
        val version = System.getenv("PERSONA_ANDROID_UPDATE_VERSION")
        val checksum = System.getenv("PERSONA_ANDROID_UPDATE_SHA256")
        assumeTrue("Live update endpoint, credential, version and hash were not supplied",
            listOf(url, token, version, checksum).all { !it.isNullOrBlank() })
        val expectedVersion = requireNotNull(version!!.toLongOrNull()) { "Invalid expected version" }
        require(checksum!!.matches(Regex("[a-fA-F0-9]{64}"))) { "Invalid expected SHA-256" }
        val base = url!!.toHttpUrl()
        require(base.isHttps && base.username.isEmpty() && base.password.isEmpty() && base.query == null && base.fragment == null) {
            "Live update probe requires a credential-free HTTPS origin"
        }
        var stage = "initialization"
        val previousProxy = ProxySelector.getDefault()
        try {
            System.getenv("PERSONA_ANDROID_UPDATE_PROXY")?.takeIf { it.isNotBlank() }?.let {
                val proxy = it.toHttpUrl()
                require(proxy.scheme == "http" && proxy.host in setOf("127.0.0.1", "localhost", "::1") &&
                    proxy.username.isEmpty() && proxy.password.isEmpty() && proxy.encodedPath == "/" &&
                    proxy.query == null && proxy.fragment == null) { "Proxy must be a credential-free loopback HTTP endpoint" }
                val route = Proxy(Proxy.Type.HTTP, InetSocketAddress(proxy.host, proxy.port))
                ProxySelector.setDefault(object : ProxySelector() {
                    override fun select(uri: URI): List<Proxy> = listOf(route)
                    override fun connectFailed(uri: URI, address: SocketAddress, error: IOException) {
                        throw IllegalStateException("Loopback proxy connection failed")
                    }
                })
            }
            HubPairingClient(
                System.getenv("PERSONA_ANDROID_UPDATE_HUB_ID")?.takeIf { it.isNotBlank() },
                System.getenv("PERSONA_ANDROID_UPDATE_FINGERPRINT")?.takeIf { it.isNotBlank() },
                listOf(base.toString()), "android-update-live-probe",
            ).use { client ->
                stage = "metadata"
                val update = requireNotNull(client.checkUpdate(token!!)) { "No published Android update" }
                assertEquals("Published version differs", expectedVersion, update.versionCode)
                assertEquals("Metadata hash differs", checksum.lowercase(), update.sha256?.lowercase())
                val digest = MessageDigest.getInstance("SHA-256")
                var bytes = 0L
                val counter = object : OutputStream() {
                    override fun write(value: Int) { bytes++ }
                    override fun write(buffer: ByteArray, offset: Int, length: Int) { bytes += length }
                }
                stage = "download"
                DigestOutputStream(counter, digest).use { client.download(token, update, it) }
                assertTrue("APK response is empty", bytes > 0)
                val actual = digest.digest().joinToString("") { "%02x".format(it) }
                assertEquals("Downloaded hash differs", checksum.lowercase(), actual)
                println("Android update live probe PASS: version=$expectedVersion sha256=$actual bytes=$bytes")
            }
        } catch (error: Exception) {
            // Remote bodies or exception URLs must never place credentials into a test report.
            val causes = generateSequence(error as Throwable) { it.cause }.take(8)
                .joinToString(" -> ") { it.javaClass.simpleName }
            val failure = error as? HubFailure
            throw AssertionError("Live update probe failed: stage=$stage kind=${failure?.kind} " +
                "status=${failure?.status} causes=$causes")
        } finally {
            ProxySelector.setDefault(previousProxy)
        }
    }
}
