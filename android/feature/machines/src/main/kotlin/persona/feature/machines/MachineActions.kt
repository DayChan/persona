package persona.feature.machines

import android.content.res.Resources
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import persona.core.contracts.Machine
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.MachineManagement
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.UiText
import persona.core.designsystem.failureReasonText
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull

/** What a finished action says; the route turns it into text in the page's language. */
internal sealed interface MachineNotice {
    data object Connected : MachineNotice
    /**
     * Still offline after the wait, with why: the plain words of [connectionFailureText] when the hub's error says,
     * else the hub's own error, else nothing (#4537).
     */
    data class NotYet(val reason: UiText?) : MachineNotice
    data class ReconnectFailed(val reason: UiText) : MachineNotice
    data class DeployFailed(val reason: UiText) : MachineNotice
    /** "Also show botmux sessions" was not saved; the switch is back where it was. */
    data class BotmuxFailed(val reason: UiText) : MachineNotice
    /** Move to…: the computer is [name]'s now (docs/assistant/managers.md). */
    data class Moved(val name: String) : MachineNotice
    data class MoveFailed(val reason: UiText) : MachineNotice
    /** Renamed: the hub calls the computer [name] now. */
    data class Renamed(val name: String) : MachineNotice
    data class RenameFailed(val reason: UiText) : MachineNotice

    fun text(resources: Resources): String = when (this) {
        Connected -> resources.getString(R.string.machines_notice_connected)
        is NotYet -> reason?.let { resources.getString(R.string.machines_notice_not_yet_reason, it.resolve(resources)) }
            ?: resources.getString(R.string.machines_notice_not_yet)
        is ReconnectFailed -> resources.getString(R.string.machines_notice_reconnect_failed, reason.resolve(resources))
        is DeployFailed -> resources.getString(R.string.machines_notice_deploy_failed, reason.resolve(resources))
        is BotmuxFailed -> resources.getString(R.string.machines_notice_botmux_failed, reason.resolve(resources))
        is Moved -> resources.getString(R.string.machines_notice_moved, name)
        is MoveFailed -> resources.getString(R.string.machines_notice_move_failed, reason.resolve(resources))
        is Renamed -> resources.getString(R.string.machines_notice_renamed, name)
        is RenameFailed -> resources.getString(R.string.machines_notice_rename_failed, reason.resolve(resources))
    }
}

private const val RECONNECT_POLL_MILLIS = 3_000L

/**
 * Why a machine is still offline, as the offline card reads it (#4463): a known error_code, else plain words read from
 * the hub's error, else that error.
 */
internal fun notYetReason(machine: Machine): UiText? =
    (machineErrorText(machine.errorCode) ?: connectionFailureText(machine))?.let { UiText.Res(it) }
        ?: machine.error?.takeIf { it.isNotBlank() }?.let { UiText.Plain(it) }

internal data class MachineActionsState(
    /** The machine's launchers; null until read (offline machines have none to list). */
    val launchers: List<AgentLauncher>? = null,
    val launchersLoading: Boolean = false,
    /** Why the launchers could not be read (the hub's reason), shown as "Couldn't read launchers: ..." */
    val launchersError: UiText? = null,
    val reconnecting: Boolean = false,
    /** Machines whose "Redeploy" was sent and not answered yet; the hub's `deploying` takes over after that. */
    val deployStarting: Set<String> = emptySet(),
    /** "Also show botmux sessions" (#2575); null until the hub answered, and on an older hub without the route: no switch. */
    val botmux: Boolean? = null,
    val botmuxSaving: Boolean = false,
    /** Move to… was sent and not answered yet. */
    val moving: Boolean = false,
    /** Rename was sent and not answered yet. */
    val renaming: Boolean = false,
)

/**
 * What the machine pages change on a machine (iOS `MachineDetailView`): its launchers, "Also show botmux sessions", "Reconnect" (waits up to
 * [reconnectWaitMillis] for it to come online and says how it went) and "Redeploy". Results are short notices.
 */
