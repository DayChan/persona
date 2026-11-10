package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** One line of `personad node doctor`'s report (server/internal/node/doctor.go); [state] is current / stale / missing. */
@Serializable
data class DoctorCheck(
    @SerialName("name") val name: String,
    /** ok | warn | fail */
    @SerialName("status") val status: String = "ok",
    @SerialName("state") val state: String = "",
    @SerialName("detail") val detail: String = "",
    @SerialName("hint") val hint: String = "",
    @SerialName("items") val items: List<String> = emptyList(),
)

/** `GET /api/v1/machines/{id}/doctor` and `POST …/doctor/fix` (docs/server/machines.md "Doctor: what this computer can do"). */
@Serializable
data class DoctorReport(
    @SerialName("ok") val ok: Boolean = false,
    @SerialName("dir") val dir: String = "",
    @SerialName("checks") val checks: List<DoctorCheck> = emptyList(),
    /** The generated files a fix wrote again. */
    @SerialName("fixed") val fixed: List<String> = emptyList(),
)

/** One subscription rate-limit window of an account (#2432, docs/server/usage-windows.md). */
@Serializable
data class UsageWindow(
    @SerialName("label") val label: String = "",
    @SerialName("window_minutes") val windowMinutes: Int = 0,
    @SerialName("used_percent") val usedPercent: Double = 0.0,
    @SerialName("resets_at") val resetsAt: String? = null,
    /** An allowance a provider keeps for one model next to the plan's own (#2595). */
    @SerialName("model") val model: String? = null,
)

/** An account the machines' agents are signed in to, from `GET /api/v1/usage/windows`. */
@Serializable
data class UsageAccount(
    @SerialName("id") val id: String,
    @SerialName("provider") val provider: String = "",
    @SerialName("name") val name: String = "",
    @SerialName("plan") val plan: String? = null,
    @SerialName("windows") val windows: List<UsageWindow> = emptyList(),
    @SerialName("sampled_at") val sampledAt: String = "",
    @SerialName("stale") val stale: Boolean = false,
    /** When it turns stale if nothing newer comes (#1905): kept numbers are judged again against it (liveAccounts). */
    @SerialName("stale_at") val staleAt: String? = null,
    /** Seen in an agent's files on one of the computers. */
    @SerialName("local") val local: Boolean = false,
    /** The account-pool gateways that report it, by name. */
    @SerialName("sources") val sources: List<String> = emptyList(),
    /** The computers (by name) that read it (#1907); an older hub only says [local]. */
    @SerialName("machines") val machines: List<String> = emptyList(),
    /** Why a gateway's numbers are old: error (it could not read this account just now), or rejected | unreachable | failed (its own last read failed). */
    @SerialName("problem") val problem: String? = null,
)

/** `GET /api/v1/usage/windows`. */
@Serializable
data class UsageWindows(@SerialName("accounts") val accounts: List<UsageAccount> = emptyList())
