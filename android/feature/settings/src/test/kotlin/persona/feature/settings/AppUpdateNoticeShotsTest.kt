package persona.feature.settings

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.LifecycleRegistry
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.AppUpdate
import persona.core.contracts.api.AppUpdates
import persona.core.designsystem.*
import java.io.File
import java.io.IOException

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h700dp-xhdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class AppUpdateNoticeShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private data class Scene(val style: AppStyle, val dark: Boolean)

    @Test fun hostChecksOnLaunchAndForegroundWithThrottleAndHonorsDisabledMode() {
        var calls = 0
        var now = 1_000_000L
        val api = object : AppUpdates {
            override suspend fun check(): AppUpdate? { calls++; return null }
        }
        val controller = AppUpdateState(api, 1, { now })
        val owner = object : LifecycleOwner {
            val registry = LifecycleRegistry.createUnsafe(this)
            override val lifecycle: Lifecycle get() = registry
        }
        var enabled by mutableStateOf(false)
        rule.runOnIdle { owner.registry.currentState = Lifecycle.State.STARTED }
        rule.setContent {
            CompositionLocalProvider(LocalLifecycleOwner provides owner) {
                AppUpdateHost(controller, api, enabled) { Text("usable content") }
            }
        }
        rule.waitForIdle()
        org.junit.Assert.assertEquals(0, calls)
        rule.onNodeWithText("usable content").assertExists()
        rule.runOnIdle { enabled = true }
        rule.waitForIdle()
        org.junit.Assert.assertEquals(1, calls)
        repeat(5) {
            rule.runOnIdle { owner.registry.currentState = Lifecycle.State.CREATED }
            rule.runOnIdle { owner.registry.currentState = Lifecycle.State.STARTED }
            rule.waitForIdle()
        }
        org.junit.Assert.assertEquals(1, calls)
        rule.runOnIdle {
            owner.registry.currentState = Lifecycle.State.CREATED
            now += AppUpdateState.CHECK_INTERVAL_MS
            owner.registry.currentState = Lifecycle.State.STARTED
        }
        rule.waitForIdle()
        org.junit.Assert.assertEquals(2, calls)
        rule.runOnIdle { enabled = false }
        rule.waitForIdle()
        rule.runOnIdle {
            owner.registry.currentState = Lifecycle.State.CREATED
            now += AppUpdateState.CHECK_INTERVAL_MS
            owner.registry.currentState = Lifecycle.State.STARTED
        }
        rule.waitForIdle()
        org.junit.Assert.assertEquals(2, calls)
    }

    @Test fun releaseDownloadAndRetryAcrossAllStylesAndSchemes() = runBlocking {
        val update = AppUpdate(2_000_000_000, "1.0.2000000000", "/apk", "a".repeat(64))
        val api = object : AppUpdates { override suspend fun check() = update }
        val controller = AppUpdateState(api, 1)
        controller.check()
        var scene by mutableStateOf(Scene(AppStyle.Glass, false))
        rule.setContent {
            val store = remember { AppStyleStore(rule.activity) }
            val state by controller.state.collectAsState()
            PersonaTheme(style = scene.style, darkTheme = scene.dark, reduceMotion = true) {
                CompositionLocalProvider(LocalAppStyleStore provides store, LocalLanguageHost provides rule.activity) {
                    Column(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("update-notice")) {
                        AppUpdateNotice(state, controller::dismiss) { AppUpdateInstallButton(controller) }
                        Spacer(Modifier.height(24.dp))
                        Text("Persona", color = PersonaTheme.palette.ink, modifier = Modifier.padding(horizontal = 20.dp))
                    }
                }
            }
        }
        fun capture(phase: String) {
            for (style in AppStyle.entries) for (dark in listOf(false, true)) {
                rule.runOnIdle { scene = Scene(style, dark) }
                rule.waitForIdle()
                rule.onNodeWithText(rule.activity.getString(R.string.settings_update_new_version, update.versionName)).assertExists()
                val label = rule.activity.getString(if (phase == "downloading") R.string.settings_update_downloading else R.string.settings_update_install)
                if (phase == "downloading") rule.onNodeWithText(label).assertIsNotEnabled()
                else rule.onNodeWithText(label).assertIsEnabled()
                val bitmap = rule.onNodeWithTag("update-notice").captureToImage().asAndroidBitmap()
                val output = File("build/app-update-shots/${style.key}-${if (dark) "dark" else "light"}-$phase.png")
                check(output.parentFile!!.mkdirs() || output.parentFile!!.isDirectory)
                output.outputStream().use { check(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
            }
        }
        capture("available")
        val download = CompletableDeferred<Unit>()
        val job = launch(start = CoroutineStart.UNDISPATCHED) {
            controller.install { download.await(); throw AppUpdateFailure(UpdateStage.Download, IOException("connection lost")) }
        }
        capture("downloading")
        download.complete(Unit)
        job.join()
        capture("failed")
        rule.onNodeWithText(rule.activity.getString(R.string.settings_update_download_failed)).assertExists()
        for (stage in listOf(UpdateStage.Metadata, UpdateStage.Verification, UpdateStage.Install)) {
            controller.install { throw AppUpdateFailure(stage, IOException("fixture")) }
            capture(stage.name.lowercase())
            rule.onNodeWithText(rule.activity.getString(stage.message)).assertExists()
        }
        rule.onNodeWithContentDescription(rule.activity.getString(R.string.settings_update_later)).performClick()
        rule.runOnIdle { check(!controller.state.value.notice) }
    }
    @Test fun returningFromInstallPermissionContinuesOnlyAfterGrant() {
        var checks = 0
        val update = AppUpdate(2_000_000_000, "test", "/apk", "a".repeat(64))
        val controller = AppUpdateState(object : AppUpdates {
            override suspend fun check(): AppUpdate { checks++; return update }
        }, 1, initialLatest = update)
        val pm = org.robolectric.Shadows.shadowOf(rule.activity.packageManager)
        pm.setCanRequestPackageInstalls(false)
        rule.setContent { PersonaTheme { AppUpdateInstallButton(controller) } }
        val install = rule.activity.getString(R.string.settings_update_install)
        rule.onNodeWithText(install).performClick()
        val first = org.robolectric.Shadows.shadowOf(rule.activity).nextStartedActivityForResult
        org.junit.Assert.assertEquals(android.provider.Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, first.intent.action)
        rule.runOnIdle { rule.activity.activityResultRegistry.dispatchResult(first.requestCode, android.app.Activity.RESULT_CANCELED, null) }
        rule.waitForIdle()
        org.junit.Assert.assertEquals(0, checks)
        rule.onNodeWithText(install).performClick()
        val second = org.robolectric.Shadows.shadowOf(rule.activity).nextStartedActivityForResult
        pm.setCanRequestPackageInstalls(true)
        rule.runOnIdle { rule.activity.activityResultRegistry.dispatchResult(second.requestCode, android.app.Activity.RESULT_OK, null) }
        rule.waitUntil(10_000) { checks == 1 }
    }

}
