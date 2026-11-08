package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.*
import kotlinx.serialization.json.*

class HttpExtensionModelTest {
    private fun withHub(legacy: Boolean = false, block: suspend (MockWebServer, HttpExtensionManagement) -> Unit) = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
            block(server, HttpExtensionManagement({ path, method, body ->
                http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
            }, legacy = legacy))
        }
    }

    @Test fun contributionSwitchesAndSandboxReadBothRoutes() {
        for (legacy in listOf(false, true)) withHub(legacy) { server, api ->
            val root = if (legacy) "plugins" else "extensions"
            server.enqueue(MockResponse().setBody("""{"id":"example","kind":"ui","contributions":[
                {"point":"ui","id":"notes","title":"笔记","enabled":false,"can_disable":true},
                {"point":"connectors","id":"notes","enabled":true,"can_disable":false,"via":"connector"}],
                "sandbox":{"enabled":true,"default":true,"chosen":false,"active":false,"why":"当前机器无法隔离",
                    "egress":{"allowed":7,"denied":2,"last_denied":"example.com"}}}"""))
            val detail = api.setContribution("example", "ui", "notes", false)
            assertEquals(2, detail.contributions!!.size)
            assertFalse(detail.extension.sandbox!!.active)
            assertEquals(2L, detail.extension.sandbox!!.egress!!.denied)
            server.takeRequest().let {
                assertEquals("/api/v1/$root/example/-/contributions/ui/notes", (it.path).zhAny())
                assertEquals("PUT", (it.method).zhAny())
                assertEquals("""{"enabled":false}""", (it.body.readUtf8()).zhAny())
            }
            server.enqueue(MockResponse().setBody("""{"id":"example"}"""))
            api.setContribution("example", "example-export.notes", "new", true)
            assertEquals("/api/v1/$root/example/-/contributions/example-export.notes/new", (server.takeRequest().path).zhAny())
            try { api.setContribution("example", "../notes", "new", false); fail() } catch (_: IllegalArgumentException) { }
            // The hub lets an extension id start with a digit (1password), so its exported points do too (#5193).
            server.enqueue(MockResponse().setBody("""{"id":"x"}"""))
            api.setContribution("x", "1password.vault", "c", true)
            assertEquals("/api/v1/$root/x/-/contributions/1password.vault/c", (server.takeRequest().path).zhAny())
            for (bad in listOf("_ext.vault", "1password.2vault", "1password.va-ult", "1password.", "a.b.c"))
                try { api.setContribution("x", bad, "c", true); fail(bad) } catch (_: IllegalArgumentException) { }
        }
    }

    @Test fun previewsCarryEveryMembersDigestInOrderAndOldOnesSendNoBundle() = withHub { server, api ->
        val preview = JsonCodec.decodeFromString<ExtensionPreview>("""{"preview_id":"main","permissions_digest":"d-main",
            "manifest":{"id":"pack","extension_pack":[{"id":"member"}]},
            "bundle":[{"preview_id":"dep","permissions_digest":"d-dep","manifest":{"id":"dep","version":"1.0.0"}},
                {"preview_id":"member","permissions_digest":"d-member","manifest":{"id":"member","version":"2.0.0"},
                    "depends":[{"id":"dep","state":"missing"}]}]}""")
        assertTrue(preview.dependenciesReady)
        server.enqueue(MockResponse().setBody("""{"id":"pack"}"""))
        api.install(preview)
        val body = JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject
        assertEquals(listOf("dep", "member"), (body["bundle"]!!.jsonArray.map { it.jsonObject["preview_id"]!!.jsonPrimitive.content }).zhAny())
        assertEquals(listOf("d-dep", "d-member"), (body["bundle"]!!.jsonArray.map { it.jsonObject["permissions_digest"]!!.jsonPrimitive.content }).zhAny())
        server.enqueue(MockResponse().setBody("""{"id":"pack"}"""))
        api.upgrade("pack", ExtensionPreview(previewId = "old", permissionsDigest = "digest"))
        assertFalse("bundle" in JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject)
    }

    @Test fun declarationsReadAndOldHub404MeansNoneNotSuccessOnOtherFailures() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"prompts":[{"extension":"example","extension_name":"示例","id":"today","title":"今天","text":"总结今天"}]}"""))
        assertEquals("总结今天", (api.quickPrompts().single().text).zhAny())
        assertEquals("/api/v1/extensions/-/prompts", (server.takeRequest().path).zhAny())
        server.enqueue(MockResponse().setBody("""{"themes":[{"extension":"example","id":"ocean","name":"海蓝","light":{"accent":"#0059A6"},"dark":{"accent":"#8BC4FF"}}]}"""))
        assertEquals("example/ocean", (api.themes().single().key).zhAny())
        assertEquals("/api/v1/extensions/-/themes", (server.takeRequest().path).zhAny())
        server.enqueue(MockResponse().setBody("""{"templates":[{"extension":"example","id":"quiet","title":"安静","text":"只看提到我的"}]}"""))
        assertEquals("只看提到我的", (api.triageTemplates().single().text).zhAny())
        assertEquals("/api/v1/extensions/-/triage-templates", (server.takeRequest().path).zhAny())
        repeat(3) { server.enqueue(MockResponse().setResponseCode(404)) }
        assertTrue(api.quickPrompts().isEmpty()); assertTrue(api.themes().isEmpty()); assertTrue(api.triageTemplates().isEmpty())
        repeat(3) { server.takeRequest() }
        server.enqueue(MockResponse().setResponseCode(503).setBody("""{"error":"busy"}"""))
        try { api.themes(); fail() } catch (e: HubFailure) { assertEquals(503, e.status) }
    }

    @Test fun switchingOffOrRemovingWithDependentsAsksThenGoesOnWithCascade() = withHub { server, api ->
        // #5192: 409 has_dependents names the extensions depending on it; cascade switches them off too.
        server.enqueue(MockResponse().setResponseCode(409).setBody("""{"code":"has_dependents","error":"B depends on it",
            "dependents":[{"id":"ext-b","name":"B"},{"id":"ext-c","name":""}]}"""))
        try { api.setEnabled("ext-a", false); fail() } catch (e: ExtensionHasDependents) {
            assertEquals(listOf("ext-b", "ext-c"), e.dependents.map { it.id })
            assertEquals(listOf("B", "ext-c"), e.dependents.map { it.label })
            assertEquals("B depends on it", e.message)
        }
        assertEquals("""{"enabled":false}""", server.takeRequest().body.readUtf8())
        server.enqueue(MockResponse().setBody("""{"id":"ext-a","enabled":false}"""))
        api.setEnabled("ext-a", false, cascade = true)
        assertEquals("""{"enabled":false,"cascade":true}""", server.takeRequest().body.readUtf8())
        server.enqueue(MockResponse().setBody("{}"))
        api.uninstall("ext-a", keepData = true, cascade = true)
        server.takeRequest().let {
            assertEquals("DELETE", it.method)
            assertEquals("/api/v1/extensions/ext-a/-/?keep_data=1&cascade=1", it.path)
        }
        server.enqueue(MockResponse().setBody("{}"))
        api.uninstall("ext-a", keepData = false, cascade = true)
        assertEquals("/api/v1/extensions/ext-a/-/?cascade=1", server.takeRequest().path)
        server.enqueue(MockResponse().setBody("{}"))
        api.uninstall("ext-a", keepData = false)
        assertEquals("/api/v1/extensions/ext-a/-/", server.takeRequest().path)
    }
}
