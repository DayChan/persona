package persona.feature.knowledge

import java.io.IOException
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.api.HubCodedFailure

class KnowledgeAvailabilityTest {
    @Test fun businessFailuresAreNotNetworkFailures() {
        assertEquals(R.string.knowledge_disabled_text, loadMessage(ResponseFailure("kb_disabled")))
        assertEquals(R.string.knowledge_opening, loadMessage(ResponseFailure("kb_unavailable")))
        assertEquals(persona.core.designsystem.R.string.designsystem_knowledge_read_only, loadMessage(ResponseFailure("kb_read_only", 403)))
        assertEquals(persona.core.designsystem.R.string.designsystem_feedback_assistant_restarting, loadMessage(ResponseFailure("assistant_offline")))
        assertEquals(R.string.knowledge_timeout, loadMessage(ResponseFailure("timeout", 502)))
        assertEquals(persona.core.designsystem.R.string.designsystem_feedback_pairing_expired, loadMessage(ResponseFailure("invalid_token", 401)))
        assertEquals(R.string.knowledge_forbidden, loadMessage(ResponseFailure("forbidden", 403)))
        assertEquals(R.string.knowledge_missing, loadMessage(ResponseFailure("not_found", 404)))
        assertEquals(R.string.knowledge_unreadable, loadMessage(ResponseFailure(null)))
        assertEquals(R.string.knowledge_unreachable, loadMessage(IOException("connection refused")))
        assertEquals(R.string.knowledge_disabled_text,
            loadMessage(IOException("wrapped", ResponseFailure("kb_disabled"))))
    }
}

private class ResponseFailure(override val code: String?, override val status: Int = 503) : IOException(), HubCodedFailure
