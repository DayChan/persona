package persona.feature.files

import persona.core.contracts.api.*
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

enum class FilesPane { Files, Git }

/** The file on disk, in a [FileSelection.Compare]. */
const val WORKTREE = "WORKTREE"

/** A page shown over the browser. Pages stack: a commit's file opens over the commit, a linked file over its README. */
sealed interface FileSelection {
    /** [line] (1-based): opened from a message's `path:12`, the text scrolls to that line and marks it. */
    data class File(val path: String, val line: Int? = null) : FileSelection
    data class Diff(val file: SessionGitFile, val staged: Boolean) : FileSelection
    data class Commit(val hash: String) : FileSelection
    /** One file of a commit: its part of the commit's diff (the commit is read once and kept). */
    data class CommitFile(val hash: String, val file: SessionGitFile) : FileSelection
    /**
     * A document's or picture's two versions side by side (#2805): [path] and [beforePath] (a rename's old name)
     * relative to the repository's root; [before] / [after] are `HEAD`, `INDEX`, a commit, `hash^` or `WORKTREE`
     * (the file on disk). [deleted]: gone from the working tree, so there is no new version to read.
     */
    data class Compare(val path: String, val beforePath: String, val before: String, val after: String, val deleted: Boolean = false,
        /** The working-tree change it shows a half of; null for a commit's file. */
        val change: SessionGitFile? = null) : FileSelection {
        /** Staged and changed again: the page switches between Unstaged and Staged, as the text diff does. */
        val bothHalves: Boolean get() = change?.let { it.staged && it.unstaged } == true
        val staged: Boolean get() = before == "HEAD" && after == "INDEX"
    }
}

/** One version of a compared file: its revision and where it is on the machine; no [path]: not in that version. */
data class CompareSide(val rev: String, val path: String?)

data class CompareSides(val before: CompareSide, val after: CompareSide)

data class FilesState(
    /** The top of the breadcrumb: the session's folder, or above it once the user went up. Absolute once listed. */
    val root: String = "",
    /** The folder the browser shows. */
    val here: String = "",
    val directory: SessionDirectory? = null,
    val directoryLoading: Boolean = false,
    val directoryError: String? = null,
    val git: SessionGitStatus? = null,
    val gitLoading: Boolean = false,
    val gitError: Boolean = false,
    val older: List<SessionGitCommit> = emptyList(),
    val moreLoading: Boolean = false,
    val moreError: Boolean = false,
    val noMore: Boolean = false,
    /** A fast-forward pull is running ("Pulling"): the button is off and status / history loads wait for it. */
    val pulling: Boolean = false,
    /** The last pull's line under the branch: brought new commits / already up to date, or the hub's reason. */
    val pullMessage: String? = null,
    val pullFailed: Boolean = false,
    val stack: List<FileSelection> = emptyList(),
    /** The pages were opened straight from the conversation (a path in a message): back returns there. */
    val direct: Boolean = false,
    val content: SessionFileContent? = null,
    val diff: SessionGitDiff? = null,
    val commit: SessionGitShow? = null,
    /** The two versions of the file a [FileSelection.Compare] shows, once both are found. */
    val compare: CompareSides? = null,
    val previewError: String? = null,
) {
    val selection: FileSelection? get() = stack.lastOrNull()
    /** The browser is in a folder below its top: back goes up a level before it leaves. */
    val belowRoot: Boolean get() = here.isNotEmpty() && here.trimEnd('/') != root.trimEnd('/')
    val crumbs: List<Pair<String, String>> get() = FilesPaths.crumbs(root, here)
    /** The folder above the browser's, if any: the ⋯ menu's "Parent folder", which may go above the session's folder. */
    val parent: String? get() = FilesPaths.parent(here)
}

