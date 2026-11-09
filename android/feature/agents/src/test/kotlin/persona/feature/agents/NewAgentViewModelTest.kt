package persona.feature.agents

import androidx.lifecycle.viewModelScope
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.*
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import java.io.IOException

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class NewAgentViewModelTest {
    @Test fun historyResumePersistsBeforePostingAndReopeningOnlyQueriesItsReceipt() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = MemoryStore()
        val service = FakeService().apply { beforeStart = { assertNotNull(store.operation) } }
        val machines = MutableStateFlow(listOf(Machine("first", online = true)))
        val history = Session(machineId = "first", sessionId = "history", status = "offline", cwd = "/code/project", launcher = "claude")
        val model = NewAgentViewModel(service, store, machines)
        var restored: NewAgentViewModel? = null
        try {
            advanceUntilIdle()
            assertEquals(0, service.starts)
            model.resume(history)
            model.resume(history)
            advanceUntilIdle()
            assertEquals(1, service.starts)
            val pending = requireNotNull(store.operation)
            assertEquals("history", pending.request.resumeSessionId)
            assertNull(pending.request.prompt)
            assertNull(pending.request.workspaceId)
            model.resume(history)
            advanceUntilIdle()
            assertEquals(1, service.starts)
            assertEquals(pending.id, service.queried)
            model.viewModelScope.cancel()
            val reopened = NewAgentViewModel(service, store, machines)
            restored = reopened
            advanceUntilIdle()
            service.result = AgentStartResult.Created(history.copy(sessionId = "resumed", status = "idle"))
            reopened.resume(history)
            advanceUntilIdle()
            assertEquals(1, service.starts)
            assertEquals(pending.id, service.queried)
            assertEquals("resumed", reopened.state.value.created?.sessionId)
            assertEquals(pending, store.operation)
            assertTrue(store.saved.isEmpty())
            reopened.opened()
            advanceUntilIdle()
            assertNull(store.operation)
        } finally {
            model.viewModelScope.cancel()
            restored?.viewModelScope?.cancel()
            Dispatchers.resetMain()
        }
    }

    @Test fun selectedAgentTypeIsSavedWithoutClaudeStartupOptions() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = MemoryStore()
        val service = FakeService().apply { available = listOf(AgentLauncher("wrapper", agentType = "codex")) }
        val model = NewAgentViewModel(service, store, MutableStateFlow(listOf(Machine("first", online = true))))
        try {
            advanceUntilIdle()
            model.selection(model.state.value.selection.copy(mode = "plan", effort = "high"))
            model.submit()
            advanceUntilIdle()
            assertEquals("codex", store.operation?.request?.agentType)
            assertNull(store.operation?.request?.permissionMode)
            assertNull(store.operation?.request?.effort)
            model.submit()
            advanceUntilIdle()
            assertEquals(1, service.starts)
            assertEquals(store.operation?.id, service.queried)
        } finally {
            model.viewModelScope.cancel()
            Dispatchers.resetMain()
        }
    }

    @Test fun aCodexStartKeepsTheMachinesClaudeModeAndEffort() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = MemoryStore().apply { saved["first"] = AgentDefaults("/code/old", "claude", "plan", "high") }
        val service = FakeService().apply {
            available = listOf(AgentLauncher("claude"), AgentLauncher("codex", agentType = "codex"))
            result = AgentStartResult.Created(Session(machineId = "first", sessionId = "codex-1"))
        }
        val model = NewAgentViewModel(service, store, MutableStateFlow(listOf(Machine("first", online = true))))
        try {
            advanceUntilIdle()
            model.selection(model.state.value.selection.copy(cwd = "/code/new", launcher = "codex"))
            model.submit() // the answer is lost
            advanceUntilIdle()
            model.submit() // asking again finds it created
            advanceUntilIdle()
            assertEquals("codex", service.request?.agentType)
            // The folder and launcher are this start's; the mode and effort stay for the next Claude Code.
            assertEquals(AgentDefaults("/code/new", "codex", "plan", "high"), store.saved["first"])
        } finally {
            model.viewModelScope.cancel()
            Dispatchers.resetMain()
        }
    }

    @Test fun lostStartReplySurvivesReopeningAndOnlyQueriesTheSameOperation() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val machines = MutableStateFlow(listOf(Machine("first", online = true), Machine("second", online = true)))
        val store = MemoryStore()
        val service = FakeService()
        val model = NewAgentViewModel(service, store, machines)
        try {
            advanceUntilIdle()
            model.selection(model.state.value.selection.copy(cwd = "/code/project", mode = "plan", effort = "high"))
            model.prompt("首句")
            model.submit()
            model.submit()
            advanceUntilIdle()
            assertEquals(1, service.starts)
            assertTrue(model.state.value.locked)
            val pending = requireNotNull(store.operation)
            assertEquals("首句", pending.request.prompt)
            assertEquals("plan", pending.request.permissionMode)
            model.selectMachine("second")
            assertEquals("first", model.state.value.machineId)
            model.viewModelScope.cancel()

            val restored = NewAgentViewModel(service, store, machines)
            try {
                advanceUntilIdle()
                assertEquals(pending, restored.state.value.pending)
                service.result = AgentStartResult.Pending
                restored.submit()
                advanceUntilIdle()
                assertEquals(1, service.starts)
                assertEquals(pending.id, service.queried)
                service.result = AgentStartResult.Created(Session(machineId = "first", sessionId = "created"))
                restored.submit()
                advanceUntilIdle()
                assertEquals("created", restored.state.value.created?.sessionId)
                assertEquals(AgentDefaults("/code/project", "claude", "plan", "high"), store.saved["first"])
                restored.opened()
                advanceUntilIdle()
                assertNull(store.operation)
                restored.selectMachine("second")
                advanceUntilIdle()
                restored.selection(restored.state.value.selection.copy(mode = "acceptEdits"))
                restored.selectMachine("first")
                advanceUntilIdle()
                assertEquals("plan", restored.state.value.selection.mode)
                assertEquals("/code/project", restored.state.value.selection.cwd)
            } finally { restored.viewModelScope.cancel() }
        } finally {
            model.viewModelScope.cancel()
            Dispatchers.resetMain()
        }
    }

    @Test fun spaceTabKeepsItsMachineAndDirectoryAndSendsTheWorkspace() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val machines = MutableStateFlow(listOf(Machine("first", online = true), Machine("second", online = true)))
        val service = FakeService()
        val model = NewAgentViewModel(service, MemoryStore(), machines)
        try {
            advanceUntilIdle()
            val space = AgentSpacePreset("second", "workspace", "/code/space", "空间")
            model.startIn(space)
            advanceUntilIdle()
            assertEquals("second", model.state.value.machineId)
            assertEquals("/code/space", model.state.value.selection.cwd)
            model.selectMachine("first")
            assertEquals("second", model.state.value.machineId)
            model.startIn(null)
            advanceUntilIdle()
            assertNull(model.state.value.space)
            assertEquals("/code", model.state.value.selection.cwd)
            model.startIn(space)
            advanceUntilIdle()
            model.submit()
            advanceUntilIdle()
            assertEquals(AgentStartRequest("second", "/code/space", "claude", workspaceId = "workspace"), service.request)
        } finally {
            model.viewModelScope.cancel()
            Dispatchers.resetMain()
        }
    }

    private class MemoryStore : AgentCreationStore {
        var operation: AgentStartOperation? = null
        val saved = mutableMapOf<String, AgentDefaults>()
        override suspend fun lastMachine(scope: String) = "first"
        override suspend fun defaults(scope: String, machine: String) = saved[machine] ?: AgentDefaults()
        override suspend fun saveDefaults(scope: String, machine: String, defaults: AgentDefaults) { saved[machine] = defaults }
        override suspend fun pending(scope: String) = operation
        override suspend fun pending(scope: String, value: AgentStartOperation?) { operation = value }
    }
    private class FakeService : AgentCreationService {
        override val scope = "hub/pairing"
        var starts = 0
        var queried: String? = null
        var request: AgentStartRequest? = null
        var result: AgentStartResult = AgentStartResult.Unknown
        var available = listOf(AgentLauncher("claude"))
        var beforeStart: () -> Unit = {}
        override suspend fun launchers(machineId: String) = available
        override suspend fun directories(machineId: String, path: String) = AgentDirectories(path.ifEmpty { "/code" })
        override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult {
            beforeStart()
            starts++
            this.request = request
            throw IOException("Lost response")
        }
        override suspend fun operation(operationId: String): AgentStartResult { queried = operationId; return result }
    }
}
