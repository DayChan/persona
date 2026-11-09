package persona.feature.settings

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import android.content.res.Resources
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import persona.core.contracts.api.CatalogExtension
import persona.core.contracts.api.ExtensionManagement
import persona.core.contracts.api.ExtensionMarket
import persona.core.contracts.api.ExtensionOperation
import persona.core.contracts.api.ExtensionPermission
import persona.core.contracts.api.ExtensionPreview
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.time.Duration
import java.time.Instant

// Settings › Extensions › Extension market (docs/extensions/extensions.md "Extension market"): the catalog, one extension of it, and the markets. The models are
// in ExtensionMarketState.kt.

private const val SearchDelayMillis = 300L

/** The catalog: search, narrow to a market, each extension's version, words, tags and how it stands here. */
@Composable
fun ExtensionMarketPage(extensions: ExtensionManagement?, onBack: () -> Unit, onExtension: (CatalogExtension) -> Unit, onMarkets: () -> Unit) {
    if (extensions == null) {
        JListPage(title = stringResource(R.string.settings_ext_market), onBack = onBack) { item { JNotice(stringResource(R.string.settings_ext_demo_none)) } }
        return
    }
    val res = LocalResources.current
    val model = remember(extensions, res) { CatalogModel(extensions, res) }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    var tokenFor by remember { mutableStateOf<ExtensionMarket?>(null) }
    // Typing waits a moment before asking the hub; the market filter asks at once.
    LaunchedEffect(model, state.query) { if (state.catalog != null) delay(SearchDelayMillis); model.load() }
    LaunchedEffect(model, state.market) { if (state.catalog != null) model.load() }
    val catalog = state.catalog
    JListPage(title = stringResource(R.string.settings_ext_market), onBack = onBack, largeTitle = false,
        actions = { JIconButton(PhIcons.SlidersHorizontal, stringResource(R.string.settings_ext_manage_markets), onMarkets) },
        refreshing = state.loading && catalog != null, onRefresh = { scope.launch { model.load(refresh = true) } }) {
        item("search") {
            JSearchField(state.query, model::setQuery, stringResource(R.string.settings_ext_market_search), Modifier.padding(top = Tokens.Space.s))
        }
        if (state.markets.size > 1) item("filter") { MarketFilter(state.markets, state.market, model::setMarket) }
        state.error?.let { error ->
            item("error") {
                JNotice(error, Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou, actionLabel = stringResource(R.string.settings_ext_retry),
                    onAction = { scope.launch { model.load() } })
            }
        }
        marketProblems(state.markets.filter { state.market == null || it.id == state.market }, state.tokenWorking, onMarkets) { market ->
            model.clearTokenError()
            tokenFor = market
        }
        when {
            catalog == null && state.loading -> item("loading") { JLoading() }
            catalog != null && catalog.markets.isEmpty() -> item("no-markets") {
                JEmptyState(stringResource(R.string.settings_ext_no_markets_title), stringResource(R.string.settings_ext_no_markets_text),
                    icon = PhIcons.Cube, action = { JButton(stringResource(R.string.settings_ext_add_market), onMarkets, kind = JButtonKind.Secondary, size = JButtonSize.Small) })
            }
            catalog != null && catalog.extensions.isEmpty() -> item("empty") {
                JEmptyState(stringResource(if (state.query.isBlank()) R.string.settings_ext_market_empty_title else R.string.settings_ext_market_not_found),
                    stringResource(if (state.query.isBlank()) R.string.settings_ext_market_empty_text else R.string.settings_ext_market_not_found_text), icon = PhIcons.MagnifyingGlass)
            }
        }
        val list = catalog?.extensions.orEmpty()
        if (list.isNotEmpty()) {
            item("list-title") { JSectionHeader(stringResource(if (state.query.isBlank()) R.string.settings_ext_market_list else R.string.settings_ext_market_results), count = list.size) }
            itemsIndexed(list, key = { _, it -> "plugin-${it.market}-${it.id}" }) { index, extension ->
                JGroupItem(GroupPosition.of(index, list.size)) {
                    JRow(extension.label, subtitle = catalogSubtitle(res, extension, showMarket = state.market == null && state.markets.size > 1), subtitleLines = 4,
                        leadingIcon = PhIcons.PuzzlePiece,
                        trailing = CatalogText.badge(res, extension)?.let { badge -> { JTag(badge, standingTone(extension)) } },
                        chevron = true, onClick = { onExtension(extension) })
                }
            }
            item("footer") {
                SettingsCaption(stringResource(R.string.settings_ext_market_footer),
                    Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.m))
            }
        }
    }
    // A GitHub market's error can be fixed right here: the same token entry as "Manage markets".
    tokenFor?.let { market ->
        TokenSheet(market, busy = state.tokenWorking == market.id, error = state.tokenError,
            onDismiss = { tokenFor = null; model.clearTokenError() }) { token ->
            scope.launch {
                if (model.setToken(market.id, token)) {
                    tokenFor = null
                    feedback?.notice(res.getString(if (token.isBlank()) R.string.settings_ext_token_removed else R.string.settings_ext_token_changed))
                }
            }
        }
    }
}

