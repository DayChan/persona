package persona.core.designsystem

import android.content.Context
import android.content.ContextWrapper
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.AppLanguage

/**
 * The application's `attachBaseContext` reads the language before the application is attached, when the base context's
 * `applicationContext` is still null. Below Android 13 that read goes to the app's own preferences (#3009).
 */
@RunWith(AndroidJUnit4::class)
class AppLanguagesTest {
    /** A base context as `Application.attachBaseContext` gets it: no application context yet. */
    private class Unattached(base: Context) : ContextWrapper(base) {
        override fun getApplicationContext(): Context? = null
    }

    private val app: Context = ApplicationProvider.getApplicationContext()

    @Test @Config(sdk = [31])
    fun readsTheStoredChoiceBeforeTheApplicationIsAttached() {
        app.getSharedPreferences("persona-language", Context.MODE_PRIVATE).edit().putString("language", AppLanguage.Ja.tag).commit()
        AppLanguages.init(Unattached(app))
        assertEquals(AppLanguage.Ja, AppLanguages.choice.value)
        assertEquals(AppLanguage.Ja, AppLanguages.shown(Unattached(app)))
    }

    @Test @Config(sdk = [31])
    fun withNothingStoredFollowsThePhoneBeforeTheApplicationIsAttached() {
        app.getSharedPreferences("persona-language", Context.MODE_PRIVATE).edit().clear().commit()
        AppLanguages.init(Unattached(app))
        assertEquals(AppLanguage.System, AppLanguages.choice.value)
    }
}
