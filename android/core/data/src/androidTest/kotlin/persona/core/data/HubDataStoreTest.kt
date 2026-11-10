package persona.core.data

import android.content.Context
import android.content.ContextWrapper
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.api.EndpointOverrides
import persona.core.contracts.api.LocalProxyRoute
import persona.core.contracts.api.SshRoute
import persona.core.contracts.api.MessageRequest
import persona.core.contracts.api.MessageTransport
import persona.core.contracts.api.MessageTransportResult
import persona.feature.inbox.ReplyRequest
import persona.feature.inbox.ReplyTarget
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class HubDataStoreTest {
    @Test fun coldStartRetainsDataAndPairingAgainErasesEntireOldScope() = runBlocking {
        fixture { context, parent ->
            var store = newStore(context, parent)
            try {
                val first = store.pair(credentials())
                val firstScope = first.scope
                val roots = listOf(first.filesDirectory, first.cacheDirectory, first.noBackupDirectory)
                val image = File(first.imageCacheDirectory.apply { mkdirs() }, "image").apply { writeText("image") }
                File(first.inboxMediaDirectory.apply { mkdirs() }, "attachment").writeText("attachment")
                val key = stringPreferencesKey("value")
                first.listPreferences.edit { it[key] = "list" }
                first.readingPositions.edit { it[key] = "position" }
                first.backgroundChecks.edit { it[key] = "check" }
                first.inboxReplies.update(first.scope.path) {
                    it.copy(outbox = listOf(InboxPendingReply(ReplyTarget.Lark("feed"), ReplyRequest("reply", "reply-id"))))
                }
                val session = SessionKey("machine", "session")
                first.items.save(session, CachedChat(items = listOf(Item("item", role = "user", kind = "text", text = "cached"))))
                val choices = EndpointOverrides(manual = listOf("https://hub.example.org:18662"), order = listOf("https://hub.example.org:18662", "https://example.com:443"))
                assertEquals(choices, first.endpoints.update { choices })
                assertTrue(File(first.noBackupDirectory, "endpoints.enc").exists())
                store.close()

                store = newStore(context, parent)
                val restored = requireNotNull(store.restore())
                assertEquals(firstScope, restored.scope)
                assertEquals("image", image.readText())
                assertEquals("list", restored.listPreferences.data.first()[key])
                assertEquals("position", restored.readingPositions.data.first()[key])
                assertEquals("check", restored.backgroundChecks.data.first()[key])
                assertEquals("reply-id", restored.inboxReplies.read(firstScope.path).outbox.single().request.clientId)
                assertEquals("item", restored.items.load(session).items.single().id)
                assertEquals(choices, restored.endpoints.value.value)
                val oldJournal = restored.inboxReplies

                val second = store.pair(credentials())
                assertEquals(firstScope.hubId, second.scope.hubId)
                assertNotEquals(firstScope.pairingGeneration, second.scope.pairingGeneration)
                roots.forEach { assertFalse("Old scope survived: ${it.name}", it.exists()) }
                assertTrue(second.items.load(session).items.isEmpty())
                assertEquals(EndpointOverrides(), second.endpoints.value.value)
                assertTrue(second.listPreferences.data.first().asMap().isEmpty())
                assertTrue(second.inboxReplies.read(second.scope.path).outbox.isEmpty())
                try {
                    oldJournal.update(firstScope.path) { it }
                    fail("A closed journal must not recreate discarded files")
                } catch (_: IllegalStateException) { }
                roots.forEach { assertFalse(it.exists()) }
            } finally { store.close() }
        }
    }

    @Test fun unpairAfterColdStartErasesScopeWithoutOpeningIt() = runBlocking {
        fixture { context, parent ->
            var store = newStore(context, parent)
            try {
                val data = store.pair(credentials())
                val roots = listOf(data.filesDirectory, data.cacheDirectory, data.noBackupDirectory)
                roots.forEach { File(it.apply { mkdirs() }, "record").writeText("record") }
                store.close()
                store = newStore(context, parent)
                store.unpair()
                roots.forEach { assertFalse(it.exists()) }
                assertNull(store.restore())
            } finally { store.close() }
        }
    }

    @Test fun tunnelsAreKeptWithTheirPairingAndTheDeviceKeyIsMadeOnce() = runBlocking {
        fixture { context, parent ->
            var store = newStore(context, parent)
            try {
                val first = store.pair(credentials())
                val ssh = SshRoute("ssh.example.com", user = "me", hostKey = "SHA256:example")
                val proxy = LocalProxyRoute(port = 7890, username = "me", password = "secret")
                assertEquals(PairedTunnels(), store.tunnels(first.scope))
                store.updateTunnels(first.scope) { it.copy(ssh = ssh) }
                assertEquals(PairedTunnels(ssh, proxy), store.updateTunnels(first.scope) { it.copy(proxy = proxy) })
                assertTrue(File(first.noBackupDirectory, "tunnels.enc").exists())
                var made = 0
                val seed = store.deviceSshKey { made++; ByteArray(32) { 7 } }
                assertArrayEquals(seed, store.deviceSshKey { made++; ByteArray(32) })
                assertEquals(1, made)
                store.close()

                // A cold start reads both back; the registry migrated above, so nothing here waits for a published group.
                store = newStore(context, parent)
                assertEquals(PairedTunnels(ssh, proxy), store.tunnels(first.scope))
                assertArrayEquals(seed, store.deviceSshKey { error("The key was made again") })
            } finally { store.close() }
        }
    }

    private fun newStore(context: Context, parent: CoroutineScope) = HubDataStore(context, parent) { _, _ ->
        object : MessageTransport {
            override val readyToSend = MutableStateFlow(false)
            override val supportsClientOps = false
            override suspend fun send(request: MessageRequest): MessageTransportResult = error("Unexpected network call")
        }
    }

    private fun credentials() = HubCredentials("https://example.com", "example-hub", null, emptyList(), "test-token")

    private suspend fun fixture(test: suspend (Context, CoroutineScope) -> Unit) {
        val base = InstrumentationRegistry.getInstrumentation().targetContext
        val root = File(base.cacheDir, "scope-test-${java.util.UUID.randomUUID()}").apply { mkdirs() }
        val context = object : ContextWrapper(base) {
            override fun getApplicationContext(): Context = this
            override fun getFilesDir() = File(root, "files").apply { mkdirs() }
            override fun getCacheDir() = File(root, "cache").apply { mkdirs() }
            override fun getNoBackupFilesDir() = File(root, "no-backup").apply { mkdirs() }
        }
        val parent = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        try { test(context, parent) }
        finally { parent.cancel(); root.deleteRecursively() }
    }
}
