package persona.feature.machines

import androidx.annotation.StringRes
import persona.core.contracts.Machine
import persona.core.contracts.api.DoctorCheck
import persona.core.contracts.api.DoctorReport

/*
 * "What this computer can do" (docs/server/machines.md "Self-check", the web's lib/node-health.ts): what `personad node doctor` finds on a computer,
 * read as features. Each feature stands on a set of the node's checks and takes the worst state among them; the raw
 * list stays one tap away.
 */

internal enum class FeatureState(@StringRes val label: Int) {
    Ready(R.string.machines_health_state_ready), Partial(R.string.machines_health_state_partial),
    Broken(R.string.machines_health_state_broken), Off(R.string.machines_health_state_off),
}

private class FeatureSpec(
    val id: String,
    @StringRes val name: Int,
    @StringRes val detail: Int,
    val checks: List<String>,
    /** Something a computer may well not use: a warning alone means "Off". */
    val optional: Boolean = false,
    /** For an optional one: whether this computer needs it, which turns the warning into "Broken". */
    val needed: ((engine: String) -> Boolean)? = null,
)

/** The checks the node reports and no feature stands on: the user's own settings, shown as information. */
internal val InfoChecks = setOf("global-permission-hooks")

private val Features = listOf(
    FeatureSpec("approvals", R.string.machines_health_approvals, R.string.machines_health_approvals_detail,
        listOf("node.sock", "claude-settings.json", "claude-hooks", "assistant-settings.json", "assistant-hooks", "command-paths")),
    FeatureSpec("status", R.string.machines_health_status, R.string.machines_health_status_detail, listOf("node.sock", "command-paths")),
    FeatureSpec("terminal", R.string.machines_health_terminal, R.string.machines_health_terminal_detail, listOf("herdr"),
        optional = true, needed = { it in setOf("herdr", "herdr-both") }),
    FeatureSpec("mcp", R.string.machines_health_mcp, R.string.machines_health_mcp_detail, listOf("node.sock", "mcp.json", "command-paths")),
    FeatureSpec("skills", R.string.machines_health_skills, R.string.machines_health_skills_detail, listOf("agent-plugin", "claude-version")),
    FeatureSpec("runtimes", R.string.machines_health_runtimes, R.string.machines_health_runtimes_detail, listOf("runtime.sock"), optional = true),
)

/** Why a feature is not ready: a resource (a generated file "Fix" rewrites) or the node's own words. */
internal sealed interface HealthNote {
    data class Res(@StringRes val id: Int) : HealthNote
    data class Plain(val text: String) : HealthNote
}

internal data class FeatureView(
    val id: String,
    @StringRes val name: Int,
    @StringRes val detail: Int,
    val state: FeatureState,
    val checks: List<DoctorCheck>,
    val note: HealthNote?,
)

private fun rank(status: String) = when (status) { "fail" -> 2; "warn" -> 1; else -> 0 }

private fun worst(checks: List<DoctorCheck>): DoctorCheck? =
    checks.filter { it.status != "ok" }.maxByOrNull { rank(it.status) }

/** A stale or missing generated file has a fix here ("Fix"), so the node's hint, which names the command line, is not repeated. */
private fun noteOf(check: DoctorCheck?): HealthNote? = when {
    check == null -> null
    check.state == "stale" -> HealthNote.Res(R.string.machines_health_note_stale)
    check.state == "missing" -> HealthNote.Res(R.string.machines_health_note_missing)
    else -> (check.hint.ifBlank { check.detail }).takeIf { it.isNotBlank() }?.let(HealthNote::Plain)
}

/**
 * The report as features, in a fixed order. A feature none of whose checks the report has (an older node reports fewer)
 * is left out; a check no feature covers and that is not fine makes "Other checks", so nothing hides.
 */
