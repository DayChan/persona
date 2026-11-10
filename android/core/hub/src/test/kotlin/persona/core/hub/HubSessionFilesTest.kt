package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.Protocol
import okhttp3.Request
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import okhttp3.HttpUrl.Companion.toHttpUrl
import org.junit.Assert.*
import org.junit.Test

class HubSessionFilesTest {
    @Test fun fileAndGitRequestsEncodePathsAndAcceptEmptyAndPartialResponses() = runBlocking {
        val requests = mutableListOf<String>()
        var body = """{"path":"/workspace/a b#%.kt","name":"a b#%.kt","text":"val a = 1","future":true}"""
        var status = 200
        val api = HubSessionFiles({ path ->
            requests += path
            Response.Builder().request(Request.Builder().url("https://example.com$path").build())
                .protocol(Protocol.HTTP_1_1).code(status).message("response").body(body.toResponseBody()).build()
        })
        assertEquals("val a = 1", (api.file("machine/segment", "/workspace/中文 &", "/workspace/a b#%.kt").text).zhAny())
        val url = ("https://example.com" + requests.last()).toHttpUrl()
        assertEquals("/api/v1/machines/machine%2Fsegment/file", (url.encodedPath).zhAny())
        assertEquals("/workspace/中文 &", (url.queryParameter("cwd")).zhAny())
        assertEquals("/workspace/a b#%.kt", (url.queryParameter("path")).zhAny())

        body = """{"is_repo":true,"files":null,"commits":null}"""
        assertTrue(api.status("machine", "/workspace").files.isEmpty())
        body = """{"path":"/workspace","entries":null,"truncated":true}"""
        assertTrue(api.directory("machine", "/workspace", "/workspace").truncated)
        body = """{"path":"a.kt","diff":"+new","truncated":true}"""
        assertTrue(api.diff("machine", "/workspace", "a.kt", true).truncated)
        assertEquals("1", (("https://example.com" + requests.last()).toHttpUrl().queryParameter("staged")).zhAny())
        body = """{"commits":null}"""
        assertTrue(api.log("machine", "/workspace", 30).commits.isEmpty())
        assertEquals("30", (("https://example.com" + requests.last()).toHttpUrl().queryParameter("skip")).zhAny())
        body = """{"commit":{"hash":"abc"},"files":null,"diff":""}"""
        assertEquals("abc", (api.show("machine", "/workspace", "abc").commit.hash).zhAny())
        body = """{"stats":[{"path":"src/a.kt","kind":"file","size":12},{"path":"~/x y/","kind":"dir"},{"path":"/gone/a.md"}]}"""
        val stats = api.stat("machine", "/workspace", listOf("src/a.kt", "~/x y/", "/gone/a.md")).stats
        assertEquals(listOf("file", "dir", "missing"), (stats.map { it.kind }).zhAny())
        assertEquals(12L, stats.first().size)
        val statUrl = ("https://example.com" + requests.last()).toHttpUrl()
        assertEquals("/api/v1/machines/machine/stat", (statUrl.encodedPath).zhAny())
        assertEquals("src/a.kt\n~/x y/\n/gone/a.md", (statUrl.queryParameter("paths")).zhAny())
        assertEquals("/workspace", (statUrl.queryParameter("cwd")).zhAny())
        status = 404
        body = """{"error":"not found","code":"not_found"}"""
        try { api.file("machine", "/workspace", "missing"); fail("Expected HTTP failure") }
        catch (failure: HubFailure) { assertEquals(404, failure.status) }
    }

    @Test fun aVersionOfAFileComesFromGitBlobWhenTheHubHasIt() = runBlocking {
        val requests = mutableListOf<String>()
        var body = """{"path":"/cache/blobs/ab12.png","size":10}"""
        var features = emptySet<String>()
        val respond: suspend (String) -> Response = { path ->
            requests += path
            Response.Builder().request(Request.Builder().url("https://example.com$path").build())
                .protocol(Protocol.HTTP_1_1).code(200).message("response").body(body.toResponseBody()).build()
        }
        val api = HubSessionFiles(respond, features = { features })
        assertFalse(api.comparesVersions("machine"))
        features = setOf("git_blob")
        assertTrue(api.comparesVersions("machine"))
        assertEquals("/cache/blobs/ab12.png", api.blob("machine", "/workspace", "art/a b.png", "abc^").path)
        val url = ("https://example.com" + requests.last()).toHttpUrl()
        assertEquals("/api/v1/machines/machine/git/blob", url.encodedPath)
        assertEquals("art/a b.png", url.queryParameter("path"))
        assertEquals("abc^", url.queryParameter("rev"))
        body = """{"missing":true}"""
        assertTrue(api.blob("machine", "/workspace", "a.png", "HEAD").missing)
    }

