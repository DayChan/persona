package persona.feature.machines

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
import androidx.compose.ui.test.onAllNodesWithText
import kotlinx.coroutines.launch
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonElement
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.time.Instant

/**
 * The machine pages' parity with the web / iOS (#3038): "Subscription limits" on the overview, "What this computer can do" (features, "Fix", all checks)
 * and "Browser" (ego lite's first-run setup) on a Mac's page. Every style in light and dark, to `build/parity-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class MachinesParityShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/parity-shots").also { it.mkdirs() }

    private val now = 1_793_600_000L
    private fun at(offsetSeconds: Long) = Instant.ofEpochSecond(now + offsetSeconds).toString()
    private val mac = Machine("mac", name = "MacBook", kind = "local", online = true, engine = "herdr",
        system = MachineSystem(os = "darwin", distro = "macos", name = "macOS 26", cores = 10, memory = 32L shl 30),
        browser = MachineBrowser(enabled = true, installed = true, version = "0.6.2", job = MachineJob("open", "done")))
    private val report = DoctorReport(ok = false, checks = listOf(
        DoctorCheck("node.sock", "ok", detail = "listening"),
        DoctorCheck("claude-settings.json", "warn", state = "stale", detail = "hooks differ from this version"),
        DoctorCheck("claude-hooks", "ok", detail = "6 hooks"),
        DoctorCheck("command-paths", "ok", detail = "personad found"),
        DoctorCheck("herdr", "ok", detail = "herdr 0.9.1"),
        DoctorCheck("mcp.json", "ok", detail = "current", state = "current"),
        DoctorCheck("agent-plugin", "ok", detail = "installed"),
        DoctorCheck("claude-version", "ok", detail = "2.1.0"),
        DoctorCheck("runtime.sock", "warn", detail = "no runtime connected"),
        DoctorCheck("global-permission-hooks", "warn", detail = "Your own settings allow every Bash command", hint = "Approvals for Bash won't reach the phone"),
    ))
    private val accounts = listOf(
        UsageAccount("a", "claude", "Claude Max", plan = "max", sampledAt = at(-40), local = true, machines = listOf("MacBook"), sources = listOf("gateway"),
            windows = listOf(UsageWindow("5h", 300, 86.0, at(3600)), UsageWindow("7d", 10080, 41.0, at(3 * 86400 + 7200)))),
        UsageAccount("b", "codex", "Codex Pro", plan = "pro", sampledAt = at(-600), stale = true, problem = "rejected", sources = listOf("Relay"),
            windows = listOf(UsageWindow("5h", 300, 12.0, at(1800)))),
    )
    private val api = object : MachineMetricsApi {
        override suspend fun dashboard() = MetricsDashboard(listOf(MachineMetrics("mac", true, mac.system)), step = 10)
        override suspend fun history(machineId: String, range: MetricRange) = MetricsHistory(MachineMetrics("mac", true, mac.system), range.wire, range.step)
    }
    private val repository = object : SessionsRepository {
        override val machines = MutableStateFlow(listOf(mac))
        override val sessions = MutableStateFlow(emptyList<Session>())
        override suspend fun refresh() = Unit
    }
    private val management = object : MachineManagement {
        override suspend fun launchers(machineId: String) = emptyList<AgentLauncher>()
        override suspend fun launcherQuota(machineId: String, name: String, refresh: Boolean) = LauncherQuotaAnswer()
        override suspend fun reconnect(machineId: String) = Unit
        override suspend fun deploy(machineId: String) = Unit
        override suspend fun herdr(machineId: String): HerdrState = error("unused")
        override suspend fun startHerdr(machineId: String): HerdrState = error("unused")
        override suspend fun setEngine(machineId: String, engine: String) = Machine(machineId, engine = engine)
        override suspend fun claudeSettings(machineId: String) = emptyList<ClaudeSetting>()
        override suspend fun setClaudeSetting(machineId: String, key: String, value: JsonElement) = emptyList<ClaudeSetting>()
        override suspend fun codexSettings(machineId: String) = emptyList<ClaudeSetting>()
        override suspend fun setCodexSetting(machineId: String, key: String, value: JsonElement) = emptyList<ClaudeSetting>()
        override suspend fun doctor(machineId: String) = report
        override suspend fun browser(machineId: String) = mac.browser!!
        override suspend fun usageWindows() = accounts
    }
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h3000dp-xhdpi")
    @Test fun parity() {
        val list = MachinesViewModel(api, repository, null) { now }
        val page = MachinesViewModel(api, repository, "mac") { now }
        runBlocking { list.load(MetricRange.Hour); page.load(MetricRange.Hour) }
        val listHealth = MachineHealthViewModel(management)
        val pageHealth = MachineHealthViewModel(management)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var detailPage by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(if (detailPage) 3000.dp else 1100.dp).background(PersonaTheme.palette.background)) {
                    if (detailPage) MachinesScreen("mac", page, navigator, health = pageHealth)
                    else MachinesScreen(null, list, navigator, health = listHealth)
                }
            }
        }
        // the overview polls once a minute from its route; here once
        rule.runOnIdle { kotlinx.coroutines.MainScope().launch { kotlinx.coroutines.withTimeoutOrNull(100) { listHealth.pollUsage() } } }
        for (d in listOf(false, true)) for (s in AppStyle.entries) for (dk in listOf(false, true)) {
            rule.runOnIdle { detailPage = d; style = s; dark = dk }
            rule.waitForIdle()
            val expected = if (d) listOf("这台电脑能做什么", "1 项功能需要处理", "修复", "没有设置：运行时接入", "浏览器", "打开 ego lite")
                else listOf("订阅额度", "Claude Max", "86%", "用得偏快", "已过期", "MacBook · gateway", "网关不认这把 Key")
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "machines-${if (d) "detail" else "list"}-${s.name.lowercase()}-${if (dk) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            expected.forEach { assertTrue("$d shows $it", rule.onAllNodesWithText(it, substring = true).fetchSemanticsNodes().isNotEmpty()) }
        }
    }
}
