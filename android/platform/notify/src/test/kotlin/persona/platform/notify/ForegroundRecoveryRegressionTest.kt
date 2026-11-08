package persona.platform.notify

import android.app.Application
import android.content.Context
import androidx.work.testing.WorkManagerTestInitHelper
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.setMain
import org.junit.After
import org.junit.Assert.*
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import org.robolectric.util.ReflectionHelpers
import persona.core.contracts.FleetState
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.*

/** Returning to the app is explicit foreground intent, unlike a background service restart. */
@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
@GraphicsMode(GraphicsMode.Mode.LEGACY)
class ForegroundRecoveryRegressionTest {
    private lateinit var app: Application
    private val scopes = mutableListOf<CoroutineScope>()

    @Before fun setup() {
        Dispatchers.setMain(StandardTestDispatcher())
        app = RuntimeEnvironment.getApplication()
        app.getSharedPreferences("notify_recovery", Context.MODE_PRIVATE).edit().clear().commit()
        // Resuming schedules the watchdog. The test WorkManager runs its tasks, queries and workers on the calling thread,
        // so none of them outlives the test: leftover SQLite work breaks the next class's Robolectric sandbox
        // (docs/lessons/android.md, "Testing").
        WorkManagerTestInitHelper.initializeTestWorkManager(app)
    }

    @After fun cleanup() {
        scopes.forEach { it.cancel() }
        Dispatchers.resetMain()
        WorkManagerTestInitHelper.closeWorkDatabase()
    }

    @Test fun deliberatelyReturningToThePairedAppClearsThePauseAndStartsItsResidentService() {
        // Persisted by ExitJournal.capture after a REASON_USER_REQUESTED exit; capture is already complete.
        app.getSharedPreferences("notify_recovery", Context.MODE_PRIVATE).edit()
            .putBoolean("user_stopped", true).putLong("last_exit", 123L).commit()
        val journal = ExitJournal(app)
        val runtime = runtime(journal)
        assertTrue(journal.userStopped)

        runtime.onForeground()

        assertFalse("An explicit foreground return must release the persisted resident-connection pause", journal.userStopped)
        val started = shadowOf(app).nextStartedService
        assertNotNull("Returning to the paired app must start the resident service again", started)
        assertEquals("persona.platform.notify.HubService", started.component?.className)
    }

    @Test fun aBackgroundStartDoesNotReleaseThePersistedPauseByItself() {
        app.getSharedPreferences("notify_recovery", Context.MODE_PRIVATE).edit().putBoolean("user_stopped", true).commit()
        val journal = ExitJournal(app)
        journal.capture()
        assertTrue(journal.userStopped)
        assertFalse(mayStartResidentConnection(paired = true, userStopped = journal.userStopped,
            foreground = false, batteryUnrestricted = true, capsule = true, overlayVisible = true))
        assertNull(shadowOf(app).nextStartedService)
    }

    @Test fun deliberatelyTouchingTheCapsuleClearsThePauseAndRestartsThePairedConnection() {
        app.getSharedPreferences("notify_recovery", Context.MODE_PRIVATE).edit().putBoolean("user_stopped", true).commit()
        val journal = ExitJournal(app)
        val runtime = runtime(journal)

        runtime.onUserInteraction()

        assertFalse("A deliberate capsule interaction must release the stopped marker", journal.userStopped)
        val started = shadowOf(app).nextStartedService
        assertNotNull("A deliberate capsule interaction must resume the resident service", started)
        assertEquals("persona.platform.notify.HubService", started.component?.className)
    }

    @Test fun repeatedInteractionsWithAHealthyResidentServiceDoNotRequestDuplicateStarts() {
        val journal = ExitJournal(app)
        val runtime = runtime(journal)
        val wasRunning = HubService.running
        ReflectionHelpers.setStaticField(HubService::class.java, "running", true)
        try {
            repeat(5) { runtime.onUserInteraction() }
            assertFalse(journal.userStopped)
            assertNull("Healthy touches should not repeatedly start the resident service", shadowOf(app).nextStartedService)
        } finally {
            ReflectionHelpers.setStaticField(HubService::class.java, "running", wasRunning)
        }
    }

    private fun runtime(journal: ExitJournal): NotificationRuntime {
        val pairing = object : PairingService {
            override val isPaired = MutableStateFlow(true)
            override suspend fun pair(request: PairingRequest) = error("Pairing is outside this regression")
            override suspend fun unpair() = error("Unpairing is outside this regression")
        }
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        }
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<persona.core.contracts.ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: kotlinx.serialization.json.JsonObject) = true
        }
        val notifications = object : NotificationPreferencesSource {
            override val preferences = MutableStateFlow<DeviceNotificationPreferences?>(null)
            override suspend fun refreshPreferences() = preferences.value
        }
        val notifier = Notifier(app, sessions, notifications)
        val fleet = FleetFeed(app, connection, pairing, object : FleetSource {
            override fun pairing() = "paired-test-scope"
            override suspend fun fetch() = FleetState()
        })
        val runtime = NotificationRuntime(app, pairing, journal, notifier, fleet, sessions, ResidentConnectionRequests(app))
        // Isolate the real foreground branch from unrelated installation/startup collectors.
        ReflectionHelpers.setField(runtime, "started", true)
        for (owner in listOf(notifier, fleet, runtime)) scopes += ReflectionHelpers.getField<CoroutineScope>(owner, "scope")
        return runtime
    }
}
