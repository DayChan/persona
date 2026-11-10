package persona.core.designsystem

import androidx.compose.foundation.focusable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusManager
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertLeftPositionInRootIsEqualTo
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class HomeChromeTest {
    @get:Rule val rule = createComposeRule()

    @Test fun openedDrawerPreventsKeyboardFocusFromEnteringTheHomeBehindIt() {
        var opened by mutableStateOf(false)
        var homeFocused = false
        val home = FocusRequester()
        val drawer = FocusRequester()
        lateinit var focus: FocusManager
        rule.setContent {
            focus = LocalFocusManager.current
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                JShellDrawer(opened, onDismiss = { opened = false }, drawer = {
                    Box(Modifier.fillMaxSize().focusRequester(drawer).focusable())
                }) {
                    Box(Modifier.fillMaxSize().focusRequester(home)
                        .onFocusChanged { homeFocused = it.isFocused }.focusable())
                }
            }
        }
        rule.runOnIdle { assertTrue(home.requestFocus()) }
        rule.runOnIdle {
            assertTrue(homeFocused)
            focus.clearFocus(force = true)
            opened = true
        }
        rule.waitForIdle()
        rule.runOnIdle {
            assertFalse(home.requestFocus())
            assertFalse(homeFocused)
            assertTrue(drawer.requestFocus())
            focus.clearFocus(force = true)
            opened = false
        }
        rule.waitForIdle()
        rule.runOnIdle { assertTrue(home.requestFocus()) }
        rule.runOnIdle { assertTrue(homeFocused) }
    }

    @Test fun theSidebarColumnComesAndGoesWithoutRebuildingThePage() {
        var regular by mutableStateOf(false)
        var shown by mutableStateOf(true)
        var built = 0
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                JSidebarColumn(if (regular) ({ Box(Modifier.fillMaxSize().testTag("sidebar")) }) else null,
                    width = 120.dp, pageStart = 140.dp, shown = shown) {
                    remember { built++ }
                    Box(Modifier.fillMaxSize().testTag("page"))
                }
            }
        }
        // A phone: the page fills the box.
        rule.onNodeWithTag("sidebar").assertDoesNotExist()
        rule.onNodeWithTag("page").assertLeftPositionInRootIsEqualTo(0.dp)
        // Unfolded: the column, and the page past it (here past a fold 20dp wide).
        regular = true
        rule.onNodeWithTag("sidebar").assertExists()
        rule.onNodeWithTag("page").assertLeftPositionInRootIsEqualTo(140.dp)
        // Hidden with the avatar, then shown again.
        shown = false
        rule.onNodeWithTag("sidebar").assertDoesNotExist()
        rule.onNodeWithTag("page").assertLeftPositionInRootIsEqualTo(0.dp)
        shown = true
        rule.onNodeWithTag("page").assertLeftPositionInRootIsEqualTo(140.dp)
        // Folded again.
        regular = false
        rule.onNodeWithTag("sidebar").assertDoesNotExist()
        rule.onNodeWithTag("page").assertLeftPositionInRootIsEqualTo(0.dp)
        rule.runOnIdle { assertEquals("the page was composed once, through every change", 1, built) }
    }
}
