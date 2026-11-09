package persona.feature.settings

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.launch
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import org.junit.Before
import org.junit.After
import persona.core.contracts.api.AppUpdate
import persona.core.contracts.api.AppUpdates
import java.io.IOException

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35])
@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class AppUpdateStateTest {
    @Before fun setMainDispatcher() { Dispatchers.setMain(StandardTestDispatcher()) }
    @After fun resetMainDispatcher() { Dispatchers.resetMain() }

    private val release = AppUpdate(101, "1.0.101", "/apk", "a".repeat(64))
    private class Api(var latest: AppUpdate?) : AppUpdates {
        var calls = 0
        var failure: Exception? = null
        var blocked: CompletableDeferred<Unit>? = null
        override suspend fun check(): AppUpdate? {
            calls++
            blocked?.await()
            failure?.let { throw it }
            return latest
        }
    }
    private class Saved {
        var now = 1_000_000L
        var attempt = 0L
        var dismissed = 0L
        fun state(api: AppUpdates) = AppUpdateState(api, 100, { now }, { attempt }, { attempt = it }, { dismissed }, { dismissed = it })
    }

    @Test fun onlyANewerReleaseOffersInstallation() = runTest {
        for (code in listOf(99L, 100L, 101L)) {
            val api = Api(release.copy(versionCode = code))
            val model = Saved().state(api)
            model.check(manual = true)
            assertEquals(code > 100, model.state.value.notice)
            assertEquals(if (code > 100) code else null, model.state.value.latest?.versionCode)
            assertEquals(if (code > 100) null else R.string.settings_update_none, model.state.value.message)
            assertFalse(model.state.value.checking)
        }
    }

    @Test fun automaticFailuresAreQuietManualFailuresAreActionableAndRetryWorks() = runTest {
        val saved = Saved()
        val api = Api(release).apply { failure = IOException("offline") }
        val model = saved.state(api)
        model.check()
        assertNull(model.state.value.message)
        assertFalse(model.state.value.checking)
        model.check(manual = true)
        assertEquals(R.string.settings_update_check_failed, model.state.value.message)
        api.failure = null
        model.check(manual = true)
        assertEquals(release, model.state.value.latest)
        assertNull(model.state.value.message)
        assertEquals(3, api.calls)
    }

    @Test fun sixHourThrottleSurvivesControllerRecreationAndRepeatedForegrounds() = runTest {
        val saved = Saved()
        val api = Api(release)
        val model = saved.state(api)
        model.check()
        repeat(12) { model.check() }
        saved.state(api).check()
        assertEquals(1, api.calls)
        saved.now += AppUpdateState.CHECK_INTERVAL_MS - 1
        saved.state(api).check()
        assertEquals(1, api.calls)
        saved.now++
        saved.state(api).check()
        assertEquals(2, api.calls)
    }

    @Test fun manualCheckBypassesAutomaticThrottle() = runTest {
        val saved = Saved()
        val api = Api(null)
        val model = saved.state(api)
        model.check()
        model.check(manual = true)
        assertEquals(2, api.calls)
        assertEquals(R.string.settings_update_none, model.state.value.message)
    }

    @Test fun checkingDoesNotQueueAnotherCheckOrDownload() = runTest {
        val api = Api(release)
        val model = Saved().state(api)
        model.check()
        api.blocked = CompletableDeferred()
        val job = launch(start = CoroutineStart.UNDISPATCHED) { model.check(manual = true) }
        assertTrue(model.state.value.checking)
        var downloads = 0
        model.check(manual = true)
        model.install { downloads++ }
        assertEquals(2, api.calls)
        assertEquals(0, downloads)
        api.blocked!!.complete(Unit)
        job.join()
        assertFalse(model.state.value.checking)
    }

    @Test fun downloadingRejectsDuplicateDownloadsAndChecksThenAllowsRetry() = runTest {
        val api = Api(release)
        val model = Saved().state(api)
        model.check()
        val blocked = CompletableDeferred<Unit>()
        var downloads = 0
        val job = launch(start = CoroutineStart.UNDISPATCHED) {
            model.install { downloads++; blocked.await(); throw IOException("download interrupted") }
        }
        assertTrue(model.state.value.installing)
        model.install { downloads++ }
        model.check(manual = true)
        assertEquals(1, downloads)
        assertEquals(2, api.calls)
        blocked.complete(Unit)
        job.join()
        assertFalse(model.state.value.installing)
        assertEquals(R.string.settings_update_install_failed, model.state.value.message)
        model.install { downloads++ }
        assertEquals(2, downloads)
        assertEquals(R.string.settings_update_verified, model.state.value.message)
    }

    @Test fun dismissalSurvivesRecreationButANewerReleaseIsShown() = runTest {
        val saved = Saved()
        val api = Api(release)
        val model = saved.state(api)
        model.check()
        model.dismiss()
        assertFalse(model.state.value.notice)
        assertEquals(release.versionCode, saved.dismissed)
        val recreated = saved.state(api)
        recreated.check(manual = true)
        assertFalse(recreated.state.value.notice)
        assertEquals(release, recreated.state.value.latest)
        api.latest = release.copy(versionCode = 102, versionName = "1.0.102")
        recreated.check(manual = true)
        assertTrue(recreated.state.value.notice)
    }

    @Test fun defaultStorageStillThrottlesAndRemembersDismissalInMemory() = runTest {
        val api = Api(release)
        val model = AppUpdateState(api, 100, { 1_000_000L })
        model.check()
        repeat(5) { model.check() }
        assertEquals(1, api.calls)
        model.dismiss()
        model.check(manual = true)
        assertFalse(model.state.value.notice)
        api.latest = release.copy(versionCode = 102)
        model.check(manual = true)
        assertTrue(model.state.value.notice)
    }

    @Test fun restoredMetadataShowsNoticeWithoutWaitingForThrottledNetwork() = runTest {
        val api = Api(release)
        val model = AppUpdateState(api, 100, { 1_000_001L }, { 1_000_000L }, initialLatest = release)
        model.check()
        assertEquals(0, api.calls)
        assertEquals(release, model.state.value.latest)
        assertTrue(model.state.value.notice)
        val alreadyInstalled = AppUpdateState(api, 101, initialLatest = release)
        assertNull(alreadyInstalled.state.value.latest)
    }

    @Test fun switchedSourceDiscardsInFlightAnswerAndBlocksInstallation() = runTest {
        val api = Api(release).apply { blocked = CompletableDeferred() }
        var valid = true
        val model = AppUpdateState(api, 100, validSource = { valid })
        val check = launch(start = CoroutineStart.UNDISPATCHED) { model.check() }
        valid = false
        api.blocked!!.complete(Unit)
        check.join()
        assertNull(model.state.value.latest)
        assertFalse(model.state.value.checking)
        var downloads = 0
        model.install { downloads++ }
        assertEquals(0, downloads)
    }

    @OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
    @Test fun downloadSurvivesTheCallingUiScopeBeingCancelled() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        try {
            val model = AppUpdateState(Api(release), 100, initialLatest = release)
            val blocked = CompletableDeferred<Unit>()
            var finished = false
            val ui = launch { model.startInstall { blocked.await(); finished = true } }
            runCurrent()
            assertTrue(model.state.value.installing)
            ui.cancel()
            blocked.complete(Unit)
            runCurrent()
            assertTrue(finished)
            assertFalse(model.state.value.installing)
            assertEquals(R.string.settings_update_verified, model.state.value.message)
        } finally { Dispatchers.resetMain() }
    }

    @Test fun clockRollbackDoesNotSuppressChecksForever() = runTest {
        val saved = Saved()
        val api = Api(null)
        saved.state(api).check()
        saved.now--
        saved.state(api).check()
        assertEquals(2, api.calls)
    }
    @Test fun installRefreshesCachedMetadataBeforeDownloading() = runTest {
        val current = release.copy(versionCode = 102, sha256 = "b".repeat(64))
        val api = Api(current)
        val model = AppUpdateState(api, 100, initialLatest = release)
        var downloaded: AppUpdate? = null
        model.install { downloaded = it }
        assertEquals(current, downloaded)
        assertEquals(current, model.state.value.latest)
        assertEquals(1, api.calls)
    }

    @Test fun refreshFailureNeverDownloadsCachedReleaseAndCanRetry() = runTest {
        val api = Api(release).apply { failure = IOException("offline") }
        val model = AppUpdateState(api, 100, initialLatest = release)
        var downloads = 0
        model.install { downloads++ }
        assertEquals(0, downloads)
        assertEquals(R.string.settings_update_metadata_failed, model.state.value.message)
        api.failure = null
        model.install { downloads++ }
        assertEquals(1, downloads)
    }

    @Test fun eachFailureStageHasItsOwnActionableMessage() = runTest {
        val model = AppUpdateState(Api(release), 100, initialLatest = release)
        for (stage in UpdateStage.entries) {
            model.install { throw AppUpdateFailure(stage, IOException("fixture")) }
            assertEquals(stage.message, model.state.value.message)
        }
    }

}
