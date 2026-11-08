package persona.feature.settings

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.material3.Text
import android.content.res.Resources
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import persona.core.contracts.api.ExtensionDependency
import persona.core.contracts.api.ExtensionPreview
import persona.core.contracts.api.ExtensionSandbox
import persona.core.designsystem.*

internal object ExtensionModelText {
    fun dependency(res: Resources, d: ExtensionDependency): String = listOfNotNull(
        d.name?.ifBlank { null } ?: d.id,
        d.version?.ifBlank { null },
        when (d.state) {
            "ok" -> res.getString(R.string.settings_ext_dep_ok)
            "missing" -> res.getString(R.string.settings_ext_dep_missing)
            "disabled" -> res.getString(R.string.settings_ext_dep_disabled)
            "not_ready" -> res.getString(R.string.settings_ext_dep_not_ready)
            "old" -> res.getString(R.string.settings_ext_dep_old, d.installed.orEmpty())
            else -> res.getString(R.string.settings_ext_dep_unknown)
        },
    ).joinToString(" · ")

    fun blockedPreview(res: Resources, preview: ExtensionPreview): String = res.getString(if (
        preview.depends.any { it.state == "not_ready" } || preview.bundle.any { member -> member.depends.any { it.state == "not_ready" } }
    ) R.string.settings_ext_cannot_confirm_not_ready else R.string.settings_ext_cannot_confirm)

    fun sandbox(res: Resources, s: ExtensionSandbox): String = when {
        s.active -> res.getString(R.string.settings_ext_sandbox_active)
        s.enabled -> res.getString(R.string.settings_ext_sandbox_inactive)
        else -> res.getString(R.string.settings_ext_sandbox_off)
    }
}

internal fun LazyListScope.sandbox(s: ExtensionSandbox?) {
    s ?: return
    item("sandbox-title") { JSectionHeader(stringResource(R.string.settings_ext_sandbox_title)) }
    item("sandbox-state") {
        JGroupItem(GroupPosition.Single) {
            JRow(ExtensionModelText.sandbox(LocalResources.current, s), subtitle = s.why?.ifBlank { null }, subtitleLines = 4)
        }
    }
    item("sandbox-note") {
        SettingsCaption(stringResource(R.string.settings_ext_sandbox_note),
            Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
    }
    s.egress?.let { e -> item("sandbox-egress") {
        SettingsCaption(stringResource(R.string.settings_ext_sandbox_egress,
            pluralStringResource(R.plurals.settings_ext_sandbox_allowed, e.allowed.quantity(), e.allowed),
            pluralStringResource(R.plurals.settings_ext_sandbox_denied, e.denied.quantity(), e.denied)) +
            e.lastDenied?.let { "\n" + stringResource(R.string.settings_ext_sandbox_last_denied, it) }.orEmpty(), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
    } }
}

@Composable
internal fun BundlePreview(preview: ExtensionPreview) {
    val res = LocalResources.current
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        if (preview.depends.isNotEmpty()) {
            Text(stringResource(R.string.settings_ext_depends), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            preview.depends.forEach { JInlineStatus(ExtensionModelText.dependency(res, it),
                tone = if (it.state == "ok") InlineTone.Done else InlineTone.Caution) }
        }
        preview.manifest?.extensionPack?.takeIf { it.isNotEmpty() }?.let { members ->
            Text(stringResource(R.string.settings_ext_pack_members), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            members.forEach { JRow(it.id, subtitle = listOfNotNull(it.version, it.market).joinToString(" · ").ifBlank { null }) }
            SettingsCaption(stringResource(R.string.settings_ext_pack_note))
        }
        if (preview.bundle.isNotEmpty()) {
            Text(pluralStringResource(R.plurals.settings_ext_bundle_title, preview.bundle.size, preview.bundle.size), style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
            preview.bundle.forEach { member ->
                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    JRow(member.manifest?.name?.ifBlank { null } ?: member.manifest?.id.orEmpty(),
                        subtitle = member.manifest?.version, value = stringResource(if (member.upgrade) R.string.settings_ext_upgrade else R.string.settings_ext_install))
                    PermissionList(stringResource(R.string.settings_ext_it_can), member.permissions)
                    if (member.permissions.isEmpty()) SettingsCaption(stringResource(R.string.settings_ext_no_permissions))
                    if (!member.addedPermissions.isNullOrEmpty()) PermissionList(stringResource(R.string.settings_ext_added_permissions), member.addedPermissions.orEmpty())
                    member.depends.forEach { SettingsCaption(ExtensionModelText.dependency(res, it)) }
                    member.warnings.orEmpty().forEach { JInlineStatus(it, tone = InlineTone.Caution) }
                }
            }
        }
        preview.unresolved.forEach { JInlineStatus(stringResource(R.string.settings_ext_unresolved, it), tone = InlineTone.Failed) }
        if (!preview.dependenciesReady) JInlineStatus(ExtensionModelText.blockedPreview(res, preview), tone = InlineTone.Caution)
    }
}

/** A count for picking a plural form. */
internal fun Long.quantity(): Int = coerceIn(0L, Int.MAX_VALUE.toLong()).toInt()
