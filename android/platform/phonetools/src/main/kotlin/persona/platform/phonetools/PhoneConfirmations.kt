package persona.platform.phonetools

import android.app.*
import android.content.*
import android.net.Uri
import android.os.Build
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap

/**
 * How a confirmation ended (#1875). Only [Denied] is the user saying no: the agent hears "declined" for that alone,
 * and for the others that nobody answered, so it neither tells the user they refused nor saves without asking.
 */
enum class PhoneConfirmation {
    Allowed,
    Denied,
    /** Shown, but nobody answered within [PhoneConfirmations.TIMEOUT_MILLIS]; it was taken back. */
    Unanswered,
    /** Persona was in the background and could not post the notification (notifications or the channel off). */
    NotShown,
    /** The phone tools closed while it was open (unpaired, disconnected, turned off). */
    Withdrawn,
}

/** A token is valid only while its single local confirmation is pending. Nothing is persisted. */
class PhoneConfirmations(context: Context) : AutoCloseable {
    private val context = context.applicationContext
    private val manager = context.getSystemService(NotificationManager::class.java)
    private val keyguard = context.getSystemService(KeyguardManager::class.java)
    private val _pending = MutableStateFlow<PendingPhoneConfirmation?>(null)
    val pending = _pending.asStateFlow()
    private var answer: CompletableDeferred<PhoneConfirmation>? = null
    @Volatile private var foreground = false

    suspend fun confirm(text: PhoneConfirmationText): PhoneConfirmation {
        val summary = text(context.resources)
        val token = UUID.randomUUID().toString()
        val deferred = CompletableDeferred<PhoneConfirmation>()
        answer = deferred
        callbacks[token] = { approved -> resolve(token, approved) }
        _pending.value = PendingPhoneConfirmation(token, summary)
        return try {
            if (!foreground && !showNotification()) return PhoneConfirmation.NotShown
            withTimeoutOrNull(TIMEOUT_MILLIS) { deferred.await() } ?: PhoneConfirmation.Unanswered
        } finally {
            callbacks.remove(token); manager.cancel(token, 0); _pending.value = null; answer = null
        }
    }
    fun resolve(token: String, allowed: Boolean) {
        if (_pending.value?.token != token) return
        if (allowed && keyguard.isDeviceLocked) return
        answer?.complete(if (allowed) PhoneConfirmation.Allowed else PhoneConfirmation.Denied)
    }
    fun setForeground(value: Boolean) {
        foreground = value
        val current = _pending.value ?: return
        if (value) manager.cancel(current.token, 0)
        // the dialog went away with the app, and nothing else can ask
        else if (!showNotification()) answer?.complete(PhoneConfirmation.NotShown)
    }
    private fun showNotification(): Boolean {
        val current = _pending.value ?: return false
        return try {
            manager.createNotificationChannel(NotificationChannel(CHANNEL, context.getString(R.string.phonetools_confirm_channel), NotificationManager.IMPORTANCE_HIGH))
            if (!manager.areNotificationsEnabled() || manager.getNotificationChannel(CHANNEL).importance == NotificationManager.IMPORTANCE_NONE) return false
            fun action(allowed: Boolean): Notification.Action {
                val intent = Intent(context, PhoneConfirmationReceiver::class.java)
                    .setAction("persona.phone.confirm").setData(Uri.parse("persona-phone://confirmation/${current.token}/$allowed"))
                    .putExtra("token", current.token).putExtra("allowed", allowed)
                val pendingIntent = PendingIntent.getBroadcast(context, 0, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
                val builder = Notification.Action.Builder(null, context.getString(if (allowed) R.string.phonetools_allow else R.string.phonetools_deny), pendingIntent)
                // setAuthenticationRequired is API 31; calling it on API 30 throws NoSuchMethodError (an Error, so the
                // catch below misses it) and no confirmation ever shows. On API 30 the system does not ask for an unlock
                // before sending the button, so allow can be tapped straight from the lock screen; resolve() and the
                // receiver still refuse it while the device is locked, so that tap is dropped (the notification stays):
                // unlock, then tap allow or open the app. Trade-off: one more step on API 30 instead of no confirmation.
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) builder.setAuthenticationRequired(allowed)
                return builder.build()
            }
            val builder = Notification.Builder(context, CHANNEL).setSmallIcon(persona.core.designsystem.R.drawable.ic_stat)
                .setContentTitle(context.getString(R.string.phonetools_confirm_notification)).setContentText(current.summary)
                .setStyle(Notification.BigTextStyle().bigText(current.summary)).setVisibility(Notification.VISIBILITY_PRIVATE)
                .setCategory(Notification.CATEGORY_RECOMMENDATION).setTimeoutAfter(TIMEOUT_MILLIS)
                .addAction(action(false)).addAction(action(true))
            context.packageManager.getLaunchIntentForPackage(context.packageName)?.let { intent ->
                builder.setContentIntent(PendingIntent.getActivity(context, 0, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))
            }
            manager.notify(current.token, 0, builder.build()); true
        } catch (_: Exception) { false }
    }
    override fun close() { answer?.complete(PhoneConfirmation.Withdrawn); _pending.value?.let { callbacks.remove(it.token); manager.cancel(it.token, 0) } }
    companion object {
        /**
         * How long a confirmation stays open before it is taken back as [PhoneConfirmation.Unanswered] (the old Android
         * app's 90 s). The hub would wait 30 minutes for a confirmed write (`phoneConfirmWait`), as iOS does, but
         * PhoneToolRegistry runs one call at a time: an open confirmation holds back every other phone call, and those
         * give up at the hub after 2 minutes. So the phone stops first and tells the agent nobody answered.
         */
        internal const val TIMEOUT_MILLIS = 90_000L
        internal const val CHANNEL = "phone_tools_confirmation"
        internal val callbacks = ConcurrentHashMap<String, (Boolean) -> Unit>()
    }
}

data class PendingPhoneConfirmation(val token: String, val summary: String)

class PhoneConfirmationReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action !in persona.core.contracts.ProtocolCompatibility.phoneConfirmationActions) return
        val token = intent.getStringExtra("token") ?: return
        val allowed = intent.getBooleanExtra("allowed", false)
        if (!allowed) { PhoneConfirmations.callbacks[token]?.invoke(false); return }
        // "Allow" needs an unlock, and the lock screen reports it a moment after it let the button through.
        val keyguard = context.getSystemService(KeyguardManager::class.java)
        val pending = goAsync()
        CoroutineScope(SupervisorJob() + Dispatchers.Main).launch {
            try { if (!lockedAfterUnlockSettles({ keyguard.isDeviceLocked })) PhoneConfirmations.callbacks[token]?.invoke(true) }
            finally { pending.finish() }
        }
    }
}

/**
 * Whether the phone is still locked once an unlock in progress has had [settleMillis] to land. SystemUI sends a
 * notification button that needs authentication as soon as the user's PIN is accepted, a moment before KeyguardManager
 * stops reporting the device as locked: read at once, "Allow" tapped on the lock screen was dropped and the call declined
 * when the confirmation timed out (docs/lessons/android.md; the same as :platform:notify's).
 */
internal suspend fun lockedAfterUnlockSettles(
    locked: () -> Boolean,
    settleMillis: Long = UNLOCK_SETTLE_MILLIS,
    pollMillis: Long = 100,
): Boolean {
    if (!locked()) return false
    return withTimeoutOrNull(settleMillis) {
        while (locked()) delay(pollMillis)
        false
    } ?: true
}

/** How long an unlock that let a button through may take to show (config value, not a measurement). */
internal const val UNLOCK_SETTLE_MILLIS = 3_000L
