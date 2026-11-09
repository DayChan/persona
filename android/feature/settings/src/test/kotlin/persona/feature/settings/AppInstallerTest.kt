package persona.feature.settings

import android.content.Context
import android.content.Intent
import android.content.pm.PackageInfo
import android.content.pm.PackageInstaller
import androidx.test.core.app.ApplicationProvider
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.Implementation
import org.robolectric.annotation.Implements
import org.robolectric.shadows.ShadowPackageInstaller
import persona.core.contracts.api.AppUpdate
import persona.core.contracts.api.AppUpdates
import java.io.IOException
import java.io.OutputStream

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], shadows = [AppInstallerTest.CapturingPackageInstaller::class])
class AppInstallerTest {
    // Robolectric 4.17 does not copy requireUserAction into SessionInfo. Capture the actual submitted params.
    @Implements(PackageInstaller::class)
    class CapturingPackageInstaller : ShadowPackageInstaller() {
        companion object { var submitted: PackageInstaller.SessionParams? = null }
        @Implementation
        public override fun createSession(params: PackageInstaller.SessionParams): Int {
            submitted = params
            return super.createSession(params)
        }
    }

    private val context = ApplicationProvider.getApplicationContext<Context>()
    private val pm get() = shadowOf(context.packageManager)
    private val digest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
    private val update = AppUpdate(42, "42", "/api/v1/app/android/apk", digest)
    private var downloads = 0
    private var payload = "abc"
    private var archivePackage = context.packageName
    private var archiveVersion = 42L
    private var installedWhileDownloading: Long? = null
    private var failDownload = false
    private val updates = object : AppUpdates {
        override suspend fun check() = update
        override suspend fun download(update: AppUpdate, destination: OutputStream) {
            downloads++
            destination.write(payload.toByteArray())
            if (failDownload) throw IOException("download interrupted")
            val apk = context.cacheDir.listFiles()!!.single { it.name.startsWith("persona-update-") }
            pm.setPackageArchiveInfo(apk.absolutePath, PackageInfo().apply {
                packageName = archivePackage
                setLongVersionCode(archiveVersion)
            })
            installedWhileDownloading?.let { pm.getInternalMutablePackageInfo(context.packageName).setLongVersionCode(it) }
        }
    }

    @Before fun setup() {
        CapturingPackageInstaller.submitted = null
        pm.setCanRequestPackageInstalls(true)
        pm.getInternalMutablePackageInfo(context.packageName).setLongVersionCode(41)
    }

    @Test fun verifiedNewVersionCreatesSessionRequiringSystemConfirmation() = runBlocking {
        AppInstaller.install(context, updates, update)
        val session = context.packageManager.packageInstaller.mySessions.single()
        assertEquals(context.packageName, session.appPackageName)
        assertEquals(PackageInstaller.SessionParams.USER_ACTION_REQUIRED, org.robolectric.util.ReflectionHelpers.getField<Int>(CapturingPackageInstaller.submitted!!, "requireUserAction"))
        assertEquals(1, downloads)
        assertTrue(context.cacheDir.listFiles()!!.any { it.name.startsWith("persona-update-") })
    }

    @Test fun pendingSessionAndCancelledInstallReuseOneVerifiedDownload() = runBlocking {
        AppInstaller.install(context, updates, update)
        val installer = context.packageManager.packageInstaller
        // Robolectric does not seal SessionInfo when its session commits; model Android's pending state explicitly.
        org.robolectric.util.ReflectionHelpers.setField(installer.mySessions.single(), "sealed", true)
        assertThrows(IllegalStateException::class.java) { runBlocking { AppInstaller.install(context, updates, update) } }
        assertEquals(1, downloads)
        installer.abandonSession(installer.mySessions.single().sessionId)
        AppInstaller.install(context, updates, update)
        assertEquals(1, downloads)
        assertEquals(1, installer.mySessions.size)
    }

