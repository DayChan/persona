package persona.android.navigation

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Where a wide window's two panes meet and where a dragged divider settles (the old app's FoldTest, in dp). */
class PaneSplitTest {
    private fun flat(at: Float) = PaneHinge(at, at)

    @Test fun theDetailGetsMostOfAnInnerScreen() {
        // A book-style foldable's inner screen, 770.67 dp, the fold flat down the middle: not halves any more.
        val s = paneSplit(770.67f, listOf(flat(385.33f)), null)
        assertEquals(320f, s.left, 0.01f)
        assertFalse(s.fixed)
        assertEquals(listOf(385.33f), s.snaps)
    }

    @Test fun aFlatFoldIsWhereALetGoDividerSnaps() {
        val snaps = listOf(385f)
        assertEquals(385f, settleDivider(360f, 770f, snaps)!!, 0.01f)
        assertEquals(350f, settleDivider(350f, 770f, snaps)!!, 0.01f) // too far from it
    }

    @Test fun aSeparatingFoldFixesTheSplit() {
        // Half open like a book, or two screens with a gap: the list ends where the fold begins.
        val s = paneSplit(800f, listOf(PaneHinge(390f, 410f, separating = true)), 0.3f)
        assertEquals(390f, s.left, 0.01f)
        assertTrue(s.fixed)
        assertTrue(s.snaps.isEmpty())
    }

    @Test fun aFoldTooNearAnEdgeIsIgnored() {
        // A fold 200 dp from the left (a window over part of the screen): the panes would be too narrow there.
        val s = paneSplit(1000f, listOf(PaneHinge(200f, 200f, separating = true)), null)
        assertEquals(380f, s.left, 0.01f)
        assertFalse(s.fixed)
        assertTrue(s.snaps.isEmpty())
    }

    @Test fun aTriFoldTakesTheFoldNearest() {
        // Opened all the way, folds at a third and two thirds: the default lands on the first one.
        val s = paneSplit(1224f, listOf(flat(408f), flat(816f)), null)
        assertEquals(408f, s.left, 0.01f)
        assertEquals(listOf(408f, 816f), s.snaps)
        // Both half open: fixed at the one nearest the usual split.
        val bent = paneSplit(1224f, listOf(PaneHinge(408f, 408f, true), PaneHinge(816f, 816f, true)), null)
        assertEquals(408f, bent.left, 0.01f)
        assertTrue(bent.fixed)
    }

    @Test fun theDefaultKeepsBothPanesUsable() {
        assertEquals(420f, paneSplit(1280f, emptyList(), null).left, 0.01f) // a tablet: capped
        assertEquals(300f, paneSplit(700f, emptyList(), null).left, 0.01f) // a narrower inner screen: the detail keeps 400
        assertEquals(280f, paneSplit(600f, emptyList(), null).left, 0.01f) // the narrowest wide area
        assertEquals(280f, maxLeft(600f), 0.01f) // the detail keeps 320
        assertEquals(320f, defaultLeft(771f), 0.01f)
        assertEquals(403f, defaultLeft(1061f), 0.5f) // a tri-fold opened
        assertEquals(320f, defaultLeft(800f), 0.01f) // a phone on its side
    }

    @Test fun theUsersShareIsKeptWithinTheLeastWidths() {
        assertEquals(640f, paneSplit(1280f, emptyList(), 0.5f).left, 0.01f) // wider than the default cap
        assertEquals(480f, paneSplit(800f, emptyList(), 0.9f).left, 0.01f)
        assertEquals(280f, paneSplit(800f, emptyList(), 0.1f).left, 0.01f)
        assertEquals(0.5f, shareOf(640f, 1280f), 0.0001f)
    }

    @Test fun draggedFarLeftTheListCloses() {
        assertNull(settleDivider(200f, 800f, emptyList()))
        assertEquals(280f, settleDivider(230f, 800f, emptyList())!!, 0.01f) // not far enough: back to its least width
        assertEquals(480f, settleDivider(700f, 800f, emptyList())!!, 0.01f)
        // Where the tab keeps its list, the same drag only stops at the least width.
        assertEquals(280f, settleDivider(200f, 800f, emptyList(), canCollapse = false)!!, 0.01f)
        assertEquals(0f, dragRange(800f, canCollapse = true).start, 0.01f)
        assertEquals(280f, dragRange(800f, canCollapse = false).start, 0.01f)
        assertEquals(480f, dragRange(800f, canCollapse = false).endInclusive, 0.01f)
    }

    @Test fun aDragTicksOnceAtAFoldAndAtTheClosingPoint() {
        val ticks = DragTicks()
        assertFalse(ticks.reached(300f, listOf(385f), canCollapse = true))
        assertTrue(ticks.reached(370f, listOf(385f), canCollapse = true))
        assertFalse("still at the same fold", ticks.reached(380f, listOf(385f), canCollapse = true))
        assertFalse(ticks.reached(300f, listOf(385f), canCollapse = true))
        assertTrue(ticks.reached(200f, listOf(385f), canCollapse = true))
        assertFalse(ticks.reached(150f, listOf(385f), canCollapse = true))
        assertFalse("a tab that keeps its list never ticks for closing", DragTicks().reached(200f, emptyList(), canCollapse = false))
    }

    @Test fun theSidebarColumnIsAListWideAndNoWiderThanTheDrawer() {
        // An open foldable upright (the page keeps 400dp), on its side, and a tablet on its side; the page starts where
        // the column ends. The narrowest regular window still gets a list-wide column.
        assertEquals(SidebarSplit(301f, 301f), sidebarSplit(701f, emptyList()))
        assertEquals(SidebarSplit(280f, 280f), sidebarSplit(600f, emptyList()))
        assertEquals(SidebarSplit(320f, 320f), sidebarSplit(841f, emptyList()))
        assertEquals(SidebarSplit(PaneSplits.SidebarMax, PaneSplits.SidebarMax), sidebarSplit(1280f, emptyList()))
        // A flat fold is barely there: the column does not move to it.
        assertEquals(SidebarSplit(320f, 320f), sidebarSplit(841f, listOf(flat(420f))))
    }

    @Test fun halfOpenTheSidebarAndThePageKeepClearOfTheFold() {
        // Book posture: the column ends where the fold begins, the page starts past it.
        assertEquals(SidebarSplit(410f, 430f), sidebarSplit(841f, listOf(PaneHinge(410f, 430f, separating = true))))
        // A separating fold too near an edge for a column on each side is ignored.
        assertEquals(SidebarSplit(320f, 320f), sidebarSplit(841f, listOf(PaneHinge(150f, 160f, separating = true))))
    }
}
