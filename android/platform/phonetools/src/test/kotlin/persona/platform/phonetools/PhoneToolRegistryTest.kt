package persona.platform.phonetools

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.*
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class PhoneToolRegistryTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources
    private fun text(result: PhoneToolResult) = result.content.single().jsonObject.getValue("text").jsonPrimitive.content

    @Test fun registrationAndStaleCallsRespectAvailabilityPermissionsAndSources() = runTest {
        var online = true
        var foreground = true
        var permission = true
        var sources = PhoneSource.entries.toSet()
        var executions = 0
        val registry = PhoneToolRegistry({ sources }, { online }, { foreground }, { _, _ -> permission },
            { error("Reads must not ask for confirmation") }, { _, _ -> executions++; PhoneToolResult.text("ok") })
        assertEquals(12, registry.enabledSpecs().size)
        assertFalse(registry.all.any { it.name.startsWith("phone_reminders_") || it.name == "phone_health_write_sample" })
        assertTrue(registry.all.any { it.name == "phone_health_summary" })
        assertTrue(registry.all.all { it.inputSchema["type"]?.jsonPrimitive?.content == "object" })
        foreground = false
        assertFalse(registry.enabledSpecs().any { it.name == "phone_clipboard_get" || it.name == "phone_location_current" })
        val background = registry.call(PhoneToolCall("background", "phone_clipboard_get", buildJsonObject {}))
        assertTrue(background.isError)
        assertTrue(text(background).contains("open on screen"))
        sources = emptySet()
        assertTrue(registry.enabledSpecs().isEmpty())
        val off = registry.call(PhoneToolCall("disabled", "phone_device_info", buildJsonObject {}))
        assertTrue(off.isError)
        assertEquals("“设备信息” 已在手机上关闭 / The device data source is turned off on the phone. " +
            "The user can turn it on in Persona › Settings › Connectors › This phone (Persona › 设置 › 连接器 › 这台手机).", text(off))
        sources = PhoneSource.entries.toSet()
        permission = false
        assertTrue(registry.enabledSpecs().isEmpty())
        assertTrue(text(registry.call(PhoneToolCall("denied", "phone_contacts_search", buildJsonObject {}))).startsWith("没有权限：通讯录"))
        permission = true
        online = false
        assertTrue(registry.enabledSpecs().isEmpty())
        online = true
        val unknown = registry.call(PhoneToolCall("unknown", "phone_teleport", buildJsonObject {}))
        assertTrue(unknown.isError)
        assertTrue(text(unknown).startsWith("未知工具 / Unknown tool: phone_teleport."))
        // Out-of-range numbers and unknown arguments are no reason to refuse (iOS clamps and ignores them).
        assertFalse(registry.call(PhoneToolCall("clamped", "phone_contacts_search",
            buildJsonObject { put("query", "test"); put("limit", 0); put("extra", true) })).isError)
        assertFalse(registry.call(PhoneToolCall("valid", "phone_device_info", buildJsonObject {})).isError)
        assertEquals(2, executions)
        // The same call delivered twice runs once.
        assertFalse(registry.call(PhoneToolCall("valid", "phone_device_info", buildJsonObject {})).isError)
        assertEquals(2, executions)
    }

    @Test fun calendarWritesRequireOneLocalDecisionAndRecheckStateAfterApproval() = runTest {
        var allow = false
        var enabled = true
        var revokeOnConfirm = false
        var confirmations = 0
        var executions = 0
        val registry = PhoneToolRegistry({ if (enabled) setOf(PhoneSource.Calendar) else emptySet() }, { true }, { true }, { _, _ -> true },
            { render ->
                val summary = render(res)
                assertTrue(summary.startsWith("新建日程「测试日程」\n")); assertTrue(summary.contains(" → "))
                confirmations++
                if (revokeOnConfirm) enabled = false
                if (allow) PhoneConfirmation.Allowed else PhoneConfirmation.Denied
            }, { _, _ -> executions++; PhoneToolResult.text("created") })
        val args = buildJsonObject {
            put("title", "测试日程"); put("start", "2026-11-01T10:00:00+08:00"); put("end", "2026-11-01T11:00:00+08:00")
        }
        val refused = registry.call(PhoneToolCall("refused", "phone_calendar_create_event", args))
        assertTrue(refused.isError)
        // Word for word what the iOS app answers.
        assertEquals("The user declined on the phone.", text(refused))
        assertEquals(0, executions)
        allow = true
        val call = PhoneToolCall("accepted", "phone_calendar_create_event", args)
        assertFalse(registry.call(call).isError)
        assertFalse(registry.call(call).isError)
        assertEquals(1, executions)
        assertEquals(2, confirmations)
        revokeOnConfirm = true
        assertTrue(registry.call(PhoneToolCall("revoked", call.name, args)).isError)
        assertEquals(1, executions)
        enabled = true
        // Arguments that cannot be shown are refused without asking anyone.
        val invalid = registry.call(PhoneToolCall("invalid", call.name, JsonObject(args + ("end" to JsonPrimitive("2026-11-01T09:00:00+08:00")))))
        assertTrue(invalid.isError)
        assertEquals("参数错误 / Invalid argument: end must be after start", text(invalid))
        assertEquals(3, confirmations)
        assertTrue(text(registry.call(PhoneToolCall("untitled", call.name, JsonObject(args - "title")))).contains("title is required"))
        assertEquals(3, confirmations)
    }

    /** Only Deny is a refusal (#1875): nobody answering, or no way to ask, must not read as "the user declined". */
    @Test fun aConfirmationNobodyAnsweredIsNotADecline() = runTest {
        var outcome = PhoneConfirmation.Unanswered
        var executions = 0
        val registry = PhoneToolRegistry({ setOf(PhoneSource.Calendar) }, { true }, { true }, { _, _ -> true },
            { outcome }, { _, _ -> executions++; PhoneToolResult.text("created") })
        val args = buildJsonObject {
            put("title", "测试日程"); put("start", "2026-11-01T10:00:00+08:00"); put("end", "2026-11-01T11:00:00+08:00")
        }
        val unanswered = registry.call(PhoneToolCall("unanswered", "phone_calendar_create_event", args))
        assertTrue(unanswered.isError)
        assertFalse(text(unanswered).contains(PhoneToolText.DECLINED))
        assertTrue(text(unanswered).contains("within 90 seconds"))
        assertTrue(text(unanswered).contains("This is not a refusal"))
        outcome = PhoneConfirmation.NotShown
        val notShown = registry.call(PhoneToolCall("not-shown", "phone_calendar_create_event", args))
        assertTrue(notShown.isError)
        assertFalse(text(notShown).contains(PhoneToolText.DECLINED))
        assertTrue(text(notShown).contains("could not be shown on the phone"))
        assertTrue(text(notShown).contains("Android Settings › Apps › Persona › Notifications"))
        outcome = PhoneConfirmation.Withdrawn
        val withdrawn = registry.call(PhoneToolCall("withdrawn", "phone_calendar_create_event", args))
        assertEquals(text(PhoneToolText.interrupted()), text(withdrawn))
        outcome = PhoneConfirmation.Denied
        assertEquals(PhoneToolText.DECLINED, text(registry.call(PhoneToolCall("denied", "phone_calendar_create_event", args))))
        assertEquals(0, executions)
        // the same call delivered again gets the same answer, without asking again
        outcome = PhoneConfirmation.Allowed
        assertEquals(text(unanswered), text(registry.call(PhoneToolCall("unanswered", "phone_calendar_create_event", args))))
        assertEquals(0, executions)
    }

    @Test fun refusalsNameTheCurrentPageAndTheAndroidSetting() = runTest {
        var permitted = false
        val registry = PhoneToolRegistry({ PhoneSource.entries.toSet() }, { true }, { true }, { _, _ -> permitted },
            { PhoneConfirmation.Allowed }, { _, _ -> throw PhoneToolFailure(PhoneToolFailure.Kind.Denied, "通知 / Notifications.") })
        val contacts = text(registry.call(PhoneToolCall("contacts", "phone_contacts_search", buildJsonObject {})))
        // The page that used to be Settings › Phone data is now Settings › Connectors › This phone.
        assertFalse(contacts.contains("手机数据") || contacts.contains("Phone data"))
        assertTrue(contacts.contains("Persona › 设置 › 连接器 › 这台手机"))
        assertTrue(contacts.contains("Persona › Settings › Connectors › This phone"))
        // Android stops asking after two refusals: say which system permission to allow, and where.
        assertTrue(contacts.contains("系统设置 › 应用 › Persona › 权限 里允许「通讯录」"))
        assertTrue(contacts.contains("allow Contacts in Android Settings › Apps › Persona › Permissions"))
        val photos = text(registry.call(PhoneToolCall("photos", "phone_photos_recent", buildJsonObject {})))
        assertTrue(photos.contains("允许「照片和视频」") && photos.contains("allow Photos and videos"))
        // A refusal raised while running (notifications turned off) gets the same directions.
        permitted = true
        val notify = registry.call(PhoneToolCall("notify", "phone_notify", buildJsonObject {}))
        assertTrue(notify.isError)
        assertTrue(text(notify).contains("Android Settings › Apps › Persona › Notifications"))
        assertFalse(text(notify).contains("手机数据"))
        val calendar = text(registry.call(PhoneToolCall("calendar", "phone_calendar_events", buildJsonObject {})))
        assertTrue(calendar.contains("允许「日历」") && calendar.contains("allow Calendar"))
        val location = text(registry.call(PhoneToolCall("location", "phone_location_current", buildJsonObject {})))
        assertTrue(location.contains("允许「位置信息」") && location.contains("allow Location"))
        // Android 12 and older call the photos permission storage.
        val sdk = PhoneToolText.photosSdk
        try {
            PhoneToolText.photosSdk = { 32 }
            assertTrue(PhoneToolText.grantHint(PhoneSource.Photos).contains("allow Files and media"))
        } finally { PhoneToolText.photosSdk = sdk }
        assertTrue(PhoneToolText.grantHint(null).contains("tap Allow for that data"))
        // No Allow button exists for sources without a permission.
        assertFalse(PhoneToolText.grantHint(PhoneSource.Clipboard).contains("授权"))
        assertFalse(PhoneToolText.grantHint(PhoneSource.Clipboard).contains("Allow"))
        val health = PhoneToolText.grantHint(PhoneSource.Health)
        assertTrue(health.contains("Health Connect › App permissions › Persona"))
        assertFalse(PhoneToolText.grantHint(PhoneSource.Device).contains("系统设置"))
    }
}
