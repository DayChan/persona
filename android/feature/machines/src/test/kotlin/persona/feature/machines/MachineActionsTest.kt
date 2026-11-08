package persona.feature.machines

import androidx.lifecycle.viewModelScope
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.UiText
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.JsonElement
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class MachineActionsTest {
    private class Sessions(machines: List<Machine>) : SessionsRepository {
        override val machines = MutableStateFlow(machines)
        override val sessions = MutableStateFlow(emptyList<Session>())
        var refreshes = 0
        var onRefresh: () -> Unit = {}
        override suspend fun refresh() { refreshes++; onRefresh() }
    }

    private open class Management : MachineManagement {
        val calls = mutableListOf<String>()
        var failure: Exception? = null
        var onReconnect: () -> Unit = {}
        override suspend fun launchers(machineId: String): List<AgentLauncher> {
            calls += "launchers:$machineId"; failure?.let { throw it }
            return listOf(AgentLauncher("claude", model = "anthropic/claude-opus-5"), AgentLauncher("plain"))
        }
        override suspend fun launcherQuota(machineId: String, name: String, refresh: Boolean) = persona.core.contracts.api.LauncherQuotaAnswer()
        override suspend fun reconnect(machineId: String) { calls += "reconnect:$machineId"; failure?.let { throw it }; onReconnect() }
        override suspend fun deploy(machineId: String) { calls += "deploy:$machineId"; failure?.let { throw it } }
        override suspend fun rename(machineId: String, name: String): String {
            calls += "rename:$machineId:$name"; failure?.let { throw it }; return name.ifBlank { machineId }
        }
        override suspend fun herdr(machineId: String): persona.core.contracts.api.HerdrState = error("unused")
        override suspend fun startHerdr(machineId: String): persona.core.contracts.api.HerdrState = error("unused")
        override suspend fun setEngine(machineId: String, engine: String) = Machine(machineId, engine = engine)
        override suspend fun claudeSettings(machineId: String) = emptyList<ClaudeSetting>()
        override suspend fun setClaudeSetting(machineId: String, key: String, value: JsonElement) = emptyList<ClaudeSetting>()
        override suspend fun codexSettings(machineId: String) = emptyList<ClaudeSetting>()
        override suspend fun setCodexSetting(machineId: String, key: String, value: JsonElement) = emptyList<ClaudeSetting>()
        /** Null: an older hub without the route. */
        var botmuxOn: Boolean? = true
        var botmuxFailure: Exception? = null
        override suspend fun botmux(machineId: String): Boolean? { calls += "botmux:$machineId"; return botmuxOn }
        override suspend fun setBotmux(machineId: String, on: Boolean): Boolean {
            calls += "set-botmux:$machineId:$on"; botmuxFailure?.let { throw it }; botmuxOn = on; return on
        }
    }

    private fun <T> test(block: suspend TestScope.() -> T) = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        try { block() } finally { Dispatchers.resetMain() }
    }

    @Test fun reconnectWaitsForTheMachineToComeOnlineAndSaysSo() = test {
        val sessions = Sessions(listOf(Machine("lab", online = false, error = "ssh: connection refused")))
        val management = Management().apply { onReconnect = { sessions.machines.value = listOf(Machine("lab", online = true)) } }
        val model = MachineActionsViewModel(management, sessions)
        try {
            model.reconnect("lab")
            model.reconnect("lab")
            runCurrent()
            assertEquals(listOf("reconnect:lab"), management.calls)
            assertEquals(MachineNotice.Connected, model.notices.first())
            assertFalse(model.state.value.reconnecting)
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun reconnectReadsTheListWhenNoPushSaysTheMachineIsBack() = test {
        val sessions = Sessions(listOf(Machine("lab", online = false)))
        // The hub has it back, but no machines push arrives: the next read of the list shows it.
        sessions.onRefresh = { sessions.machines.value = listOf(Machine("lab", online = true)) }
        val model = MachineActionsViewModel(Management(), sessions)
        try {
            model.reconnect("lab")
            runCurrent()
            assertTrue(model.state.value.reconnecting)
            advanceTimeBy(3_001)
            runCurrent()
            assertEquals(MachineNotice.Connected, model.notices.first())
            assertEquals(1, sessions.refreshes)
            advanceTimeBy(30_000)
            runCurrent()
            assertEquals(1, sessions.refreshes)
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun reconnectGivesUpAfterTheWaitWithTheMachinesError() = test {
        val sessions = Sessions(listOf(Machine("lab", online = false, error = "ssh: connection refused")))
        val model = MachineActionsViewModel(Management(), sessions, reconnectWaitMillis = 15_000)
        try {
            model.reconnect("lab")
            runCurrent()
            assertTrue(model.state.value.reconnecting)
            advanceTimeBy(15_001)
            runCurrent()
            // The hub's error is read as the offline card reads it: why and what to do, not the raw English (#4537).
            assertEquals(MachineNotice.NotYet(UiText.Res(R.string.machines_offline_unreachable_ssh)), model.notices.first())
            assertFalse(model.state.value.reconnecting)
            val refused = Management().apply { failure = HubFailure(HubError.Server, "machine is offline", 503) }
            val other = MachineActionsViewModel(refused, sessions)
            try {
                other.reconnect("lab")
                runCurrent()
                assertEquals(MachineNotice.ReconnectFailed(UiText.Plain("machine is offline")), other.notices.first())
            } finally { other.viewModelScope.cancel() }
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun notYetSaysWhyInPlainWordsAndKeepsAnErrorItCannotRead() {
        assertEquals(UiText.Res(R.string.machines_offline_auth_link),
            notYetReason(Machine("mac", kind = "link", online = false, error = "pairing: 401 unauthorized")))
        assertEquals(UiText.Res(R.string.machines_offline_timeout),
            notYetReason(Machine("lab", online = false, error = "ssh: connect to host port 22: Operation timed out")))
        assertEquals(UiText.Res(R.string.machines_offline_version_local),
            notYetReason(Machine("lab", online = false, error = "protocol version mismatch")))
        // A known error_code wins, as on the card.
        assertEquals(UiText.Res(R.string.machines_onb_code_link_offline),
            notYetReason(Machine("mac", kind = "link", online = false, error = "dial tcp: connection refused", errorCode = "link_offline")))
        assertEquals(UiText.Plain("node said something new"), notYetReason(Machine("lab", online = false, error = "node said something new")))
        assertNull(notYetReason(Machine("lab", online = false, error = " ")))
        assertNull(notYetReason(Machine("lab", online = false)))
    }

    @Test fun deployRefreshesTheListAndReportsTheHubsReason() = test {
        val sessions = Sessions(listOf(Machine("mac", online = true, nodeMismatch = true)))
        val management = Management()
        val model = MachineActionsViewModel(management, sessions)
        try {
            model.deploy("mac")
            assertEquals(setOf("mac"), model.state.value.deployStarting)
            runCurrent()
            assertEquals(listOf("deploy:mac"), management.calls)
            assertEquals(1, sessions.refreshes)
            assertTrue(model.state.value.deployStarting.isEmpty())
            management.failure = HubFailure(HubError.Server, "这台电脑已经是 hub 这一版", 409)
            model.deploy("mac")
            runCurrent()
            assertEquals(MachineNotice.DeployFailed(UiText.Plain("这台电脑已经是 hub 这一版")), model.notices.first())
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun launchersAreReadOnlyOnlineAndFailuresSayWhy() = test {
        val management = Management()
        val model = MachineActionsViewModel(management, Sessions(emptyList()))
        try {
            model.loadLaunchers("mac", online = false)
            runCurrent()
            assertTrue(management.calls.isEmpty())
            assertNull(model.state.value.launchers)
            model.loadLaunchers("mac", online = true)
            runCurrent()
            assertEquals(listOf("claude", "plain"), model.state.value.launchers?.map { it.name })
            assertEquals(listOf("claude-opus-5", null), model.state.value.launchers?.map(::launcherModel))
            management.failure = HubFailure(HubError.Server, "mac: machine is offline", 503)
            model.loadLaunchers("mac", online = true)
            runCurrent()
            assertEquals(UiText.Plain("mac: machine is offline"), model.state.value.launchersError)
            // What was read before stays listed.
            assertEquals(2, model.state.value.launchers?.size)
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun renameSaysTheHubsNameAndRefreshesOrSaysWhyNot() = test {
        val sessions = Sessions(listOf(Machine("mac", online = true)))
        val management = Management()
        val model = MachineActionsViewModel(management, sessions)
        try {
            model.rename("mac", "  Studio  ")
            assertTrue(model.state.value.renaming)
            model.rename("mac", "again") // one at a time
            runCurrent()
            assertEquals(listOf("rename:mac:Studio"), management.calls)
            assertEquals(MachineNotice.Renamed("Studio"), model.notices.first())
            assertEquals(1, sessions.refreshes)
            assertFalse(model.state.value.renaming)
            model.rename("mac", "")
            runCurrent()
            assertEquals(MachineNotice.Renamed("mac"), model.notices.first())
            management.failure = HubFailure(HubError.Server, "The name can be at most 40 characters, on one line", 400)
            model.rename("mac", "x".repeat(41))
            runCurrent()
            assertEquals(MachineNotice.RenameFailed(UiText.Plain("The name can be at most 40 characters, on one line")), model.notices.first())
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun botmuxSwitchSavesAtOnceAndAFailedSaveFlipsBack() = test {
        val management = Management()
        val model = MachineActionsViewModel(management, Sessions(emptyList()))
        try {
            assertNull(model.state.value.botmux)
            model.loadBotmux("mac")
            runCurrent()
            assertEquals(true, model.state.value.botmux)
            model.setBotmux("mac", false)
            // Shown at once, before the hub answers; a second flip while saving is ignored.
            assertEquals(false, model.state.value.botmux)
            assertTrue(model.state.value.botmuxSaving)
            model.setBotmux("mac", true)
            runCurrent()
            assertEquals(listOf("botmux:mac", "set-botmux:mac:false"), management.calls)
            assertEquals(false, model.state.value.botmux)
            assertFalse(model.state.value.botmuxSaving)
            management.botmuxFailure = HubFailure(HubError.Server, "database is down", 500)
            model.setBotmux("mac", true)
            runCurrent()
            assertEquals(false, model.state.value.botmux)
            assertEquals(MachineNotice.BotmuxFailed(UiText.Plain("database is down")), model.notices.first())
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun anOlderHubOrAFailedReadShowsNoBotmuxSwitch() = test {
        val older = Management().apply { botmuxOn = null }
        val model = MachineActionsViewModel(older, Sessions(emptyList()))
        val broken = object : Management() {
            override suspend fun botmux(machineId: String): Boolean? = throw HubFailure(HubError.Server, "machine is offline", 503)
        }
        val other = MachineActionsViewModel(broken, Sessions(emptyList()))
        try {
            model.loadBotmux("mac")
            other.loadBotmux("mac")
            runCurrent()
            assertNull(model.state.value.botmux)
            assertNull(other.state.value.botmux)
            // Nothing to flip: no switch, no call.
            model.setBotmux("mac", false)
            runCurrent()
            assertEquals(listOf("botmux:mac"), older.calls)
        } finally { model.viewModelScope.cancel(); other.viewModelScope.cancel() }
    }

    @Test fun deployLineDiskShareAndBootTime() {
        assertTrue(showsDeploy(Machine("a", nodeMismatch = true)))
        assertTrue(showsDeploy(Machine("a", deploying = true)))
        assertFalse(showsDeploy(Machine("a")))
        assertFalse(showsDeploy(null))
        val points = listOf(MetricPoint(1, disk = 50.0), MetricPoint(2, gap = true), MetricPoint(3))
        assertEquals(listOf(25.0, null, null), diskPercentPoints(points, 200).map { it.disk })
        assertTrue(diskPercentPoints(points, null).all { it.disk == null })
        // The boot time's words are the language's (MachinesShotsTest reads them in each language).
        assertNull(MetricFormat.boot(0.0, java.util.Locale.ENGLISH))
        assertTrue(MetricKind.Disk.percent && MetricKind.Cpu.percent && !MetricKind.Memory.percent)
    }
}
