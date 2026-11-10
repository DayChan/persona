package persona.core.domain

import org.junit.Assert.*
import org.junit.Test

class OperationFailureTest {
    @Test fun knowledgeConfigurationIsNotANetworkFailure() {
        for ((code, reason, action) in listOf(
            Triple("kb_disabled", FailureReason.KnowledgeDisabled, FailureAction.None),
            Triple("kb_unavailable", FailureReason.KnowledgeUnavailable, FailureAction.Retry),
            Triple("kb_read_only", FailureReason.KnowledgeReadOnly, FailureAction.None),
        )) {
            val failure = mapOperationFailure(code, null, true)
            assertEquals(reason, failure.reason)
            assertEquals(action, failure.action)
            assertFalse(failure.offline)
        }
    }

    @Test fun deliveryEvidenceAndTerminalCodesDetermineRecovery() {
        assertEquals(FailureAction.Verify, mapOperationFailure("input_draft", "unknown", true).action)
        assertEquals(FailureAction.Verify, mapOperationFailure(null, "yes", true).action)
        assertEquals(FailureAction.OpenTerminal, mapOperationFailure("input_draft", "none", true).action)
        assertEquals(FailureAction.Verify, mapOperationFailure("stale_session", "none", true).action)
        assertEquals(FailureAction.PairAgain, mapOperationFailure("device_revoked", "unknown", true).action)
        assertEquals(FailureAction.UpdateHub, mapOperationFailure("incompatible", "none", true).action)
        assertEquals(FailureAction.None, mapOperationFailure("future_code", "none", false).action)
        assertEquals(FailureAction.Retry, mapOperationFailure("future_code", "none", true).action)
        assertEquals(FailureReason.NotDelivered, mapOperationFailure("no_network", null, true, FailureCategory.Network).reason)
        assertEquals(FailureAction.Verify, mapOperationFailure(null, null, true, FailureCategory.Network).action)
    }
}
