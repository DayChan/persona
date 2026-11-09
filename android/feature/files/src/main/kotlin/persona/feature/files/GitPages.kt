package persona.feature.files

import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.api.SessionGitCommit
import persona.core.contracts.api.SessionGitFile
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import java.time.Instant

/** The Git half: the branch against its upstream, the working tree's changes, the history (all read-only). */
internal fun LazyListScope.gitBrowser(state: FilesState, controller: FilesController) {
    val git = state.git
    when {
        state.gitError && git == null -> item { RetryState(stringResource(R.string.files_git_failed), stringResource(R.string.files_error_connection), controller::refreshGit) }
        git == null -> item { Loading() }
        !git.isRepo -> item { JEmptyState(stringResource(R.string.files_git_not_repo), stringResource(R.string.files_git_not_repo_message)) }
        else -> {
            if (state.gitError) item { RetryState(stringResource(R.string.files_git_refresh_failed), stringResource(R.string.files_git_refresh_failed_message), controller::refreshGit) }
            if (state.gitLoading) item { JLinearProgress(null, Modifier.fillMaxWidth()) }
            item("branch") {
                JSectionHeader(git.branch ?: stringResource(R.string.files_git_detached),
                    action = if (git.upstream != null && !git.gone) ({ PullButton(git.behind, state, controller) }) else null)
                JGroupItem(GroupPosition.Single) {
                    JRow(git.upstream ?: stringResource(R.string.files_git_no_upstream), subtitle = git.root, leadingIcon = PhIcons.GitBranch, value = gitSync(LocalResources.current, git))
                }
                state.pullMessage?.let { PullResult(it, state.pullFailed) }
            }
            item("changes") {
                val (added, deleted) = GitDiffs.totals(git.files)
                // The totals at the heading's far end, on its baseline (the heading's own trailing slot sits mid-row).
                Row {
                    JSectionHeader(stringResource(R.string.files_git_changes), Modifier.weight(1f).alignByBaseline(), count = git.files.size)
                    if (added + deleted > 0) {
                        Text("+$added", Modifier.alignByBaseline(), style = PersonaTheme.type.micro, color = PersonaTheme.palette.diffAdd, maxLines = 1)
                        Spacer(Modifier.width(Tokens.Space.s))
                        Text("−$deleted", Modifier.alignByBaseline().padding(end = rowPadding), style = PersonaTheme.type.micro,
                            color = PersonaTheme.palette.diffDel, maxLines = 1)
                    }
                }
            }
            if (git.files.isEmpty()) item { JEmptyState(stringResource(R.string.files_git_clean), stringResource(R.string.files_git_clean_message)) }
            itemsIndexed(git.files, key = { _, it -> "file:${it.path}" }, contentType = { _, _ -> "change" }) { index, file ->
                JGroupItem(GroupPosition.of(index, git.files.size)) {
                    ChangeRow(file, stage = true) {
                        val staged = file.staged && !file.unstaged
                        // A document or picture compares its two versions (#2805) when the hub hands out old ones.
                        controller.push(if (comparable(controller, file)) compareChange(file, staged) else FileSelection.Diff(file, staged))
                    }
                }
            }
            item("history") { JSectionHeader(stringResource(R.string.files_git_history)) }
            val commits = git.commits + state.older
            val remote = remoteOf(git.upstream)
            itemsIndexed(commits, key = { _, it -> "commit:${it.hash}" }, contentType = { _, _ -> "commit" }) { index, commit ->
                JGroupItem(GroupPosition.of(index, commits.size)) {
                    CommitRow(commit, head = commit.hash == git.head?.hash, remote = remote) { controller.push(FileSelection.Commit(commit.hash)) }
                }
            }
            if (git.commits.isEmpty()) item { Notice(stringResource(R.string.files_git_no_commits), padded = false) }
            if (!state.noMore) item {
                JTextButton(when { state.moreLoading -> stringResource(R.string.files_git_loading); state.moreError -> stringResource(R.string.files_git_more_failed); else -> stringResource(R.string.files_git_more) },
                    controller::more, Modifier.fillMaxWidth(), enabled = !state.moreLoading && !state.gitLoading)
            }
        }
    }
}

private fun comparable(controller: FilesController, file: SessionGitFile) =
    !file.conflicted && DocumentCompare.kind(file.path) != null && controller.comparesVersions

