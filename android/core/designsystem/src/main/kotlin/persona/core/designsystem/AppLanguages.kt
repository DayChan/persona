package persona.core.designsystem

import android.app.Activity
import android.app.LocaleManager
import android.content.Context
import android.content.res.Configuration
import android.content.res.Resources
import android.os.Build
import android.os.LocaleList
import androidx.compose.runtime.staticCompositionLocalOf
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import persona.core.contracts.AppLanguage
import persona.core.contracts.ClientLanguage
import java.util.Locale

/**
 * Settings › Language (docs/protocol/i18n.md): follow the phone, or always Simplified Chinese, English or Japanese.
 *
 * On Android 13 and later the choice is the system's per-app language ([LocaleManager]), so it also shows in the
 * phone's own App language settings, which may change it; earlier the app keeps it itself. Either way every
 * activity starts through [wrap], which pins its resources to the [shown] language: the phone's Traditional Chinese
 * shows Simplified, a language the app does not have shows English, and dates follow the text.
 */
object AppLanguages {
    private const val PREFERENCES = "persona-language"
    private const val KEY = "language"

    private val chosen = MutableStateFlow(AppLanguage.System)
    /** What Settings › Language says; [AppLanguage.System] follows the phone. */
    val choice: StateFlow<AppLanguage> = chosen.asStateFlow()

    /** Reads the choice; the application calls this before anything shows text. */
    fun init(context: Context) {
        chosen.value = stored(context)
        ClientLanguage.current = shown(context)
    }

    /** The language the app's text is in now: the choice, or the phone's language resolved to one the app has. */
    fun shown(context: Context): AppLanguage = when (val choice = stored(context)) {
        AppLanguage.System -> phoneLanguage(context)
        else -> choice
    }

    /** The language the app shows when it follows the phone. */
    fun phoneLanguage(context: Context): AppLanguage = AppLanguage.resolve(systemLocales(context))

    /** The locale for [language]'s resources and formats ([AppLanguage.ZhHans] is the `values-zh` resources). */
    fun locale(language: AppLanguage): Locale = Locale.forLanguageTag(language.tag ?: "en")

    /** Changes the choice; the open activities are recreated in the new language. */
    fun choose(activity: Activity, language: AppLanguage) {
        val app = activity.applicationContext
        app.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE).edit().putString(KEY, language.tag).apply()
        chosen.value = language
        ClientLanguage.current = shown(app)
        if (Build.VERSION.SDK_INT >= 33) {
            // The system recreates the activities and updates the application's resources itself.
            app.getSystemService(LocaleManager::class.java).applicationLocales =
                language.tag?.let(LocaleList::forLanguageTags) ?: LocaleList.getEmptyLocaleList()
        } else {
            // Notifications and widgets are built from the application context: move it to the new language too.
            @Suppress("DEPRECATION")
            app.resources.updateConfiguration(pinned(app.resources.configuration, locale(shown(app))), app.resources.displayMetrics)
            activity.recreate()
        }
    }

    /**
     * [base] with its resources in the [shown] language, for `attachBaseContext` (activities, services, the
     * application). Only the locale is overridden: dark mode, size and density keep following the phone.
     */
    fun wrap(base: Context): Context {
        val language = shown(base)
        ClientLanguage.current = language
        val locale = locale(language)
        if (fits(base.resources.configuration.locales[0], language)) return base
        return base.createConfigurationContext(pinned(Configuration(base.resources.configuration), locale))
    }

    /**
     * Whether resources in [current] already come in [language], so its regional formats (en-GB dates) are kept.
     * Chinese must be Simplified: `zh-TW` would miss `values-zh` and fall back to English.
     */
    internal fun fits(current: Locale, language: AppLanguage): Boolean = when {
        AppLanguage.of(current) != language -> false
        language != AppLanguage.ZhHans -> true
        current.script.isNotEmpty() -> current.script == "Hans"
        else -> current.country in setOf("", "CN", "SG", "MY")
    }

    private fun pinned(configuration: Configuration, locale: Locale) = Configuration(configuration).apply {
        setLocales(LocaleList(locale))
        setLayoutDirection(locale)
    }

    private fun stored(context: Context): AppLanguage {
        if (Build.VERSION.SDK_INT >= 33) {
            // The phone's App language page may have changed it while the app was away.
            val locales = context.getSystemService(LocaleManager::class.java)?.applicationLocales
            if (locales != null) return if (locales.isEmpty) AppLanguage.System else AppLanguage.of(locales[0]) ?: AppLanguage.System
        }
        // Below Android 13 this runs from the application's attachBaseContext, where applicationContext is still null.
        return AppLanguage.fromTag((context.applicationContext ?: context)
            .getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE).getString(KEY, null))
    }

    private fun systemLocales(context: Context): List<Locale> {
        val list = if (Build.VERSION.SDK_INT >= 33) context.getSystemService(LocaleManager::class.java)?.systemLocales
            else null
        val locales = list ?: Resources.getSystem().configuration.locales
        return (0 until locales.size()).map(locales::get)
    }
}

/** The activity that Settings › Language recreates; null in previews and tests, where the page shows no choices. */
val LocalLanguageHost = staticCompositionLocalOf<Activity?> { null }
