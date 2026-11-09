package persona.android.debug

import android.content.Intent
import android.net.Uri
import persona.android.navigation.DeveloperTools
import persona.android.navigation.LaunchAdapter
import persona.android.navigation.LaunchInput
import persona.android.navigation.notificationRoute
import persona.android.navigation.open
import persona.android.navigation.terminalLink
import persona.core.navigation.MachinesRoute
import persona.core.navigation.RouteInput
import persona.feature.knowledge.KnowledgeRoute
import persona.platform.phonetools.PhoneToolsRoute
import persona.core.contracts.api.PairingRequest
import persona.core.navigation.AppTab
import persona.core.navigation.ResolvedRoute
import persona.core.navigation.parseDebug
import java.net.URI

/** Pages without a public deep link, for direct captures. */
private val pages = mapOf(
    "machines" to ResolvedRoute(AppTab.Agents, MachinesRoute),
    "knowledge" to ResolvedRoute(AppTab.Persona, KnowledgeRoute),
    "phone" to ResolvedRoute(AppTab.Settings, PhoneToolsRoute),
    "mail" to ResolvedRoute(AppTab.Settings, persona.core.navigation.SettingsPageRoute(persona.core.navigation.SettingsPage.Mail)),
    "feishu" to ResolvedRoute(AppTab.Settings, persona.core.navigation.SettingsPageRoute(persona.core.navigation.SettingsPage.Feishu)),
    "catalog" to ResolvedRoute(AppTab.Settings, ComponentCatalogRoute),
    "voice" to ResolvedRoute(AppTab.Persona, persona.core.navigation.VoiceRoute("voice", "语音")),  // i18n-ignore: debug launch arguments, developer-only
    "channels" to ResolvedRoute(AppTab.Settings, persona.core.navigation.SettingsPageRoute(persona.core.navigation.SettingsPage.Channels)),
    "feishu-bot" to ResolvedRoute(AppTab.Settings, persona.core.navigation.SettingsPageRoute(persona.core.navigation.SettingsPage.FeishuBot)),
    "managers" to ResolvedRoute(AppTab.Settings, persona.core.navigation.SettingsPageRoute(persona.core.navigation.SettingsPage.Managers)),
    "connectors" to ResolvedRoute(AppTab.Settings, persona.core.navigation.SettingsPageRoute(persona.core.navigation.SettingsPage.Connectors)),
    "connectors/sent" to ResolvedRoute(AppTab.Settings, persona.core.navigation.SettingsPageRoute(persona.core.navigation.SettingsPage.Sent)),
    "sent" to ResolvedRoute(AppTab.Settings, persona.core.navigation.SettingsPageRoute(persona.core.navigation.SettingsPage.Sent)),
    "hub-update" to ResolvedRoute(AppTab.Settings, persona.core.navigation.SettingsPageRoute(persona.core.navigation.SettingsPage.HubUpdate)),
    "translate" to ResolvedRoute(AppTab.Settings, persona.core.navigation.SettingsPageRoute(persona.core.navigation.SettingsPage.Translation)),
    "triage" to ResolvedRoute(AppTab.Settings, persona.core.navigation.SettingsPageRoute(persona.core.navigation.SettingsPage.Triage)),
)

class DebugLaunchAdapter : LaunchAdapter {
    override val tools = DebugStatePreview()

    @Suppress("DEPRECATION")
    override fun read(intent: Intent): LaunchInput? {
        val extras = intent.extras ?: return null
        val arguments = extras.keySet().filter { it.startsWith("debug.") }.associateWith {
            extras.get(it)?.toString().orEmpty()
        }.toMutableMap()
        intent.getStringExtra("openSession")?.let { arguments["debug.openSession"] = it }
        if (arguments.isEmpty()) return null
        val state = arguments["debug.state"]
        require(state == null || state in DebugStatePreview.states) { "未知的调试状态" }  // i18n-ignore: debug launch arguments, developer-only
        require(state == null || arguments["debug.mockAddress"] == null) { "状态预览使用本地演示数据，请勿同时传入假 hub 地址" }  // i18n-ignore: debug launch arguments, developer-only
        tools.state = state
        if (state != null && arguments.keys.none { it.startsWith("debug.open") }) {
            arguments["debug.openTab"] = if (state in setOf("load-failed", "loading")) "Inbox" else "Agents"
        }
        val address = arguments["debug.mockAddress"]
        val pairing = address?.let {
            val uri = URI(it)
            val loopback = byteArrayOf(127, 0, 0, 1).joinToString(".") { byte -> byte.toString() }
            require(uri.scheme == "http" && uri.host == loopback && uri.userInfo == null &&
                uri.rawPath in listOf("", "/") && uri.query == null && uri.fragment == null && uri.port in 1..65535)
            PairingRequest(it, "mock")
        }
        // App-only pages and links are resolved here instead of by the shared parser.
        val catalog = arguments.remove("debug.openCatalog") == "true"
        val link = arguments.remove("debug.openLink")
        val page = arguments.remove("debug.openPage")?.let { requireNotNull(pages[it] ?: managementPage(it)) { "Unknown debug page" } }
        // Settings › Assistant opens one of its parts for captures: notes, memory, memory-file, memory-edit, memory-delete, machine, launcher, timezone, move.
        arguments.remove("debug.assistantSettings")?.let { persona.feature.assistant.settings.AssistantSettingsPreview.open = it }
        // "Model configuration" opens "Add model provider" (manual | preset) with persona://open?target=settings&page=modelconfig.
        arguments.remove("debug.newProvider")?.let { persona.feature.settings.NewProviderPreview.open = it }
        arguments.remove("debug.translateChats")?.let { persona.feature.settings.TranslationPreview.picking = it == "1" }
        // "New session" (debug.openPage new-agent) takes steps once loaded: launcher:<name>, effort:<level>, model:<id>, models[:<query>].
        arguments.remove("debug.newAgent")?.let { persona.feature.agents.NewAgentPreview.steps = it }
        // "Voice chat" on an emulator without audio (-no-audio): silence up, nothing played, a made-up level for the orb.
        if (arguments.remove("debug.voiceAudio") == "fake") persona.feature.voice.VoiceDebug.useFakeAudio()
        return LaunchInput(
            resolve = { router ->
                when {
                    catalog -> ResolvedRoute(AppTab.Settings, ComponentCatalogRoute)
                    page != null -> page
                    // The same order as MainActivity: terminal fallback, notification tab, then general deep links.
                    link != null -> terminalLink(link) ?: notificationRoute(Uri.parse(link))?.let { router.open(it) }
                        ?: router.parse(RouteInput.DeepLink(link))
                    else -> router.parseDebug(arguments)
                }
            },
            darkTheme = when (arguments["debug.theme"]) { "dark" -> true; "light" -> false; else -> null },
            automation = state != null || arguments["debug.automation"] == "true", pairing = pairing,
            prepare = if (state == null) null else { shell -> shell.restoreDemo(); shell.enterDemo() },
            posture = arguments["debug.posture"]?.takeIf { it == "tabletop" || it == "book" },
            style = arguments["debug.style"]?.let { key ->
                requireNotNull(persona.core.designsystem.AppStyle.entries.firstOrNull { it.key == key }) { "未知的界面风格" }  // i18n-ignore: debug launch arguments, developer-only
            },
        )
    }
}
