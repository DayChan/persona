package persona.feature.settings

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.sync.Mutex
import persona.core.contracts.api.AppUpdate
import persona.core.contracts.api.AppUpdates

/** Shared by foreground discovery and Settings. The gate never queues duplicate user actions. */
class AppUpdateState(
    internal val api: AppUpdates,
    private val installed: Long,
    private val clock: () -> Long = System::currentTimeMillis,
    private val lastAttempt: () -> Long = { 0L },
    private val saveAttempt: (Long) -> Unit = {},
    private val dismissed: () -> Long = { 0L },
    private val saveDismissed: (Long) -> Unit = {},
    initialLatest: AppUpdate? = null,
    private val saveLatest: (AppUpdate?) -> Unit = {},
    private val validSource: () -> Boolean = { true },
) {
    data class State(
        val latest: AppUpdate? = null,
        val checking: Boolean = false,
        val installing: Boolean = false,
        val message: Int? = null,
        val notice: Boolean = false,
    )
    private val gate = Mutex()
    private var attempted = lastAttempt()
    private var hiddenVersion = dismissed()
    private val installScope = kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.SupervisorJob() + kotlinx.coroutines.Dispatchers.Main.immediate)
    private val restored = initialLatest?.takeIf { it.versionCode > installed }
    private val mutable = MutableStateFlow(State(latest = restored, notice = restored != null && restored.versionCode != hiddenVersion))
    val state = mutable.asStateFlow()

    suspend fun check(manual: Boolean = false) {
        if (!gate.tryLock()) return
        try {
            val now = clock()
            val previous = attempted
            if (!manual && previous > 0 && now >= previous && now - previous < CHECK_INTERVAL_MS) return
            attempted = now
            saveAttempt(now)
            mutable.update { it.copy(checking = true, message = if (manual) null else it.message) }
            try {
                val latest = api.check()?.takeIf { it.versionCode > installed }
                if (!validSource()) return
                saveLatest(latest)
                mutable.update { it.copy(latest = latest,
                    notice = latest != null && latest.versionCode != hiddenVersion,
                    message = if (manual && latest == null) R.string.settings_update_none else null) }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) {
                // Automatic failure is deliberately quiet; Settings can report it and retry immediately.
                mutable.update { it.copy(message = if (manual) R.string.settings_update_check_failed else it.message) }
            } finally { mutable.update { it.copy(checking = false) } }
        } finally { gate.unlock() }
    }

    fun dismiss() {
        state.value.latest?.let { hiddenVersion = it.versionCode; saveDismissed(it.versionCode) }
        mutable.update { it.copy(notice = false) }
    }

    fun permissionRequired() { mutable.update { it.copy(message = R.string.settings_update_allow_unknown) } }

    fun startInstall(action: suspend (AppUpdate) -> Unit) {
        installScope.launch { install(action) }
    }

    suspend fun install(action: suspend (AppUpdate) -> Unit) {
        if (!gate.tryLock()) return
        try {
            if (!validSource()) return
            if (state.value.latest == null) return
            mutable.update { it.copy(installing = true, message = null) }
            try {
                // Automatic discovery may be throttled for hours while releases keep advancing.
                val latest = try { api.check()?.takeIf { it.versionCode > installed } }
                catch (cancelled: CancellationException) { throw cancelled }
                catch (error: Exception) { throw AppUpdateFailure(UpdateStage.Metadata, error) }
                if (!validSource()) return
                saveLatest(latest)
                mutable.update { it.copy(latest = latest, notice = latest != null) }
                if (latest == null) {
                    mutable.update { it.copy(message = R.string.settings_update_none) }
                    return
                }
                action(latest)
                mutable.update { it.copy(message = R.string.settings_update_verified) }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                val stage = (error as? AppUpdateFailure)?.stage ?: UpdateStage.Install
                // Do not log exception messages: transport exceptions can contain credential-bearing URLs.
                android.util.Log.w("AppUpdate", "Update failed: stage=$stage type=${error.cause?.javaClass?.simpleName ?: error.javaClass.simpleName}")
                mutable.update { it.copy(message = stage.message) }
            }
            finally { mutable.update { it.copy(installing = false) } }
        } finally { gate.unlock() }
    }

    companion object { const val CHECK_INTERVAL_MS = 6 * 60 * 60 * 1000L }
}
