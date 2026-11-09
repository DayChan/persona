package persona.feature.inbox

/** Account-scoped preferences for retained recalls and message edit history. */
data class LarkMessageDisplay(
    val accountId: String = "",
    val antiRecall: Boolean = false,
    val showEditHistory: Boolean = false,
) {
    companion object {
        val Hidden = LarkMessageDisplay()
    }
}
