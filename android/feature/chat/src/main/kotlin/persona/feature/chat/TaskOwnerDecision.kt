package persona.feature.chat

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.Todo
import persona.core.designsystem.*
import persona.core.domain.tasks.AssistantExtra
import persona.core.domain.tasks.AssistantTaskCards

/** Acceptance carries the exact round the user read, independently of any later task refresh. */
@Composable
internal fun TaskOwnerDecision(todo: Todo, host: AssistantTaskHost) {
    val ask = todo.ask ?: return
    if (ask.outcome == "decide") {
        TaskDecisionCard(todo, host)
        return
    }
    val busy by host.tasks.busy.collectAsStateWithLifecycle()
    val resources = LocalResources.current
    JCard(Modifier.fillMaxWidth()) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text(ask.verdict, style = PersonaTheme.type.bodyStrong, color = PersonaTheme.palette.ink)
            ask.recommend?.takeIf(String::isNotBlank)?.let { recommendation ->
                Text(stringResource(R.string.chat_tray_recommended, recommendation), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
            if (ask.askedAt.isNotBlank() && (ask.outcome != "decide" || !ask.recommend.isNullOrBlank())) {
                JButton(stringResource(R.string.chat_tray_accept), {
                    host.tasks.acceptTask(todo, host.onError) { host.onError(IllegalStateException(resources.getString(R.string.chat_tray_accept_changed))) }
                }, Modifier.testTag("assistant-accept-${todo.id}"), loading = "accept:${todo.id}" in busy)
            }
        }
    }
}

/** The owner's decision keeps direct completion separate from following Persona's recommendation. */
@Composable
internal fun TaskDecisionCard(todo: Todo, host: AssistantTaskHost, enabled: Boolean = true, onOpen: (() -> Unit)? = null) {
    val ask = todo.ask?.takeIf { it.outcome == "decide" } ?: return
    if (todo.isDone || todo.cancelledAt != null) return
    val busy by host.tasks.busy.collectAsStateWithLifecycle()
    val sending = "accept:${todo.id}" in busy
    var failed by remember(todo.id, ask) { mutableStateOf(false) }
    var changed by remember(todo.id, ask) { mutableStateOf(false) }
    val onChanged = { changed = true }
    JCard(Modifier.fillMaxWidth().testTag("assistant-task-decision-${todo.id}")) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (onOpen != null) Text(todo.title, style = PersonaTheme.type.bodyStrong, color = PersonaTheme.palette.ink)
            Text(ask.verdict, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink)
            ask.recommend?.takeIf(String::isNotBlank)?.let {
                Text(stringResource(R.string.chat_tray_recommended, it), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
            if (failed) Text(stringResource(R.string.chat_tray_finish_failed), style = PersonaTheme.type.caption, color = PersonaTheme.palette.danger.text)
            if (changed) Text(stringResource(R.string.chat_tray_accept_changed), style = PersonaTheme.type.caption, color = PersonaTheme.palette.cautionText)
            val finish = JAction(stringResource(R.string.chat_tray_finish_now), enabled = enabled) {
                failed = false; changed = false
                host.tasks.finishTaskNow(todo, { failed = true }, onChanged)
            }
            val follow = if (ask.askedAt.isNotBlank() && !ask.recommend.isNullOrBlank()) JAction(
                stringResource(R.string.chat_tray_follow_recommendation), enabled = enabled) {
                failed = false; changed = false
                host.tasks.acceptTask(todo, host.onError, onChanged)
            } else null
            DecisionActions(follow, listOfNotNull(onOpen?.let { open -> JAction(stringResource(R.string.chat_link_open), enabled = enabled, onClick = open) }) + finish,
                stringResource(R.string.chat_task_detail_more), busy = sending)
            if (sending) JSpinner()
        }
    }
}

/** The row's detail retains the current execution's receipt/PR and report without duplicating the chat entity. */
@Composable
internal fun TaskLifecycleDetails(todo: Todo, host: AssistantTaskHost) {
    val feeds by host.tasks.feeds.collectAsStateWithLifecycle()
    val plans by host.tasks.plans.collectAsStateWithLifecycle()
    val cards by host.tasks.taskCards.collectAsStateWithLifecycle()
    val merged = AssistantTaskCards.merge(feeds[host.conversation].orEmpty(), plans, listOf(todo), cards[host.conversation].orEmpty())
        .firstOrNull { it.todo?.id == todo.id && (todo.planId == null || it.identity.planId == todo.planId) }
    listOfNotNull(merged?.receipt, merged?.report).forEach { item -> AssistantExtraLine(AssistantExtra.Feed(item), host) }
}
