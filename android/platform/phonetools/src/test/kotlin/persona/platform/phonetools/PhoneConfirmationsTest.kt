package persona.platform.phonetools

import android.app.NotificationManager
import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.async
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(AndroidJUnit4::class)
class PhoneConfirmationsTest {
    private val context get() = ApplicationProvider.getApplicationContext<Context>()

    /**
     * minSdk is 30: the confirmation must post there too (setAuthenticationRequired is API 31, #4221). The unlock flag
     * does not exist on API 30, so only posting is checked there.
     */
    @Test @Config(sdk = [30]) fun postsTheConfirmationOnApi30() = postsAndDenies(expectAuthentication = false)

    @Test @Config(sdk = [35]) fun allowNeedsAnUnlockFromApi31() = postsAndDenies(expectAuthentication = true)

    private fun postsAndDenies(expectAuthentication: Boolean) = runTest {
        val confirmations = PhoneConfirmations(context)
        val answer = async { confirmations.confirm { "Add an event" } }
        runCurrent()
        val token = confirmations.pending.value?.token
        assertNotNull("confirmation should be pending", token)
        val posted = shadowOf(context.getSystemService(NotificationManager::class.java)).allNotifications
        assertEquals(1, posted.size)
        val actions = posted.single().actions
        assertEquals(2, actions.size)
        if (expectAuthentication) {
            assertFalse(actions[0].isAuthenticationRequired)
            assertTrue(actions[1].isAuthenticationRequired)
        }
        confirmations.resolve(token!!, false)
        assertEquals(PhoneConfirmation.Denied, answer.await())
        confirmations.close()
    }

    private val notifications get() = shadowOf(context.getSystemService(NotificationManager::class.java))

    @Test @Config(sdk = [35]) fun anAllowAnswersAllowed() = runTest {
        val confirmations = PhoneConfirmations(context)
        val answer = async { confirmations.confirm { "Add an event" } }
        runCurrent()
        confirmations.resolve(confirmations.pending.value!!.token, true)
        assertEquals(PhoneConfirmation.Allowed, answer.await())
    }

    /** #1875: 90 seconds without an answer is not a Deny; the confirmation and its notification go. */
    @Test @Config(sdk = [35]) fun nobodyAnsweringIsUnanswered() = runTest {
        val confirmations = PhoneConfirmations(context)
        val answer = async { confirmations.confirm { "Add an event" } }
        runCurrent()
        assertEquals(1, notifications.allNotifications.size)
        advanceTimeBy(PhoneConfirmations.TIMEOUT_MILLIS - 1)
        runCurrent()
        assertFalse(answer.isCompleted)
        advanceTimeBy(2)
        runCurrent()
        assertEquals(PhoneConfirmation.Unanswered, answer.await())
        assertNull(confirmations.pending.value)
        assertEquals(0, notifications.allNotifications.size)
    }

    @Test @Config(sdk = [35]) fun inTheBackgroundWithNotificationsOffItIsNotShown() = runTest {
        notifications.setNotificationsEnabled(false)
        val confirmations = PhoneConfirmations(context)
        assertEquals(PhoneConfirmation.NotShown, confirmations.confirm { "Add an event" })
        assertNull(confirmations.pending.value)
    }

    @Test @Config(sdk = [35]) fun leavingTheDialogWhenNoNotificationCanBePostedIsNotShown() = runTest {
        val confirmations = PhoneConfirmations(context)
        confirmations.setForeground(true)
        val answer = async { confirmations.confirm { "Add an event" } }
        runCurrent()
        assertNotNull(confirmations.pending.value)
        assertEquals(0, notifications.allNotifications.size) // the dialog in the app asks
        notifications.setNotificationsEnabled(false)
        confirmations.setForeground(false)
        assertEquals(PhoneConfirmation.NotShown, answer.await())
    }

    @Test @Config(sdk = [35]) fun closingWithdrawsIt() = runTest {
        val confirmations = PhoneConfirmations(context)
        val answer = async { confirmations.confirm { "Add an event" } }
        runCurrent()
        confirmations.close()
        assertEquals(PhoneConfirmation.Withdrawn, answer.await())
    }
}
