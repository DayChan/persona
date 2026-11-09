package persona.core.hub

import persona.core.contracts.api.*
import java.io.ByteArrayOutputStream
import java.net.InetAddress
import java.security.MessageDigest
import java.util.Base64
import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.junit.Assert.*
import org.junit.Test

class HubPairingClientTest {
    @Test fun pinnedPairingExchangesCredentialsAndDownloadsVerifiedApk() = runBlocking {
        val certificate = HeldCertificate.Builder().commonName("example.com").build()
        val fingerprint = Base64.getUrlEncoder().withoutPadding().encodeToString(
            MessageDigest.getInstance("SHA-256").digest(certificate.certificate.publicKey.encoded))
        val apk = "test apk".toByteArray()
        val checksum = MessageDigest.getInstance("SHA-256").digest(apk).joinToString("") { "%02x".format(it) }
        MockWebServer().use { server ->
            server.useHttps(HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(), false)
            server.start(InetAddress.getLoopbackAddress(), 0)
            server.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse = when (request.path) {
                    "/api/v1/health" -> {
                        assertNull(request.getHeader("Authorization"))
                        MockResponse().setBody("""{"ok":true,"hub_id":"test-hub"}""")
                    }
                    "/api/v1/auth/tokens" -> {
                        assertEquals("Bearer pairing-code", (request.getHeader("Authorization")).zhAny())
                        assertTrue(request.body.readUtf8().contains("phone"))
                        MockResponse().setBody("""{"id":"device-id","token":"device-token"}""")
                    }
                    "/api/v1/machines" -> {
                        assertEquals("Bearer device-token", (request.getHeader("Authorization")).zhAny())
                        MockResponse().setBody("""{"machines":[]}""")
                    }
                    "/api/v1/app/android/latest" -> MockResponse().setBody(
                        """{"version_code":2,"version_name":"test","sha256":"$checksum","download_url":"/api/v1/app/android/apk?sha256=$checksum"}""")
                    "/api/v1/app/android/apk?sha256=$checksum" -> {
                        assertEquals("Bearer device-token", (request.getHeader("Authorization")).zhAny())
                        MockResponse().setBody(String(apk))
                    }
                    else -> MockResponse().setResponseCode(404)
                }
            }
            val address = server.url("/").toString()
            val stages = mutableListOf<PairingStage>()
            HubPairingClient("test-hub", fingerprint, listOf(address), "test").use { client ->
                val paired = client.pair("pairing-code", "Test device") { stages += it.stage }
                assertEquals("device-token", (paired.token).zhAny())
                assertEquals("device-id", (paired.deviceId).zhAny())
                assertEquals("test-hub", (paired.hubId).zhAny())
                assertTrue(stages.indexOf(PairingStage.VerifyIdentity) < stages.indexOf(PairingStage.ExchangeCredentials))
                assertTrue(client.diagnostics(paired.token, paired.deviceId).candidates.any { it.result.zh() == "已通过身份校验" })
                val update = requireNotNull(client.checkUpdate(paired.token))
                val destination = ByteArrayOutputStream()
                client.download(paired.token, update, destination)
                assertArrayEquals(apk, destination.toByteArray())
                try {
                    client.download(paired.token, update.copy(sha256 = "0".repeat(64)), ByteArrayOutputStream())
                    fail("Checksum mismatch must fail")
                } catch (error: HubFailure) {
                    assertEquals("apk_checksum_mismatch", error.code)
                    assertFalse(error.retryable)
                }
                try {
                    client.download(paired.token, update.copy(downloadUrl = "https://example.com/apk"), ByteArrayOutputStream())
                    fail("Cross-origin download must fail")
                } catch (_: IllegalArgumentException) { }
            }
            val requestsBeforeMismatch = server.requestCount
            HubPairingClient("test-hub", "A".repeat(43), listOf(address), "test").use { client ->
                try { client.pair("pairing-code", "Test device") {}; fail("Invalid pin must fail") }
                catch (error: HubFailure) { assertEquals(HubError.CertificateMismatch, error.kind) }
            }
            assertEquals(requestsBeforeMismatch, server.requestCount)
        }
    }

    @Test fun legacyFallbackOnlyAcceptsMissingExchangeEndpointAndBlocksPublicPlaintext() = runBlocking {
        MockWebServer().use { server ->
            server.start(InetAddress.getLoopbackAddress(), 0)
            var exchangeStatus = 404
            server.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest) = when (request.path) {
                    "/api/v1/health" -> MockResponse().setBody("""{"ok":true}""")
                    "/api/v1/auth/tokens" -> MockResponse().setResponseCode(exchangeStatus)
                    "/api/v1/machines" -> MockResponse().setBody("""{"machines":[]}""")
                    else -> MockResponse().setResponseCode(404)
                }
            }
            HubPairingClient(null, null, listOf(server.url("/").toString()), "test").use { client ->
                val paired = client.pair("legacy-token", "Test device") {}
                assertEquals("legacy-token", (paired.token).zhAny())
                assertTrue(paired.hubId.startsWith("legacy-"))
                exchangeStatus = 401
                try { client.pair("expired-code", "Test device") {}; fail("Expired codes must fail") }
                catch (error: HubFailure) { assertEquals(HubError.PairingExpired, error.kind) }
            }
        }
        HubPairingClient(null, null, listOf("http://192.0.2.1/"), "test").use { client ->
            try { client.pair("must-not-leak", "Test device") {}; fail("Public HTTP must fail") }
            catch (error: HubFailure) { assertEquals(HubError.TlsRequired, error.kind) }
        }
    }
}
