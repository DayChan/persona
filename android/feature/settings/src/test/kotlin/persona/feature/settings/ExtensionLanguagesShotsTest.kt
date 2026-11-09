package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.*
import persona.core.designsystem.*
import java.io.File

/**
 * Settings › Extensions in each of the app's languages (docs/protocol/i18n.md): the list, an extension's page (switch, form, secrets,
 * contributions, sandbox, permissions, about), the market, one of its extensions, the markets, and the settings home's
 * "Extensions" section with a card that failed. Every style light and dark, written as PNGs to `build/i18n-shots/` (or
 * `-Dpersona.shots=<dir>`). The extensions' own words (names, form labels) are data and stay as the hub sends them.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ExtensionLanguagesShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    private val notes = HubExtension("example-notes", "Example Notes", "connectors", "needs_setup", version = "1.2.0", source = "installed",
        enabled = true, description = "Notes from an example service.", contributes = mapOf("connectors" to listOf("notes")),
        needsSetup = listOf("API Key"), sandbox = ExtensionSandbox(enabled = true, active = true, egress = ExtensionEgress(allowed = 12, denied = 1,
            lastDenied = "tracker.example.net")))
    private val list = listOf(
        notes,
        HubExtension("voice", "Voice", "voice", "running", version = "2.0.0", source = "builtin", enabled = true),
        HubExtension("example-gateway", "Example Gateway", "ui", "stopped", version = "1.0.0", source = "installed", enabled = true),
        HubExtension("example-broken", "Example Broken", "connectors", "error", source = "installed", error = "exit status 1"),
        HubExtension("example-off", "Example Off", "connectors", "disabled", source = "hub.json", enabled = false),
    )
    private val detail = ExtensionDetail(notes, publisher = "Example Inc.", homepage = "https://notes.example.com", origin = "market:3f2a9c1d0b",
        previousVersion = "1.1.0",
        form = listOf(
            ExtensionField("base_url", "string", title = "base_url", format = "uri", default = JsonPrimitive("https://notes.example.com"), required = true),
            ExtensionField("every", "integer", title = "every"),
            ExtensionField("folders", "array", title = "folders", items = ExtensionItems("string")),
            ExtensionField("archive", "boolean", title = "archive"),
        ),
        config = JsonObject(mapOf("every" to JsonPrimitive(30), "folders" to kotlinx.serialization.json.JsonArray(listOf(JsonPrimitive("inbox"))))),
        secrets = listOf(ExtensionSecret("api_key", "API Key", required = true), ExtensionSecret("webhook", "Webhook", set = true)),
        permissions = listOf(ExtensionPermission("network", "notes.example.com")),
        contributions = listOf(
            ExtensionContribution("connectors", "notes", "Notes", enabled = true, canDisable = true),
            ExtensionContribution("themes", "paper", "Paper", enabled = true, via = "config"),
            ExtensionContribution("connectors", "mail", "Mail", enabled = false, via = "connector"),
        ),
    )
    private val marketA = ExtensionMarket("3f2a9c1d0b", "https://plugins.example.com/persona/", "Example", extensions = 3,
        fetchedAt = "2026-11-04T06:00:00Z", signature = MarketSignature(state = "verified"), official = true)
    private val marketB = ExtensionMarket("8e01b7c4a2", "github:example/team-plugins", "Team", hasToken = true, extensions = 2,
        signature = MarketSignature(state = "mismatch"))
    private val catalog = listOf(
        CatalogExtension(marketA.id, "Example", "example-calendar", "Example Calendar", "0.9.0", description = "Events from an example calendar.",
            tags = listOf("calendar"), permissions = listOf(ExtensionPermission("network", "calendar.example.com")),
            contributes = mapOf("connectors" to listOf("calendar"))),
        CatalogExtension(marketA.id, id = "example-gateway", name = "Example Gateway", version = "1.1.0", installed = "1.0.0",
            installedSource = "installed", fromThisMarket = true, upgradable = true),
        CatalogExtension(marketA.id, id = "example-notes", name = "Example Notes", version = "1.2.0", installed = "1.2.0", installedSource = "builtin"),
        CatalogExtension(marketB.id, id = "team-windows", name = "Windows tools", version = "0.2.0", problem = "linux-amd64"),
    )

    private val api = object : ExtensionApiStub() {
        override suspend fun listing() = ExtensionListing(list, markets = true)
        override suspend fun extensions() = list
        override suspend fun extension(id: String) = detail
        override suspend fun catalog(query: String, market: String?, refresh: Boolean) =
            ExtensionCatalog(listOf(marketA, marketB), catalog.filter { market == null || it.market == market }, "linux-amd64")
        override suspend fun markets() = listOf(marketA, marketB)
    }

    private val slots = ExtensionSlotsState(available = true, places = listOf(
        ExtensionUiPlace("example-gateway", "today", "Today", "overview_card", extensionName = "Example Gateway", size = "compact"),
        ExtensionUiPlace("example-gateway", "usage", "Usage", "nav_page", extensionName = "Example Gateway", icon = "chart"),
    ))

    @Config(sdk = [35], qualifiers = "zh-w400dp-h2400dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", listOf("扩展市场", "这台 hub 的扩展", "需要设置"), listOf("运行隔离", "密钥", "提供", "信息", "扩展配置"),
        listOf("可以安装的扩展", "可升级"), listOf("这台 hub 读的市场", "官方"), listOf("扩展", "重试"))

    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi")
    @Test fun english() = shots("en", listOf("Extension market", "This hub's extensions", "Needs setup"), listOf("Isolation", "Secrets", "Provides", "Information", "Extension configuration"),
        listOf("Extensions to install", "Upgrade available"), listOf("Markets this hub reads", "Official"), listOf("Extensions", "Retry"))

    @Config(sdk = [35], qualifiers = "ja-w400dp-h2400dp-xhdpi")
    @Test fun japanese() = shots("ja", listOf("拡張機能マーケット", "このハブの拡張機能", "設定が必要"), listOf("実行の分離", "シークレット", "提供機能", "情報", "拡張機能の設定"),
        listOf("インストールできる拡張機能", "アップグレード可"), listOf("このハブが読むマーケット", "公式"), listOf("拡張機能", "再試行"))

    private enum class Page { List, Detail, Market, Catalog, Markets, Slots }

    private fun shots(language: String, list: List<String>, detail: List<String>, market: List<String>, markets: List<String>, slots: List<String>) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var page by mutableStateOf(Page.List)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(2200.dp).background(PersonaTheme.palette.background)) {
                    when (page) {
                        Page.List -> ExtensionsPage(api, onBack = {}, onExtension = { _, _ -> }, onMarket = {})
                        Page.Detail -> ExtensionPage(api, "example-notes", onBack = {}, onPage = {})
                        Page.Market -> ExtensionMarketPage(api, onBack = {}, onExtension = {}, onMarkets = {})
                        Page.Catalog -> ExtensionCatalogPage(api, marketA.id, "example-gateway", onBack = {}, onInstalled = {})
                        Page.Markets -> ExtensionMarketsPage(api, onBack = {})
                        Page.Slots -> SlotsSection()
                    }
                }
            }
        }
        val expect = mapOf(Page.List to list, Page.Detail to detail, Page.Market to market, Page.Catalog to emptyList(),
            Page.Markets to markets, Page.Slots to slots)
        for (p in Page.entries) for (s in AppStyle.entries) for (d in listOf(false, true)) {
            page = p; style = s; dark = d
            rule.waitForIdle()
            expect.getValue(p).forEach { assertTrue("$language ${p.name}: $it", rule.onAllNodesWithText(it).fetchSemanticsNodes().isNotEmpty()) }
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val name = "extensions-${p.name.lowercase()}-$language-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
            File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    /** The settings home's "Extensions" section: a card whose view could not be read ("Retry"), a nav page. */
    @Composable
    private fun SlotsSection() {
        val failed = ExtensionViewState(error = ExtensionFailure(R.string.settings_ext_load_failed, "hub"))
        val handle = ExtensionViewHandle({ _, _ -> }, { _, _ -> }, {}, {})
        LazyColumn(Modifier.fillMaxWidth(), contentPadding = PaddingValues(Tokens.Space.l)) {
            extensionSlotItems(slots, listOf(ExtensionCardSlot(slots.cards.single(), failed, handle) {}), selected = { false }, onOpen = {})
        }
    }
}
