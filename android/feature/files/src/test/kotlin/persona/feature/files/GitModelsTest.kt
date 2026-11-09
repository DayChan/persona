package persona.feature.files

import persona.core.contracts.JsonCodec
import persona.core.contracts.api.*
import persona.core.designsystem.Status
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import java.time.Instant

/** The node's answers as they come (shapes from a real node; times with the machine's offset), read by the Git pages. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class GitModelsTest {
    private val res get() = RuntimeEnvironment.getApplication().resources

    @Test fun decodesStatus() {
        val status = JsonCodec.decodeFromString<SessionGitStatus>(
            """{"is_repo":true,"root":"/r","branch":"main","upstream":"origin/main","ahead":2,"behind":0,"stashes":1,
               "head":{"hash":"abc123","short":"abc","subject":"x","author":"a","date":"2026-06-01T10:00:00+08:00","refs":"HEAD -> main, origin/main, tag: v1"},
               "files":[{"path":"a/b.kt","index":"M","worktree":"M","added":3,"deleted":1},
                        {"path":"n.txt","index":"?","worktree":"?"},
                        {"path":"c.go","orig_path":"old.go","index":"R","worktree":"."},
                        {"path":"m.go","index":"U","worktree":"U"}],
               "commits":[{"hash":"abc123","short":"abc","subject":"x","author":"a","date":"0001-01-01T00:00:00Z"}],"extra":1}""",
        )
        assertTrue(status.isRepo)
        assertEquals(listOf("main", "origin/main", "tag: v1"), status.head!!.refNames)
        assertEquals(Instant.parse("2026-06-01T02:00:00Z"), fileTime(status.head!!.date))
        assertNull(fileTime(status.commits[0].date)) // Go's zero time
        assertNull(fileDate(res, status.commits[0].date))
        val (partly, untracked, renamed, conflict) = status.files
        assertEquals("M", partly.letter)
        assertTrue(partly.staged && partly.unstaged)
        assertEquals("部分已暂存", res.getString(partly.stage))
        assertEquals(Status.Unread, partly.tone)
        assertEquals("b.kt", partly.name)
        assertEquals("a", partly.folder)
        assertEquals("U", untracked.letter)
        assertFalse(untracked.staged)
        assertEquals("未跟踪", res.getString(untracked.stage))
        assertEquals(Status.Unknown, untracked.tone)
        assertEquals("R", renamed.letter)
        assertTrue(renamed.staged && !renamed.unstaged)
        assertEquals("old.go", renamed.originalPath)
        assertEquals("", renamed.folder)
        assertEquals("已暂存", res.getString(renamed.stage))
        assertEquals("!", conflict.letter)
        assertEquals("有冲突", res.getString(conflict.stage))
        assertEquals(Status.NeedsYou, conflict.tone)
        assertEquals("↑2  贮藏 1", gitSync(res, status))
    }

    @Test fun nodeAnswersDecode() {
        // As a node answers for a throwaway repository: the machine's offset on every time, nanoseconds on files.
        val status = JsonCodec.decodeFromString<SessionGitStatus>(
            """{"is_repo":true,"root":"/tmp/t","branch":"master","head":{"hash":"321e4a2395fe01a647752b26d29581827f01a1bf","short":"321e4a2","subject":"init","author":"t","date":"2026-10-31T13:58:40+08:00","refs":"HEAD -> master"},"ahead":0,"behind":0,"files":[{"path":"src/a.txt","index":".","worktree":"M","added":2,"deleted":1},{"path":"n.txt","index":"?","worktree":"?","added":0,"deleted":0}],"stashes":0,"commits":[]}""",
        )
        assertEquals(Instant.parse("2026-10-31T05:58:40Z"), fileTime(status.head?.date))
        assertEquals(listOf("master"), status.head?.refNames)
        assertEquals(listOf("M", "U"), status.files.map { it.letter })
        assertEquals("未暂存", res.getString(status.files[0].stage))
        assertNull(gitSync(res, status)) // no upstream, no stash: nothing to say
        val listing = JsonCodec.decodeFromString<SessionDirectory>(
            """{"entries":[{"name":"src","dir":true,"size":4096,"mtime":"2026-10-31T13:58:40.205893083+08:00"}],"path":"/tmp/t","truncated":false}""",
        )
        assertEquals(Instant.parse("2026-10-31T05:58:40.205893083Z"), fileTime(listing.entries.single().mtime))
        val synced = status.copy(upstream = "origin/master")
        assertEquals("已同步", gitSync(res, synced))
        assertEquals("↑1  ↓3", gitSync(res, synced.copy(ahead = 1, behind = 3)))
        // a deleted upstream (#1884) is not "up to date"
        val gone = JsonCodec.decodeFromString<SessionGitStatus>(
            """{"is_repo":true,"branch":"feature","upstream":"origin/feature","gone":true,"ahead":0,"behind":0,"files":[],"stashes":0,"commits":[]}""",
        )
        assertEquals("远端已删除", gitSync(res, gone))
        assertFalse(status.gone)
    }

    @Test fun refNamesDropABareHead() {
        assertEquals(emptyList<String>(), SessionGitCommit("h", refs = "HEAD").refNames)
        assertEquals(emptyList<String>(), SessionGitCommit("h").refNames)
        assertEquals(listOf("feat/x", "upstream/feat/x"), SessionGitCommit("h", refs = "HEAD -> feat/x, upstream/feat/x").refNames)
    }

    @Test fun theUpstreamsRemoteTellsLocalBranchesFromRemoteOnes() {
        assertTrue(isLocalBranch("main"))
        assertTrue(isLocalBranch("feat/x"))
        assertFalse(isLocalBranch("origin/main"))
        assertFalse(isLocalBranch("tag: v1"))
        assertEquals("upstream", remoteOf("upstream/feat/x"))
        assertEquals("origin", remoteOf(null))
        assertTrue(isLocalBranch("origin/x", remote = "upstream"))
        assertFalse(isLocalBranch("upstream/feat/x", remote = remoteOf("upstream/feat/x")))
        assertEquals("abc1234", SessionGitCommit("abc1234def", short = "").shortHash)
    }

    @Test fun splitsAShowPerFileKeepingSpaces() {
        val show = "diff --git a/a.txt b/a.txt\n@@ -1 +1 @@\n-x\n+y\ndiff --git a/my dir/b c.txt b/my dir/b c.txt\n@@ -0,0 +1 @@\n+z"
        val parts = GitDiffs.split(show)
        assertEquals(setOf("a.txt", "my dir/b c.txt"), parts.keys)
        assertTrue(parts.getValue("my dir/b c.txt").endsWith("+z"))
        assertEquals(listOf(FileLineKind.Info, FileLineKind.Delete, FileLineKind.Add),
            fileLines(parts.getValue("a.txt"), diff = true).map { it.kind })
        val last = GitDiffs.part(show, SessionGitFile("my dir/b c.txt"))
        assertTrue(last.last)
        assertTrue(last.diff!!.endsWith("+z"))
        assertFalse(GitDiffs.part(show, SessionGitFile("a.txt")).last)
        // A cut diff (the node stops at 512 KB) may not reach a file at all.
        assertNull(GitDiffs.part(show, SessionGitFile("later.txt")).diff)
    }

    @Test fun aFilesDiffShowsItsHunksNotItsHeader() {
        val diff = "diff --git a/q.sql b/q.sql\nindex 1..2 100644\n--- a/q.sql\n+++ b/q.sql\n@@ -10,3 +10,3 @@ select\n keep\n--- old comment\n+-- new comment\n\\ No newline at end of file"
        val lines = fileLines(diff, diff = true)
        assertEquals(listOf(FileLineKind.Info, FileLineKind.Context, FileLineKind.Delete, FileLineKind.Add, FileLineKind.Info), lines.map { it.kind })
        // Inside a hunk "--- x" is a removed "-- x", not a header.
        assertEquals(FileTextLine("-- old comment", old = 11, kind = FileLineKind.Delete), lines[2])
        assertEquals(FileTextLine("-- new comment", new = 11, kind = FileLineKind.Add), lines[3])
        val created = fileLines("diff --git a/n.txt b/n.txt\nnew file mode 100644\n--- /dev/null\n+++ b/n.txt\n@@ -0,0 +1 @@\n+hi", diff = true)
        assertEquals(listOf("new file mode 100644", "@@ -0,0 +1 @@", "hi"), created.map { it.text })
    }

    @Test fun aRenamedFileIsFoundUnderEitherName() {
        val show = "diff --git a/old.go b/old.go\ndeleted file mode 100644\n--- a/old.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-x"
        assertNotNull(GitDiffs.part(show, SessionGitFile("c.go", originalPath = "old.go", index = "R")).diff)
    }

    @Test fun totalsAddUpTheChanges() {
        assertEquals(5 to 1, GitDiffs.totals(listOf(SessionGitFile("a", added = 3, deleted = 1), SessionGitFile("b", added = 2))))
    }

    @Test fun readmeHtmlBecomesMarkdown() {
        val md = "<p align=\"center\"><img src=\"logo.png\" alt=\"Logo\"></p>\n<a href=\"https://example.com\">Site</a>\n```\n<keep>\n```\ntext"
        assertEquals("![Logo](logo.png)\n[Site](https://example.com)\n```\n<keep>\n```\ntext", FileMarkdownText.simplifyHtml(md))
        assertEquals("plain", FileMarkdownText.simplifyHtml("plain"))
        assertEquals("a b", FileMarkdownText.simplifyHtml("<p>a<br/>b</p>"))
        assertEquals("Tom & Jerry", FileMarkdownText.simplifyHtml("<b>Tom &amp; Jerry</b>"))
        // A line that is only a dropped tag disappears instead of leaving an empty paragraph mark.
        assertEquals("before\nafter", FileMarkdownText.simplifyHtml("before\n<div>\nafter"))
    }

    @Test fun bareLinksEndAtTheFirstNonAsciiCharacter() {
        assertEquals("详见 <https://example.com/a>。后面", FileMarkdownText.links("详见 https://example.com/a。后面"))
        assertEquals("(see <https://example.com/x_(y)>).", FileMarkdownText.links("(see https://example.com/x_(y))."))
        // Links already written as Markdown, autolinks and code are left alone.
        assertEquals("[a](https://example.com) <https://example.com> `https://example.com`",
            FileMarkdownText.links("[a](https://example.com) <https://example.com> `https://example.com`"))
        assertEquals("[api]: <https://example.com/api>", FileMarkdownText.links("[api]: https://example.com/api"))
    }

    @Test fun linksAndImagesInAMarkdownFileNameFilesNextToIt() {
        val base = "/Users/me/code/weather"
        assertEquals("/Users/me/code/weather/docs/icon.png", FileMarkdownText.target("docs/icon.png", base))
        assertEquals("/Users/me/code/weather/docs/guide.md", FileMarkdownText.target("./docs/guide.md#install", base))
        assertEquals("/Users/me/code/LICENSE", FileMarkdownText.target("../LICENSE", base))
        assertEquals("/Users/me/code/weather/my file.md", FileMarkdownText.target("my%20file.md", base))
        assertEquals("/etc/hosts", FileMarkdownText.target("/etc/hosts", base))
        assertEquals("~/notes.txt", FileMarkdownText.target("~/notes.txt", base))
        assertNull(FileMarkdownText.target("https://example.com/a.png", base))
        assertNull(FileMarkdownText.target("mailto:me@example.com", base))
        assertNull(FileMarkdownText.target("#usage", base))
        assertNull(FileMarkdownText.target("//cdn.example.com/x.png", base))
    }
}
