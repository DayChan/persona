package persona.android.di

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.nio.file.Files

/**
 * Verifies that the InboxRepository facade in InboxModule explicitly forwards
 * every method defined on InboxRepository to avoid silent fallback to default interface implementations.
 * (Fixes #1619)
 */
class InboxFacadeTest {
    private val modulePath = "android/app/src/main/java/persona/android/di/InboxModule.kt"

    private fun facadeSource(start: File): File? =
        generateSequence(start.canonicalFile) { it.parentFile }
            .map { File(it, modulePath) }
            .firstOrNull { it.isFile }

    @Test
    fun facadeOverridesAllInboxRepositoryMethods() {
        val moduleFile = facadeSource(File("."))
        assertNotNull("InboxModule.kt must exist in the current checkout", moduleFile)

        val content = requireNotNull(moduleFile).readText()
        val facadeBlock = content.substringAfter("val inbox = object : persona.feature.inbox.InboxRepository {")
            .substringBefore("val assistant = object : AssistantConversations {")

        val requiredMethods = listOf(
            "momentsPost",
            "momentsComments",
            "urlPreview",
            "react",
            "cardAction",
            "userProfile",
            "quickReactions",
            "emojis",
            "refresh",
            "refreshNow",
            "loadOlder",
            "refreshConversation",
            "loadNewer",
            "latestConversation",
            "markVisible",
            "setDone",
            "readDraft",
            "saveDraft",
            "reply",
            "downloadFile",
            "replyStatus",
            "verifyReply",
            "askPersona",
            "personaContext",
            "draftReply",
            "handOff"
        )

        for (method in requiredMethods) {
            assertTrue(
                "InboxRepository facade in InboxModule.kt must explicitly override $method to forward to active inbox",
                facadeBlock.contains("override suspend fun $method") ||
                facadeBlock.contains("override fun $method") ||
                facadeBlock.contains("override val $method")
            )
        }
    }

    @Test
    fun assistantFacadeOverridesMainConversationResolution() {
        val moduleFile = facadeSource(File("."))
        assertNotNull("InboxModule.kt must exist in the current checkout", moduleFile)

        val facadeBlock = requireNotNull(moduleFile).readText()
            .substringAfter("val assistant = object : AssistantConversations {")
        assertTrue(
            "AssistantConversations facade must resolve the main conversation through the active scoped repository",
            facadeBlock.contains(
                "override suspend fun mainConversationOf(manager: String) = use { it.assistant.mainConversationOf(manager) }"
            )
        )
    }

    @Test
    fun facadeSourceSupportsRepositoryAndroidAndAppWorkingDirectories() {
        val root = Files.createTempDirectory("inbox-facade-source-").toFile().canonicalFile
        try {
            val module = File(root, modulePath)
            check(module.parentFile.mkdirs())
            module.writeText("package fixture\n")
            for (start in listOf(root, File(root, "android"), File(root, "android/app"))) {
                assertEquals(module.canonicalFile, facadeSource(start)?.canonicalFile)
            }
        } finally {
            root.deleteRecursively()
        }
    }

    @Test
    fun missingFacadeSourceAndDirectoriesAreRejected() {
        val root = Files.createTempDirectory("inbox-facade-missing-").toFile().canonicalFile
        try {
            assertNull(facadeSource(root))
            check(File(root, modulePath).mkdirs())
            assertNull(facadeSource(File(root, "android/app")))
        } finally {
            root.deleteRecursively()
        }
    }
}
