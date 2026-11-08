package persona.feature.assistant.settings

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.*

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1800dp")
class AssistantSettingsManagerTest {
    @get:Rule val rule = createComposeRule()

    internal class Hub(
        private val values: MutableMap<String, AssistantConfig>,
        private val owner: String = "assistant",
    ) : AssistantManagement {
        override fun forManager(manager: String?) = Hub(values, requireNotNull(manager))
        override suspend fun config() = values.getValue(owner)
        override suspend fun updateConfig(update: AssistantConfigUpdate): AssistantConfig {
            val previous = values.getValue(owner)
            return previous.copy(defaultPermissionMode = update.defaultPermissionMode ?: previous.defaultPermissionMode,
                timezone = update.timezone ?: previous.timezone).also { values[owner] = it }
        }
        override suspend fun memory() = emptyList<MemoryFile>()
        override suspend fun warm() {}
        override suspend fun endConversation(session: SessionKey) {}
        override suspend fun tasks() = listOf(ScheduledTask(id = "daily", title = "Daily review", prompt = "Review",
            repeats = ScheduledTask.DAILY, time = "09:00", enabled = true))
        override suspend fun saveTask(task: ScheduledTask) = task
        override suspend fun deleteTask(id: String) {}
        override suspend fun runTask(id: String) = SessionKey("computer", id)
        override suspend fun freshTask(id: String) = tasks().first()
        override suspend fun runs() = emptyList<Run>()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) {}
        override suspend fun saveMemory(name: String, content: String) {}
        override suspend fun deleteMemory(name: String) {}
    }

    @Test fun unknownPermissionSupportKeepsTaskModelNavigationScoped() {
        val values = mutableMapOf(
            "assistant" to AssistantConfig(manager = "assistant", defaultPermissionMode = "plan", main = "personal-history"),
            "work" to AssistantConfig(manager = "work", defaultPermissionMode = "default", main = "work-history"),
        )
        val hub = Hub(values)
        val roster = ManagerRoster(listOf(HubManager("assistant", "Personal", online = true, isDefault = true),
            HubManager("work", "Work", online = true)), "assistant")
        var opened: Pair<String, String>? = null
        rule.setContent {
            CompositionLocalProvider(LocalManagerRoster provides roster) {
                PersonaTheme(reduceMotion = true) {
                    AssistantSettingsScreen(hub, emptyList(), onBack = {}, onModelConfiguration = { purpose, owner -> opened = purpose to owner })
                }
            }
        }
        rule.waitForIdle()
        rule.onAllNodesWithText("Work").onLast().performClick()
        rule.waitForIdle()
        rule.onAllNodesWithText("New conversation permissions").onLast().assertIsNotEnabled()
        rule.onNodeWithText("Unavailable until this assistant can confirm support for permission defaults. Other settings remain available.").assertExists()
        assertEquals("default", values.getValue("work").defaultPermissionMode)
        assertEquals("plan", values.getValue("assistant").defaultPermissionMode)
        assertEquals("work-history", values.getValue("work").main)
        rule.onNodeWithText("Scheduled tasks, follow-ups and runs").performScrollTo().performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Daily review").performScrollTo().performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Task model").performScrollTo().performClick()
        rule.waitForIdle()
        assertEquals("task.daily" to "work", opened)
    }
    @Test fun supportedManagersCanSaveThroughProductionAdapterAndKeepSwitcher() {
        val values = mutableMapOf(
            "assistant" to AssistantConfig(manager = "assistant", defaultPermissionMode = "default", main = "personal-history",
                permissionDefaults = AssistantPermissionDefaults(1, "assistant", AssistantPermissionDefaults.MODES)),
            "work" to AssistantConfig(manager = "work", defaultPermissionMode = "plan", main = "work-history",
                permissionDefaults = AssistantPermissionDefaults(1, "work", AssistantPermissionDefaults.MODES)),
        )
        val roster = ManagerRoster(listOf(HubManager("assistant", "Personal", online = true, isDefault = true),
            HubManager("work", "Work", online = true)), "assistant")
        rule.setContent {
            CompositionLocalProvider(LocalManagerRoster provides roster) {
                PersonaTheme(reduceMotion = true) {
                    AssistantSettingsScreen(Hub(values), emptyList(), onBack = {})
                }
            }
        }
        rule.waitForIdle()
        // Exactly one title and one chip per manager; there is no separate owner row.
        rule.onAllNodesWithText("Personal").assertCountEquals(1)
        rule.onAllNodesWithText("Work").assertCountEquals(1)
        rule.onAllNodesWithText("New conversation permissions").onLast().assertIsEnabled().performClick()
        rule.onNodeWithText("Allow edits").performClick()
        rule.waitForIdle()
        assertEquals("acceptEdits", values.getValue("assistant").defaultPermissionMode)
        rule.onNodeWithText("Work").performClick()
        rule.waitForIdle()
        rule.onAllNodesWithText("New conversation permissions").onLast().assertIsEnabled().performClick()
        rule.onNodeWithText("Full access").performClick()
        rule.waitForIdle()
        assertEquals("bypassPermissions", values.getValue("work").defaultPermissionMode)
        rule.onNodeWithText("Personal").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Allow edits").assertExists()
        assertEquals("personal-history", values.getValue("assistant").main)
        assertEquals("work-history", values.getValue("work").main)
    }

}
