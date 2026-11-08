package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * GET /api/v1/assistant without the conversations: which machine the assistant runs on, with which launcher, in
 * which directory, and what it is told about the user (docs/protocol/protocol.md, docs/assistant/assistant.md).
 */
@Serializable
data class AssistantConfig(
    @SerialName("machine_id") val machineId: String = "",
    val launcher: String = "",
    /** The assistant directory on that machine; empty while the machine has not reported its home. */
    val cwd: String = "",
    /** The IANA zone routines and the assistant's sense of "today" use; null until a phone has set one. */
    val timezone: String? = null,
    /** What the user wrote in the app; null while nothing was saved there and hub.json's notes apply. */
    val notes: String? = null,
    /** What the assistant is actually told: [notes], else hub.json's. */
    @SerialName("notes_effective") val notesEffective: String = "",
    /** The main conversation's session id; it lives on [machineId]. */
    val main: String? = null,
    /**
     * "Notify only in the main chat": tasks the main conversation sends out stay out of pushes and the badge (the main conversation
     * tells you instead); null from a hub without the setting.
     */
    @SerialName("main_tasks_quiet") val mainTasksQuiet: Boolean? = null,
    /** Stable owner returned by hubs supporting per-assistant settings. */
    val manager: String? = null,
    @SerialName("default_permission_mode") val defaultPermissionMode: String? = null,
    @SerialName("permission_defaults") val permissionDefaults: AssistantPermissionDefaults? = null,
)

/**
 * PUT /api/v1/assistant. Only the fields that are set go on the wire and change; the hub keeps the rest.
 * [notes] set to "" is sent (it clears what the user wrote, unlike leaving it null).
 */
@Serializable
data class AssistantConfigUpdate(
    @SerialName("machine_id") val machineId: String? = null,
    val launcher: String? = null,
    val timezone: String? = null,
    val notes: String? = null,
    /** "-" clears the main conversation: it stays on the machine it ran on and cannot continue elsewhere. */
    val main: String? = null,
    @SerialName("main_tasks_quiet") val mainTasksQuiet: Boolean? = null,
    @SerialName("default_permission_mode") val defaultPermissionMode: String? = null,
) {
    companion object {
        const val CLEAR_MAIN = "-"
    }
}

/** Manager-owned contract for scoped persistence and future-session inheritance; absent on older managers. */
@Serializable
data class AssistantPermissionDefaults(
    val version: Int = 0,
    val manager: String = "",
    val modes: List<String> = emptyList(),
) {
    fun supports(owner: String?): Boolean = !owner.isNullOrBlank() && manager == owner && version == 1 &&
        modes.containsAll(MODES)

    companion object {
        val MODES = listOf("default", "acceptEdits", "plan", "auto", "bypassPermissions")
    }
}
