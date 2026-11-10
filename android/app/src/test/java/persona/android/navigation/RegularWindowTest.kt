package persona.android.navigation

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** Which windows get the open iPhone Duo / iPad layout, in dp (width × height). */
class RegularWindowTest {
    @Test fun openFoldablesAndTabletsAreRegularEitherWayUp() {
        // The open iPhone Duo's inner screen, about 669 × 951 pt, is where the split starts.
        assertTrue(isRegularWindow(669f, 951f))
        // Pixel Fold inner screen (2208 × 1840 px at 420 dpi), both ways up.
        assertTrue(isRegularWindow(841f, 701f))
        assertTrue(isRegularWindow(701f, 841f))
        // A tablet, both ways up, and a small one upright.
        assertTrue(isRegularWindow(1280f, 800f))
        assertTrue(isRegularWindow(800f, 1280f))
        assertTrue(isRegularWindow(600f, 960f))
    }

    @Test fun phonesStayCompactInBothOrientations() {
        // A folded foldable's outer screen and ordinary phones, upright.
        assertFalse(isRegularWindow(411f, 797f))
        assertFalse(isRegularWindow(412f, 915f))
        assertFalse(isRegularWindow(360f, 640f))
        // The same phones on their side: wide, but too low for the sidebar's column.
        assertFalse(isRegularWindow(915f, 412f))
        assertFalse(isRegularWindow(797f, 411f))
        assertFalse(isRegularWindow(640f, 360f))
        // Half of a tablet's split screen.
        assertFalse(isRegularWindow(599f, 800f))
    }

    @Test fun listsBesideDetailsEverywhereTheyWereAndInEveryRegularWindow() {
        // Regular windows, now from 600 dp (an open foldable upright was one pane below 760 dp).
        assertTrue(listsBesideDetails(701f, 841f))
        assertTrue(listsBesideDetails(669f, 951f))
        // A wide phone on its side keeps the two panes it had.
        assertTrue(listsBesideDetails(915f, 412f))
        assertTrue(listsBesideDetails(797f, 411f))
        // A narrower phone on its side, and every phone upright, keep one.
        assertFalse(listsBesideDetails(640f, 360f))
        assertFalse(listsBesideDetails(412f, 915f))
    }
}
