package persona.feature.settings

import persona.core.navigation.SettingsPage
import org.junit.Assert.*
import org.junit.Test

class SettingsNavigationTest {
    @Test fun everyDestinationKeepsItsExistingPageNameAndRoundTrips() {
        val pages = mapOf(
            SettingsPage.Diagnostics to "diag",
            SettingsPage.Readiness to "ready",
            SettingsPage.Notifications to "notify",
            SettingsPage.PhoneData to "phone",
            SettingsPage.Appearance to "style",
            SettingsPage.Devices to "devices",
            SettingsPage.About to "about",
            SettingsPage.Pairing to "pair",
            SettingsPage.ClaudeCode to "claude",
            SettingsPage.ModelConfig to "models",
            SettingsPage.Hubs to "hubs",
            SettingsPage.Persona to "persona",
            SettingsPage.Extensions to "plugins",
            SettingsPage.Mail to "mail",
            SettingsPage.Feishu to "feishu",
            SettingsPage.Channels to "channels",
            SettingsPage.HubUpdate to "hub-update",
            SettingsPage.Translation to "translate",
            SettingsPage.Triage to "triage",
            SettingsPage.Capsule to "capsule",
            SettingsPage.Language to "language",
            SettingsPage.Codex to "codex",
            SettingsPage.Providers to "providers",
            SettingsPage.Storage to "storage",
            SettingsPage.Desktops to "desktops",
            SettingsPage.Managers to "managers",
            SettingsPage.FeishuBot to "feishu-bot",
            SettingsPage.Sent to "connectors/sent",
            SettingsPage.Connectors to "connectors",
        )
        assertEquals(SettingsPage.entries.toSet(), pages.keys)
        assertEquals(pages.size, pages.values.toSet().size)
        pages.forEach { (page, name) ->
            assertEquals(name, page.toInitialPage())
            assertEquals(page, settingsPageFromInitialPage(name))
            assertEquals(page, settingsPageFromInitialPage(page.toInitialPage()))
        }
    }

    @Test fun legacySentPageNamesResolveToTheNestedLedger() {
        assertEquals(SettingsPage.Sent, settingsPageFromInitialPage("sent"))
        assertEquals("connectors/sent", settingsPageFromInitialPage("sent")!!.toInitialPage())
    }

    @Test fun rootAndUnknownNamesHaveNoDetailDestination() {
        listOf("settings", "assistant", "", "unknown", "DIAG").forEach { assertNull(settingsPageFromInitialPage(it)) }
    }
}