/** "Pull" (N behind) on the branch heading: a fast-forward pull only; "Pulling" and off while it runs. */
@Composable
private fun PullButton(behind: Int, state: FilesState, controller: FilesController) {
    val label = if (behind > 0) stringResource(R.string.files_git_pull_behind, behind) else stringResource(R.string.files_git_pull)
    // The visible label is what TalkBack reads; the state says "Pulling" or how far behind, like iOS's accessibilityValue.
    val pullState = if (state.pulling) stringResource(R.string.files_git_pulling)
        else pluralStringResource(R.plurals.files_git_behind_commits, behind, behind)
    JButton(label, controller::pull, Modifier.semantics { stateDescription = pullState }, kind = JButtonKind.Quiet, size = JButtonSize.Small,
        icon = PhIcons.ArrowDown, enabled = !state.moreLoading && state.git?.branch != null,
        loading = state.pulling, loadingText = stringResource(R.string.files_git_pulling))
}

/** The last pull's outcome under the branch card: settled in the positive colour, the hub's reason in the danger colour. */
@Composable
private fun PullResult(message: String, failed: Boolean) {
    val palette = PersonaTheme.palette
    val tint = if (failed) palette.danger.text else palette.tone(Tone.Positive).text
    Row(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.s).semantics { liveRegion = LiveRegionMode.Polite },
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        JIcon(if (failed) PhIcons.WarningCircle else PhIcons.CheckCircle, null, size = Tokens.Space.l, tint = tint)
        Text(message, style = PersonaTheme.type.caption, color = tint)
    }
}

/** A change's letter in its tone: settled when staged, plain otherwise, needs you when it conflicts. */
@Composable
internal fun GitLetter(file: SessionGitFile, stage: Boolean = true) {
    JTag(file.letter, status = if (stage) file.tone else if (file.conflicted) Status.NeedsYou else Status.Unknown)
}

/**
 * A changed file: its name and folder (← the old path of a rename), the letter, where it stands ([stage]: the
 * working tree's changes, not a commit's), ± lines or "Binary".
 */
@Composable
private fun ChangeRow(file: SessionGitFile, stage: Boolean, onClick: () -> Unit) {
    val palette = PersonaTheme.palette
    val where = listOfNotNull(file.originalPath?.let { "← $it" }, file.folder.ifEmpty { null }).joinToString("  ")
    JRow(file.name, subtitle = where.ifEmpty { null }, leadingIcon = fileIcon(file.name), onClick = onClick, subtitleLines = 1, trailing = {
        Column(horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                GitLetter(file, stage)
                if (stage) JTag(stringResource(file.stage), status = file.tone)
            }
            if (file.binary) OneLine(stringResource(R.string.files_git_binary))
            else if (file.added + file.deleted > 0) Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                Text("+${file.added}", style = PersonaTheme.type.micro, color = palette.diffAdd, maxLines = 1)
                Text("−${file.deleted}", style = PersonaTheme.type.micro, color = palette.diffDel, maxLines = 1)
            }
        }
    })
}

/** A commit: the subject (two lines), short hash, author and when, HEAD, its branches and tags. */
@Composable
private fun CommitRow(commit: SessionGitCommit, head: Boolean, remote: String, onClick: () -> Unit) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val now = remember { Instant.now() }
    JRow(onClick = onClick) {
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
            Text(commit.subject.ifEmpty { commit.shortHash }, style = type.body, color = palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                Text(commit.shortHash, style = type.micro, color = palette.accent, maxLines = 1)
                Text(commit.author, style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false))
                fileDate(LocalResources.current, commit.date, now)?.let { Text(it, style = type.caption, color = palette.ink2, maxLines = 1) }
                if (head) Text("HEAD", style = type.micro, color = palette.accent, maxLines = 1)
            }
            val refs = commit.refNames
            if (refs.isNotEmpty()) Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                // The local branch stands out; remote branches and tags are facts beside it.
                refs.forEach { JTag(it, status = if (isLocalBranch(it, remote)) Status.Working else null) }
            }
        }
    }
}

