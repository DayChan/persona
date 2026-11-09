package persona.feature.settings

import android.content.res.Resources
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.*

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh")
class ExtensionSettingsStateTest {
    private val res: Resources get() = ApplicationProvider.getApplicationContext<android.content.Context>().resources
    private val installed = ExtensionDetail(HubExtension("notes", source = "installed", enabled = true, autoUpgrade = true, version = "1.0.0"),
        origin = "market:example/notes", config = buildJsonObject { put("folder", "Saved") }, form = listOf(ExtensionField("folder")))

    @Test fun authorityRequiresManagerOwnedMetadata() {
        val supported = ExtensionSettingsCapabilities(installed)
        assertTrue(supported.editable); assertTrue(supported.check); assertTrue(supported.automatic); assertTrue(supported.uninstall)
        for (source in listOf("builtin", "hub.json", "dev", null)) {
            val capability = ExtensionSettingsCapabilities(installed.copy(extension = installed.extension.copy(source = source)))
            assertFalse("$source check", capability.check)
            assertFalse("$source automatic", capability.automatic)
            assertFalse("$source uninstall", capability.uninstall)
        }
        val attached = ExtensionSettingsCapabilities(installed.copy(extension = installed.extension.copy(attached = buildJsonObject { put("online", true) })))
        assertFalse(attached.check); assertFalse(attached.automatic); assertFalse(attached.uninstall)
        val local = ExtensionSettingsCapabilities(installed.copy(origin = "/private/extension"))
        assertFalse(local.check); assertFalse(local.uninstall)
        val readonly = ExtensionSettingsCapabilities(installed.copy(readOnly = true))
        assertFalse(readonly.editable); assertFalse(readonly.check); assertFalse(readonly.automatic); assertFalse(readonly.uninstall)
        assertFalse(ExtensionSettingsCapabilities(installed.copy(extension = installed.extension.copy(enabled = null))).editable)
        assertFalse(ExtensionSettingsCapabilities(installed.copy(extension = installed.extension.copy(autoUpgrade = null))).automatic)
    }

    @Test fun attachedMetadataSurvivesWireDecoding() {
        val detail = ExtensionDetail.decode(Json.parseToJsonElement("""{"id":"notes","source":"installed","attached":{"online":true,"version":"2.0.0"},"origin":"market:example/notes"}"""))
        assertNotNull(detail.extension.attached)
        assertFalse(ExtensionSettingsCapabilities(detail).uninstall)
    }

    @Test fun canonicalReadFailureKeepsDraftAndBlocksWritesUntilRetry() = runBlocking {
        var fail = false
        var writes = 0
        val api = object : ExtensionApiStub() {
            override suspend fun extension(id: String): ExtensionDetail { if (fail) error("Unavailable"); return installed }
            override suspend fun setEnabled(id: String, enabled: Boolean, cascade: Boolean): ExtensionDetail { writes++; return installed }
        }
        val model = ExtensionModel(api, "notes", res)
        model.load(); model.edit(listOf("folder"), JsonPrimitive("Draft")); fail = true; model.load()
        assertEquals(installed, model.state.value.detail)
        assertEquals(JsonPrimitive("Draft"), model.state.value.draft["folder"])
        assertTrue(model.state.value.loadFailed)
        assertFalse(model.state.value.canSave)
        assertFalse(model.setEnabled(false)); assertEquals(0, writes)
        fail = false; model.load()
        assertFalse(model.state.value.loadFailed)
        assertTrue(model.state.value.canSave)
    }

    @Test fun updateChecksAreSerializedAndNeverSaveDrafts() = runBlocking {
        val release = CompletableDeferred<Unit>()
        val entered = CompletableDeferred<Unit>()
        var checks = 0
        var configs = 0
        val api = object : ExtensionApiStub() {
            override suspend fun extension(id: String) = installed
            override suspend fun setConfig(id: String, config: JsonObject): ExtensionDetail { configs++; return installed }
            override suspend fun catalog(query: String, market: String?, refresh: Boolean): ExtensionCatalog {
                checks++; assertEquals("example", market); assertTrue(refresh); entered.complete(Unit); release.await()
                return ExtensionCatalog(markets = listOf(ExtensionMarket("example")), extensions = listOf(CatalogExtension("example", id = "notes", version = "2.0.0", upgradable = true)))
            }
        }
        val model = ExtensionModel(api, "notes", res)
        model.load(); model.edit(listOf("folder"), JsonPrimitive("Draft"))
        val first = async { model.checkForUpdates() }; entered.await()
        model.checkForUpdates(); assertFalse(model.state.value.canSave)
        release.complete(Unit); first.await()
        assertEquals(1, checks); assertEquals(0, configs)
        assertTrue(model.state.value.updates.checked)
        assertEquals("2.0.0", model.state.value.updates.candidate?.version)
        assertEquals(JsonPrimitive("Draft"), model.state.value.draft["folder"])
    }

    @Test fun missingOrFailedMarketsAreNotReportedAsUpToDate() = runBlocking {
        var response = ExtensionCatalog()
        val api = object : ExtensionApiStub() {
            override suspend fun extension(id: String) = installed
            override suspend fun catalog(query: String, market: String?, refresh: Boolean) = response
        }
        val model = ExtensionModel(api, "notes", res); model.load()
        for (catalog in listOf(ExtensionCatalog(), ExtensionCatalog(markets = listOf(ExtensionMarket("example", error = "offline"))),
            ExtensionCatalog(markets = listOf(ExtensionMarket("example"))))) {
            response = catalog; model.checkForUpdates()
            assertFalse(model.state.value.updates.checked); assertNotNull(model.state.value.updates.error)
            assertNull(model.state.value.updates.candidate)
        }
    }
}
