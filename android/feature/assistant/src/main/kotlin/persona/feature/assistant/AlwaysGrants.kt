package persona.feature.assistant

import android.content.res.Resources
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AlwaysGrant
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.parseTime
import persona.core.designsystem.*
import java.time.ZoneId
import java.util.concurrent.ConcurrentHashMap

// What "Always allow" opened in an assistant conversation, one revoke per row, in the Persona tab's "Session info"
// (#890, docs/assistant/assistant.md "Always allow in this session"): iOS / Mac's AlwaysGrantsSection, the web's
// AlwaysPermissions.

internal data class AlwaysGrantsState(
    /** Null until the hub answers. */
    val grants: List<AlwaysGrant>? = null,
    /** A hub without the route, or a device that may not use it: nothing to show, nothing to fix. */
    val unavailable: Boolean = false,
    val error: String? = null,
    /** The grant being revoked. */
    val revoking: String? = null,
    /** Revoked, but the running conversation could not be reset: it keeps the permission until the user restarts it. */
    val restartNeeded: Boolean = false,
)

internal class AlwaysGrantsModel(private val api: AssistantManagement, private val session: SessionKey, private val res: Resources) {
    private val mutable = MutableStateFlow(AlwaysGrantsState(restartNeeded = session in notReset))
    val state = mutable.asStateFlow()

    suspend fun load() {
        mutable.update { it.copy(grants = null, error = null) }
        try {
            val grants = api.alwaysGrants(session)
            mutable.update { if (grants == null) it.copy(unavailable = true) else it.copy(grants = grants) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: UnsupportedOperationException) { mutable.update { it.copy(unavailable = true) } }
        catch (error: Exception) { mutable.update { it.copy(error = failureReasonText(error).resolve(res)) } }
    }

    /** Revokes [grant]; true when it is gone. Only one at a time. */
    suspend fun revoke(grant: AlwaysGrant): Boolean {
        if (state.value.revoking != null) return false
        mutable.update { it.copy(revoking = grant.keyHash, error = null) }
        return try {
            val restart = api.revokeAlwaysGrant(session, grant.keyHash)
            if (restart) notReset += session
            mutable.update { s -> s.copy(grants = s.grants?.filterNot { it.keyHash == grant.keyHash }, restartNeeded = s.restartNeeded || restart) }
            true
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(error = res.getString(R.string.assistant_always_not_revoked, failureReasonText(error).resolve(res))) }
            false
        } finally { mutable.update { it.copy(revoking = null) } }
    }

    companion object {
        /**
         * Conversations still running with a revoked permission: only the user's restart clears that, which the app
         * can't see, so the warning outlives the sheet and lasts the app's life (as on iOS).
         */
        private val notReset: MutableSet<SessionKey> = ConcurrentHashMap.newKeySet()
    }
}

/** "Session info" of the conversation on the Persona tab: its id, and what it was always allowed with a revoke each. */
@Composable
internal fun AssistantSessionInfoSheet(session: SessionKey, management: AssistantManagement, onDismiss: () -> Unit) {
    val res = LocalResources.current
    val model = remember(session, management) { AlwaysGrantsModel(management, session, res) }
    val state by model.state.collectAsStateWithLifecycle()
    val scope = rememberCoroutineScope()
    var confirming by remember(session) { mutableStateOf<AlwaysGrant?>(null) }
    LaunchedEffect(model) { model.load() }
    JSheet(onDismiss, title = stringResource(R.string.assistant_info_session), scrollable = true) {
        Column(Modifier.testTag("session-info")) {
            Text(stringResource(R.string.assistant_info_session_text, session.sessionId), style = PersonaTheme.type.callout,
                color = PersonaTheme.palette.ink2)
            if (!state.unavailable) AlwaysGrantsSection(state, onRetry = { scope.launch { model.load() } }, onRevoke = { confirming = it })
        }
    }
    confirming?.let { grant ->
        JDialog(onDismiss = { confirming = null }, title = stringResource(R.string.assistant_always_revoke_title),
            text = stringResource(R.string.assistant_always_revoke_text), destructive = true,
            confirm = stringResource(R.string.assistant_always_revoke) to { confirming = null; scope.launch { model.revoke(grant) }; Unit })
    }
}

/** The grants, or why there are none to show; [onRevoke] asks before anything is revoked. */
@Composable
internal fun AlwaysGrantsSection(
    state: AlwaysGrantsState, onRetry: () -> Unit, onRevoke: (AlwaysGrant) -> Unit, zone: ZoneId = ZoneId.systemDefault(),
) {
    val res = LocalResources.current
    val palette = PersonaTheme.palette
    JSectionHeader(stringResource(R.string.assistant_always_title))
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        if (state.restartNeeded) JInlineStatus(stringResource(R.string.assistant_always_restart), tone = InlineTone.Failed)
        val grants = state.grants
        when {
            grants == null && state.error == null -> JLoading()
            grants?.isEmpty() == true -> Text(stringResource(R.string.assistant_always_empty), style = PersonaTheme.type.callout, color = palette.ink2)
            grants != null -> Column {
                grants.forEachIndexed { index, grant ->
                    JGroupItem(GroupPosition.of(index, grants.size)) {
                        JRow(grant.toolName.ifBlank { stringResource(R.string.assistant_always_unnamed) },
                            subtitle = parseTime(grant.createdAt)?.let { stringResource(R.string.assistant_always_granted,
                                LocalizedFormats.format(it, LocalizedFormats.MONTH_DAY_TIME, res.configuration.locales[0], zone)) },
                            trailing = {
                                JButton(stringResource(R.string.assistant_always_revoke), { onRevoke(grant) }, kind = JButtonKind.Secondary,
                                    size = JButtonSize.Small, enabled = state.revoking == null, loading = state.revoking == grant.keyHash)
                            })
                    }
                }
            }
        }
        state.error?.let { error ->
            JInlineStatus(error, tone = InlineTone.Failed, action = if (grants == null) ({
                JButton(stringResource(R.string.assistant_always_retry), onRetry, kind = JButtonKind.Secondary, size = JButtonSize.Small)
            }) else null)
        }
        Text(stringResource(R.string.assistant_always_scope), Modifier, style = PersonaTheme.type.caption, color = palette.ink3)
    }
}
