package persona.feature.knowledge

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.add
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.HubScope
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * Knowledge base: the list and an entry's page in each of the app's languages (docs/protocol/i18n.md), Glass light and dark and the other
 * styles light. Written as PNGs to `build/i18n-shots/` (or `-Dpersona.shots=<dir>`) to look at.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class KnowledgeShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1100dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", list = "知识库", detail = "问 Persona")

    @Config(sdk = [35], qualifiers = "en-w400dp-h1100dp-xhdpi")
    @Test fun english() = shots("en", list = "Knowledge", detail = "Ask Persona")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1100dp-xhdpi")
    @Test fun japanese() = shots("ja", list = "ナレッジ", detail = "Personaに質問")

    /** "Recordings": the kind's list and a recording still being transcribed, its bookmarks and transcript (#3235). */
    @Config(sdk = [35], qualifiers = "zh-w400dp-h1700dp-xhdpi")
    @Test fun recordingChinese() = recordingShots("zh", "转写中")

    @Config(sdk = [35], qualifiers = "en-w400dp-h1700dp-xhdpi")
    @Test fun recordingEnglish() = recordingShots("en", "Transcribing")

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1100dp-xhdpi")
    @Test fun availabilityStates() {
        var failure: String? = "kb_disabled"
        var dark by mutableStateOf(false)
        val repository = object : KnowledgeRepository by DemoKnowledge {
            override suspend fun list(kind: KnowledgeKind, tag: String?): KnowledgeList {
                when (failure) {
                    "empty" -> return KnowledgeList(emptyList(), emptyList())
                    "ready" -> return DemoKnowledge.list(kind, tag)
                    "network" -> throw java.io.IOException("offline")
                    else -> throw object : java.io.IOException(), HubCodedFailure {
                        override val code = failure
                        override val status = 503
                    }
                }
            }
        }
        val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(repository) }
        val model = KnowledgeViewModel(connection)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("shot").width(400.dp).height(1060.dp).background(PersonaTheme.palette.background)) {
                    KnowledgeScreen(model, NoNavigator)
                }
            }
        }
        for ((state, expected) in listOf("kb_disabled" to "知识库已关闭", "kb_unavailable" to "知识库正在启动，请稍后重试。",
            "kb_read_only" to "知识库为只读。请先修改管理者配置，再编辑或同步录音。",
            "assistant_offline" to "管理者不在线或正在重启，请稍后重试。",
            "network" to "暂时连不上知识库，请稍后重试", "empty" to "还没有项目", "ready" to "Weather App")) {
            failure = state
            model.refresh()
            rule.waitForIdle()
            rule.onNodeWithText(expected).assertExists()
            for (isDark in listOf(false, true)) {
                dark = isDark
                rule.waitForIdle()
                File(out(), "knowledge-$state-${if (isDark) "dark" else "light"}.png").outputStream().use {
                    rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it)
                }
            }
        }
    }

    private fun recordingShots(language: String, transcribing: String) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var page by mutableStateOf("list")
        val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(DemoKnowledge) }
        val listModel = KnowledgeViewModel(connection).also { it.kind(KnowledgeKind.Recordings) }
        val detailModel = KnowledgeDetailViewModel(KnowledgeKind.Recordings, "20261105-140000-watch", connection, NoSender)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1660.dp).background(PersonaTheme.palette.background)) {
                    if (page == "list") KnowledgeScreen(listModel, NoNavigator)
                    else KnowledgeDetailScreen(detailModel, NoNavigator, openLinkInApp = {})
                }
            }
        }
        for (name in listOf("list", "entry")) {
            page = name
            for ((s, d) in listOf(AppStyle.Glass to false, AppStyle.Glass to true, AppStyle.Brief to false, AppStyle.Board to false)) {
                style = s; dark = d
                rule.waitForIdle()
                rule.onNodeWithText(if (name == "list") "Weekly sync" else transcribing).assertExists()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                val file = "knowledge-recording-$name-$language-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
                File(out(), file).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
        }
    }

    private fun shots(language: String, list: String, detail: String) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var page by mutableStateOf("list")
        val connection = object : KnowledgeConnection { override val current = MutableStateFlow<KnowledgeRepository?>(DemoKnowledge) }
        val listModel = KnowledgeViewModel(connection)
        val detailModel = KnowledgeDetailViewModel(KnowledgeKind.Projects, "weather", connection, NoSender)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1060.dp).background(PersonaTheme.palette.background)) {
                    if (page == "list") KnowledgeScreen(listModel, NoNavigator)
                    else KnowledgeDetailScreen(detailModel, NoNavigator, openLinkInApp = {})
                }
            }
        }
        for (name in listOf("list", "entry")) {
            page = name
            for ((s, d) in listOf(AppStyle.Glass to false, AppStyle.Glass to true, AppStyle.Brief to false, AppStyle.Board to false)) {
                style = s; dark = d
                rule.waitForIdle()
                rule.onNodeWithText(if (name == "list") list else detail).assertExists()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                val file = "knowledge-$name-$language-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
                File(out(), file).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
        }
    }
}