/** The lines under a catalog extension: its words, how it stands (version, installed, why not), its tags and market. */
private fun catalogSubtitle(res: Resources, p: CatalogExtension, showMarket: Boolean): String = listOfNotNull(
    p.description?.takeIf { it.isNotBlank() },
    CatalogText.line(res, p),
    listOfNotNull(p.tags.orEmpty().takeIf { it.isNotEmpty() }?.joinToString(" · ") { "#$it" },
        p.marketName?.takeIf { showMarket && it.isNotBlank() }).joinToString(res.getString(R.string.settings_ext_tags_separator)).takeIf { it.isNotBlank() },

).joinToString("\n")

internal fun standingTone(p: CatalogExtension): Tone = when (CatalogText.standing(p)) {
    CatalogStanding.Upgradable -> Tone.Caution
    CatalogStanding.Installed, CatalogStanding.InstalledElsewhere -> Tone.Positive
    CatalogStanding.Unavailable -> Tone.Danger
    CatalogStanding.Available -> Tone.Neutral
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun MarketFilter(markets: List<ExtensionMarket>, selected: String?, onSelect: (String?) -> Unit) {
    FlowRow(Modifier.padding(top = Tokens.Space.m), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        JChip(stringResource(R.string.settings_ext_market_all), selected = selected == null, onClick = { onSelect(null) })
        markets.forEach { market -> JChip(market.label, selected = selected == market.id, onClick = { onSelect(market.id) }) }
    }
}

/** A market the hub could not read, or read with entries it dropped. */
private fun LazyListScope.marketProblems(markets: List<ExtensionMarket>, working: String?, onMarkets: () -> Unit, onToken: (ExtensionMarket) -> Unit) {
    markets.forEach { market ->
        market.error?.takeIf { it.isNotBlank() }?.let { error ->
            item("market-error-${market.id}") {
                // The official market asks no token of a newcomer here (docs/assistant/onboarding.md 4.5): the hub reads it elsewhere
                // when GitHub rate-limits it, and whoever still wants a token gets a quiet way to "Manage markets", where it is set.
                if (market.official) OfficialMarketError(market, error, onMarkets)
                else MarketError(market, error, busy = working == market.id) { onToken(market) }
            }
        }
    }
}

/** The official market not read: why, and — without a token — a quiet way to the markets page. */
@Composable
private fun OfficialMarketError(market: ExtensionMarket, error: String, onMarkets: () -> Unit) {
    Column(Modifier.padding(top = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        JInlineStatus(stringResource(R.string.settings_ext_labeled, market.label, error), tone = InlineTone.Failed)
        if (wantsGitHubToken(market)) {
            JButton(stringResource(R.string.settings_ext_official_token_link), onMarkets, kind = JButtonKind.Quiet, size = JButtonSize.Small)
        }
    }
}

/**
 * Why a market could not be read, and — for a GitHub market without a token — how to get past the hourly limit:
 * the same token entry as "Manage markets".
 */
@Composable
private fun MarketError(market: ExtensionMarket, error: String, busy: Boolean, onToken: () -> Unit) {
    Column(Modifier.padding(top = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        JInlineStatus(stringResource(R.string.settings_ext_labeled, market.label, error), tone = InlineTone.Failed)
        if (wantsGitHubToken(market)) {
            SettingsCaption(stringResource(R.string.settings_ext_github_token_hint))
            JButton(stringResource(R.string.settings_ext_github_token_fill), onToken, kind = JButtonKind.Secondary,
                size = JButtonSize.Small, loading = busy)
        }
    }
}

/**
 * One extension of a market: what it is, what it may do, and the way to install, upgrade or remove it. Installing and
 * upgrading first fetch and check the package and list what it may do; nothing happens until the user agrees.
 */
@Composable
fun ExtensionCatalogPage(
    extensions: ExtensionManagement?, marketId: String, extensionId: String, onBack: () -> Unit, onInstalled: (String) -> Unit,
    manager: String? = null,
    /** Installed and it signs in (`next: auth`, #4507): its page, with the sign-in started. */
    onSignIn: (String) -> Unit = onInstalled,
) {
    if (extensions == null) {
        JListPage(title = stringResource(R.string.settings_ext_title), onBack = onBack) { item { JNotice(stringResource(R.string.settings_ext_demo_none)) } }
        return
    }
    val res = LocalResources.current
    val model = remember(extensions, marketId, extensionId, res, manager) {
        CatalogExtensionModel(extensions, marketId, extensionId, res, manager = manager)
    }
    // Both API names share the same owner for preview, consent, installation and operation progress.
    PinnedExtension(listOf("extensions", "plugins").flatMap { base ->
        listOf(listOf(base, extensionId), listOf(base, "-", "catalog"), listOf(base, "-", "install"), listOf(base, "-", "operations"))
    }, model.owner)
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    var removing by remember { mutableStateOf(false) }
    LaunchedEffect(model) { model.load() }
    // In steps (a hub with extension_operations): the hub's pushes, and the one going on when the page opens again.
    LaunchedEffect(model) { model.watch(signIn = onSignIn) { feedback?.notice(it) } }
    val extension = state.extension
    val actions = extension?.let(CatalogText::actions).orEmpty()
    val op = state.operation
    // While one goes on the button says which step it is at.
    val going = state.previewing || op?.active == true
    val goingText = op?.takeIf { it.active }?.let { OperationText.line(res, it) } ?: stringResource(R.string.settings_ext_downloading)
    JListPage(title = extension?.label ?: stringResource(R.string.settings_ext_title), onBack = onBack, largeTitle = false,
        refreshing = state.loading && extension != null, onRefresh = { scope.launch { model.load() } },
        bottomBar = if (actions.isEmpty()) null else ({
            Column(Modifier.fillMaxWidth().padding(horizontal = pagePadding, vertical = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                val removal = op?.kind == "uninstall"
                if (CatalogAction.Install in actions) JButton(stringResource(R.string.settings_ext_install), { scope.launch { model.preview() } }, Modifier.fillMaxWidth(),
                    loading = going && !removal, loadingText = goingText, enabled = !state.busy)
                if (CatalogAction.Upgrade in actions) JButton(stringResource(R.string.settings_ext_upgrade_to, extension?.version.orEmpty()), { scope.launch { model.preview() } }, Modifier.fillMaxWidth(),
                    loading = going && !removal, loadingText = goingText, enabled = !state.busy)
                if (CatalogAction.Uninstall in actions) JButton(stringResource(R.string.settings_ext_uninstall), { removing = true }, Modifier.fillMaxWidth(),
                    kind = JButtonKind.Destructive, size = if (actions.size > 1) JButtonSize.Small else JButtonSize.Large,
                    loading = state.acting && state.preview == null || op?.active == true && removal, enabled = !state.busy)
            }
        })) {
        when {
            extension == null && state.loading -> item("loading") { JLoading() }
            extension == null && state.error != null -> item("error") {
                JNotice(state.error!!, Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou, actionLabel = stringResource(R.string.settings_ext_retry),
                    onAction = { scope.launch { model.load() } })
            }
            extension == null && state.missing -> item("missing") {
                JEmptyState(stringResource(R.string.settings_ext_market_missing_title), stringResource(R.string.settings_ext_market_missing_text), icon = PhIcons.Cube)
            }
        }
        if (extension != null) catalogExtensionBody(extension, state, res, onOpenInstalled = { onInstalled(extension.id) },
            operation = { op -> OperationSteps(op, busy = state.acting, modifier = Modifier.padding(top = Tokens.Space.m),
                onRetry = { scope.launch { model.retry() } }, onClose = { scope.launch { model.closeOperation() } },
                onReview = { scope.launch { model.review() } }) })
    }
    state.preview?.let { preview ->
        PreviewSheet(extension, preview, busy = state.acting, onDismiss = { scope.launch { model.dismissPreview() } },
            onConfirm = { scope.launch { model.confirm()?.let { feedback?.notice(it) } } },
            onPreviewAgain = { scope.launch { model.previewAgain() } })
    }
    if (removing && extension != null) UninstallDialog(extension.label, onDismiss = { removing = false }, installedVersion = extension.installed) { keep ->
        removing = false
        scope.launch { if (model.uninstall(keep)) feedback?.notice(res.getString(R.string.settings_ext_uninstalled, extension.label)) }
    }
    // Others depend on it (#5611): asked as on its own page, then removed with them switched off.
    state.cascade?.let { ask ->
        val name = extension?.label ?: extensionId
        JDialog({ model.dismissCascade() }, title = stringResource(R.string.settings_ext_dependents_title), text = ExtensionText.cascade(res, ask, name),
            destructive = true, confirm = stringResource(R.string.settings_ext_dependents_remove) to {
                scope.launch { if (model.confirmCascade()) feedback?.notice(res.getString(R.string.settings_ext_uninstalled, name)) }
            })
    }
}

private fun LazyListScope.catalogExtensionBody(
    extension: CatalogExtension, state: CatalogExtensionState, res: Resources, onOpenInstalled: () -> Unit,
    operation: @Composable (ExtensionOperation) -> Unit,
) {
    item("header") {
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s)) {
            // Why it cannot be installed is said in red below; the header keeps to the version.
            val line = if (CatalogText.standing(extension) == CatalogStanding.Unavailable) extension.version else CatalogText.line(res, extension)
            JRow(extension.label, subtitle = line, subtitleLines = 3, leadingIcon = PhIcons.PuzzlePiece,
                trailing = CatalogText.badge(res, extension)?.let { badge -> { JTag(badge, standingTone(extension)) } })
        }
    }
    state.operation?.let { op -> item("operation") { operation(op) } }
    extension.description?.takeIf { it.isNotBlank() }?.let { item("description") { Note(it) } }
    extension.tags.orEmpty().takeIf { it.isNotEmpty() }?.let { tags -> item("tags") { Tags(tags) } }
    state.actionError?.let { error -> item("action-error") { JInlineStatus(error, Modifier.padding(top = Tokens.Space.s), tone = InlineTone.Failed) } }
    state.error?.let { error -> item("error") { JInlineStatus(error, Modifier.padding(top = Tokens.Space.s), tone = InlineTone.Failed) } }
    CatalogText.noActionReason(res, extension)?.let { reason ->
        item("why") {
            JInlineStatus(reason, Modifier.padding(top = Tokens.Space.s),
                tone = if (CatalogText.standing(extension) == CatalogStanding.Unavailable) InlineTone.Failed else InlineTone.Busy)
        }
    }
    if (!extension.installed.isNullOrBlank()) item("installed") {
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
            JRow(stringResource(R.string.settings_ext_its_settings), leadingIcon = PhIcons.GearSix, value = stringResource(R.string.settings_ext_installed_version, extension.installed.orEmpty()), onClick = onOpenInstalled)
        }
    }
    permissionRows("permissions", res.getString(R.string.settings_ext_it_can), extension.permissions)
    item("permissions-note") { Note(stringResource(R.string.settings_ext_market_permissions_note)) }
    val points = extension.contributes.orEmpty().filterValues { it.isNotEmpty() }.toSortedMap().toList()
    if (points.isNotEmpty()) {
        item("contributes-title") { JSectionHeader(stringResource(R.string.settings_ext_provides)) }
        itemsIndexed(points, key = { _, it -> "point-${it.first}" }) { index, (point, ids) ->
            JGroupItem(GroupPosition.of(index, points.size)) { JRow(ExtensionText.point(res, point), value = ids.joinToString(res.getString(R.string.settings_ext_list_separator))) }
        }
    }
    val about = listOfNotNull(
        res.getString(R.string.settings_ext_about_version) to extension.version,
        state.marketName?.let { res.getString(R.string.settings_ext_about_market) to it },
        extension.publisher?.takeIf { it.isNotBlank() }?.let { res.getString(R.string.settings_ext_about_publisher) to it },
    )
    val homepage = extension.homepage?.takeIf { ExtensionViewText.isHttps(it) }
    item("about-title") { JSectionHeader(stringResource(R.string.settings_ext_about)) }
    val count = about.size + if (homepage != null) 1 else 0
    about.forEachIndexed { index, (label, value) ->
        item("about-$label") { JGroupItem(GroupPosition.of(index, count)) { JRow(label, value = value) } }
    }
    homepage?.let { url ->
        item("about-homepage") {
            val uri = LocalUriHandler.current
            JGroupItem(GroupPosition.of(count - 1, count)) {
                JRow(stringResource(R.string.settings_ext_about_homepage), leadingIcon = PhIcons.ArrowSquareOut, chevron = false, onClick = { runCatching { uri.openUri(url) } })
            }
        }
    }
    item("end") { androidx.compose.foundation.layout.Spacer(Modifier.padding(top = Tokens.Space.xxl)) }
}

private fun LazyListScope.permissionRows(key: String, title: String, permissions: List<ExtensionPermission>) {
    if (permissions.isEmpty()) return
    item("$key-title") { JSectionHeader(title) }
    itemsIndexed(permissions, key = { _, it -> "$key-${it.key}-${it.title}" }) { index, permission ->
        JGroupItem(GroupPosition.of(index, permissions.size)) {
            JRow(permission.title, subtitle = permission.detail?.takeIf { it.isNotBlank() }, subtitleLines = 4)
        }
    }
}

@Composable
private fun Note(text: String) = SettingsCaption(text, Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun Tags(tags: List<String>) {
    FlowRow(Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.xs), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        tags.forEach { JTag(it) }
    }
}

/**
 * What the package may do, checked by the hub, before it is installed: an upgrade that asks for more says so first
 * and lists the new lines alone, with a caution, not an error. Nothing is installed without the yes. Once the preview
 * (or one in its bundle) expires by [now], also while the sheet is open, it says so and offers [onPreviewAgain]
 * instead of the yes (#1316).
 */
@Composable
internal fun PreviewSheet(
    extension: CatalogExtension?, preview: ExtensionPreview, busy: Boolean, onDismiss: () -> Unit, onConfirm: () -> Unit,
    modifier: Modifier = Modifier, onPreviewAgain: () -> Unit = {}, now: () -> Instant = Instant::now,
) {
    val name = preview.manifest?.name?.takeIf { it.isNotBlank() } ?: extension?.label ?: preview.manifest?.id.orEmpty()
    val more = CatalogText.asksForMore(preview)
    val expired = rememberExpired(preview, now)
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    JSheet(onDismiss, title = stringResource(if (preview.upgrade) R.string.settings_ext_upgrade_name else R.string.settings_ext_install_name, name), dismissible = !busy, scrollable = true) {
        Column(modifier.padding(vertical = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            val version = preview.manifest?.version.orEmpty()
            Text(if (preview.upgrade) stringResource(R.string.settings_ext_version_change, preview.from.orEmpty(), version)
                else stringResource(R.string.settings_ext_version, version), style = type.callout, color = palette.ink2)
            if (more) {
                JInlineStatus(stringResource(R.string.settings_ext_more_permissions), tone = InlineTone.Caution)
                PermissionList(stringResource(R.string.settings_ext_added), preview.addedPermissions.orEmpty())
            }
            BundlePreview(preview)
            PermissionList(stringResource(if (preview.upgrade) R.string.settings_ext_it_can_after_upgrade else R.string.settings_ext_it_can), preview.permissions)
            preview.warnings.orEmpty().forEach { JInlineStatus(it, tone = InlineTone.Caution) }
            SettingsCaption(stringResource(R.string.settings_ext_preview_note) +
                (preview.expiresAt?.let(CatalogText::time)?.let { stringResource(R.string.settings_ext_preview_expires, it) } ?: ""))
            if (expired) {
                JInlineStatus(stringResource(R.string.settings_ext_preview_expired), tone = InlineTone.Caution)
                JButton(stringResource(R.string.settings_ext_preview_again), onPreviewAgain, Modifier.fillMaxWidth(),
                    kind = JButtonKind.Primary, loading = busy, enabled = !busy)
            } else JButton(CatalogText.confirmLabel(LocalResources.current, preview), onConfirm, Modifier.fillMaxWidth(),
                kind = JButtonKind.Primary, loading = busy, enabled = !busy && preview.dependenciesReady)
            JButton(stringResource(R.string.settings_ext_preview_cancel), onDismiss, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary, enabled = !busy)
        }
    }
}

/** Whether [preview] has expired by [now]; it turns true when the earliest expiry passes (rechecked at least every minute). */
@Composable
private fun rememberExpired(preview: ExtensionPreview, now: () -> Instant): Boolean {
    var expired by remember(preview) { mutableStateOf(preview.expired(now())) }
    LaunchedEffect(preview) {
        val by = preview.expiresBy ?: return@LaunchedEffect
        while (!preview.expired(now())) delay(Duration.between(now(), by).toMillis().coerceIn(1L, 60_000L))
        expired = true
    }
    return expired
}

@Composable
internal fun PermissionList(title: String, permissions: List<ExtensionPermission>) {
    if (permissions.isEmpty()) return
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        Text(title, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        Column {
            permissions.forEachIndexed { index, permission ->
                JGroupItem(GroupPosition.of(index, permissions.size)) {
                    JRow(permission.title, subtitle = permission.detail?.takeIf { it.isNotBlank() }, subtitleLines = 4)
                }
            }
        }
    }
}

/**
 * Removing stops it and deletes its settings and secrets; its data folder stays unless the user unticks it (as on the
 * web: a plain "Remove" loses nothing a reinstall could pick up again). The market's page and the extension's own share it.
 */
@Composable
internal fun UninstallDialog(name: String, onDismiss: () -> Unit, installedVersion: String? = null, onConfirm: (Boolean) -> Unit) {
    var keep by remember { mutableStateOf(true) }
    val version = installedVersion?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_extension_missing)
    val text = stringResource(R.string.settings_extension_remove_version, version) + "\n\n" + stringResource(R.string.settings_ext_uninstall_text)
    JDialog(onDismiss, title = stringResource(R.string.settings_ext_uninstall_title, name), text = text,
        confirm = stringResource(R.string.settings_ext_uninstall) to { onConfirm(keep) }, destructive = true) {
        Row(Modifier.fillMaxWidth().padding(top = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            JCheckbox(keep, { keep = it })
            Column(Modifier.padding(start = Tokens.Space.s)) {
                Text(stringResource(R.string.settings_ext_keep_data), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
                Text(stringResource(R.string.settings_ext_keep_data_hint), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
            }
        }
    }
}

/** "Manage markets": the markets the hub reads; add (an address, a token if private), refresh, change the token, remove. */
@Composable
fun ExtensionMarketsPage(extensions: ExtensionManagement?, onBack: () -> Unit) {
    if (extensions == null) {
        JListPage(title = stringResource(R.string.settings_ext_manage_markets), onBack = onBack) { item { JNotice(stringResource(R.string.settings_ext_demo_none)) } }
        return
    }
    val res = LocalResources.current
    val model = remember(extensions, res) { MarketsModel(extensions, res) }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    var adding by remember { mutableStateOf(false) }
    var acting by remember { mutableStateOf<ExtensionMarket?>(null) }
    var tokenFor by remember { mutableStateOf<ExtensionMarket?>(null) }
    var removing by remember { mutableStateOf<ExtensionMarket?>(null) }
    val uri = LocalUriHandler.current
    LaunchedEffect(model) { model.load() }
    val markets = state.markets
    JListPage(title = stringResource(R.string.settings_ext_manage_markets), onBack = onBack, largeTitle = false,
        actions = { JIconButton(PhIcons.Plus, stringResource(R.string.settings_ext_add_market), { model.clearAddError(); adding = true }) },
        refreshing = state.loading && markets != null, onRefresh = { scope.launch { model.load() } }) {
        state.error?.let { error ->
            item("error") {
                JNotice(error, Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou, actionLabel = stringResource(R.string.settings_ext_retry),
                    onAction = { scope.launch { model.load() } })
            }
        }
        state.actionError?.let { error -> item("action-error") { JInlineStatus(error, Modifier.padding(top = Tokens.Space.s), tone = InlineTone.Failed) } }
        // The switch for every market extension (docs/extensions/extensions.md "Auto-upgrade"); a hub without it shows none.
        state.autoUpgrade?.takeIf { !markets.isNullOrEmpty() }?.let { auto ->
            item("auto-upgrade") {
                JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
                    JSwitchRow(stringResource(R.string.settings_ext_auto_upgrades), auto, { on ->
                        scope.launch {
                            if (model.setAutoUpgrade(on)) feedback?.notice(res.getString(if (on) R.string.settings_ext_auto_upgrades_on else R.string.settings_ext_auto_upgrades_off))
                        }
                    }, leadingIcon = PhIcons.ArrowsClockwise, enabled = !state.savingAuto)
                }
            }
            item("auto-upgrade-note") {
                Note(stringResource(if (auto) R.string.settings_ext_auto_upgrades_footer_on else R.string.settings_ext_auto_upgrades_footer_off))
            }
        }
        when {
            markets == null && state.loading -> item("loading") { JLoading() }
            markets != null && markets.isEmpty() -> item("empty") {
                JEmptyState(stringResource(R.string.settings_ext_markets_empty_title), stringResource(R.string.settings_ext_markets_empty_text), icon = PhIcons.Cube,
                    action = { JButton(stringResource(R.string.settings_ext_add_market), { model.clearAddError(); adding = true }, kind = JButtonKind.Secondary, size = JButtonSize.Small) })
            }
        }
        if (!markets.isNullOrEmpty()) {
            item("title") { JSectionHeader(stringResource(R.string.settings_ext_markets_header), count = markets.size) }
            markets.forEachIndexed { index, market ->
                item("market-${market.id}") {
                    JGroupItem(GroupPosition.of(index, markets.size)) {
                        JRow(market.label, subtitle = market.spec + "\n" + CatalogText.marketLine(res, market), subtitleLines = 3, leadingIcon = PhIcons.Cube,
                            trailing = when {
                                state.working == market.id -> ({ JSpinner() })
                                !market.error.isNullOrBlank() -> ({ JIcon(PhIcons.WarningCircle, stringResource(R.string.settings_ext_unavailable), tint = PersonaTheme.palette.danger.dot) })
                                market.signature?.failed == true -> ({ JIcon(PhIcons.WarningCircle, stringResource(R.string.settings_ext_market_bad_signature), tint = PersonaTheme.palette.danger.dot) })
                                market.official -> ({ JTag(stringResource(R.string.settings_ext_market_official)) })
                                else -> null
                            },
                            chevron = true, enabled = state.working == null, onClick = { acting = market })
                    }
                }
            }
            // Under the group, not between its rows: each says which market it is about.
            markets.forEach { market ->
                market.error?.takeIf { it.isNotBlank() }?.let { error ->
                    item("market-error-${market.id}") {
                        MarketError(market, error, busy = state.working == market.id) {
                            model.clearActionError()
                            tokenFor = market
                        }
                    }
                }
                CatalogText.signatureProblem(res, market)?.let { problem ->
                    item("market-signature-${market.id}") {
                        JInlineStatus(stringResource(R.string.settings_ext_labeled, market.label, problem), Modifier.padding(top = Tokens.Space.s), tone = InlineTone.Failed)
                    }
                }
                market.warnings.orEmpty().forEachIndexed { w, warning ->
                    item("market-warning-${market.id}-$w") {
                        JInlineStatus(stringResource(R.string.settings_ext_labeled, market.label, warning), Modifier.padding(top = Tokens.Space.s), tone = InlineTone.Caution)
                    }
                }
            }
            item("footer") {
                SettingsCaption(stringResource(R.string.settings_ext_markets_footer),
                    Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.m))
            }
        }
    }
    acting?.let { market ->
        JActionSheet(listOfNotNull(
            JAction(stringResource(R.string.settings_ext_market_reload), icon = PhIcons.ArrowClockwise, onClick = {
                scope.launch { if (model.refresh(market.id)) feedback?.notice(res.getString(R.string.settings_ext_market_reloaded, market.label)) }
            }),
            JAction(stringResource(if (market.hasToken) R.string.settings_ext_token_change else R.string.settings_ext_token_set), icon = PhIcons.Key, onClick = {
                model.clearActionError()
                tokenFor = market
            }),
            market.homepage?.takeIf { ExtensionViewText.isHttps(it) }?.let { url ->
                JAction(stringResource(R.string.settings_ext_open_homepage), icon = PhIcons.ArrowSquareOut, onClick = { runCatching { uri.openUri(url) } })
            },
            JAction(stringResource(R.string.settings_ext_remove_market), icon = PhIcons.Trash, destructive = true, separatorBefore = true, onClick = { removing = market }),
        ), { acting = null }, title = market.label, note = market.description?.takeIf { it.isNotBlank() })
    }
    if (adding) AddMarketSheet(state, onDismiss = { adding = false }) { spec, token ->
        scope.launch { if (model.add(spec, token)) { adding = false; feedback?.notice(res.getString(R.string.settings_ext_market_added)) } }
    }
    tokenFor?.let { market ->
        TokenSheet(market, busy = state.working == market.id, error = state.actionError,
            onDismiss = { tokenFor = null; model.clearActionError() }) { token ->
            scope.launch {
                if (model.setToken(market.id, token)) {
                    tokenFor = null
                    model.load()
                    feedback?.notice(res.getString(if (token.isBlank()) R.string.settings_ext_token_removed else R.string.settings_ext_token_changed))
                }
            }
        }
    }
    removing?.let { market ->
        JDialog({ removing = null }, title = stringResource(R.string.settings_ext_remove_market_title, market.label), text = stringResource(R.string.settings_ext_remove_market_text),
            confirm = stringResource(R.string.settings_ext_remove) to {
                removing = null
                scope.launch { if (model.remove(market.id)) feedback?.notice(res.getString(R.string.settings_ext_market_removed)) }
            }, destructive = true)
    }
}

@Composable
private fun AddMarketSheet(state: MarketsState, onDismiss: () -> Unit, onAdd: (String, String) -> Unit) {
    var spec by remember { mutableStateOf("") }
    var token by remember { mutableStateOf("") }
    JSheet(onDismiss, title = stringResource(R.string.settings_ext_add_market), dismissible = !state.adding) {
        Column(Modifier.padding(vertical = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            JTextField(spec, { spec = it }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_ext_market_address), placeholder = stringResource(R.string.settings_ext_market_address_hint),
                singleLine = true, enabled = !state.adding,
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, keyboardType = KeyboardType.Uri))
            SettingsCaption(stringResource(R.string.settings_ext_market_address_note))
            JTextField(token, { token = it }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_ext_market_token_label), placeholder = stringResource(R.string.settings_ext_optional), singleLine = true,
                enabled = !state.adding, visualTransformation = PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password))
            SettingsCaption(stringResource(R.string.settings_ext_market_token_note))
            state.addError?.let { JInlineStatus(it, tone = InlineTone.Failed) }
            JButton(stringResource(R.string.settings_ext_add), { onAdd(spec, token) }, Modifier.fillMaxWidth(), loading = state.adding, loadingText = stringResource(R.string.settings_ext_market_reading),
                enabled = !state.adding && spec.isNotBlank())
        }
    }
}

