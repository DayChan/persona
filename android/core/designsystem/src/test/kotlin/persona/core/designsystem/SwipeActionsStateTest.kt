package persona.core.designsystem

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.StateRestorationTester
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.icons.PhIcons

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class SwipeActionsStateTest {
    @get:Rule val rule = createComposeRule()

    @Test fun anActionChangingDuringTheReturnAnimationCannotExecuteTheGestureAgain() {
        var revision by mutableStateOf(0)
        var calls = 0
        rule.setContent {
            PersonaTheme {
                JSwipeActions(start = SwipeAction("Done $revision", PhIcons.Check, Status.Unread, { calls++; revision++ })) {
                    Text("Synthetic row", Modifier.fillMaxWidth().testTag("row").clickable {})
                }
            }
        }
        repeat(2) { index ->
            rule.onNodeWithTag("row").performTouchInput { swipeRight() }
            rule.waitForIdle()
            rule.runOnIdle { assertEquals(index + 1, calls) }
        }
    }

    @Test fun aRestoredRowAcceptsANewDeliberateGesture() {
        val restoration = StateRestorationTester(rule)
        var calls = 0
        restoration.setContent {
            PersonaTheme {
                JSwipeActions(start = SwipeAction("Done", PhIcons.Check, Status.Unread, { calls++ })) {
                    Text("Synthetic row", Modifier.fillMaxWidth().testTag("row").clickable {})
                }
            }
        }
        rule.mainClock.autoAdvance = false
        rule.onNodeWithTag("row").performTouchInput { swipeRight() }
        // Stop at the dismissed anchor, before the return animation can finish saving a settled value.
        repeat(50) {
            if (calls == 0) rule.mainClock.advanceTimeByFrame()
        }
        assertEquals(1, calls)
        restoration.emulateSavedInstanceStateRestore()
        rule.mainClock.autoAdvance = true
        rule.waitForIdle()
        rule.runOnIdle { assertEquals(1, calls) }
        rule.onNodeWithTag("row").performTouchInput { swipeRight() }
        rule.waitForIdle()
        rule.runOnIdle { assertEquals(2, calls) }
    }
}
