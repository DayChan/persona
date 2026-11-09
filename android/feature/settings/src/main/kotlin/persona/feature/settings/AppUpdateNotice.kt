package persona.feature.settings

import android.provider.Settings
import android.content.Intent
import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LifecycleEventEffect
import kotlinx.coroutines.launch
import persona.core.contracts.api.AppUpdates
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons

/** Only the visible app checks; no background service or periodic mobile scheduling is promised. */
@Composable
fun AppUpdateHost(controller: AppUpdateState, updates: AppUpdates, enabled: Boolean, content: @Composable () -> Unit) {
    val scope = rememberCoroutineScope()
    val state by controller.state.collectAsState()
    LaunchedEffect(controller, enabled) { if (enabled) controller.check() }
    LifecycleEventEffect(Lifecycle.Event.ON_START) { if (enabled) scope.launch { controller.check() } }
    Column(Modifier.fillMaxSize()) {
        if (enabled && state.notice) AppUpdateNotice(state, controller::dismiss) { AppUpdateInstallButton(controller) }
        Box(Modifier.weight(1f)) { content() }
    }
}

@Composable
internal fun AppUpdateNotice(state: AppUpdateState.State, onDismiss: () -> Unit, action: @Composable () -> Unit) {
    val latest = state.latest ?: return
    JCard(Modifier.statusBarsPadding().padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s)) {
        Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            Text(stringResource(R.string.settings_update_new_version, latest.versionName), Modifier.weight(1f),
                style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
            JIconButton(PhIcons.X, stringResource(R.string.settings_update_later), onDismiss)
        }
        Text(stringResource(R.string.settings_update_auto_notice), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        Spacer(Modifier.height(Tokens.Space.s))
        action()
        state.message?.let { SettingsCaption(stringResource(it)) }
    }
}

@Composable
internal fun AppUpdateInstallButton(controller: AppUpdateState) {
    val context = LocalContext.current
    val state by controller.state.collectAsState()
    val permission = rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult()) {
        if (context.packageManager.canRequestPackageInstalls()) {
            controller.startInstall { AppInstaller.install(context.applicationContext, controller.api, it) }
        } else controller.permissionRequired()
    }
    JButton(stringResource(if (state.installing) R.string.settings_update_downloading else R.string.settings_update_install),
        enabled = !state.installing && !state.checking,
        onClick = {
            if (!context.packageManager.canRequestPackageInstalls()) {
                try {
                    permission.launch(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                        Uri.parse("package:${context.packageName}")))
                } catch (error: android.content.ActivityNotFoundException) {
                    android.util.Log.w("AppUpdate", "Install permission settings unavailable", error)
                }
                controller.permissionRequired()
            } else controller.startInstall { AppInstaller.install(context.applicationContext, controller.api, it) }
        })
}
