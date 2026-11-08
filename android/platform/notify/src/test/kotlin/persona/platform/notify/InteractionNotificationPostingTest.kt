package persona.platform.notify

import android.Manifest
import android.app.Application
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.DeviceNotificationPreferences
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.PendingStatus

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InteractionNotificationPostingTest {
    // Only the request itself counts: a verifying / result notice reusing the tag is the answer's own and stays.
    @Test fun resultNoticesAreNotRequests() {
        val app = ApplicationProvider.getApplicationContext<Application>()
        shadowOf(app).grantPermissions(Manifest.permission.POST_NOTIFICATIONS)
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        }
        val preferences = object : NotificationPreferencesSource {
            override val preferences = MutableStateFlow<DeviceNotificationPreferences?>(DeviceNotificationPreferences())
            override suspend fun refreshPreferences() = preferences.value
        }
        val notifier = Notifier(app, sessions, preferences)
        val a = Interaction("a", machineId = "machine", sessionId = "session", kind = "permission")
        val b = Interaction("b", machineId = "machine", sessionId = "session", kind = "permission")
        notifier.postInteraction(a)
        notifier.postInteraction(b)
        notifier.pending("b", b.sessionKey, PendingStatus.Sending, null)
        assertEquals(setOf("a"), notifier.showingRequests())
        notifier.retract("a")
        assertEquals(emptySet<String>(), notifier.showingRequests())
    }
}
