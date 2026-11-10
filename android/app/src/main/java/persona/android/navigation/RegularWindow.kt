package persona.android.navigation

import androidx.window.core.layout.WindowSizeClass

/**
 * iOS's regular width, where an open iPhone Duo (its inner screen is about 669 × 951 pt) and an iPad lay out columns
 * (docs/apple/ios.md "iPhone Duo", "iPad"): the shared sidebar beside the Assistant page, and Agents, Inbox and
 * Settings as a list beside the page it opens. On Android that is a window at least WindowSizeClass medium both ways,
 * 600dp wide and 480dp tall: an open foldable or a tablet, either way up. A phone, folded or not, is narrower than
 * that upright and lower than that on its side, so it keeps the compact layout in both orientations.
 */
internal fun isRegularWindow(widthDp: Float, heightDp: Float): Boolean =
    widthDp >= WindowSizeClass.WIDTH_DP_MEDIUM_LOWER_BOUND && heightDp >= WindowSizeClass.HEIGHT_DP_MEDIUM_LOWER_BOUND

/** From this width on, lists sit beside their details whatever the height: a wide phone on its side, as before. */
internal const val TwoPanesOnWidePhone = 760f

/** Whether Agents and Inbox lists sit beside what they open: every regular window, and wide phones on their side. */
internal fun listsBesideDetails(widthDp: Float, heightDp: Float): Boolean =
    isRegularWindow(widthDp, heightDp) || widthDp >= TwoPanesOnWidePhone