internal fun deriveFeatures(report: DoctorReport, engine: String = "herdr"): List<FeatureView> {
    val byName = report.checks.associateBy { it.name }
    val out = Features.mapNotNull { spec ->
        val checks = spec.checks.mapNotNull(byName::get)
        if (checks.isEmpty()) return@mapNotNull null
        val w = worst(checks)
        val state = when (w?.status) {
            "fail" -> FeatureState.Broken
            "warn" -> if (spec.optional) (if (spec.needed?.invoke(engine) == true) FeatureState.Broken else FeatureState.Off) else FeatureState.Partial
            else -> FeatureState.Ready
        }
        FeatureView(spec.id, spec.name, spec.detail, state, checks, noteOf(w))
    }
    val covered = Features.flatMap { it.checks }.toSet() + InfoChecks
    val rest = report.checks.filter { it.name !in covered }
    val w = worst(rest) ?: return out
    return out + FeatureView("other", R.string.machines_health_other, R.string.machines_health_other_detail,
        if (w.status == "fail") FeatureState.Broken else FeatureState.Partial, rest, noteOf(w))
}

/** Features that are broken or partly working ("Everything works" / N features need attention), and the ones not set up. */
internal fun attentionCount(features: List<FeatureView>) = features.count { it.state == FeatureState.Broken || it.state == FeatureState.Partial }

/** The checks "Fix" would put right: Persona's own generated files that are stale or missing. */
internal fun fixableChecks(report: DoctorReport) = report.checks.filter { it.state == "stale" || it.state == "missing" }

/** The user's own settings the node only looks at: a row of information, not a feature. */
internal fun infoChecks(report: DoctorReport) = report.checks.filter { it.name in InfoChecks && it.status != "ok" }

/** Why a self-check could not be had, from the hub's answer (the web's `doctorUnavailable`). */
internal enum class DoctorUnavailable(@StringRes val text: Int) {
    OldNode(R.string.machines_health_old_node), OldHub(R.string.machines_health_old_hub),
    Offline(R.string.machines_health_went_offline), Forbidden(R.string.machines_health_forbidden);

    companion object {
        /** An old node answers "unknown method" (in [detail] when the hub localized [message]); an old hub has no such route (a plain 404, not the JSON of "unknown machine"). */
        fun of(status: Int?, message: String?, json: Boolean, detail: String? = null): DoctorUnavailable? = when {
            listOf(message, detail).any { it?.contains("unknown method", ignoreCase = true) == true } -> OldNode
            status == 404 && !json -> OldHub
            status == 503 -> Offline
            status == 403 -> Forbidden
            else -> null
        }
    }
}

private val AuthFailed = Regex(
    """permission denied|publickey|authentication|unauthori[sz]ed|invalid[ _]token|forbidden|host key verification|access denied|\b40[13]\b|配对|认证""", // i18n-ignore: the hub's own words it matches
    RegexOption.IGNORE_CASE)
private val VersionDiffers = Regex("""version mismatch|protocol version|unknown method|incompatible|不是同一版|版本""", RegexOption.IGNORE_CASE) // i18n-ignore: the hub's own words it matches
private val TimedOut = Regex("""timed out|timeout|deadline exceeded|no pong""", RegexOption.IGNORE_CASE)
private val Unreachable = Regex(
    """refused|no route|unreachable|could not resolve|name or service|no such host|network is|connection (reset|closed|lost)|broken pipe|\beof\b|node exited|waiting for|lookup |dial """,
    RegexOption.IGNORE_CASE)

/**
 * Why an offline machine is not connected and what to do, read from the error the hub keeps (the web's
 * `classifyConnection`, same kinds, patterns and order; offline, so a version mismatch is always upgraded there): null when the error does not say, so nothing is guessed and
 * the hub's own words are shown alone. A machine being redeployed is expected to drop, so it is not read as a failure.
 * Order matters: "connection timed out" is a timeout, not "connection …".
 */
@StringRes
internal fun connectionFailureText(machine: Machine): Int? {
    val text = machine.error.orEmpty()
    if (machine.online || deploying(machine) || text.isBlank()) return null
    val link = machine.kind == "link" || machine.kind == "service"
    return when {
        AuthFailed.containsMatchIn(text) -> if (link) R.string.machines_offline_auth_link else R.string.machines_offline_auth_ssh
        // The hub cannot redeploy a computer it cannot reach (the web's can_deploy is false while offline), so the node
        // is upgraded on that computer.
        VersionDiffers.containsMatchIn(text) || machine.nodeMismatch == true -> R.string.machines_offline_version_local
        TimedOut.containsMatchIn(text) -> R.string.machines_offline_timeout
        Unreachable.containsMatchIn(text) -> if (link) R.string.machines_offline_unreachable_link else R.string.machines_offline_unreachable_ssh
        else -> null
    }
}
