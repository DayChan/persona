package persona.android.di

import persona.core.contracts.ExtensionOperationEvent
import persona.core.contracts.Hello
import persona.core.contracts.ManagersEvent
import persona.core.contracts.SessionsEvent
import persona.core.contracts.api.ExtensionOperation
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Test

class ExtensionOperationUpdatesTest {
    @Test fun aManagersPushListsOperationsAgainLikeAHello() = runBlocking {
        // #1832: a manager that restarts while the socket stays up loses its operations, and only a managers push says so.
        val op = ExtensionOperation("op1", "install", "example-notes", state = "running")
        val events = flowOf(Hello(), ExtensionOperationEvent(op), SessionsEvent("mac", emptyList()), ManagersEvent)
        assertEquals(listOf(null, op, null), extensionOperationUpdates(events).toList())
    }
}
