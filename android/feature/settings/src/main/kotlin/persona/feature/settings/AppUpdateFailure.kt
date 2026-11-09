package persona.feature.settings

internal enum class UpdateStage(val message: Int) {
    Metadata(R.string.settings_update_metadata_failed),
    Download(R.string.settings_update_download_failed),
    Verification(R.string.settings_update_verification_failed),
    Permission(R.string.settings_update_allow_unknown),
    Install(R.string.settings_update_install_failed),
}

internal class AppUpdateFailure(val stage: UpdateStage, cause: Exception) :
    IllegalStateException("Update failed at $stage", cause)
