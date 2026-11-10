package persona.feature.assistant

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import persona.core.contracts.Session
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.PendingMainConversation
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.FailureReason
import persona.core.domain.mapOperationFailure

/** The durable outbox is the only source of pending bubbles, including after process recreation. */
@Composable
internal fun AssistantPendingConversation(
    messages: List<OutboxMessage>, session: Session?, modifier: Modifier = Modifier,
    contentPadding: PaddingValues = PaddingValues(), demo: Boolean = false,
    /** A refused start's button ("Add computer", "Connect a model…"), and "Retry" once the step is done. */
    onRefusal: (ReadinessActionId) -> Unit = {},
    onRetry: ((OutboxMessage) -> Unit)? = null,
    /** The empty state's line instead of 「…连上后继续发送」 (an offline manager takes nothing: docs/assistant/managers.md). */
    emptyDetail: String? = null,
) {
    LazyColumn(modifier.fillMaxSize(),
        contentPadding = PaddingValues(start = pagePadding, end = pagePadding,
            top = contentPadding.calculateTopPadding() + 12.dp, bottom = contentPadding.calculateBottomPadding() + 16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)) {
        if (demo) item { AssistantDemoNotice() }
        item { StatusLine(assistantStatus(session, messages)) }
        if (messages.isEmpty()) item {
            JEmptyState(stringResource(R.string.assistant_pending_empty_title), emptyDetail ?: stringResource(R.string.assistant_pending_empty_detail))
        }
        items(messages, key = { it.clientMessageId }) { message ->
            Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.End,
                verticalArrangement = Arrangement.spacedBy(4.dp)) {
                UserBubble(message.text)
                message.state.pendingStatus()?.let { PendingBadge(it) }
                // A refused message says why and offers Retry, not a bubble waiting for ever: a start the hub refused
                // for a missing step also gets that step's button (docs/assistant/onboarding.md 4.4).
                if (message.state == DeliveryState.Refused) RefusalCard(message.error, onRefusal, onRetry?.let { retry -> { retry(message) } })
            }
        }
    }
}

/** Why the hub refused it, by its [code] (none for a code without words: the badge already says it was not sent). */
@Composable
private fun RefusalCard(code: String?, onAction: (ReadinessActionId) -> Unit, onRetry: (() -> Unit)?) {
    val failure = mapOperationFailure(code, "none", false)
    val feedback = operationFeedback(failure).takeIf { failure.reason != FailureReason.Failed }
    val action = feedback?.action?.readinessAction()
    Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        if (feedback != null) JNotice(feedback.localizedMessage, Modifier.fillMaxWidth(), status = Status.NeedsYou, icon = PhIcons.WarningCircle,
            actionLabel = action?.let { stringResource(it.label) }, onAction = action?.let { { onAction(it) } })
        onRetry?.let { JButton(stringResource(R.string.assistant_refused_retry), it, kind = JButtonKind.Secondary, size = JButtonSize.Small) }
    }
}

@Preview(name = "Offline first message (light)", showBackground = true, widthDp = 320, heightDp = 480)
@Preview(name = "Offline first message (dark)", showBackground = true, widthDp = 320, heightDp = 480,
    uiMode = android.content.res.Configuration.UI_MODE_NIGHT_YES)
@Composable
private fun PendingConversationPreview() {
    PersonaTheme(dynamicColor = false) {
        JPage(title = "Persona") { padding ->
            AssistantPendingConversation(listOf(OutboxMessage("preview", PendingMainConversation,
                stringResource(R.string.assistant_suggest_calendar_prompt), DeliveryState.WaitingForNetwork, mainTarget = true)), null,
                Modifier.fillMaxSize(), contentPadding = padding)
        }
    }
}
