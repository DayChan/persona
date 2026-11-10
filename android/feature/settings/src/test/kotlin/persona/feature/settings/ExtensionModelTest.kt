package persona.feature.settings

import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.*
import java.time.Instant

/** Robolectric for the words: the app's strings in Simplified Chinese (robolectric.properties). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ExtensionModelTest {
    private val res get() = ApplicationProvider.getApplicationContext<android.content.Context>().resources

    @Test fun oldDetailsKeepTheirOldBehaviourAndNewOnesReadContributions() {
        val old = ExtensionDetail.decode(JsonCodec.parseToJsonElement("""{"id":"example","contributes":{"ui":["notes"]}}"""))
        assertNull(old.contributions)
        assertNull(old.extension.sandbox)
        assertTrue(old.depends.isEmpty())
        assertTrue(old.extensionPack.isEmpty())
        val current = ExtensionDetail.decode(JsonCodec.parseToJsonElement("""{"id":"example","contributions":[
            {"point":"ui","id":"notes","enabled":false,"can_disable":true}],"sandbox":{"enabled":true,"active":false}}"""))
        assertFalse(current.contributions!!.single().enabled)
        assertTrue(current.extension.sandbox!!.enabled)
    }

    @Test fun flatBundleResolvesTransitiveDependenciesAndUnknownOrMissingOnesBlock() {
        val child = ExtensionPreview(manifest = PreviewManifest(id = "child"), depends = listOf(ExtensionDependency("base", state = "missing")))
        val main = ExtensionPreview(depends = listOf(ExtensionDependency("child", state = "missing")), bundle = listOf(
            ExtensionPreview(manifest = PreviewManifest(id = "base")), child))
        assertTrue(main.dependenciesReady)
        assertFalse(main.copy(bundle = listOf(child)).dependenciesReady)
        assertFalse(main.copy(unresolved = listOf("base")).dependenciesReady)
        assertFalse(main.copy(depends = listOf(ExtensionDependency("child", state = "disabled"))).dependenciesReady)
        assertFalse(main.copy(depends = listOf(ExtensionDependency("child", state = "not_ready"))).dependenciesReady)
        assertFalse(main.copy(depends = listOf(ExtensionDependency("child", state = "new-state"))).dependenciesReady)
        assertTrue(ExtensionPreview().dependenciesReady)
    }

    @Test fun theEarliestExpiryOfTheWholeBundleDecidesAndAnOlderHubsPreviewNeverExpiresHere() {
        val main = ExtensionPreview(previewId = "main", expiresAt = "2026-11-08T10:10:00Z", bundle = listOf(
            // A member made a minute before the main one expires a minute earlier; the hub may send its zone's offset.
            ExtensionPreview(previewId = "member", expiresAt = "2026-11-08T18:09:00+08:00"),
            ExtensionPreview(previewId = "other")))
        assertEquals(Instant.parse("2026-11-08T10:09:00Z"), main.expiresBy)
        assertTrue(main.canConfirm(Instant.parse("2026-11-08T10:08:59Z")))
        // Gone at the instant itself, as the hub counts it.
        assertTrue(main.expired(Instant.parse("2026-11-08T10:09:00Z")))
        assertFalse(main.canConfirm(Instant.parse("2026-11-08T10:09:30Z")))
        // Expired but not ready either: still refused, for both reasons.
        assertFalse(main.copy(unresolved = listOf("base")).canConfirm(Instant.parse("2026-11-08T10:00:00Z")))
        // No expiry (an older hub) or one this app cannot read: confirmable as before; the hub still decides.
        val old = ExtensionPreview(previewId = "old")
        assertNull(old.expiresBy)
        assertTrue(old.canConfirm(Instant.parse("2100-01-01T00:00:00Z")))
        assertNull(old.copy(expiresAt = "soon").expiresBy)
    }

    @Test fun notReadyAndUnknownDependenciesNeverReadAsSatisfied() {
        val d = ExtensionDependency("base", name = "基础扩展", version = ">=1.0.0", state = "not_ready")
        assertEquals("基础扩展 · >=1.0.0 · 依赖未就绪", ExtensionModelText.dependency(res, d))
        assertEquals("基础扩展 · >=1.0.0 · 状态未知，重新预览", ExtensionModelText.dependency(res, d.copy(state = "new-state")))
        assertEquals("基础扩展 · >=1.0.0 · 已满足", ExtensionModelText.dependency(res, d.copy(state = "ok")))
        assertEquals("基础扩展 · >=1.0.0 · 状态未知，请刷新详情",
            ExtensionModelText.dependency(res, d.copy(state = "new-state"), installed = true))
        val preview = ExtensionPreview(depends = listOf(d))
        val guidance = "还有依赖未就绪，暂时不能安装；先完成依赖扩展的设置，确认其依赖也已就绪，再重新预览。"
        assertEquals(guidance, ExtensionModelText.blockedPreview(res, preview))
        assertEquals(guidance, ExtensionModelText.blockedPreview(res, ExtensionPreview(bundle = listOf(preview))))
        assertEquals("还有依赖没有满足，暂时不能安装；先启用所需扩展，或补齐市场后重新预览。",
            ExtensionModelText.blockedPreview(res, preview.copy(depends = listOf(d.copy(state = "disabled")))))
    }

    @Test fun switchingOneContributionPreservesUnsavedSettingsAndFailureKeepsItsState() = runTest {
        val contribution = ExtensionContribution("ui", "notes", enabled = true, canDisable = true)
        val detail = ExtensionDetail(HubExtension("example"), config = JsonObject(mapOf("label" to JsonPrimitive("saved"))), contributions = listOf(contribution))
        var fail = false
        val api = object : ExtensionApiStub() {
            override suspend fun extension(id: String) = detail
            override suspend fun setContribution(id: String, point: String, contribution: String, enabled: Boolean): ExtensionDetail {
                assertEquals("example", id); assertEquals("ui", point); assertEquals("notes", contribution)
                if (fail) throw IllegalStateException("没改成")
                return detail.copy(contributions = listOf(detail.contributions!!.single().copy(enabled = enabled)))
            }
        }
        val model = ExtensionModel(api, "example", res)
        model.load(); model.edit(listOf("label"), JsonPrimitive("draft"))
        model.setContribution(contribution, false)
        assertEquals(JsonPrimitive("draft"), model.state.value.draft["label"])
        assertFalse(model.state.value.detail!!.contributions!!.single().enabled)
        fail = true
        model.setContribution(contribution, true)
        assertFalse(model.state.value.detail!!.contributions!!.single().enabled)
        assertNotNull(model.state.value.error)
    }

    @Test fun declarationsFilterUnsafeThemesAndDoNotClearCachedChoicesOnNetworkFailure() = runTest {
        var fail = false
        val good = ExtensionTheme("example", "示例", "ocean", "海蓝", mapOf("accent" to "#0059A6"), mapOf("accent" to "#8BC4FF"))
        val api = object : ExtensionApiStub() {
            override suspend fun themes(): List<ExtensionTheme> {
                if (fail) throw IllegalStateException("离线")
                return listOf(good, good.copy(id = "bad", problems = listOf("低对比度")))
            }
            override suspend fun quickPrompts() = listOf(ExtensionQuickPrompt(id = "today", text = "总结今天"))
            override suspend fun triageTemplates() = listOf(ExtensionTriageTemplate(id = "quiet", text = "只看提到我的"))
        }
        val model = ExtensionDeclarationsModel(api)
        model.load()
        assertEquals(listOf(good), model.state.value.themes)
        assertTrue(model.state.value.themesLoaded)
        fail = true; model.load()
        assertEquals(listOf(good), model.state.value.themes)
        val old = ExtensionDeclarationsModel(object : ExtensionApiStub() {})
        old.load()
        assertEquals(emptyList<ExtensionTheme>(), old.state.value.themes)
        assertTrue(old.state.value.themesLoaded)
    }

    @Test fun blockedPreviewCannotBeConfirmedThroughTheModel() = runTest {
        val api = object : ExtensionApiStub() {
            override suspend fun previewFromMarket(market: String, extension: String) = ExtensionPreview(unresolved = listOf("missing"))
        }
        val model = CatalogExtensionModel(api, "market", "example", res)
        assertTrue(model.preview())
        assertNull(model.confirm())
        assertNotNull(model.state.value.preview)
    }
}
