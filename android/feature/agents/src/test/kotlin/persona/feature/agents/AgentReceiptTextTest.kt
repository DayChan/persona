package persona.feature.agents

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.Resolution
import persona.feature.chat.AnswerReceipt

/** The Agents page's notice after an answer says what was answered (#4740), or the plain one when it cannot. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh")
class AgentReceiptTextTest {
    private val res get() = RuntimeEnvironment.getApplication().resources

    @Test fun saysTheCommandOrFallsBack() {
        val rm = Interaction("r", kind = "permission", summary = "rm -rf build")
        assertEquals("已拒绝：rm -rf build", AgentText.receipt(AnswerReceipt(rm, Resolution(allow = false))).resolve(res))
        assertEquals("已批准计划", AgentText.receipt(AnswerReceipt(Interaction("p", kind = "plan"), Resolution(approve = true))).resolve(res))
        assertEquals("交回电脑处理", AgentText.receipt(AnswerReceipt(rm, Resolution(dismiss = true))).resolve(res))
        // #5121: the Agents page names the plan and the approval mode too.
        val plan = Interaction("p2", kind = "plan", plan = "## Use Kotlin\n1. Port it")
        assertEquals("已批准计划（自动接受编辑）：Use Kotlin",
            AgentText.receipt(AnswerReceipt(plan, Resolution(approve = true, mode = "acceptEdits"))).resolve(res))
    }
}
