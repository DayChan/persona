package persona.platform.phonetools

import persona.core.contracts.api.*
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.json.JsonObject

/** All execution paths, including stale model calls, pass through this gate. */
class PhoneToolRegistry(
    private val enabled: () -> Set<PhoneSource>,
    private val available: () -> Boolean,
    private val foreground: () -> Boolean,
    private val permitted: (String, PhoneSource) -> Boolean,
    private val confirm: suspend (PhoneConfirmationText) -> PhoneConfirmation,
    private val execute: suspend (String, JsonObject) -> PhoneToolResult,
) {
    private val lock = Mutex()
    private val completed = linkedMapOf<String, PhoneToolResult>()
    val all: List<PhoneToolSpec> get() = PhoneToolCatalog.all.map { it.spec }
    fun enabledSpecs(): List<PhoneToolSpec> = PhoneToolCatalog.all.filter(::usable).map { it.spec }
    private fun usable(tool: ToolDefinition) = available() && tool.source in enabled() &&
        (!tool.foregroundOnly || foreground()) && permitted(tool.spec.name, tool.source)

    suspend fun call(call: PhoneToolCall): PhoneToolResult = lock.withLock {
        // Never re-execute a write when the same call is delivered again in this pairing scope.
        completed[call.callId]?.let { return@withLock it }
        val result = try {
            val tool = PhoneToolCatalog.all.firstOrNull { it.spec.name == call.name }
                ?: return@withLock PhoneToolText.unknownTool(call.name, enabledSpecs().map { it.name }).also { remember(call, it) }
            refusal(tool)?.let { return@withLock it.also { result -> remember(call, result) } }
            // Arguments that cannot even be shown are refused before anyone is asked.
            when (tool.confirmation?.let { summary -> confirm(summary(call.args)) } ?: PhoneConfirmation.Allowed) {
                PhoneConfirmation.Allowed -> {
                    refusal(tool)?.let { return@withLock it.also { result -> remember(call, result) } }
                    withTimeoutOrNull(EXECUTION_TIMEOUT_MILLIS) { execute(call.name, call.args) } ?: PhoneToolText.timedOut()
                }
                // only a tap on Deny is a refusal (#1875)
                PhoneConfirmation.Denied -> PhoneToolText.declined()
                PhoneConfirmation.Unanswered -> PhoneToolText.unanswered()
                PhoneConfirmation.NotShown -> PhoneToolText.notShown()
                PhoneConfirmation.Withdrawn -> PhoneToolText.interrupted()
            }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (failure: PhoneToolFailure) {
            PhoneToolText.failure(failure, PhoneToolCatalog.all.firstOrNull { it.spec.name == call.name }?.source)
        }
        catch (invalid: IllegalArgumentException) { PhoneToolText.invalid(invalid.message ?: "invalid arguments") }
        catch (_: SecurityException) {
            PhoneToolText.denied(PhoneToolCatalog.all.firstOrNull { it.spec.name == call.name }?.source)
        }
        catch (unavailable: IllegalStateException) { PhoneToolText.unavailable(unavailable.message) }
        catch (error: Exception) { PhoneToolText.failed(error) }
        remember(call, result)
        result
    }

    /** Why this tool cannot run now, in words the model can act on; null when it can. */
    private fun refusal(tool: ToolDefinition): PhoneToolResult? = when {
        tool.source !in enabled() -> PhoneToolText.turnedOff(tool.source)
        !permitted(tool.spec.name, tool.source) -> PhoneToolText.denied(tool.source)
        tool.foregroundOnly && !foreground() -> PhoneToolText.needsForeground()
        !available() -> PhoneToolText.unavailable(null)
        else -> null
    }

    private fun remember(call: PhoneToolCall, result: PhoneToolResult) {
        completed[call.callId] = result
        if (completed.size > 128) completed.remove(completed.keys.first())
    }

    private companion object { const val EXECUTION_TIMEOUT_MILLIS = 30_000L }
}

/** The words a refused or failed call returns (as the iOS app says them, with Android's settings path). */
internal object PhoneToolText {
    /** The iOS app's exact sentence, so the model reads the same refusal whichever phone answered. */
    const val DECLINED = "The user declined on the phone."

    fun declined() = PhoneToolResult.text(DECLINED, true)

    /** Nobody answered the confirmation in time: not a refusal, and nothing was saved. */
    fun unanswered() = PhoneToolResult.text(
        "手机上没人确认 / Nobody approved or declined this on the phone within " +  // i18n-ignore: tool result for the model
            "${PhoneConfirmations.TIMEOUT_MILLIS / 1000} seconds, so it was not saved and the confirmation was taken back. " +
            "This is not a refusal: tell the user, and call it again when they can approve it on the phone.", true)

    /** The confirmation could not be shown at all: Persona was in the background with its notifications off. */
    fun notShown() = PhoneToolResult.text(
        "手机上没能显示确认 / The confirmation could not be shown on the phone: Persona is in the background and its " +  // i18n-ignore: tool result for the model
            "notifications (or the Phone action confirmations channel) are turned off. Nothing was saved, and the user " +
            "did not refuse. Ask the user to open Persona on the phone, or turn its notifications on in Android Settings › " +
            "Apps › Persona › Notifications, then call it again.", true)

    fun unknownTool(name: String, available: List<String>) = PhoneToolResult.text(
        "未知工具 / Unknown tool: $name. 可用工具 / Available tools: ${available.joinToString(", ")}.", true)  // i18n-ignore: tool result for the model

    /** Where the switch for every source lives in the app (Settings › Connectors › This phone). */
    private const val APP_PATH_ZH = "Persona › 设置 › 连接器 › 这台手机"  // i18n-ignore: tool result for the model
    private const val APP_PATH_EN = "Persona › Settings › Connectors › This phone"

    fun turnedOff(source: PhoneSource) = PhoneToolResult.text(
        "“${source.modelName}” 已在手机上关闭 / The ${source.key} data source is turned off on the phone. " +  // i18n-ignore: tool result for the model
            "The user can turn it on in $APP_PATH_EN ($APP_PATH_ZH).", true)

    /**
     * Where to allow [source]: the app's switch first, then the Android setting, because once a permission is refused
     * twice Android stops asking and only the system settings page can grant it.
     */
    fun grantHint(source: PhoneSource?): String {
        val system = source?.let(::systemSetting)
        // Sources that need no permission (clipboard, device) have a switch but no Allow button.
        val zh = "请在安卓手机上打开 $APP_PATH_ZH" +  // i18n-ignore: tool result for the model
            when {
                source == null -> "，点对应数据的「授权」"  // i18n-ignore: tool result for the model
                system == null -> "，打开「${source.modelName}」"  // i18n-ignore: tool result for the model
                else -> "，打开「${source.modelName}」并点「授权」"  // i18n-ignore: tool result for the model
            } + (system?.let { "；如果拒绝过、系统不再弹窗，就去${it.first}。" } ?: "。")  // i18n-ignore: tool result for the model
        val en = "On the Android phone, open $APP_PATH_EN" +
            when {
                source == null -> " and tap Allow for that data"
                system == null -> " and turn on ${source.key}"
                else -> ", turn on ${source.key} and tap Allow"
            } + (system?.let { ". If it was refused before and Android no longer asks, ${it.second}." } ?: ".")
        return "$zh\n$en"
    }

    /** The Android settings page that grants [source], as (Chinese, English) sentences; null when there is none. */
    private fun systemSetting(source: PhoneSource): Pair<String, String>? = when (source) {
        PhoneSource.Contacts -> permission("通讯录", "Contacts")  // i18n-ignore: tool result for the model
        PhoneSource.Calendar -> permission("日历", "Calendar")  // i18n-ignore: tool result for the model
        PhoneSource.Location -> permission("位置信息", "Location")  // i18n-ignore: tool result for the model
        // Android 12 and older grant photos through storage (READ_EXTERNAL_STORAGE).
        PhoneSource.Photos -> if (photosSdk() >= 33) permission("照片和视频", "Photos and videos")  // i18n-ignore: tool result for the model
            else permission("文件和媒体", "Files and media")  // i18n-ignore: tool result for the model
        PhoneSource.Notifications -> "系统设置 › 应用 › Persona › 通知 里打开通知" to  // i18n-ignore: tool result for the model
            "turn notifications on in Android Settings › Apps › Persona › Notifications"
        PhoneSource.Health -> "Health Connect › 应用权限 › Persona 里允许读取" to  // i18n-ignore: tool result for the model
            "allow reading in Health Connect › App permissions › Persona"
        PhoneSource.Clipboard, PhoneSource.Device -> null
    }

    /** The SDK level the photos permission name depends on; replaceable in tests. */
    internal var photosSdk: () -> Int = { android.os.Build.VERSION.SDK_INT }

    private fun permission(zh: String, en: String) =
        "系统设置 › 应用 › Persona › 权限 里允许「$zh」" to  // i18n-ignore: tool result for the model
            "allow $en in Android Settings › Apps › Persona › Permissions"

    fun denied(source: PhoneSource?) = PhoneToolResult.text(
        (source?.let { "没有权限：${it.modelName} / Permission denied: ${it.key}.\n" } ?: "没有权限 / Permission denied.\n") +  // i18n-ignore: tool result for the model
            grantHint(source), true)

    fun needsForeground() = PhoneToolResult.text(
        "安卓只允许打开着的 Persona 使用这项数据 / Android only allows this while Persona is open on screen: " +  // i18n-ignore: tool result for the model
            "ask the user to open Persona, then retry.", true)

    fun interrupted() = PhoneToolResult.text(
        "手机工具已关闭或 Persona 已进入后台 / The tool was turned off or Persona left the screen before it finished.", true)  // i18n-ignore: tool result for the model

    fun unavailable(message: String?) = PhoneToolResult.text(message ?: "手机数据暂不可用 / Phone data is not available right now.", true)  // i18n-ignore: tool result for the model

    fun invalid(message: String) = PhoneToolResult.text("参数错误 / Invalid argument: $message", true)  // i18n-ignore: tool result for the model

    fun timedOut() = PhoneToolResult.text(
        "手机操作超时 / The phone did not finish within 30 seconds. Check what happened before trying again.", true)  // i18n-ignore: tool result for the model

    fun failed(error: Exception) = PhoneToolResult.text(
        "请求失败 / Request failed: ${error.message ?: error.javaClass.simpleName}", true)  // i18n-ignore: tool result for the model

    fun failure(failure: PhoneToolFailure, source: PhoneSource?) = when (failure.kind) {
        PhoneToolFailure.Kind.InvalidArgument -> invalid(failure.message.orEmpty())
        PhoneToolFailure.Kind.Denied -> PhoneToolResult.text(
            "没有权限 / Permission denied: ${failure.message}\n" + grantHint(source), true)  // i18n-ignore: tool result for the model
        PhoneToolFailure.Kind.Unavailable -> unavailable(failure.message)
    }
}