/** Owned by the session pane; leaving it cancels its coroutine scope. */
class FilesController(
    private val api: SessionFiles,
    private val machineId: String,
    val cwd: String,
    private val scope: CoroutineScope,
    /** Why a load failed, in words for the page (the hub's message when the machine said why). */
    private val describe: (Throwable) -> String = { it.message.orEmpty() },
    /** A pull's success line: "Pulled the latest code" when it brought commits, "Already up to date" otherwise. */
    private val pulled: (updated: Boolean) -> String = { if (it) "updated" else "up to date" },
    /** The hub connection: coming back online reads again what failed while it was away. */
    private val connection: StateFlow<ConnectionState>? = null,
) {
    private val mutable = MutableStateFlow(FilesState(root = cwd, here = cwd))
    val state = mutable.asStateFlow()
    private var directoryJob: Job? = null
    private var gitJob: Job? = null
    private var previewJob: Job? = null
    private var moreJob: Job? = null
    private var watcher: Job? = null
    private var directoryGeneration = 0
    private var gitGeneration = 0
    private var previewGeneration = 0
    /** Folders already listed: going back up shows them at once while they refresh. */
    private val listings = LinkedHashMap<String, SessionDirectory>()
    /** Files shown lately: back to a page under the one on top (a README under the file it links) shows it at once. */
    private val contents = object : LinkedHashMap<String, SessionFileContent>(4, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, SessionFileContent>?) = size > 4
    }
    /** `git show` by hash: a commit's files open their part of the diff without asking again. */
    private val shows = object : LinkedHashMap<String, SessionGitShow>(8, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, SessionGitShow>?) = size > 8
    }

    /**
     * The first load. With [initialPath] (a path from the conversation: absolute, "~…", or relative to the session's
     * folder) a file opens straight away and back returns to the conversation; a folder (a trailing slash, or the hub
     * saying it is one) opens in the browser as its top. [initialLine] opens a file at that line (iOS `goTo(line)`).
     */
    fun start(initialPath: String? = null, initialLine: Int? = null) {
        if (watcher == null) connection?.let { states ->
            // Best effort: a StateFlow can fold a quick drop and return into one Online, and nothing is retried then.
            watcher = scope.launch {
                var was = states.value
                states.collect { now ->
                    if (now == ConnectionState.Online && was != ConnectionState.Online) recover()
                    was = now
                }
            }
        }
        refreshGit()
        val asked = initialPath?.trim()?.takeIf { it.isNotEmpty() }
        when {
            asked == null -> directory()
            FilesPaths.namesDirectory(asked) -> browse(FilesPaths.resolve(asked, cwd), top = true)
            else -> {
                mutable.update { it.copy(direct = true) }
                preview(FileSelection.File(FilesPaths.resolve(asked, cwd), initialLine?.takeIf { it > 0 }))
            }
        }
    }

    /** Lists [path] (the folder shown by default); keeps showing what was there before while it reloads. */
    fun directory(path: String = state.value.here) {
        directoryJob?.cancel()
        val generation = ++directoryGeneration
        mutable.update {
            it.copy(here = path, directory = listings[path], directoryError = null, directoryLoading = true)
        }
        directoryJob = scope.launch {
            try {
                val result = api.directory(machineId, cwd, path)
                if (generation != directoryGeneration) return@launch
                listings[path] = result
                if (result.path.isNotEmpty()) listings[result.path] = result
                mutable.update {
                    // The hub answers with the folder it read ("~" expanded, "a/../b" cleaned): the breadcrumb follows.
                    val resolved = result.path.ifEmpty { path }
                    it.copy(directory = result, directoryLoading = false, here = resolved,
                        root = if (it.root == path) resolved else it.root)
                }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                if (generation == directoryGeneration) mutable.update { it.copy(directoryLoading = false, directoryError = describe(error)) }
            }
        }
    }

    /**
     * Back online: reads again whatever failed or is still on its way (the folder, Git's status or its older history,
     * the page on top), so a dropped network does not leave the page on its error until the user taps "Retry". A read
     * started while offline may still fail after this, so it is started again too. A pull is not run again.
     */
    private fun recover() {
        val now = state.value
        if (now.directoryError != null || now.directoryLoading) directory()
        if (now.gitError || now.gitLoading) refreshGit()
        else if (now.moreError || now.moreLoading) { moreJob?.cancel(); mutable.update { it.copy(moreLoading = false) }; more() }
        if (now.stack.isNotEmpty() && now.content == null && now.diff == null && now.commit == null) reload()
    }

    /** Into a folder: below the top it extends the breadcrumb; anywhere else it becomes the top. */
    fun open(path: String) = browse(path, top = !FilesPaths.isWithin(path, state.value.root))

    private fun browse(path: String, top: Boolean) {
        mutable.update {
            it.copy(root = if (top) path else it.root, stack = emptyList(), direct = false,
                content = null, diff = null, commit = null, compare = null, previewError = null)
        }
        previewJob?.cancel()
        directory(path)
    }

    /** One level up: the breadcrumb's top moves up too when the browser is already there (as far as the root). */
    fun up() {
        val now = state.value
        val parent = now.parent ?: return
        if (now.belowRoot) directory(parent) else browse(parent, top = true)
    }

    fun refreshGit() {
        if (state.value.pulling) return
        gitJob?.cancel()
        moreJob?.cancel()
        val generation = ++gitGeneration
        mutable.update { it.copy(gitLoading = true, gitError = false, moreLoading = false, moreError = false) }
        gitJob = scope.launch {
            try {
                val result = api.status(machineId, cwd)
                if (generation == gitGeneration) mutable.update {
                    it.copy(git = result, gitLoading = false, older = emptyList(), noMore = result.commits.size < 30)
                }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) {
                if (generation == gitGeneration) mutable.update { it.copy(gitLoading = false, gitError = true) }
            }
        }
    }

    fun more() {
        val snapshot = state.value
        if (snapshot.moreLoading || snapshot.noMore || snapshot.gitLoading || snapshot.pulling || snapshot.git == null) return
        val generation = gitGeneration
        mutable.update { it.copy(moreLoading = true, moreError = false) }
        moreJob = scope.launch {
            try {
                val result = api.log(machineId, cwd, snapshot.git.commits.size + snapshot.older.size)
                if (generation == gitGeneration) mutable.update {
                    val known = snapshot.git.commits.map { commit -> commit.hash }.toSet()
                    it.copy(older = (it.older + result.commits).distinctBy { commit -> commit.hash }.filterNot { commit -> commit.hash in known },
                        moreLoading = false, noMore = result.commits.size < 50)
                }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) {
                if (generation == gitGeneration) mutable.update { it.copy(moreLoading = false, moreError = true) }
            }
        }
    }

    /**
     * `git pull --ff-only` on the machine (iOS GitPullModel): the returned status replaces the shown one and the history
     * starts over; on a refusal (diverged, local changes, no upstream) the hub's reason shows and the status is read again.
     */
    fun pull() {
        val snapshot = state.value
        if (snapshot.pulling || snapshot.git?.upstream == null || snapshot.git.gone || snapshot.git.branch == null) return
        // A status or log read in flight would land after the pull and show the branch as it was.
        gitJob?.cancel()
        moreJob?.cancel()
        val generation = ++gitGeneration
        mutable.update { it.copy(pulling = true, pullMessage = null, pullFailed = false, gitLoading = false, moreLoading = false) }
        gitJob = scope.launch {
            try {
                val result = api.pull(machineId, cwd)
                if (generation == gitGeneration) mutable.update {
                    it.copy(pulling = false, pullMessage = pulled(result.updated), git = result.status, gitError = false,
                        older = emptyList(), moreError = false, noMore = result.status.commits.size < 30)
                }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                if (generation != gitGeneration) return@launch
                mutable.update { it.copy(pulling = false, pullMessage = describe(error), pullFailed = true) }
                refreshGit()
            }
        }
    }

    /** Shows [selection] as the only page over the browser; null closes the pages. */
    fun preview(selection: FileSelection?) = show(listOfNotNull(selection))

    /** Opens [selection] over the page shown now. */
    fun push(selection: FileSelection) = show(state.value.stack + selection)

    /** Reloads the page shown now. */
    fun reload() = show(state.value.stack, fresh = true)

    /** A file's whole bytes into [into] (audio plays from a local copy); see [SessionFiles.download]. */
    suspend fun download(path: String, into: java.io.File): Long = api.download(machineId, cwd, path, into)

    /**
     * A file's thumbnail (`file/thumb`): the cache key (machine, path and the listing's size and time, so a changed file
     * is asked again) and the loader at a pixel size.
     */
    fun thumbnail(path: String, size: Long, modified: String?): Pair<String, suspend (Int) -> ByteArray?> {
        val key = listOf(machineId, path, size.toString(), modified.orEmpty()).joinToString("\n")
        return key to { pixels -> api.thumbnail(machineId, cwd, path, pixels) }
    }

    /** The PDF the machine's LibreOffice makes of an Office document (`file/convert`), fetched whole into [into]. */
    suspend fun convertToPdf(path: String, into: java.io.File): Long {
        val converted = api.convertToPdf(machineId, cwd, path)
        return api.download(machineId, cwd, converted.path, into)
    }

    /**
     * Back: closes the page on top (to the conversation when it was opened from there), then goes up to the
     * breadcrumb's top. False when there is nothing left to go back to here: the caller returns to the conversation.
     */
    fun back(pane: FilesPane = FilesPane.Files): Boolean {
        val now = state.value
        return when {
            now.stack.size > 1 -> { show(now.stack.dropLast(1), fresh = false); true }
            now.stack.size == 1 -> if (now.direct) false else { show(emptyList()); true }
            pane == FilesPane.Files && now.belowRoot -> { now.parent?.let(::directory); true }
            else -> false
        }
    }

    /** Shows the top of [stack]; a file seen lately shows as it was unless [fresh]. */
    private fun show(stack: List<FileSelection>, fresh: Boolean = true) {
        previewJob?.cancel()
        val generation = ++previewGeneration
        val top = stack.lastOrNull()
        val kept = (top as? FileSelection.File)?.takeIf { !fresh }?.let { contents[it.path] }
        mutable.update { it.copy(stack = stack, content = kept, diff = null, commit = null, compare = null, previewError = null) }
        if (top == null || kept != null) return
        val gitRoot = state.value.git?.root ?: cwd
        previewJob = scope.launch {
            try {
                when (top) {
                    is FileSelection.File -> {
                        val file = api.filePreview(machineId, cwd, top.path)
                        if (generation != previewGeneration) return@launch
                        // A folder opens in the browser: opened from the conversation, as the breadcrumb's top (back
                        // returns to the conversation from there); from a page here, where the browser can reach it.
                        val folder = file.path.ifEmpty { top.path }
                        when {
                            !file.isDir -> { contents[top.path] = file; mutable.update { it.copy(content = file) } }
                            state.value.direct -> browse(folder, top = true)
                            else -> open(folder)
                        }
                    }
                    is FileSelection.Diff -> {
                        val diff = api.diff(machineId, gitRoot, top.file.path, top.staged)
                        if (generation == previewGeneration) mutable.update { it.copy(diff = diff) }
                    }
                    is FileSelection.Commit -> {
                        val show = commit(gitRoot, top.hash)
                        if (generation == previewGeneration) mutable.update { it.copy(commit = show) }
                    }
                    is FileSelection.CommitFile -> {
                        val show = commit(gitRoot, top.hash)
                        if (generation == previewGeneration) mutable.update { it.copy(commit = show) }
                    }
                    is FileSelection.Compare -> {
                        val before = side(gitRoot, top.beforePath, top.before, gone = false)
                        val after = side(gitRoot, top.path, top.after, gone = top.deleted)
                        if (generation == previewGeneration) mutable.update { it.copy(compare = CompareSides(before, after)) }
                    }
                }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                if (generation == previewGeneration) mutable.update { it.copy(previewError = describe(error)) }
            }
        }
    }

    /** Whether this machine's hub hands out old versions: the Git page compares documents and pictures then. */
    val comparesVersions: Boolean get() = api.comparesVersions(machineId)

    /** A version on the machine: the tree's file as it is, or git's copy (`git/blob`) in the node's cache. */
    private suspend fun side(root: String, path: String, rev: String, gone: Boolean): CompareSide = when {
        gone -> CompareSide(rev, null)
        rev == WORKTREE -> CompareSide(rev, joinRemotePath(root, path))
        else -> api.blob(machineId, root, path, rev).let { CompareSide(rev, if (it.missing) null else it.path) }
    }

    private suspend fun commit(root: String, hash: String): SessionGitShow =
        synchronized(shows) { shows[hash] } ?: api.show(machineId, root, hash).also { synchronized(shows) { shows[hash] = it } }
}

fun gitMark(status: SessionGitStatus?, path: String, directory: Boolean): String? = gitChange(status, path, directory)?.letter

/** The change of a file, or for a folder the first change inside it; null when unchanged or outside the repository. */
internal fun gitChange(status: SessionGitStatus?, path: String, directory: Boolean): SessionGitFile? {
    val root = status?.root?.trimEnd('/') ?: return null
    if (!path.startsWith("$root/")) return null
    val relative = path.removePrefix("$root/").trimEnd('/')
    if (!directory) return status.files.firstOrNull { it.path == relative }
    // A folder shows its first change, or a conflict inside it when there is one.
    val inside = status.files.filter { it.path.startsWith("$relative/") }
    return inside.firstOrNull { it.conflicted } ?: inside.firstOrNull()
}