@Composable
private fun TokenSheet(market: ExtensionMarket, busy: Boolean, error: String? = null, onDismiss: () -> Unit, onSave: (String) -> Unit) {
    var token by remember(market.id) { mutableStateOf("") }
    JSheet(onDismiss, title = stringResource(if (market.hasToken) R.string.settings_ext_token_change else R.string.settings_ext_token_set), dismissible = !busy) {
        Column(Modifier.padding(vertical = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            JTextField(token, { token = it }, Modifier.fillMaxWidth(), placeholder = stringResource(if (market.hasToken) R.string.settings_ext_token_replace else R.string.settings_ext_token_enter),
                singleLine = true, enabled = !busy, visualTransformation = PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password))
            SettingsCaption(stringResource(R.string.settings_ext_token_note, market.label))
            if (isGitHubMarket(market.spec)) {
                // The browser, not an in-app page: GitHub's form needs the user's GitHub session.
                val uri = LocalUriHandler.current
                SettingsCaption(stringResource(R.string.settings_ext_token_create_hint))
                JButton(stringResource(R.string.settings_ext_token_create), { runCatching { uri.openUri(GitHubTokenUrl) } }, kind = JButtonKind.Secondary,
                    size = JButtonSize.Small, icon = PhIcons.ArrowSquareOut)
            }
            error?.let { JInlineStatus(it, tone = InlineTone.Failed) }
            JButton(stringResource(R.string.settings_ext_save), { onSave(token) }, Modifier.fillMaxWidth(), loading = busy, enabled = !busy && token.isNotBlank())
            if (market.hasToken) JButton(stringResource(R.string.settings_ext_token_remove), { onSave("") }, Modifier.fillMaxWidth(), kind = JButtonKind.Destructive, enabled = !busy)
        }
    }
}
