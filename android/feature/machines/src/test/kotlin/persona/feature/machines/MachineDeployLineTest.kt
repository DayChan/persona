package persona.feature.machines

import androidx.activity.ComponentActivity
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.decodeFromString
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.designsystem.PersonaTheme

/**
 * The card's deploy line follows the hub's `can_deploy`, `deploy_reason` and `deploy_phase` (#1868,
 * docs/operations/node-update.md), as the web's `machineDeployment` does; an older hub that sends none keeps Redeploy.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class MachineDeployLineTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun machine(fields: String, mismatch: Boolean = true): Machine =
        JsonCodec.decodeFromString("""{"id":"lab","name":"lab","online":true,"node_mismatch":$mismatch$fields}""")

    private fun show(machine: Machine) = rule.setContent {
        PersonaTheme(darkTheme = false, reduceMotion = true) { MachineDeployLine(machine, starting = false, onDeploy = {}) }
    }

    @Test fun redeployTheHubWouldRefuseIsNotOfferedAndItSaysWhy() {
        show(machine(""","can_deploy":false,"deploy_reason":"Upgrade the node service on this computer once first""""))
        rule.onNodeWithText("Redeploy").assertDoesNotExist()
        rule.onNodeWithText("Upgrade the node service on this computer once first", substring = true).assertExists()
    }

    @Test fun aRefusalWithoutAReasonStillSaysWhy() {
        show(machine(""","can_deploy":false"""))
        rule.onNodeWithText("Redeploy").assertDoesNotExist()
        rule.onNodeWithText("can't be updated from here yet", substring = true).assertExists()
    }

    @Test fun waitingForIdleIsNotInstalling() {
        show(machine(""","deploying":true,"deploy_phase":"waiting_idle""""))
        rule.onNodeWithText("Waiting for the agents on this computer to go idle", substring = true).assertExists()
        rule.onNodeWithText("Installing", substring = true).assertDoesNotExist()
    }

    @Test fun aStageAloneIsADeployUnderWay() {
        val restarting = machine(""","deploy_phase":"restarting"""", mismatch = false)
        assertTrue(showsDeploy(restarting))
        show(restarting)
        rule.onNodeWithText("Restarting Persona on this computer", substring = true).assertExists()
        rule.onNodeWithText("Redeploy").assertDoesNotExist()
    }

    @Test fun anOlderHubOrOneThatAgreesKeepsRedeploy() {
        show(machine(""","can_deploy":true"""))
        rule.onNodeWithText("Redeploy").assertExists()
    }

    @Test fun anOlderHubWithoutTheFieldsKeepsRedeploy() {
        show(machine(""))
        rule.onNodeWithText("Redeploy").assertExists()
    }
}
