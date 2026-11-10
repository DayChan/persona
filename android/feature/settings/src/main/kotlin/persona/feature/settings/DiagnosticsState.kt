package persona.feature.settings

import android.content.res.Resources
import androidx.annotation.StringRes
import androidx.compose.runtime.Composable
import androidx.compose.ui.res.stringResource
import persona.core.contracts.api.*
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/**
 * Uses only observations from the running transport, never pairing snapshots; apart from them, the last pairing
 * attempt ([pairing]), which is all there is to show while pairing keeps failing.
 */
internal class DiagnosticsState(
    private val connection: HubConnection,
    private val source: ConnectionDiagnosticsSource?,
    private val history: SettingsDiagnostics?,
    attempts: PairingAttempts? = null,
) {
    val data: StateFlow<ConnectionDiagnostics> = source?.diagnostics ?: MutableStateFlow(ConnectionDiagnostics())
    val pairing: StateFlow<PairingAttempt?> = attempts?.last ?: MutableStateFlow(null)
    val busy = MutableStateFlow(false)
    /** A string resource: why the observations are missing or stale. */
    val message = MutableStateFlow<Int?>(null)
    val summary = MutableStateFlow<DeviceDisconnectSummary?>(null)

    suspend fun refresh(reprobe: Boolean = false) {
        if (!busy.compareAndSet(false, true)) return
        message.value = null
        try {
            if (reprobe) connection.reconnect()
            // Reconnection publishes probe results asynchronously through the source.
            if (!reprobe) source?.readDiagnostics()
            if (source == null) message.value = R.string.settings_diag_no_source
        } catch (error: CancellationException) { throw error }
        catch (_: Exception) { message.value = R.string.settings_diag_history_failed }
        finally { busy.value = false }
    }

    suspend fun refreshSummary() {
        try { summary.value = history?.disconnectSummary() }
        catch (error: CancellationException) { throw error }
        catch (_: Exception) { summary.value = null }
    }
}

internal fun ConnectionCandidate.probeMeasurement(resources: Resources): String {
    val duration = probeDurationMillis?.takeIf { it >= 0 && sampleCount > 0 }?.toInt()
        ?: return resources.getString(R.string.settings_diag_unmeasured)
    return resources.getString(if (sampleCount < 10) R.string.settings_diag_probe_few else R.string.settings_diag_probe,
        resources.getQuantityString(R.plurals.settings_diag_millis, duration, duration),
        resources.getQuantityString(R.plurals.settings_diag_samples, sampleCount, sampleCount))
}

/** A route's kind (`ipv6`, `name`…) as a page names it; null for a kind this app does not know. */
@StringRes internal fun routeKindRes(kind: String): Int? = when (kind) {
    "ipv6" -> R.string.settings_route_ipv6
    "name" -> R.string.settings_route_name
    "lan" -> R.string.settings_route_lan
    "ipv4" -> R.string.settings_route_ipv4
    "unknown" -> R.string.settings_route_unknown
    "ssh" -> R.string.settings_tunnel_ssh
    "proxy" -> R.string.settings_tunnel_proxy
    else -> null
}

internal fun routeKind(kind: String, resources: Resources): String = routeKindRes(kind)?.let(resources::getString) ?: kind

@Composable internal fun routeKindText(kind: String): String = routeKindRes(kind)?.let { stringResource(it) } ?: kind

/** The rows "Connection diagnostics" shows for the last pairing attempt: a label and its value; [stepLabel] names where it stopped. */
internal fun PairingAttempt.rows(resources: Resources, stepLabel: String? = step?.let { resources.getString(it.labelRes()) }): List<Pair<String, String?>> = listOf(
    resources.getString(R.string.settings_diag_address) to address,
    resources.getString(R.string.settings_diag_result) to (step?.let { resources.getString(R.string.settings_diag_failed_at, stepLabel.orEmpty()) }
        ?: resources.getString(R.string.settings_diag_succeeded)),
) + listOfNotNull(error?.let { resources.getString(R.string.settings_diag_error) to it }) + listOf(resources.getString(R.string.settings_diag_time) to at)
