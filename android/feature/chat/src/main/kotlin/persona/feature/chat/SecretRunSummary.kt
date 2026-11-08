package persona.feature.chat

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import persona.core.contracts.Interaction
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JCodeBlock
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens

/**
 * A password card's body (#1137), in the chat and in the Agents tab: why the password is needed and every command
 * exactly as it will run, read-only. The computer is in the card's headline.
 */
@Composable
fun SecretRunSummary(request: Interaction) {
    val run = request.secretRun ?: return
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        if (run.purpose.isNotEmpty()) Text(run.purpose, style = PersonaTheme.type.body)
        Text(stringResource(R.string.chat_request_secret_run_commands), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        // One block per command: a long one that wraps never reads as two.
        run.commands.forEach { JCodeBlock(it) }
    }
}

/** A password card's only answer on this phone: Decline, and where to fill it in instead. */
@Composable
fun SecretRunActions(enabled: Boolean, onDecline: () -> Unit) {
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        JButton(stringResource(R.string.chat_request_secret_run_decline), onDecline, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary, enabled = enabled)
        Text(stringResource(R.string.chat_request_secret_run_on_iphone), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
    }
}
