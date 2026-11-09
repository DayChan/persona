@file:OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)

package persona.feature.settings

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ExtensionOperation
import persona.core.contracts.api.ManagerRoster
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1100dp")
class CatalogExtensionManagerTest {
    @get:Rule val rule = createComposeRule()
    private val res get() = ApplicationProvider.getApplicationContext<android.content.Context>().resources

    @Before fun resetChoice() { AssistantManagerChoice.reset(); AssistantManagerChoice.forgetItems() }
    @After fun clearChoice() { AssistantManagerChoice.reset(); AssistantManagerChoice.forgetItems() }

    @Test fun catalogTracksOnlyItsOwnersOperationsAndSignIn() = runTest {
        val api = object : FakeOperations() {
            override suspend fun retryOperation(id: String) = super.retryOperation(id).copy(manager = "m2")
        }
        val held = op("failed", "download", 2, "download" to "failed", retryable = true).copy(manager = "m2")
        api.listed = listOf(held, held.copy(id = "other", manager = ManagerRoster.ASSISTANT_MANAGER))
        val model = CatalogExtensionModel(api, "m1", "example-notes", res, manager = "m2")
        val signedIn = mutableListOf<String>()
        val notices = mutableListOf<String>()
        model.load()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) {
            model.watch(signIn = { signedIn += it }) { notices += it }
        }
        runCurrent()
        assertEquals(held, model.state.value.operation)
        assertTrue(model.retry())
        assertEquals("m2", model.state.value.operation?.manager)
        val finished = api.started.copy(state = ExtensionOperation.Succeeded, updatedAt = "2026-11-06T08:02:00Z", next = "auth")
        api.pushes.emit(finished)
        api.pushes.emit(finished.copy(manager = "m3"))
        runCurrent()
        assertTrue(signedIn.isEmpty())
        assertTrue(notices.isEmpty())
        assertEquals(ExtensionOperation.Running, model.state.value.operation?.state)
        api.pushes.emit(finished.copy(manager = "m2"))
        runCurrent()
        assertEquals(listOf("example-notes"), signedIn)
        assertEquals(1, notices.size)
        assertNull(model.state.value.operation)
    }

    @Test fun managerScopedHttpReceiptsWithoutManagerKeepTheirRequestOwner() = runTest {
        val api = FakeOperations()
        val failed = op("failed", "download", 2, "download" to "failed", retryable = true)
        api.listed = listOf(failed)
        val model = CatalogExtensionModel(api, "m1", "example-notes", res, manager = "m2")
        model.load()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { model.watch {} }
        runCurrent()
        assertEquals(failed, model.state.value.operation)
        assertTrue(model.retry())
        assertEquals(ExtensionOperation.Running, model.state.value.operation?.state)
        val held = model.state.value.operation
        api.pushes.emit(api.started.copy(manager = "m3", state = ExtensionOperation.Succeeded, updatedAt = "2026-11-06T08:02:00Z"))
        runCurrent()
        assertEquals(held, model.state.value.operation)
        // A missing manager on a broadcast is not a response to this page's manager-bound request.
        api.pushes.emit(api.started.copy(state = ExtensionOperation.Succeeded, updatedAt = "2026-11-06T08:02:00Z"))
        runCurrent()
        assertEquals(held, model.state.value.operation)
        api.pushes.emit(api.started.copy(manager = "m2", state = ExtensionOperation.Succeeded, updatedAt = "2026-11-06T08:02:00Z"))
        runCurrent()
        assertNull(model.state.value.operation)
        assertTrue(model.preview())
        assertEquals(api.started, model.state.value.operation)
    }

    @Test fun defaultOwnerIsCapturedInsteadOfFollowingTheAssistantChoiceOrLaterDefault() = runTest {
        AssistantManagerChoice.choose(null, "m2", "m1")
        val api = FakeOperations()
        val model = CatalogExtensionModel(api, "m1", "example-notes", res)
        assertEquals("m1", model.owner)
        AssistantManagerChoice.choose(null, "m3", "m3")
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { model.watch {} }
        runCurrent()
        api.pushes.emit(api.started)
        runCurrent()
        assertNull(model.state.value.operation)
        api.pushes.emit(api.started.copy(manager = "m1"))
        runCurrent()
        assertEquals("m1", model.state.value.operation?.manager)
    }

    @Test fun catalogPinsBothApiNamesAndReleasesOnlyItsOwnPins() {
        AssistantManagerChoice.choose(null, null, "assistant")
        val catalog = listOf("extensions", "-", "catalog")
        val previous = AssistantManagerChoice.pin(listOf(catalog), "previous-manager")
        var shown by mutableStateOf(true)
        var manager by mutableStateOf<String?>("m2")
        val api = FakeOperations(stepped = false)
        val paths = listOf("extensions", "plugins").flatMap { base ->
            listOf(listOf(base, "example-notes", "-", "upgrade"), listOf(base, "example-notes", "-", "auth"),
                listOf(base, "-", "catalog"), listOf(base, "-", "catalog", "preview"),
                listOf(base, "-", "install"), listOf(base, "-", "operations"),
                listOf(base, "-", "operations", "op1", "retry"))
        }
        try {
            rule.setContent {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    if (shown) ExtensionCatalogPage(api, "m1", "example-notes", onBack = {}, onInstalled = {}, manager = manager)
                }
            }
            rule.runOnIdle {
                paths.forEach { assertEquals("m2", AssistantManagerChoice.itemHeader(it)) }
                assertNull(AssistantManagerChoice.itemHeader(listOf("extensions", "-", "markets")))
                assertNull(AssistantManagerChoice.itemHeader(listOf("extensions", "another-extension")))
                manager = "m3"
            }
            rule.runOnIdle {
                paths.forEach { assertEquals("m3", AssistantManagerChoice.itemHeader(it)) }
                shown = false
            }
            rule.runOnIdle {
                assertEquals("previous-manager", AssistantManagerChoice.itemHeader(catalog))
                paths.filter { it.take(3) != catalog }.forEach { assertNull(AssistantManagerChoice.itemHeader(it)) }
            }
        } finally {
            rule.runOnIdle { shown = false }
            rule.waitForIdle()
            previous()
        }
    }
}
