@file:OptIn(androidx.compose.material3.adaptive.ExperimentalMaterial3AdaptiveApi::class)

package persona.android.ui

import android.content.Context
import androidx.compose.animation.core.Spring
import androidx.compose.animation.core.animate
import androidx.compose.animation.core.spring
import androidx.compose.material3.adaptive.layout.PaneExpansionState
import androidx.compose.material3.adaptive.layout.PaneScaffoldDirective
import androidx.compose.material3.adaptive.layout.ThreePaneScaffoldScope
import androidx.compose.material3.adaptive.layout.rememberPaneExpansionState
import androidx.compose.material3.adaptive.navigation.BackNavigationBehavior
import androidx.compose.material3.adaptive.navigation3.ListDetailSceneStrategy
import androidx.compose.material3.adaptive.navigation3.rememberListDetailSceneStrategy
import androidx.compose.runtime.Composable
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.hapticfeedback.HapticFeedback
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.unit.Dp
import androidx.navigation3.runtime.NavKey
import persona.android.navigation.DragTicks
import persona.android.navigation.PaneHinge
import persona.android.navigation.PaneSplit
import persona.android.navigation.PaneSplits
import persona.android.navigation.closesList
import persona.android.navigation.dragRange
import persona.android.navigation.maxLeft
import persona.android.navigation.paneSplit
import persona.android.navigation.settleDivider
import persona.android.navigation.shareOf
import persona.core.designsystem.PaneDivider
import persona.core.designsystem.PaneDividerState
import persona.core.navigation.AppTab
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlin.math.roundToInt

/** Where each tab's divider was left, as a share of the panes' width; kept on this phone like the style, any pairing. */
internal class PaneSplitStore(context: Context) {
    private val prefs = context.applicationContext.getSharedPreferences("persona-panes", Context.MODE_PRIVATE)
    private val legacy = context.applicationContext.getSharedPreferences(persona.core.contracts.ProtocolCompatibility.LEGACY_PANE_PREFERENCES, Context.MODE_PRIVATE)

    fun share(tab: AppTab): Float? {
        val key = key(tab)
        if (!prefs.contains(key)) {
            val oldKey = persona.core.contracts.ProtocolCompatibility.legacyPaneKey(key)
            if (legacy.contains(oldKey)) prefs.edit().putFloat(key, legacy.getFloat(oldKey, -1f)).apply()
        }
        return prefs.getFloat(key, -1f).takeIf { it > 0f }
    }
    fun save(tab: AppTab, share: Float) {
        prefs.edit().putFloat(key(tab), share).apply()
        legacy.edit().putFloat(persona.core.contracts.ProtocolCompatibility.legacyPaneKey(key(tab)), share).apply()
    }
    private fun key(tab: AppTab) = "split.${tab.name.lowercase()}"
}

/**
 * One tab's divider between its list and the detail beside it: where the panes meet at rest (the window, its folds, the
 * share the user left, a closed list), a drag in progress and how it settles. Every change is written straight to the
 * scaffold's [PaneExpansionState] (its first pane's width), so a drag re-lays out the panes without recomposing them.
 * [tab] keeps the share the user left in [store]; null (the settings sheet) keeps it only while the panes are shown.
 */
@Stable
internal class PaneSplitter(private val tab: AppTab?, private val store: PaneSplitStore?, collapsible: Boolean) {
    var collapseEnabled by mutableStateOf(collapsible)
    /** The panes' area, in dp, and the vertical folds across it (dp from its left edge). */
    var width by mutableFloatStateOf(0f)
    var hinges by mutableStateOf(emptyList<PaneHinge>())
    /** Something is open beside the list: only then may the list be closed. */
    var detailOpen by mutableStateOf(false)
    var share by mutableStateOf(tab?.let { store?.share(it) })
        private set
    /** The user dragged the list closed; it shows again whenever nothing is open beside it. */
    var collapsed by mutableStateOf(false)
        private set
    /** The list's width while it is dragged or settles. */
    var at by mutableFloatStateOf(0f)
        private set
    var moving by mutableStateOf(false)
        private set
    var density = 1f
    private var applied: Int? = Int.MIN_VALUE
    private var settling: Job? = null
    /** Bumped by every new gesture or command, so a settle that was cut short does not end the newer one. */
    private var generation = 0
    private val ticks = DragTicks()

    val split: PaneSplit get() = paneSplit(width, hinges, share)
    val canCollapse: Boolean get() = collapseEnabled && detailOpen
    private val shownCollapsed: Boolean get() = collapsed && canCollapse

    val look: PaneDividerState get() = when {
        moving && canCollapse && closesList(at) -> PaneDividerState.Closing
        moving -> PaneDividerState.Moving
        shownCollapsed -> PaneDividerState.Collapsed
        else -> PaneDividerState.Idle
    }

    /** The list's width at rest; null when a separating fold fixes the split (the scaffold keeps the panes off it). */
    private fun restingLeft(): Float? = split.let { s -> if (s.fixed) null else if (shownCollapsed) 0f else s.left }

    private fun apply(state: PaneExpansionState, left: Float?) {
        val px = left?.let { (it * density).roundToInt() }
        if (px == applied) return
        applied = px
        if (px == null) state.clear() else state.setFirstPaneWidth(px)
    }