/** One commit: message, author and date, the hash (tap to copy), the files it touched; each opens its part of the diff. */
@Composable
internal fun CommitPage(hash: String, state: FilesState, controller: FilesController, back: () -> Unit) {
    val context = LocalContext.current
    val resources = LocalResources.current
    val feedback = LocalAppFeedback.current
    val show = state.commit
    val commit = show?.commit ?: (state.git?.commits.orEmpty() + state.older).firstOrNull { it.hash == hash } ?: SessionGitCommit(hash)
    var menu by remember { mutableStateOf(false) }
    val copyHash = { copyText(context, commit.hash, feedback, resources.getString(R.string.files_git_hash_copied)) }
    JListPage(commit.shortHash, subtitle = stringResource(R.string.files_git_commit), onBack = back,
        actions = { JIconButton(PhIcons.DotsThree, stringResource(R.string.files_git_commit_options), { menu = true }) }) {
        item("commit") { CommitCard(commit, copyHash) }
        when {
            state.previewError != null -> item { RetryState(stringResource(R.string.files_git_commit_failed), state.previewError, controller::reload) }
            show == null -> item { Loading() }
            else -> {
                item("files") { JSectionHeader(pluralStringResource(R.plurals.files_git_file_count, show.files.size, show.files.size)) }
                if (show.files.isEmpty()) item { Notice(stringResource(R.string.files_git_commit_empty), padded = false) }
                itemsIndexed(show.files, key = { _, it -> "file:${it.path}" }, contentType = { _, _ -> "change" }) { index, file ->
                    JGroupItem(GroupPosition.of(index, show.files.size)) {
                        ChangeRow(file, stage = false) {
                            controller.push(if (comparable(controller, file)) compareCommit(hash, file) else FileSelection.CommitFile(hash, file))
                        }
                    }
                }
            }
        }
    }
    if (menu) JActionSheet(buildList {
        add(JAction(stringResource(R.string.files_git_copy_hash), PhIcons.Hash, onClick = copyHash))
        show?.diff?.takeIf { it.isNotEmpty() }?.let { diff -> add(JAction(stringResource(R.string.files_copy_diff), PhIcons.Copy) { copyText(context, diff, feedback, resources.getString(R.string.files_diff_copied)) }) }
        add(JAction(stringResource(R.string.files_refresh), PhIcons.ArrowClockwise) { controller.reload() })
    }, { menu = false }, stringResource(R.string.files_git_commit))
}

@Composable
private fun CommitCard(commit: SessionGitCommit, copy: () -> Unit) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    JCard {
        Text(commit.subject.ifEmpty { commit.shortHash }, style = type.headline, color = palette.ink)
        commit.body?.trim()?.takeIf { it.isNotEmpty() }?.let { body ->
            Spacer(Modifier.height(Tokens.Space.s))
            SelectionContainer { Text(body, style = type.callout, color = palette.ink2) }
        }
        Spacer(Modifier.height(Tokens.Space.s))
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (commit.author.isNotEmpty()) Text(commit.author, style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f, fill = false))
            fullDate(commit.date, currentLocale)?.let { Text(it, style = type.caption, color = palette.ink2, maxLines = 1) }
        }
        Spacer(Modifier.height(Tokens.Space.xs))
        Row(
            Modifier.clip(PersonaTheme.shapes.controlShape).clickable(role = Role.Button, onClickLabel = stringResource(R.string.files_git_copy_hash), onClick = copy)
                .padding(vertical = Tokens.Space.xs),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            JIcon(PhIcons.Hash, null, size = Tokens.Space.l, tint = palette.accent)
            Spacer(Modifier.width(Tokens.Space.xs))
            Text(commit.hash, style = type.codeSmall, color = palette.accent, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

/** One file of a commit: its part of the commit's diff. */
@Composable
internal fun CommitFilePage(selection: FileSelection.CommitFile, state: FilesState, controller: FilesController, back: () -> Unit) {
    val context = LocalContext.current
    val resources = LocalResources.current
    val feedback = LocalAppFeedback.current
    val file = selection.file
    val show = state.commit
    val part = remember(show, file) { show?.let { GitDiffs.part(it.diff, file) } }
    var menu by remember { mutableStateOf(false) }
    val root = state.git?.root ?: controller.cwd
    JPage(file.name, subtitle = file.folder.ifEmpty { stringResource(R.string.files_git_repo_root) }, onBack = back,
        actions = { JIconButton(PhIcons.DotsThree, stringResource(R.string.files_git_change_options), { menu = true }) }) { padding ->
        val diff = part?.diff
        when {
            state.previewError != null -> LazyColumn(contentPadding = padding) { item { RetryState(stringResource(R.string.files_diff_failed), state.previewError, controller::reload) } }
            show == null -> LazyColumn(contentPadding = padding) { item { Loading() } }
            diff.isNullOrBlank() -> LazyColumn(contentPadding = padding) {
                item {
                    if (show.truncated && diff == null) JEmptyState(stringResource(R.string.files_git_part_cut), stringResource(R.string.files_git_part_cut_message))
                    else JEmptyState(stringResource(R.string.files_diff_no_text), stringResource(R.string.files_diff_no_text_message))
                }
            }
            else -> FileText(diff, diff = true, contentPadding = padding, header = {
                if (show.truncated && part.last) item("truncated") { Notice(stringResource(R.string.files_diff_truncated)) }
            })
        }
    }
    if (menu) JActionSheet(buildList {
        part?.diff?.takeIf { it.isNotEmpty() }?.let { diff -> add(JAction(stringResource(R.string.files_copy_diff), PhIcons.Copy) { copyText(context, diff, feedback, resources.getString(R.string.files_diff_copied)) }) }
        add(JAction(stringResource(R.string.files_copy_path), PhIcons.Copy) { copyText(context, joinRemotePath(root, file.path), feedback, resources.getString(R.string.files_path_copied)) })
    }, { menu = false }, file.name)
}