internal class MachineActionsViewModel(
    private val management: MachineManagement,
    private val sessions: SessionsRepository,
    private val reconnectWaitMillis: Long = 15_000,
) : ViewModel() {
    private val mutable = MutableStateFlow(MachineActionsState())
    val state = mutable.asStateFlow()
    private val noticeChannel = Channel<MachineNotice>(Channel.BUFFERED)
    /** One notice per finished action, for the app's feedback. */
    val notices = noticeChannel.receiveAsFlow()
    private var launchersJob: Job? = null

    /** Again when it comes online (after "Reconnect"): an offline machine has no launchers to list. */
    fun loadLaunchers(machineId: String, online: Boolean) {
        launchersJob?.cancel()
        if (!online) {
            mutable.update { it.copy(launchers = null, launchersLoading = false, launchersError = null) }
            return
        }
        mutable.update { it.copy(launchersLoading = true, launchersError = null) }
        launchersJob = viewModelScope.launch {
            try {
                val launchers = management.launchers(machineId)
                mutable.update { it.copy(launchers = launchers, launchersLoading = false) }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                mutable.update { it.copy(launchersLoading = false, launchersError = failureReasonText(error)) }
            }
        }
    }

    /** Reads "Also show botmux sessions"; a hub that cannot answer leaves it null, so the page draws no switch (iOS `BotmuxSwitchModel`). */
    fun loadBotmux(machineId: String) {
        viewModelScope.launch {
            val on = try { management.botmux(machineId) }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { null }
            mutable.update { it.copy(botmux = on) }
        }
    }

    /** Shows [on] at once and saves it (not part of the engine choice); a failed save puts the old value back and says why. */
    fun setBotmux(machineId: String, on: Boolean) {
        val before = state.value.botmux ?: return
        if (state.value.botmuxSaving) return
        mutable.update { it.copy(botmux = on, botmuxSaving = true) }
        viewModelScope.launch {
            try {
                val saved = management.setBotmux(machineId, on)
                mutable.update { it.copy(botmux = saved) }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                mutable.update { it.copy(botmux = before) }
                noticeChannel.trySend(MachineNotice.BotmuxFailed(failureReasonText(error)))
            } finally { mutable.update { it.copy(botmuxSaving = false) } }
        }
    }

    /** Renames the computer on its hub ("" is its id again); the hub's `machines` push brings the name to the page, read now too. */
    fun rename(machineId: String, name: String) {
        if (state.value.renaming) return
        mutable.update { it.copy(renaming = true) }
        viewModelScope.launch {
            try {
                val shown = management.rename(machineId, name.trim())
                noticeChannel.trySend(MachineNotice.Renamed(shown))
                try { sessions.refresh() } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { noticeChannel.trySend(MachineNotice.RenameFailed(failureReasonText(error))) }
            finally { mutable.update { it.copy(renaming = false) } }
        }
    }

    fun reconnect(machineId: String) {
        if (state.value.reconnecting) return
        mutable.update { it.copy(reconnecting = true) }
        viewModelScope.launch {
            try {
                try { management.reconnect(machineId) }
                catch (cancelled: CancellationException) { throw cancelled }
                catch (error: Exception) {
                    noticeChannel.trySend(MachineNotice.ReconnectFailed(failureReasonText(error)))
                    return@launch
                }
                val back = withTimeoutOrNull(reconnectWaitMillis) {
                    coroutineScope {
                        // The hub pushes the machine list when the node is back; reading it now and then covers a missed push.
                        val poll = launch {
                            while (true) {
                                delay(RECONNECT_POLL_MILLIS)
                                try { sessions.refresh() } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { }
                            }
                        }
                        sessions.machines.first { list -> list.any { it.id == machineId && it.online } }.also { poll.cancel() }
                    }
                }
                noticeChannel.trySend(if (back != null) MachineNotice.Connected else {
                    MachineNotice.NotYet(sessions.machines.value.firstOrNull { it.id == machineId }?.let(::notYetReason))
                })
            } finally { mutable.update { it.copy(reconnecting = false) } }
        }
    }

    /** Move to… [manager] (named [name] in the notice): the hub's `machines` push then shows the new stamp; read now too. */
    fun moveTo(machineId: String, manager: String, name: String) {
        if (state.value.moving) return
        mutable.update { it.copy(moving = true) }
        viewModelScope.launch {
            try {
                management.moveToManager(machineId, manager)
                noticeChannel.trySend(MachineNotice.Moved(name))
                try { sessions.refresh() } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { noticeChannel.trySend(MachineNotice.MoveFailed(failureReasonText(error))) }
            finally { mutable.update { it.copy(moving = false) } }
        }
    }

    fun deploy(machineId: String) {
        if (machineId in state.value.deployStarting) return
        mutable.update { it.copy(deployStarting = it.deployStarting + machineId) }
        viewModelScope.launch {
            try {
                management.deploy(machineId)
                // Progress and the outcome come with the machine list (deploying, deploy_error, node_mismatch).
                try { sessions.refresh() } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { noticeChannel.trySend(MachineNotice.DeployFailed(failureReasonText(error))) }
            finally { mutable.update { it.copy(deployStarting = it.deployStarting - machineId) } }
        }
    }
}