    /** Puts the panes where they rest now (after the window, the folds or what is open changed). */
    fun rest(state: PaneExpansionState) {
        if (!moving && width > 0f) apply(state, restingLeft())
    }

    fun start() {
        generation++
        settling?.cancel()
        // A drag that catches a settling divider takes it from where it is.
        if (!moving) at = if (shownCollapsed) 0f else split.left
        moving = true
    }

    fun drag(state: PaneExpansionState, deltaPx: Float, haptics: HapticFeedback) {
        if (!moving) start()
        val range = dragRange(width, canCollapse)
        at = (at + deltaPx / density).coerceIn(range.start, range.endInclusive)
        apply(state, at)
        if (ticks.reached(at, split.snaps, canCollapse)) haptics.performHapticFeedback(HapticFeedbackType.SegmentTick)
    }

    /** Let go: onto a flat fold nearby, where it is, or (dragged far left) the list closes. */
    fun stop(state: PaneExpansionState, velocityPx: Float, scope: CoroutineScope) {
        if (!moving) return
        val target = settleDivider(at, width, split.snaps, canCollapse)
        if (target == null) {
            generation++
            collapsed = true
            moving = false
            apply(state, 0f)
            return
        }
        collapsed = false
        settleTo(state, target, velocityPx / density, scope)
    }

    private fun settleTo(state: PaneExpansionState, target: Float, velocity: Float, scope: CoroutineScope) {
        settling?.cancel()
        val mine = ++generation
        moving = true
        val from = at
        settling = scope.launch {
            try {
                animate(from, target, initialVelocity = velocity,
                    animationSpec = spring(dampingRatio = Spring.DampingRatioNoBouncy, stiffness = Spring.StiffnessMediumLow)) { value, _ ->
                    at = value
                    apply(state, value)
                }
                keep(target)
            } finally {
                if (generation == mine) moving = false
            }
        }
    }

    private fun keep(left: Float) {
        val kept = shareOf(left, width)
        share = kept
        tab?.let { store?.save(it, kept) }
    }

    /** TalkBack: the list a step wider or narrower. */
    fun step(state: PaneExpansionState, by: Float, scope: CoroutineScope) {
        if (shownCollapsed) return
        at = split.left
        settleTo(state, (split.left + by).coerceIn(PaneSplits.MinLeft, maxLeft(width)), 0f, scope)
    }

    fun collapse(state: PaneExpansionState) {
        if (!canCollapse) return
        generation++
        settling?.cancel()
        moving = false
        collapsed = true
        apply(state, 0f)
    }

    fun expand(state: PaneExpansionState) {
        generation++
        settling?.cancel()
        moving = false
        collapsed = false
        apply(state, restingLeft())
    }
}

/**
 * The list-detail scene of one tab with its divider: dragged by the finger (the list follows), settling on a flat fold
 * nearby, kept per tab as a share of the width, and (where [collapsible], with something open beside it) dragged far
 * left to close the list; a separating fold fixes the split there and shows no handle. [width] is the panes' area and
 * [hinges] the vertical folds across it, both in dp. [tab] null: the settings sheet's, whose divider is not kept.
 */
@Composable
internal fun rememberShellListDetail(
    tab: AppTab?,
    directive: PaneScaffoldDirective,
    width: Dp,
    hinges: List<PaneHinge>,
    detailOpen: Boolean,
    store: PaneSplitStore?,
    collapsible: Boolean,
): ListDetailSceneStrategy<NavKey> {
    val density = LocalDensity.current.density
    val scope = rememberCoroutineScope()
    val haptics = LocalHapticFeedback.current
    val state = rememberPaneExpansionState()
    val splitter = remember(tab, store) { PaneSplitter(tab, store, collapsible) }
    // The inputs change only with the window, its folds and what is open; the scaffold follows them at once.
    SideEffect {
        splitter.density = density
        splitter.width = width.value
        splitter.hinges = hinges
        splitter.collapseEnabled = collapsible
        splitter.detailOpen = detailOpen
        splitter.rest(state)
    }
    // Whether a separating fold fixes the split does not depend on where the user left it.
    val fixed = remember(width, hinges) { paneSplit(width.value, hinges, null).fixed }
    val handle: @Composable ThreePaneScaffoldScope.(PaneExpansionState) -> Unit = remember(splitter, state, scope, haptics) {
        { _ ->
            PaneDivider(
                splitter.look,
                onDrag = { delta -> splitter.drag(state, delta, haptics) },
                onDragStarted = { splitter.start() },
                onDragStopped = { velocity -> splitter.stop(state, velocity, scope) },
                onWider = { splitter.step(state, PaneSplits.Step, scope) },
                onNarrower = { splitter.step(state, -PaneSplits.Step, scope) },
                onCollapse = if (splitter.canCollapse) ({ splitter.collapse(state) }) else null,
                onExpand = { splitter.expand(state) },
            )
        }
    }
    return rememberListDetailSceneStrategy<NavKey>(
        backNavigationBehavior = BackNavigationBehavior.PopUntilContentChange,
        directive = directive,
        paneExpansionDragHandle = if (fixed) null else handle,
        paneExpansionState = state,
    )
}
