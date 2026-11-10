package persona.feature.settings

import persona.core.contracts.api.ExtensionChoice
import persona.core.contracts.api.ExtensionField
import persona.core.contracts.api.ExtensionItems
import persona.core.contracts.api.HubLauncher
import kotlinx.serialization.json.JsonPrimitive
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4

/**
 * A string field of format machine or launcher picks from the hub's (#2448), as on iOS and the web: machines of the
 * field's systems by name, launchers not hidden (a display name before the name), a value naming none of them first and
 * marked; launchers not in yet are typed. A whole number steps within its bounds.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ExtensionPickersTest {
    private val res get() = ApplicationProvider.getApplicationContext<android.content.Context>().resources

    private val machines = listOf(
        ExtensionMachine("studio", "Mac Studio", "darwin", online = true),
        ExtensionMachine("server", "Home server", "linux", online = true),
        ExtensionMachine("pc", "", "windows", online = false),
    )
    private val launchers = listOf(
        HubLauncher("claude", source = "builtin"),
        HubLauncher("work_gw", displayName = "Work gateway", source = "persona"),
        HubLauncher("same", displayName = "same", source = "persona"),
        HubLauncher("old_gw", source = "zshrc", hidden = true),
    )
    private val machine = ExtensionField("agent_machine", format = "machine", os = listOf("darwin", "linux"))
    private val launcher = ExtensionField("agent_launcher", format = "launcher")

    @Test fun machinesOfTheFieldsSystemsByName() {
        assertEquals(listOf(PickerChoice("studio", "Mac Studio"), PickerChoice("server", "Home server")),
            ExtensionPickers.choices(res, machine, null, machines, null))
        // No x-os: any machine, by its id when it has no name.
        assertEquals(listOf("Mac Studio", "Home server", "pc"), ExtensionPickers.choices(res, machine.copy(os = null), "pc", machines, null)!!.map { it.label })
    }

    @Test fun launchersNotHiddenWithTheirDisplayName() {
        assertEquals(listOf(PickerChoice("claude", "claude"), PickerChoice("work_gw", "Work gateway (work_gw)"), PickerChoice("same", "same")),
            ExtensionPickers.choices(res, launcher, "claude", machines, launchers))
    }

    @Test fun aValueNamingNoneIsKeptFirstAndMarked() {
        val m = ExtensionPickers.choices(res, machine, "old-mac", machines, null)!!
        assertEquals(PickerChoice("old-mac", "old-mac（找不到这台机器）", missing = true), m.first())
        assertEquals(3, m.size)
        val l = ExtensionPickers.choices(res, launcher, "gone_gw", machines, launchers)!!
        assertEquals(PickerChoice("gone_gw", "gone_gw（找不到这个启动器）", missing = true), l.first())
        // A machine of another system is not one to pick, so it is marked too.
        assertTrue(ExtensionPickers.choices(res, machine, "pc", machines, null)!!.first().missing)
    }

    @Test fun otherFieldsAndLaunchersNotInAreNotPickers() {
        assertNull("launchers not in: typed", ExtensionPickers.choices(res, launcher, null, machines, null))
        assertNull(ExtensionPickers.choices(res, ExtensionField("x"), null, machines, launchers))
        assertNull(ExtensionPickers.choices(res, ExtensionField("x", format = "uri"), null, machines, launchers))
        assertNull("its own choices come first", ExtensionPickers.choices(res,
            launcher.copy(enum = listOf(ExtensionChoice(JsonPrimitive("a")))), null, machines, launchers))
        assertNull("a list of machines is switches", ExtensionPickers.choices(res,
            ExtensionField("x", type = "array", items = ExtensionItems(format = "machine")), null, machines, launchers))
    }

    @Test fun aWholeNumberStepsWithinItsBounds() {
        val hours = ExtensionField("hours", type = "integer", minimum = 1.0, maximum = 72.0, default = JsonPrimitive(4))
        assertEquals("from the default", 5L, ExtensionPickers.stepped(hours, null, up = true))
        assertEquals(3L, ExtensionPickers.stepped(hours, null, up = false))
        assertEquals(72L, ExtensionPickers.stepped(hours, 72, up = true))
        assertEquals(1L, ExtensionPickers.stepped(hours, 1, up = false))
        assertFalse(ExtensionPickers.canStep(hours, 72, up = true))
        assertFalse(ExtensionPickers.canStep(hours, 1, up = false))
        assertTrue(ExtensionPickers.canStep(hours, 4, up = true))
        val free = ExtensionField("n", type = "integer")
        assertEquals("from 0 without a default or minimum", -1L, ExtensionPickers.stepped(free, null, up = false))
        assertEquals("from the minimum without a default", 3L, ExtensionPickers.stepped(ExtensionField("n", type = "integer", minimum = 2.0), null, up = true))
    }
}
