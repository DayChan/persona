package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.designsystem.*
import java.io.File

/**
 * The ultracode switch as the mode sheet draws it under the thinking effort (a sheet does not slide up under
 * Robolectric): off, on and queued, and the node's reason, in every style, light and dark, then at Android's largest
 * font scale; the description is read in full every time (#3160). PNGs in `build/ultracode-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
// Tall enough for every row at the largest font scale; the real sheet scrolls.
@Config(sdk = [35], qualifiers = "en-w400dp-h1600dp-xhdpi")
class UltracodeSwitchShotsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun everyStateInEveryStyleAndAppearance() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var fontScale by mutableStateOf(1f)
        val off = Session(machineId = "box", sessionId = "s1", agent = "claude", status = "idle", ultracode = false)
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration, LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                        verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        JSectionHeader("Thinking effort")
                        UltracodeSwitchRow(off, null, enabled = true) {}
                        UltracodeSwitchRow(off.copy(ultracode = true), null, enabled = true) {}
                        UltracodeSwitchRow(off.copy(ultracodePending = true), Ultracode.Ask(true, sending = false), enabled = true) {}
                        UltracodeSwitchRow(off.copy(ultracodeNote = "This model can't use ultracode"), null, enabled = true) {}
                        UltracodeSwitchRow(off, Ultracode.Ask(true), enabled = false) {}
                    }
                }
            }
        }
        val shots = AppStyle.entries.flatMap { s -> listOf(Triple(s, false, 1f), Triple(s, true, 1f)) } +
            AppStyle.entries.map { Triple(it, false, 2f) }
        for ((s, d, scale) in shots) {
            style = s; dark = d; fontScale = scale
            rule.waitForIdle()
            rule.onNodeWithText("Applies next turn").assertExists()
            rule.onNodeWithText("This model can't use ultracode").assertExists()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val out = File(System.getProperty("persona.shots") ?: "build/ultracode-shots").apply { mkdirs() }
            File(out, "ultracode-${s.name.lowercase()}-${if (d) "dark" else "light"}${if (scale > 1f) "-large" else ""}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
    }
}
