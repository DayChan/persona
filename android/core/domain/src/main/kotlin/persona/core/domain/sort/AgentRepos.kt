package persona.core.domain.sort

/** Where a session's cwd sits: the repository it belongs to and the working tree inside it. */
data class RepoPlace(val repo: String, val name: String, val worktree: String, val main: Boolean)

// The layouts a working tree commonly lives in (herdr, Claude Code, a home-level collection), as on web and iOS.
private val WorktreeMarkers = listOf("/.worktrees/", "/.claude/worktrees/", "/.herdr/worktrees/")

private fun baseName(path: String) = path.split('/').lastOrNull { it.isNotEmpty() }.orEmpty()

/**
 * The repository and working tree a cwd belongs to (web's `repoOf`, iOS's `RepoPlace`). The host may name the
 * repository ([repoRoot], the session's `repo_root`, #2840: Paseo keeps its worktrees under its own folder): a cwd
 * inside it is its main checkout, any other one of its trees. Otherwise from the path alone: a tree under one of the
 * known layouts belongs to the directory above the marker, `~/.worktrees/<repo>/<tree>` to `<repo>` under it; any other
 * path is its own repository's main checkout.
 */
fun repoOf(cwd: String, repoRoot: String? = null): RepoPlace {
    val path = cwd.trimEnd('/')
    if (path.isEmpty()) return RepoPlace("", "", "", main = true)
    val root = repoRoot.orEmpty().trimEnd('/')
    if (root.isNotEmpty()) {
        val nestedTree = WorktreeMarkers.firstOrNull { path.startsWith(root + it) }?.let { marker ->
            root + marker + path.removePrefix(root + marker).substringBefore('/')
        }
        if (nestedTree != null) return RepoPlace(root, baseName(root), nestedTree, main = false)
        val inside = path == root || path.startsWith("$root/")
        return RepoPlace(root, baseName(root), if (inside) root else path, main = inside)
    }
    for (marker in WorktreeMarkers) {
        val at = path.indexOf(marker)
        if (at <= 0) continue
        val repo = path.substring(0, at)
        val rest = path.substring(at + marker.length).split('/').filter { it.isNotEmpty() }
        if (rest.size >= 2) return RepoPlace(repo + marker + rest[0], rest[0], path, main = false)
        return RepoPlace(repo, baseName(repo), path, main = false)
    }
    return RepoPlace(path, baseName(path), path, main = true)
}

/**
 * A working tree of a repository on one computer and the sessions in it (in the order they came). [name] is its [branch]
 * when the node says (#1805), else its folder's name (the main checkout: the repository's).
 */
data class AgentWorktree(
    val id: String, val name: String, val path: String, val main: Boolean, val members: List<AgentSession>, val branch: String? = null,
)

/** One repository on one computer, its working trees the main checkout first, then by name. */
data class AgentRepo(val id: String, val machineId: String, val name: String, val path: String, val worktrees: List<AgentWorktree>) {
    val members: List<AgentSession> get() = worktrees.flatMap { it.members }
    /**
     * Only its main checkout and no branch the node named: its sessions sit right under the repository, without a
     * working-tree heading. A named branch keeps its heading (#1805: machine → project → branch → sessions).
     */
    val flat: Boolean get() = worktrees.size == 1 && worktrees[0].main && worktrees[0].branch == null
}

/**
 * What a repository is called when its sessions have no cwd (iOS `RepoTree.unnamed`): sorts after every name; the
 * heading says "Other" (repoTitle).
 */
const val UnnamedRepo = "\uFFFF"

private val Chunks = Regex("\\d+|\\D+")

/**
 * A name's chunks, split once: NaturalOrder is a sort comparator, and splitting both names on every comparison of
 * hundreds of sessions was most of what froze the Agents list. Names are few, so the cache stays small.
 */
private val chunkCache = java.util.concurrent.ConcurrentHashMap<String, List<String>>()
private fun chunks(name: String): List<String> = chunkCache[name] ?: Chunks.findAll(name.lowercase()).map { it.value }.toList().also {
    if (chunkCache.size > 4_096) chunkCache.clear()
    chunkCache[name] = it
}

/** Natural order, like iOS `localizedStandardCompare`: case-insensitive, digit runs by value (repo2 before repo10). */
internal val NaturalOrder = Comparator<String> { a, b ->
    val xs = chunks(a)
    val ys = chunks(b)
    for (i in 0 until minOf(xs.size, ys.size)) {
        val x = xs[i]
        val y = ys[i]
        val c = if (x[0].isDigit() && y[0].isDigit()) {
            val (u, v) = x.trimStart('0') to y.trimStart('0')
            compareValuesBy(u to x, v to y, { it.first.length }, { it.first }, { it.second.length })
        } else x.compareTo(y)
        if (c != 0) return@Comparator c
    }
    xs.size.compareTo(ys.size)
}

private val byName = compareBy<Pair<String, String>, String>(NaturalOrder) { it.first }.thenBy { it.second }

/**
 * Where the node placed a session in git (#1805, web's `gitPlaceOf`, iOS's `RepoPlace.git`): its project (`repo_root`,
 * else the working tree itself) and working tree; null from an older node, which leaves [repoOf]'s inference.
 */
fun gitPlaceOf(repoRoot: String?, worktree: String?): RepoPlace? {
    val tree = worktree.orEmpty().trimEnd('/')
    if (tree.isEmpty()) return null
    val repo = repoRoot.orEmpty().trimEnd('/').ifEmpty { tree }
    return RepoPlace(repo, baseName(repo), tree, main = tree == repo)
}

private val AgentSession.place: RepoPlace
    get() = gitPlaceOf(session.repoRoot, session.worktree) ?: repoOf(session.cwd, session.repoRoot)

/** The repositories of [rows] by name in natural order (then path), each computer's apart; ids are `repo/<machine>/<path>`, `tree/<machine>/<path>`. */
fun agentRepos(rows: List<AgentSession>): List<AgentRepo> =
    rows.groupBy { it.session.machineId to it.place.repo }.map { (key, members) ->
        val (machine, repo) = key
        val place = members.first().place
        val name = place.name.ifEmpty { UnnamedRepo }
        val trees = members.groupBy { it.place.worktree }.map { (path, inTree) ->
            val main = inTree.first().place.main
            // Orca's sidebar and git name a working tree by its branch.
            val branch = inTree.firstNotNullOfOrNull { it.session.branch?.takeIf(String::isNotEmpty) }
            AgentWorktree("tree/$machine/$path", branch ?: if (path == repo) name else baseName(path), path, main, inTree, branch)
        }.sortedWith(compareBy<AgentWorktree> { !it.main }.thenComparing({ it.name to it.path }, byName))
        AgentRepo("repo/$machine/$repo", machine, name, repo, trees)
    }.sortedWith(compareBy<AgentRepo, Pair<String, String>>(byName) { it.name to it.path }.thenBy { it.machineId })
