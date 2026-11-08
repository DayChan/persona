package persona.feature.assistant.settings

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.AssistantConfig
import persona.core.contracts.api.AssistantConfigUpdate
import persona.core.contracts.api.MemoryFile
import persona.core.contracts.api.AssistantPermissionDefaults

class AssistantPermissionCompatibilityTest {
    private class Api : AssistantSettingsApi {
        var stored = AssistantConfig(manager = "work", defaultPermissionMode = "default")
        var supported = emptySet<String>()
            set(value) {
                field = value
                stored = stored.copy(permissionDefaults = value.firstOrNull()?.let {
                    AssistantPermissionDefaults(1, it, AssistantPermissionDefaults.MODES)
                })
            }
        var writes = 0
        var reads = 0
        var read: suspend () -> AssistantConfig = { stored }
        var write: suspend (AssistantConfigUpdate) -> AssistantConfig = { update ->
            stored.copy(defaultPermissionMode = update.defaultPermissionMode).also { stored = it }
        }
        override suspend fun config(): AssistantConfig { reads++; return read() }
        override suspend fun updateConfig(update: AssistantConfigUpdate): AssistantConfig { writes++; return write(update) }
        override suspend fun memory() = emptyList<MemoryFile>()
        override suspend fun saveMemory(name: String, content: String) = error("Not used")
        override suspend fun deleteMemory(name: String) = error("Not used")
    }
    private class Zone : TimezoneChoice {
        override val phoneZone = "UTC"
        override var followPhone = false
    }
    private fun Api.model() = AssistantSettingsState(this, Zone())

    @Test fun identityAndOptionalValuesDoNotEstablishCapability() = runTest {
        for (value in listOf(null, "default", "plan")) {
            val api = Api().apply { stored = stored.copy(defaultPermissionMode = value) }
            val model = api.model(); model.load()
            assertFalse(model.state.value.permissionSupported)
            model.setDefaultPermission("bypassPermissions")
            assertEquals(0, api.writes)
            assertEquals(value, model.state.value.config?.defaultPermissionMode)
        }
    }

    @Test fun capabilityForAnotherManagerDoesNotAuthorizeTheShownManager() = runTest {
        val api = Api().apply { supported = setOf("personal") }
        val model = api.model(); model.load(); model.setDefaultPermission("plan")
        assertEquals(0, api.writes)
    }

    @Test fun unknownOrPartialContractDoesNotAuthorizeWrites() = runTest {
        for (contract in listOf(
            AssistantPermissionDefaults(2, "work", AssistantPermissionDefaults.MODES),
            AssistantPermissionDefaults(1, "work", listOf("default")),
            AssistantPermissionDefaults(1, "", AssistantPermissionDefaults.MODES),
        )) {
            val api = Api().apply { stored = stored.copy(permissionDefaults = contract) }
            val model = api.model(); model.load(); model.setDefaultPermission("plan")
            assertFalse(model.state.value.permissionSupported)
            assertEquals(0, api.writes)
        }
    }

    @Test fun unrelatedConfigResponseCannotKeepAStaleCapabilityEnabled() = runTest {
        val api = Api().apply {
            supported = setOf("work")
            write = { stored.copy(timezone = it.timezone, permissionDefaults = null) }
        }
        val model = AssistantSettingsState(api, Zone().apply { followPhone = true })
        model.load()
        assertFalse(model.state.value.permissionSupported)
        model.setDefaultPermission("plan")
        assertEquals(1, api.writes) // Only the automatic timezone update.
    }

    @Test fun removedContractOnReadbackCannotReportSuccess() = runTest {
        val api = Api().apply { supported = setOf("work") }
        val model = api.model(); model.load()
        api.read = { api.stored.copy(permissionDefaults = null) }
        model.setDefaultPermission("plan")
        assertTrue(model.state.value.notice?.error == true)
        assertEquals("default", model.state.value.config?.defaultPermissionMode)
    }

    @Test fun ignoredFieldOnOldManagerCannotBecomeSuccessfulSave() = runTest {
        val api = Api().apply {
            supported = setOf("work")
            stored = stored.copy(defaultPermissionMode = null)
            write = { stored }
        }
        val model = api.model(); model.load(); model.setDefaultPermission("plan")
        assertEquals(1, api.writes)
        assertEquals(1, api.reads)
        assertTrue(model.state.value.notice?.error == true)
        assertNull(model.state.value.config?.defaultPermissionMode)
    }

    @Test fun successfulPutWithoutPersistenceFailsIndependentGet() = runTest {
        val api = Api().apply {
            supported = setOf("work")
            write = { stored.copy(defaultPermissionMode = it.defaultPermissionMode) }
        }
        val model = api.model(); model.load(); model.setDefaultPermission("plan")
        assertEquals(2, api.reads)
        assertTrue(model.state.value.notice?.error == true)
        assertEquals("default", model.state.value.config?.defaultPermissionMode)
    }

    @Test fun getMustReturnTheSameOwnerAsThePut() = runTest {
        val api = Api().apply { supported = setOf("work") }
        val model = api.model(); model.load()
        api.read = { api.stored.copy(manager = "personal") }
        model.setDefaultPermission("plan")
        assertTrue(model.state.value.notice?.error == true)
        assertEquals("work", model.state.value.config?.manager)
        assertEquals("default", model.state.value.config?.defaultPermissionMode)
    }

    @Test fun matchingGetCommitsAndSurvivesReloadWithoutTouchingAnotherOwner() = runTest {
        val api = Api().apply { supported = setOf("work") }
        val other = Api().apply { stored = stored.copy(manager = "personal", defaultPermissionMode = "auto") }
        val model = api.model(); model.load(); model.setDefaultPermission("plan")
        assertEquals(2, api.reads)
        assertEquals("plan", model.state.value.config?.defaultPermissionMode)
        val reopened = api.model(); reopened.load()
        assertEquals("plan", reopened.state.value.config?.defaultPermissionMode)
        assertEquals("auto", other.stored.defaultPermissionMode)
        assertEquals(0, other.writes)
    }

    @Test fun latePutAfterOwnerReloadDoesNotIssueGetOrReplaceCurrentOwner() = runTest {
        val reply = CompletableDeferred<AssistantConfig>()
        val api = Api().apply { supported = setOf("work"); write = { reply.await() } }
        val model = api.model(); model.load()
        val saving = launch { model.setDefaultPermission("plan") }; runCurrent()
        api.stored = AssistantConfig(manager = "personal", defaultPermissionMode = "auto")
        model.load()
        reply.complete(AssistantConfig(manager = "work", defaultPermissionMode = "plan")); saving.join()
        assertEquals(2, api.reads)
        assertEquals("personal", model.state.value.config?.manager)
        assertEquals("auto", model.state.value.config?.defaultPermissionMode)
        assertFalse(model.state.value.permissionSupported)
    }

    @Test fun lateGetAfterOwnerReloadDoesNotReplaceCurrentOwner() = runTest {
        val reply = CompletableDeferred<AssistantConfig>()
        val api = Api().apply { supported = setOf("work") }
        val model = api.model(); model.load()
        api.read = { reply.await() }
        val saving = launch { model.setDefaultPermission("plan") }; runCurrent()
        api.stored = AssistantConfig(manager = "personal", defaultPermissionMode = "auto")
        api.read = { api.stored }; model.load()
        reply.complete(AssistantConfig(manager = "work", defaultPermissionMode = "plan")); saving.join()
        assertEquals("personal", model.state.value.config?.manager)
        assertEquals("auto", model.state.value.config?.defaultPermissionMode)
    }
}