    @Test fun thumbnailsReturnThePictureAndNullWhenTheMachineCannotDrawIt(): Unit = runBlocking {
        val requests = mutableListOf<Pair<String, Long>>()
        var status = 200
        var type = "image/jpeg"
        var errorCode = "no_thumbnail"
        val respond: suspend (String, Long) -> Response = { path, seconds ->
            requests += path to seconds
            val response = Response.Builder().request(Request.Builder().url("https://example.com$path").build()).protocol(Protocol.HTTP_1_1)
                .code(status).message("response").header("Content-Type", type)
                .body(if (status == 200) byteArrayOf(1, 2, 3).toResponseBody() else """{"error":"x","code":"$errorCode"}""".toResponseBody()).build()
            // As OkHttpHubConnection.request: any status but 2xx arrives as a HubFailure, not as a response.
            if (status !in 200..299) response.use { throw HubFailure.from(it) }
            response
        }
        val api = HubSessionFiles({ error("thumbnails wait longer than the shared read timeout") }, slow = respond)
        assertArrayEquals(byteArrayOf(1, 2, 3), api.thumbnail("m", "/w", "docs/a b.pdf", 2000))
        val url = ("https://example.com" + requests.last().first).toHttpUrl()
        assertEquals("/api/v1/machines/m/file/thumb", url.encodedPath)
        assertEquals("docs/a b.pdf", url.queryParameter("path"))
        assertEquals("1024", url.queryParameter("size"))
        assertEquals(HubSessionFiles.THUMB_TIMEOUT_SECONDS, requests.last().second)
        status = 404
        assertNull(api.thumbnail("m", "/w", "a.docx", 128))
        status = 200; type = "text/html"
        try { api.thumbnail("m", "/w", "a.docx", 128); fail("Expected a protocol failure") }
        catch (failure: HubFailure) { assertEquals(HubError.Protocol, failure.kind) }
        status = 409; type = "application/json"; errorCode = "node_outdated"
        try { api.thumbnail("m", "/w", "a.docx", 128); fail("Expected HTTP failure") }
        catch (failure: HubFailure) { assertEquals(409, failure.status) }
        // A timeout is not the machine saying it has no picture: it stays an error.
        val timedOut = HubSessionFiles({ error("unused") }, slow = { _, _ -> throw HubFailure(HubError.Timeout) })
        try { timedOut.thumbnail("m", "/w", "a.docx", 128); fail("Expected a timeout") }
        catch (failure: HubFailure) { assertEquals(HubError.Timeout, failure.kind) }
    }

    @Test fun convertAsksForAPdfAndWaitsForLibreOffice(): Unit = runBlocking {
        val requests = mutableListOf<Pair<String, Long>>()
        val api = HubSessionFiles({ error("conversions wait longer than the shared read timeout") }, slow = { path, seconds ->
            requests += path to seconds
            Response.Builder().request(Request.Builder().url("https://example.com$path").build()).protocol(Protocol.HTTP_1_1)
                .code(200).message("response").body("""{"path":"/cache/a.pdf","size":42,"mime":"application/pdf"}""".toResponseBody()).build()
        })
        val got = api.convertToPdf("m", "/w", "a.docx")
        assertEquals("/cache/a.pdf", got.path)
        assertEquals(42L, got.size)
        val url = ("https://example.com" + requests.last().first).toHttpUrl()
        assertEquals("/api/v1/machines/m/file/convert", url.encodedPath)
        assertEquals("pdf", url.queryParameter("to"))
        assertEquals("a.docx", url.queryParameter("path"))
        assertEquals(HubSessionFiles.CONVERT_TIMEOUT_SECONDS, requests.last().second)
        assertTrue(HubSessionFiles.CONVERT_TIMEOUT_SECONDS > 120)
    }