private object NoNavigator : Navigator {
    override fun navigate(key: NavKey) = Unit
    override fun back() = Unit
    override fun selectTab(tab: AppTab) = Unit
}

private object NoSender : MainConversationSender {
    override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
    override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
}

/** Made-up projects: one important, one ongoing, one done, one that is not a project. */
private object DemoKnowledge : KnowledgeRepository {
    override val scope = HubScope("demo", "shots")
    private val projects = listOf(
        KnowledgeEntry("projects", "weather", "Weather App", "Widgets and a watch face for the forecast", "P0", 92,
            listOf("ios", "widget"), "2026-11-04T09:30:00+08:00", "short · active"),
        KnowledgeEntry("projects", "site", "Website", "The landing page and docs", "P2", 45, listOf("web"), "2026-11-01T18:05:00+08:00", "long · active"),
        KnowledgeEntry("projects", "launch", "Beta launch", "Shipped to testers", "P1", 60, emptyList(), "2026-10-20T10:00:00+08:00", "short · done"),
        KnowledgeEntry("projects", "promo", "Newsletter", null, "P3", 5, emptyList(), null, "short · ignored"),
    )
    private val recordings = listOf(
        KnowledgeEntry("recordings", "20261105-140000-watch", "Weekly sync", "Release dates and who tests the widgets", "P1", 70,
            listOf("meeting"), "2026-11-05T14:00:00+08:00", "watch"),
        KnowledgeEntry("recordings", "20261104-091500-phone", "Voice memo", "Ideas for the watch face", null, 0,
            emptyList(), "2026-11-04T09:15:00+08:00", "phone"),
    )
    override suspend fun list(kind: KnowledgeKind, tag: String?) = KnowledgeList(when (kind) {
        KnowledgeKind.Projects -> projects
        KnowledgeKind.Recordings -> recordings
        else -> emptyList()
    }, listOf("ios", "web"))
    override suspend fun detail(kind: KnowledgeKind, id: String) = if (kind == KnowledgeKind.Recordings) recording(id) else project(kind, id)

    private fun recording(id: String) = KnowledgeDetail("recordings", id, "~/.persona/data/recordings/$id",
        meta = buildJsonObject {
            put("id", id); put("name", id); put("title", "Weekly sync"); put("one_line", "Release dates and who tests the widgets")
            put("importance", "P1"); put("relevance", 70); put("started_at", "2026-11-05T14:00:00+08:00"); put("duration", 1925)
            put("language", "en-US"); put("device", "watch"); put("complete", false)
            put("tags", buildJsonArray { add("meeting") })
            put("bookmarks", buildJsonArray {
                add(buildJsonObject { put("at", 95); put("label", "Release date") })
                add(buildJsonObject { put("at", 610) })
            })
        },
        summary = "- Widgets ship on Monday\n- Alex tests the watch face",
        segments = listOf(
            KnowledgeSegment(1.0, 4.0, "Morning, everyone. Let's start with the release."),
            KnowledgeSegment(95.5, 99.0, "The widgets are ready, so Monday works."),
            KnowledgeSegment(612.0, 618.0, "Alex, can you test the watch face this week?"),
        ))

    private fun project(kind: KnowledgeKind, id: String) = KnowledgeDetail(kind.wire, id, "~/.persona/data/projects/weather",
        meta = buildJsonObject {
            put("name", "Weather App"); put("one_line", "Widgets and a watch face for the forecast"); put("importance", "P0")
            put("relevance", 92); put("kind", "short"); put("status", "active"); put("updated", "2026-11-04T09:30:00+08:00")
            put("tags", buildJsonArray { add("ios"); add("widget") })
            put("dirs", buildJsonArray { add("laptop:/home/me/code/weather/app") })
        },
        summary = "# Weather App\n\n- Widgets ship next week\n- Watch face in review",
        log = (1..8).map { "- 2026-11-0$it 10:00 · Agent · laptop · Step $it done" },
        links = listOf(KnowledgeEntry("people", "alex", "Alex", "Designer"), KnowledgeEntry("docs", "spec", "Widget spec")))
    override suspend fun search(query: String) = emptyList<KnowledgeHit>()
}