    @Test fun uncommittedSessionFromInterruptedPreparationIsAbandonedBeforeRetry() = runBlocking {
        val installer = context.packageManager.packageInstaller
        val staleId = installer.createSession(PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
            setAppPackageName(context.packageName)
        })
        AppInstaller.install(context, updates, update)
        assertNull(installer.getSessionInfo(staleId))
        assertEquals(1, downloads)
        assertEquals(1, installer.mySessions.size)
        assertNotEquals(staleId, installer.mySessions.single().sessionId)
    }

    @Test fun equalAndOlderVersionsAreRejectedBeforeDownload() {
        for (version in listOf(40L, 41L)) {
            assertThrows(IllegalStateException::class.java) { runBlocking { AppInstaller.install(context, updates, update.copy(versionCode = version)) } }
        }
        assertEquals(0, downloads)
        assertNoSessionOrTemporaryApk()
    }

    @Test fun newerInstalledDuringDownloadIsNotReplaced() {
        installedWhileDownloading = 43
        assertThrows(IllegalStateException::class.java) { runBlocking { AppInstaller.install(context, updates, update) } }
        assertNoSessionOrTemporaryApk()
    }

    @Test fun checksumAndArchiveIdentityFailuresNeverCreateInstallSession() {
        payload = "abd"
        assertRejected()
        payload = "abc"
        archivePackage = "other.application"
        assertRejected()
        archivePackage = context.packageName
        archiveVersion = 43
        assertRejected()
    }

    @Test fun interruptedDownloadCleansPartialApk() {
        failDownload = true
        assertEquals(UpdateStage.Download, assertThrows(AppUpdateFailure::class.java) { runBlocking { AppInstaller.install(context, updates, update) } }.stage)
        assertNoSessionOrTemporaryApk()
    }

    @Test fun missingPermissionOrHashNeverDownloads() {
        assertEquals(UpdateStage.Verification, assertThrows(AppUpdateFailure::class.java) { runBlocking { AppInstaller.install(context, updates, update.copy(sha256 = null)) } }.stage)
        pm.setCanRequestPackageInstalls(false)
        assertRejected()
        assertEquals(0, downloads)
    }

    @Test fun pendingUserActionOpensSystemConfirmationAndDoesNotClaimSuccess() {
        val confirmation = Intent("fixture.system.INSTALL_CONFIRM")
        val callback = Intent(context, InstallResultActivity::class.java)
            .putExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_PENDING_USER_ACTION)
            .putExtra(Intent.EXTRA_INTENT, confirmation)
        val activity = Robolectric.buildActivity(InstallResultActivity::class.java, callback).create().get()
        assertEquals(confirmation.action, shadowOf(activity).nextStartedActivity.action)
        assertFalse(context.getSharedPreferences("settings-update", Context.MODE_PRIVATE).contains("result"))
        assertTrue(activity.isFinishing)
    }

    @Test fun missingConfirmationAbandonsOnlyTheReferencedSessionSoRetryIsPossible() {
        val installer = context.packageManager.packageInstaller
        fun pending() = installer.createSession(PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
            setAppPackageName(context.packageName)
        })
        val failed = pending()
        val unrelated = pending()
        val callback = Intent(context, InstallResultActivity::class.java)
            .putExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_PENDING_USER_ACTION)
            .putExtra(PackageInstaller.EXTRA_SESSION_ID, failed)
        val activity = Robolectric.buildActivity(InstallResultActivity::class.java, callback).create().get()
        assertNull(installer.getSessionInfo(failed))
        assertNotNull(installer.getSessionInfo(unrelated))
        assertTrue(activity.isFinishing)
        assertNull(shadowOf(activity).nextStartedActivity)
        assertFalse(context.getSharedPreferences("settings-update", Context.MODE_PRIVATE).contains("result"))
    }

    @Test fun userCancellationIsRecordedAsCancellation() {
        val callback = Intent(context, InstallResultActivity::class.java)
            .putExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE_ABORTED)
        Robolectric.buildActivity(InstallResultActivity::class.java, callback).create()
        assertEquals(context.getString(R.string.settings_install_cancelled),
            context.getSharedPreferences("settings-update", Context.MODE_PRIVATE).getString("result", null))
    }

    private fun assertRejected() {
        assertThrows(IllegalStateException::class.java) { runBlocking { AppInstaller.install(context, updates, update) } }
        assertNoSessionOrTemporaryApk()
    }
    private fun assertNoSessionOrTemporaryApk() {
        assertTrue(context.packageManager.packageInstaller.mySessions.isEmpty())
        assertNoTemporaryApk()
    }
    private fun assertNoTemporaryApk() {
        assertTrue(context.cacheDir.listFiles()!!.none { it.name.startsWith("persona-update-") })
    }
}
