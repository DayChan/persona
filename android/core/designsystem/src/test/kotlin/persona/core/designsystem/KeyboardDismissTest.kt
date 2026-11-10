package persona.core.designsystem

import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusDirection
import androidx.compose.ui.focus.FocusManager
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.click
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeUp
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.withLink
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

/**
 * A tap on a page's blank space clears the text field's focus (and with it the keyboard); a tap that something under
 * the finger handles (a button, a long press, a scroll, the field itself, the composer around it) still does its own
 * thing and leaves the focus where it was.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class KeyboardDismissTest {
    @get:Rule val rule = createComposeRule()

    private var focused by mutableStateOf(false)
    private var clicks = 0
    private var linkClicks = 0
    private var longPresses = 0
    private var blankTaps = 0
    private var style by mutableStateOf(AppStyle.Glass)
    private lateinit var listState: LazyListState

    private fun setPage(nested: Boolean = false) {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                var text by remember { mutableStateOf("") }
                val state = rememberLazyListState()
                listState = state
                val fieldFocus = remember { FocusRequester() }
                val page: @Composable () -> Unit = {
                    JPage("对话", bottomBar = {
                        ComposerFrame(Modifier.testTag("composer"), onBlankTap = { blankTaps++; fieldFocus.requestFocus() }) {
                            BasicTextField(text, { text = it }, Modifier.fillMaxWidth().testTag("field").focusRequester(fieldFocus)
                                .onFocusChanged { focused = it.isFocused })
                        }
                    }) { padding ->
                        Column(Modifier.fillMaxSize().padding(padding)) {
                            Box(Modifier.size(120.dp, 48.dp).testTag("button").clickable { clicks++ })
                            Text(buildAnnotatedString {
                                withLink(LinkAnnotation.Clickable("message-link") { linkClicks++ }) { append("消息链接") }
                            }, Modifier.testTag("link"))
                            Box(Modifier.size(120.dp, 48.dp).testTag("card").pointerInput(Unit) { detectTapGestures(onLongPress = { longPresses++ }) })
                            Box(Modifier.fillMaxWidth().height(48.dp).testTag("message").onLongPress(Unit) { longPresses++ })
                            LazyColumn(Modifier.fillMaxWidth().height(200.dp).testTag("list"), state = state) {
                                items(50) { Text("消息 $it", Modifier.fillMaxWidth().height(40.dp)) }
                            }
                            Box(Modifier.fillMaxWidth().weight(1f).testTag("blank"))
                        }
                    }
                }
                if (nested) JPage("外页") { _ -> page() } else page()
            }
        }
        focusField()
    }

    private fun focusField() {
        rule.onNodeWithTag("field").performTouchInput { click() }
        rule.waitForIdle()
        assertTrue("the field takes focus when tapped", focused)
    }

    @Test fun blankTapClearsFocusInEveryStyle() {
        setPage()
        for (each in AppStyle.entries) {
            style = each
            rule.waitForIdle()
            focusField()
            rule.onNodeWithTag("blank").performTouchInput { click(center) }
            rule.waitForIdle()
            assertFalse("blank tap clears the focus in $style", focused)
        }
    }

    @Test fun tapOnTheFieldKeepsFocus() {
        setPage()
        rule.onNodeWithTag("field").performTouchInput { click() }
        rule.waitForIdle()
        assertTrue(focused)
    }

    @Test fun tapOnTheComposerAroundTheFieldKeepsFocus() {
        setPage(nested = true)
        // The composer's padding, outside the field: in glass the composer floats over the page's content.
        for (each in AppStyle.entries) {
            style = each
            rule.waitForIdle()
            focusField()
            rule.onNodeWithTag("composer").performTouchInput { click(centerLeft.copy(x = 14f)) }
            rule.waitForIdle()
            assertTrue("composer padding keeps focus inside nested pages in $style", focused)
        }
    }

    @Test fun blankTapOnTheComposerFocusesTheField() {
        setPage()
        // #894: the composer's padding, not only the text line, puts the caret in the field.
        for (each in AppStyle.entries) {
            style = each
            rule.waitForIdle()
            rule.onNodeWithTag("blank").performTouchInput { click(center) }
            rule.waitForIdle()
            assertFalse(focused)
            val before = blankTaps
            // Left of the inner field's horizontal inset in all styles, clear of any leading action.
            rule.onNodeWithTag("composer").performTouchInput { click(centerLeft.copy(x = 14f)) }
            rule.waitForIdle()
            assertEquals("one blank tap in $style", before + 1, blankTaps)
            assertTrue("composer padding focuses the field in $style", focused)
        }
    }

    @Test fun tapOnTheFieldIsNotABlankTap() {
        setPage()
        rule.onNodeWithTag("field").performTouchInput { click() }
        rule.waitForIdle()
        assertEquals(0, blankTaps)
    }

    @Test fun buttonStillClicksAndKeepsFocus() {
        setPage()
        rule.onNodeWithTag("button").performTouchInput { click() }
        rule.waitForIdle()
        assertEquals(1, clicks)
        assertTrue(focused)
    }

    @Test fun messageLinkStillOpensAndKeepsFocus() {
        setPage()
        rule.onNodeWithTag("link").performTouchInput { click() }
        rule.waitForIdle()
        assertEquals(1, linkClicks)
        assertTrue(focused)
    }

    @Test fun longPressStillFiresAndKeepsFocus() {
        setPage()
        rule.onNodeWithTag("card").performTouchInput { longClick() }
        rule.waitForIdle()
        assertEquals(1, longPresses)
        assertTrue(focused)
    }

    @Test fun tapOnWhatOnlyHasALongPressClearsFocus() {
        setPage()
        rule.onNodeWithTag("message").performTouchInput { click() }
        rule.waitForIdle()
        assertFalse(focused)
        assertEquals(0, longPresses)
        focusField()
        rule.onNodeWithTag("message").performTouchInput { longClick() }
        rule.waitForIdle()
        assertEquals(1, longPresses)
        assertTrue(focused)
    }

    // #5724: a screen reader or switch access cannot long press; they reach it as the node's long-click action.
    @Test fun longPressIsAnAccessibilityAction() {
        setPage()
        rule.onNodeWithTag("message").performSemanticsAction(SemanticsActions.OnLongClick)
        rule.waitForIdle()
        assertEquals(1, longPresses)
    }

    @Test fun scrollKeepsFocus() {
        setPage()
        rule.onNodeWithTag("list").performTouchInput { swipeUp() }
        rule.waitForIdle()
        assertTrue("the list actually scrolled", listState.firstVisibleItemIndex > 0 || listState.firstVisibleItemScrollOffset > 0)
        assertTrue(focused)
    }

    // #894: tabs stay composed when hidden; switching must let go of the field the old tab had focused. A fake focus
    // manager: in Robolectric (not in touch mode) a cleared focus goes straight back to the only field on screen, which
    // a hidden tab's unplaced field cannot do on a phone.
    @Test fun changingTheKeyReleasesTheFieldButTheFirstValueDoesNot() {
        var tab by mutableStateOf("persona")
        val forcedClears = mutableListOf<Boolean>()
        val focusManager = object : FocusManager {
            override fun clearFocus(force: Boolean) { forcedClears += force }
            override fun moveFocus(focusDirection: FocusDirection) = false
        }
        rule.setContent {
            CompositionLocalProvider(LocalFocusManager provides focusManager) { ReleaseTextInputOnChange(tab) }
        }
        rule.waitForIdle()
        assertEquals("the first value is not a switch", emptyList<Boolean>(), forcedClears)
        tab = "inbox"
        rule.waitForIdle()
        assertEquals("a switch clears the focus, forced", listOf(true), forcedClears)
        tab = "inbox"
        rule.waitForIdle()
        assertEquals("the same tab again is not a switch", listOf(true), forcedClears)
        tab = "persona"
        rule.waitForIdle()
        assertEquals(listOf(true, true), forcedClears)
    }

    private fun setSheet() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                var text by remember { mutableStateOf("") }
                JSheet(onDismiss = {}) {
                    BasicTextField(text, { text = it }, Modifier.fillMaxWidth().height(48.dp).testTag("field").onFocusChanged { focused = it.isFocused })
                    Box(Modifier.fillMaxWidth().height(48.dp).testTag("button").clickable { clicks++ })
                    Box(Modifier.fillMaxWidth().height(120.dp).testTag("blank"))
                }
            }
        }
        focusField()
    }

    @Test fun sheetBlankTapClearsFocus() {
        setSheet()
        rule.onNodeWithTag("blank").performTouchInput { click() }
        rule.waitForIdle()
        assertFalse(focused)
    }

    @Test fun sheetControlsKeepFocus() {
        setSheet()
        rule.onNodeWithTag("field").performTouchInput { click() }
        rule.onNodeWithTag("button").performTouchInput { click() }
        rule.waitForIdle()
        assertEquals(1, clicks)
        assertTrue(focused)
    }
}
