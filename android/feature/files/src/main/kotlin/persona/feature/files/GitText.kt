package persona.feature.files

import android.content.res.Resources
import androidx.annotation.StringRes
import persona.core.contracts.api.SessionGitCommit
import persona.core.contracts.api.SessionGitFile
import persona.core.contracts.api.SessionGitStatus
import persona.core.designsystem.Status

// What the Git pages read out of the node's answers (git/status, git/log, git/show).

/** The file's own name; the folder it is in ("" at the repository root). */
internal val SessionGitFile.name: String get() = path.substringAfterLast('/')
internal val SessionGitFile.folder: String get() = if ('/' in path) path.substringBeforeLast('/') else ""

/** The tone of a change's letter: conflicts need you, staged changes are settled, the rest are plain. */
internal val SessionGitFile.tone: Status get() = when {
    conflicted -> Status.NeedsYou
    staged -> Status.Unread
    else -> Status.Unknown
}

/** Where a change stands: "Conflict", "Untracked", "Partially staged", "Staged" or "Unstaged". */
@get:StringRes
internal val SessionGitFile.stage: Int get() = when {
    conflicted -> R.string.files_git_conflicted
    untracked -> R.string.files_git_untracked
    staged && unstaged -> R.string.files_git_partly_staged
    staged -> R.string.files_git_staged
    else -> R.string.files_git_unstaged
}

/** Branch and tag names of a commit (`%D`): "HEAD -> main" becomes "main", a bare HEAD is dropped. */
internal val SessionGitCommit.refNames: List<String>
    get() = refs.orEmpty().split(',').map { it.trim().removePrefix("HEAD -> ") }.filter { it.isNotEmpty() && it != "HEAD" }

/**
 * A local branch (not a tag, not a branch of [remote]): the refs chip that stands out. Local branches may have
 * slashes too (feat/x), so the remote's name decides, from the upstream ("origin/main") when there is one.
 */
internal fun isLocalBranch(ref: String, remote: String = "origin") = !ref.startsWith("tag: ") && !ref.startsWith("$remote/")

/** The remote of an upstream branch ("origin/main" is origin). */
internal fun remoteOf(upstream: String?) = upstream?.substringBefore('/')?.takeIf { it.isNotEmpty() && '/' in upstream } ?: "origin"

internal val SessionGitCommit.shortHash: String get() = short.ifEmpty { hash.take(7) }

/** How the branch stands against its upstream: "In sync", deleted on the remote, or ↑ ahead ↓ behind; the stash when there is one. */
internal fun gitSync(resources: Resources, git: SessionGitStatus): String? {
    val sync = when {
        git.upstream == null -> null
        git.gone -> resources.getString(R.string.files_git_upstream_gone)
        git.ahead == 0 && git.behind == 0 -> resources.getString(R.string.files_git_synced)
        else -> listOfNotNull(git.ahead.takeIf { it > 0 }?.let { "↑$it" }, git.behind.takeIf { it > 0 }?.let { "↓$it" }).joinToString("  ")
    }
    return listOfNotNull(sync, git.stashes.takeIf { it > 0 }?.let { resources.getQuantityString(R.plurals.files_git_stashes, it, it) }).joinToString("  ").ifEmpty { null }
}

internal object GitDiffs {
    /**
     * A multi-file diff (`git show`) split per file, keyed by the new path. The path is taken from the end of the
     * `diff --git a/… b/…` line, so spaces in it survive.
     */
    fun split(diff: String): Map<String, String> {
        val parts = LinkedHashMap<String, String>()
        var path: String? = null
        val lines = ArrayList<String>()
        fun flush() { path?.let { parts[it] = lines.joinToString("\n") } }
        for (line in diff.split('\n')) {
            if (line.startsWith("diff --git ")) {
                flush()
                lines.clear()
                val at = line.lastIndexOf(" b/")
                path = if (at >= 0) line.substring(at + 3) else null
            }
            lines += line
        }
        flush()
        return parts
    }

    /** One file's part of a commit's diff; [last] is true when the diff ends inside it (a cut diff is cut there). */
    data class Part(val diff: String?, val last: Boolean)

    fun part(diff: String, file: SessionGitFile): Part {
        val parts = split(diff)
        val key = file.path.takeIf { it in parts } ?: file.originalPath?.takeIf { it in parts }
        return Part(key?.let { parts[it] }, key != null && key == parts.keys.lastOrNull())
    }

    /** Added and deleted lines of the working tree's changes, for the "Changes" heading. */
    fun totals(files: List<SessionGitFile>): Pair<Int, Int> = files.sumOf { it.added } to files.sumOf { it.deleted }
}