    @Test fun previewAsksForAStreamAndReadsAnOlderHubsBase64(): Unit = runBlocking {
        val requests = mutableListOf<String>()
        var body = """{"path":"/w/clip.mp4","name":"clip.mp4","size":9000000,"mime":"video/mp4","stream":true,"revision":"r1"}"""
        val api = HubSessionFiles({ path ->
            requests += path
            Response.Builder().request(Request.Builder().url("https://example.com$path").build()).protocol(Protocol.HTTP_1_1)
                .code(200).message("response").body(body.toResponseBody()).build()
        })
        val streamed = api.filePreview("m", "/w", "clip.mp4")
        assertTrue(streamed.stream)
        assertNull(streamed.data)
        assertEquals("1", ("https://example.com" + requests.last()).toHttpUrl().queryParameter("stream"))
        // An older hub ignores the flag: the base64 comes as before.
        body = """{"path":"/w/a.png","name":"a.png","size":3,"mime":"image/png","data":"AQID"}"""
        val old = api.filePreview("m", "/w", "a.png")
        assertFalse(old.stream)
        assertEquals("AQID", old.data)
        // Plain file() (pictures in Markdown, the chat's photos) still asks for the bytes.
        api.file("m", "/w", "a.png")
        assertNull(("https://example.com" + requests.last()).toHttpUrl().queryParameter("stream"))
    }

    @Test fun pullPostsTheFolderAndDecodesTheNewStatus(): Unit = runBlocking {
        val posts = mutableListOf<Pair<String, String>>()
        var status = 200
        var answer = """{"status":{"is_repo":true,"branch":"main","upstream":"origin/main","behind":0,"commits":[{"hash":"fff","subject":"更新示例代码"}],"files":null},"updated":true,"future":1}"""
        val api = HubSessionFiles({ error("GET used for a pull") }, post = { path, body ->
            posts += path to body
            Response.Builder().request(Request.Builder().url("https://example.com$path").build()).protocol(Protocol.HTTP_1_1)
                .code(status).message("response").body(answer.toResponseBody()).build()
        })
        val pulled = api.pull("machine/segment", "/workspace/中文 &")
        assertTrue(pulled.updated)
        assertEquals("main", pulled.status.branch)
        assertEquals(listOf("fff"), pulled.status.commits.map { it.hash })
        assertEquals("/api/v1/machines/machine%2Fsegment/git/pull", posts.last().first)
        assertEquals("""{"cwd":"/workspace/中文 &"}""", posts.last().second)

        answer = """{"status":{"is_repo":true}}"""
        assertFalse(api.pull("machine", "/workspace").updated)

        // A refusal as the hub answers it (#1888): a 409 with the reason in the app's language, the node's words as the detail.
        status = 409
        answer = """{"code":"uncommitted_changes","delivered":"none","error":"工作区有未提交的改动，请先在电脑上提交或 stash，再拉取。","detail":"the working tree has uncommitted changes: commit or stash them first"}"""
        try { api.pull("machine", "/workspace"); fail("Expected HTTP failure") }
        catch (failure: HubFailure) {
            assertEquals(409, failure.status)
            assertEquals("工作区有未提交的改动，请先在电脑上提交或 stash，再拉取。", failure.message)
        }
    }

    @Test fun downloadStreamsTheWholeFileIntoPlace(): Unit = runBlocking {
        val requests = mutableListOf<String>()
        var status = 200
        val bytes = ByteArray(300_000) { (it % 251).toByte() }
        val respond: suspend (String) -> Response = { path ->
            requests += path
            Response.Builder().request(Request.Builder().url("https://example.com$path").build()).protocol(Protocol.HTTP_1_1)
                .code(status).message("response").body(if (status == 200) bytes.toResponseBody() else """{"code":"not_found"}""".toResponseBody()).build()
        }
        val api = HubSessionFiles({ error("buffered request used for a download") }, stream = respond)
        val dir = kotlin.io.path.createTempDirectory("dl").toFile()
        val into = java.io.File(dir, "a/song.mp3")
        assertEquals(300_000L, api.download("m", "/w", "music/song a.mp3", into))
        assertArrayEquals(bytes, into.readBytes())
        val url = ("https://example.com" + requests.last()).toHttpUrl()
        assertEquals("/api/v1/machines/m/file/download", (url.encodedPath).zhAny())
        assertEquals("music/song a.mp3", (url.queryParameter("path")).zhAny())
        assertEquals("/w", (url.queryParameter("cwd")).zhAny())

        status = 404
        val missing = java.io.File(dir, "missing.mp3")
        try { api.download("m", "/w", "missing.mp3", missing); fail("Expected HTTP failure") }
        catch (failure: HubFailure) { assertEquals(404, failure.status) }
        assertFalse(missing.exists())
        assertEquals(listOf("song.mp3"), (java.io.File(dir, "a").list()!!.toList()).zhAny())
        dir.deleteRecursively()
    }
}
