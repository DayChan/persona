package persona.core.data

import android.content.Context
import androidx.room.Room
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.PreferenceDataStoreFactory
import androidx.datastore.preferences.core.Preferences
import java.io.File
import java.io.IOException
import java.util.Base64
import java.util.UUID
import persona.core.contracts.HubScope
import persona.core.contracts.api.MessageTransport
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext

/**
 * Create one application-wide instance. Pairing networking stays in core:hub.
 *
 * Two ways to use it. The single-pairing API ([pair], [unpair], [restore]) keeps one active scope behind `active.enc`.
 * The hub-group API ([registry], [storeHub], [openGroup], [publish] …, docs/android/android.md "Multiple hubs") keeps any number of
 * pairings and opens one data scope per group; the first [registry] read turns the single pairing into the primary
 * group without moving a file. Once the registry exists, [restore] returns what was last [publish]ed (the selected
 * group) and waits for the first [publish] instead of opening a scope itself, so the two never open one scope twice.
 */
class HubDataStore(
    context: Context,
    private val parentScope: CoroutineScope,
    private val cacheBudgets: CacheBudgets = CacheBudgets(),
    private val transport: (HubScope, HubCredentials) -> MessageTransport,
) {
    private val context = context.applicationContext
    private val lock = Mutex()
    private val job = SupervisorJob(parentScope.coroutineContext[Job])
    private val ioScope = CoroutineScope(parentScope.coroutineContext + job + Dispatchers.IO)
    private val encrypted by lazy { EncryptedCredentials(this.context) }
    private val active by lazy { encrypted.active(ioScope) }
    private val groups by lazy { encrypted.groups(ioScope) }
    private var loaded = false
    private var current: ScopedHubData? = null
    private class OpenGroup(val data: ScopedHubData, val home: HubScope)
    /** Group scopes opened with [openGroup], by storage path. */
    private val opened = mutableMapOf<String, OpenGroup>()
    private var published: ScopedHubData? = null
    private val publishing = MutableStateFlow(false)

    suspend fun restore(): ScopedHubData? {
        val ready = withContext(Dispatchers.IO) { lock.withLock {
            when {
                publishing.value -> Result.success(published)
                encrypted.groupsFile.exists() -> null
                else -> Result.success(restoreLocked())
            }
        } }
        if (ready != null) return ready.getOrThrow()
        publishing.first { it }
        return lock.withLock { published }
    }

    /** Even pairing with the same hub creates a fresh generation and discards the old scope. */
    suspend fun pair(credentials: HubCredentials): ScopedHubData = withContext(Dispatchers.IO) { lock.withLock {
        require(credentials.hubAddress.isNotBlank() && credentials.deviceToken.isNotBlank())
        val scope = HubScope(credentials.hubId, UUID.randomUUID().toString())
        discardLocked()
        val location = ActivePairing(hubId = scope.hubId, generation = scope.pairingGeneration)
        active.updateData { location.copy(discarding = true) }
        loaded = false
        writeCredentialsLocked(scope, credentials)
        active.updateData { location }
        loaded = false
        requireNotNull(restoreLocked())
    } }

    suspend fun unpair() = withContext(Dispatchers.IO) { lock.withLock {
        discardLocked()
    } }

    suspend fun close() = lock.withLock {
        opened.values.forEach { it.data.close() }
        opened.clear()
        published = null
        current?.close()
        current = null
        job.cancelAndJoin()
    }

    /** The hub groups; the first read migrates a single pairing into the primary group (see [migrateRegistry]). */
    suspend fun registry(): GroupsDocument = withContext(Dispatchers.IO) { lock.withLock { registryLocked() } }

    /** Writes [transform] of the registry and returns what was written. */
    suspend fun updateRegistry(transform: (GroupsDocument) -> GroupsDocument): GroupsDocument =
        withContext(Dispatchers.IO) { lock.withLock {
            registryLocked()
            groups.updateData { transform(it) }
        } }

    /**
     * Writes the document of [change] (HubGroupRegistry.kt) and returns the change. The scopes it lists are not
     * erased here: close the groups using them first, then [erase].
     */
    suspend fun change(change: (GroupsDocument) -> RegistryChange): RegistryChange =
        withContext(Dispatchers.IO) { lock.withLock {
            registryLocked()
            var result: RegistryChange? = null
            groups.updateData { document -> change(document).also { result = it }.document }
            requireNotNull(result)
        } }

    /** Writes [credentials] into a new pairing scope and returns it; the registry is the caller's to update. */
    suspend fun storeHub(credentials: HubCredentials): HubScope = withContext(Dispatchers.IO) { lock.withLock {
        check(job.isActive) { "Hub data store is closed" }
        require(credentials.hubAddress.isNotBlank() && credentials.deviceToken.isNotBlank())
        val scope = HubScope(credentials.hubId, UUID.randomUUID().toString())
        writeCredentialsLocked(scope, credentials)
        scope
    } }

    /** A pairing's credentials; null when the scope has none. An unreadable file throws and stays as it is. */
    suspend fun credentials(scope: HubScope): HubCredentials? = withContext(Dispatchers.IO) { lock.withLock {
        check(job.isActive) { "Hub data store is closed" }
        readCredentialsLocked(scope)
    } }

    /** A pairing's route choices (manual candidates, disabled ones, order); unreadable or missing is the default. */
    suspend fun endpoints(scope: HubScope): persona.core.contracts.api.EndpointOverrides = withContext(Dispatchers.IO) { lock.withLock {
        check(job.isActive) { "Hub data store is closed" }
        opened.values.firstOrNull { it.home == scope }?.let { return@withLock it.data.endpoints.value.value }
        current?.takeIf { it.scope == scope }?.let { return@withLock it.endpoints.value.value }
        val storeJob = SupervisorJob(job)
        try {
            EndpointOverridesStore.open(encrypted.endpoints(scope.path, CoroutineScope(ioScope.coroutineContext + storeJob))).value.value
        } finally {
            withContext(NonCancellable) { storeJob.cancelAndJoin() }
        }
    } }

    /** A pairing's SSH tunnel and local proxy; unreadable or missing is neither. */
    suspend fun tunnels(scope: HubScope): PairedTunnels = withContext(Dispatchers.IO) { lock.withLock {
        check(job.isActive) { "Hub data store is closed" }
        temporary({ encrypted.tunnels(scope.path, it) }) { it.data.first() }.routes()
    } }

    /** Saves [transform] of a pairing's tunnels and returns what was saved. */
    suspend fun updateTunnels(scope: HubScope, transform: (PairedTunnels) -> PairedTunnels): PairedTunnels =
        withContext(Dispatchers.IO) { lock.withLock {
            check(job.isActive) { "Hub data store is closed" }
            check(scope.path in registryLocked().groups.flatMap { group -> group.hubs.map { it.pairing.path } }) { "Not a paired hub" }
            temporary({ encrypted.tunnels(scope.path, it) }) { store -> store.updateData { transform(it.routes()).document() } }.routes()
        } }

    /**
     * This device's SSH key's seed (docs/operations/network.md "Connecting to hub through SSH"), made by [generate] the
     * first time. It stays when pairings go: servers list it in their authorized_keys. Throws when it cannot be read.
     */
    suspend fun deviceSshKey(generate: () -> ByteArray): ByteArray = withContext(Dispatchers.IO) { lock.withLock {
        check(job.isActive) { "Hub data store is closed" }
        temporary(encrypted::sshKey) { store ->
            val saved = store.data.first().seed
                ?: store.updateData { if (it.seed == null) it.copy(seed = Base64.getEncoder().encodeToString(generate())) else it }.seed
            Base64.getDecoder().decode(checkNotNull(saved))
        }
    } }

    /** Opens a store for one read or write; DataStore refuses a second live instance of a file. Callers hold [lock]. */
    private suspend fun <T, R> temporary(open: (CoroutineScope) -> DataStore<T>, use: suspend (DataStore<T>) -> R): R {
        val storeJob = SupervisorJob(job)
        try {
            return use(open(CoroutineScope(ioScope.coroutineContext + storeJob)))
        } finally {
            withContext(NonCancellable) { storeJob.cancelAndJoin() }
        }
    }

    /**
     * Opens a group's data: databases, caches and files in [storage]; credentials and route choices of [home] (the
     * group's first hub, whose pairing scope is often [storage] itself). The transport factory gets (storage, the
     * home's credentials). Opening an open group again returns it.
     */
    suspend fun openGroup(storage: HubScope, home: HubScope): ScopedHubData = withContext(Dispatchers.IO) { lock.withLock {
        check(job.isActive) { "Hub data store is closed" }
        opened[storage.path]?.let { open ->
            check(open.home == home) { "Group data is open with another home pairing" }
            return@withLock open.data
        }
        check(current?.scope != storage && current?.scope != home) { "Scope is open as the single pairing" }
        check(opened.values.none { it.home == home }) { "Home pairing is open for another group" }
        open(storage, home).also { opened[storage.path] = OpenGroup(it, home) }
    } }

    /** Closes a group opened with [openGroup]; its files stay. */
    suspend fun closeGroup(data: ScopedHubData) = withContext(Dispatchers.IO) { lock.withLock {
        val entry = opened.entries.firstOrNull { it.value.data === data } ?: return@withLock
        opened.remove(entry.key)
        if (published === data) published = null
        data.close()
    } }

    /** Forgets a pairing: its whole scope, or with [keepData] only its credentials and route choices (the directory is a group's storage). */
    suspend fun eraseHub(scope: HubScope, keepData: Boolean) =
        erase(listOf(ScopeErasure(StoredScope.of(scope), if (keepData) Erase.Credentials else Erase.All)))

    /** Drops a group's data scope but keeps a pairing that lives in the same directory. */
    suspend fun eraseStorage(scope: HubScope) = erase(listOf(ScopeErasure(StoredScope.of(scope), Erase.Data)))

    /** Carries out a [RegistryChange]'s erasures. Scopes still in use by an open group are refused. */
    suspend fun erase(erasures: List<ScopeErasure>) = withContext(Dispatchers.IO) { lock.withLock {
        check(job.isActive) { "Hub data store is closed" }
        val roots = listOf(context.filesDir, context.cacheDir, context.noBackupFilesDir)
        erasures.forEach { (stored, mode) ->
            val scope = stored.scope
            val home = opened.values.any { it.home == scope }
            val storage = opened.containsKey(stored.path)
            check(current?.scope != scope) { "Scope is open as the single pairing" }
            when (mode) {
                Erase.All -> { check(!home && !storage) { "Close the group before erasing its scope" }; eraseScope(roots, scope) }
                Erase.Credentials -> { check(!home) { "Close the group before erasing its home pairing" }; eraseCredentials(context.noBackupFilesDir, scope) }
                Erase.Data -> { check(!storage) { "Close the group before erasing its data" }; eraseData(roots, context.noBackupFilesDir, scope) }
            }
        }
    } }

    /**
     * Forgets every pairing and group: closes them, erases every scope directory (listed or not) and leaves an empty
     * registry. A crash part way is finished by the next start (the single-pairing tombstone, then [sweep]).
     */
    suspend fun eraseAll() = withContext(Dispatchers.IO) { lock.withLock {
        check(job.isActive) { "Hub data store is closed" }
        opened.values.forEach { it.data.close() }
        opened.clear()
        published = null
        current?.close()
        current = null
        active.updateData { it.copy(discarding = it.hubId != null) }
        try { groups.updateData { GroupsDocument() } }
        catch (_: androidx.datastore.core.CorruptionException) {
            // Nothing in an unreadable registry can be kept; the directories below go anyway.
            encrypted.groupsFile.delete()
            groups.updateData { GroupsDocument() }
        }
        val roots = listOf(context.filesDir, context.cacheDir, context.noBackupFilesDir)
        scopeDirectories(roots).forEach { eraseScope(roots, it) }
        active.updateData { ActivePairing() }
        loaded = true
    } }

    /** Erases scope directories the registry no longer refers to (left by a crash between writing it and erasing). */
    suspend fun sweep(): Int = withContext(Dispatchers.IO) { lock.withLock {
        check(job.isActive) { "Hub data store is closed" }
        if (!encrypted.groupsFile.exists()) return@withLock 0
        val document = groups.data.first()
        val pointer = active.data.first()
        val keep = (document.groups.flatMap { group -> group.hubs.map { it.pairing.path } + group.storage.path } +
            document.moves.map { it.storage.path } +
            listOfNotNull(pointer.hubId?.let { hub -> pointer.generation?.let { "$hub/$it" } }) +
            opened.keys + opened.values.map { it.home.path } + listOfNotNull(current?.scope?.path)).toSet()
        val roots = listOf(context.filesDir, context.cacheDir, context.noBackupFilesDir)
        val orphans = scopeDirectories(roots).filter { it.path !in keep }
        orphans.forEach { eraseScope(roots, it) }
        orphans.size
    } }

    /**
     * What a closed group's data still owes its hubs (GroupMerge.kt): read from its database without opening the
     * scope. Nothing when it has no database.
     */
    suspend fun unsentAt(storage: HubScope): UnsentData = withContext(Dispatchers.IO) { lock.withLock {
        check(job.isActive) { "Hub data store is closed" }
        check(!opened.containsKey(storage.path) && current?.scope != storage) { "Close the group before moving its data" }
        val file = File(context.filesDir, "hub-data/${storage.path}/data.db")
        if (!file.exists()) return@withLock UnsentData(emptyList(), emptyList())
        val database = Room.databaseBuilder(context, PersistentDatabase::class.java, file.absolutePath).build()
        try {
            val dao = database.records()
            unsentOf(dao.outbox(storage.path), dao.drafts(storage.path))
        } finally { database.close() }
    } }

    /** From now on [restore] returns [selected] (the selected group's data, or null). */
    suspend fun publish(selected: ScopedHubData?) {
        lock.withLock { published = selected }
        publishing.value = true
    }

    private suspend fun registryLocked(): GroupsDocument {
        check(job.isActive) { "Hub data store is closed" }
        if (encrypted.groupsFile.exists()) return groups.data.first()
        val pointer = active.data.first()
        val scope = if (pointer.hubId != null && pointer.generation != null) HubScope(pointer.hubId, pointer.generation) else null
        if (scope != null && pointer.discarding) {
            // An interrupted disconnect: finish it, as restore would.
            erase(scope)
            active.updateData { ActivePairing() }
            loaded = false
        }
        val present = scope != null && !pointer.discarding && (readCredentialsLocked(scope)
            ?: throw IOException("Pairing credentials are missing; original data retained")) != null
        val migrated = migrateRegistry(pointer.hubId, pointer.generation, pointer.discarding, present, System.currentTimeMillis())
        return groups.updateData { migrated }
    }

    private suspend fun writeCredentialsLocked(scope: HubScope, credentials: HubCredentials) {
        val storeJob = SupervisorJob(job)
        try {
            val dataStore = encrypted.credentials(scope.path, CoroutineScope(ioScope.coroutineContext + storeJob))
            dataStore.updateData { CredentialDocument(credentials = credentials) }
        } finally {
            withContext(NonCancellable) { storeJob.cancelAndJoin() }
        }
    }

    private suspend fun readCredentialsLocked(scope: HubScope): HubCredentials? {
        // DataStore refuses a second live instance of a file: reuse an open scope's credentials.
        opened.values.firstOrNull { it.home == scope }?.let { return it.data.credentials }
        current?.takeIf { it.scope == scope }?.let { return it.credentials }
        val storeJob = SupervisorJob(job)
        try {
            return encrypted.credentials(scope.path, CoroutineScope(ioScope.coroutineContext + storeJob)).data.first().credentials
        } finally {
            withContext(NonCancellable) { storeJob.cancelAndJoin() }
        }
    }

    private suspend fun restoreLocked(): ScopedHubData? {
        check(job.isActive) { "Hub data store is closed" }
        if (loaded) return current
        val pointer = active.data.first()
        if (pointer.hubId == null && pointer.generation == null) {
            loaded = true
            return null
        }
        val scope = HubScope(requireNotNull(pointer.hubId), requireNotNull(pointer.generation))
        if (pointer.discarding) {
            erase(scope)
            active.updateData { ActivePairing() }
            loaded = true
            return null
        }
        current = open(scope, scope)
        loaded = true
        return current
    }

    private suspend fun discardLocked() {
        check(job.isActive) { "Hub data store is closed" }
        val pointer = active.data.first()
        if (pointer.hubId == null && pointer.generation == null) return
        val scope = HubScope(requireNotNull(pointer.hubId), requireNotNull(pointer.generation))
        // The tombstone makes interrupted cleanup resumable before another scope can open.
        active.updateData { it.copy(discarding = true) }
        current?.close()
        current = null
        loaded = false
        erase(scope)
        active.updateData { ActivePairing() }
        loaded = true
    }

    private suspend fun open(storage: HubScope, home: HubScope): ScopedHubData = withContext(Dispatchers.IO) {
        val scopeJob = SupervisorJob(job)
        var persistent: PersistentDatabase? = null
        var cache: ItemsDatabase? = null
        try {
            val coroutineScope = CoroutineScope(ioScope.coroutineContext + scopeJob)
            val credentials = encrypted.credentials(home.path, coroutineScope).data.first().credentials
                ?: throw IOException("Pairing credentials are missing; original data retained")
            check(credentials.hubId == home.hubId) { "Pairing scope does not match credentials" }
            val endpoints = EndpointOverridesStore.open(encrypted.endpoints(home.path, coroutineScope))
            val persistentPath = File(context.filesDir, "hub-data/${storage.path}/data.db").apply { parentFile!!.mkdirs() }
            val cachePath = File(context.cacheDir, "hub-data/${storage.path}/items.db").apply { parentFile!!.mkdirs() }
            persistent = Room.databaseBuilder(context, PersistentDatabase::class.java, persistentPath.absolutePath).build()
            cache = Room.databaseBuilder(context, ItemsDatabase::class.java, cachePath.absolutePath).addMigrations(ItemsDatabase.MIGRATION_1_2).build()
            // Force schema validation before returning a writable scope. Never destructively migrate.
            persistent.openHelper.writableDatabase
            cache.openHelper.writableDatabase
            val sender = OutboxMessageSender(storage, RoomOutboxStore(persistent, storage), transport(storage, credentials), coroutineScope)
            ScopedHubData(storage, credentials, sender, DraftStore(persistent, storage), SessionItemsCache(cache, storage, cacheBudgets.items),
                coroutineScope, persistent, cache,
                File(context.filesDir, "hub-data/${storage.path}"),
                File(context.cacheDir, "hub-data/${storage.path}"),
                File(context.noBackupFilesDir, "hub-data/${storage.path}"), endpoints, cacheBudgets)
        } catch (failure: Exception) {
            withContext(NonCancellable) {
                scopeJob.cancelAndJoin()
                persistent?.close()
                cache?.close()
            }
            throw failure
        }
    }

    private suspend fun erase(scope: HubScope) = withContext(Dispatchers.IO) {
        eraseScope(listOf(context.filesDir, context.cacheDir, context.noBackupFilesDir), scope)
    }
}

/** Deletes `hub-data/<scope>` under each root: everything one pairing stored, including its route choices. */
internal fun eraseScope(roots: List<File>, scope: HubScope) {
    roots.forEach { root ->
        val directory = File(root, "hub-data/${scope.path}")
        if (directory.exists() && !directory.deleteRecursively()) throw IOException("Cannot discard pairing scope")
    }
    dropEmptyParents(roots, scope)
}

/** The files of a pairing in its no-backup scope directory; everything else there is group data. */
internal val pairingFiles = setOf("credentials.enc", "endpoints.enc", "tunnels.enc")

/** Deletes a pairing's credentials, route choices and tunnels only: the directory still holds a group's data. */
internal fun eraseCredentials(noBackup: File, scope: HubScope) {
    pairingFiles.forEach { name ->
        val file = File(noBackup, "hub-data/${scope.path}/$name")
        if (file.exists() && !file.delete()) throw IOException("Cannot discard pairing credentials")
    }
}

/** Deletes a group's data in a scope but keeps the pairing that lives in the same directory. */
internal fun eraseData(roots: List<File>, noBackup: File, scope: HubScope) {
    roots.forEach { root ->
        val directory = File(root, "hub-data/${scope.path}")
        if (!directory.exists()) return@forEach
        val doomed = if (root == noBackup) directory.listFiles().orEmpty().filter { it.name !in pairingFiles } else listOf(directory)
        doomed.forEach { if (!it.deleteRecursively()) throw IOException("Cannot discard group data") }
    }
    dropEmptyParents(roots, scope)
}

/** Every `hub-data/<hub>/<generation>` directory under [roots]; malformed names are skipped. */
internal fun scopeDirectories(roots: List<File>): List<HubScope> = roots.flatMap { root ->
    File(root, "hub-data").listFiles().orEmpty().filter { it.isDirectory }.flatMap { hub ->
        hub.listFiles().orEmpty().filter { it.isDirectory }.mapNotNull { generation ->
            runCatching { HubScope(hub.name, generation.name) }.getOrNull()
        }
    }
}.distinct()

private fun dropEmptyParents(roots: List<File>, scope: HubScope) {
    roots.forEach { root -> File(root, "hub-data/${scope.hubId}").takeIf { it.isDirectory && it.list().isNullOrEmpty() }?.delete() }
}

class ScopedHubData internal constructor(
    val scope: HubScope,
    val credentials: HubCredentials,
    val messages: OutboxMessageSender,
    val drafts: DraftStore,
    val items: SessionItemsCache,
    /** Assembly must launch scope-owned repositories in this scope. It is cancelled before erasure. */
    val coroutineScope: CoroutineScope,
    internal val persistent: PersistentDatabase,
    private val cache: ItemsDatabase,
    val filesDirectory: File,
    val cacheDirectory: File,
    val noBackupDirectory: File,
    /** Manual candidates, disabled ones and the order to try; erased with the pairing. */
    val endpoints: EndpointOverridesStore,
    val cacheBudgets: CacheBudgets = CacheBudgets(),
) {
    val openWindowReconciliation = OpenWindowReconciliation()
    private val fileLock = Mutex()
    private val images by lazy { DisplayImageCache(imageCacheDirectory, cacheBudgets.images, items::activeSessions) }
    init { items.onRelease = {
        if (coroutineScope.coroutineContext[Job]?.isActive == true) images.trim()
    } }

    suspend fun cachedImage(session: persona.core.contracts.SessionKey, itemId: String,
        width: Int, height: Int, fetch: suspend () -> ByteArray): File = withContext(Dispatchers.IO) {
        writeFiles { images.read(session, itemId, width, height) }?.let { return@withContext it }
        val bytes = fetch()
        writeFiles { images.put(session, itemId, width, height, bytes) }
    }
    val imageCacheDirectory: File get() = directory(cacheDirectory, "images")
    val inboxMediaDirectory: File get() = directory(cacheDirectory, "inbox-media")
    val listPreferences: DataStore<Preferences> by lazy { preferences("lists") }
    val readingPositions: DataStore<Preferences> by lazy { preferences("reading") }
    val backgroundChecks: DataStore<Preferences> by lazy { preferences("background-checks") }
    val inboxReplies = InboxReplyJournal(File(noBackupDirectory, "inbox-replies"), scope.path)
    val operations = OneShotOperationJournal(File(noBackupDirectory, "operations"), scope)

    internal fun checkActive() { check(coroutineScope.coroutineContext[Job]!!.isActive) { "Pairing scope is closed" } }

    internal suspend fun <T> writeFiles(write: () -> T): T = fileLock.withLock {
        checkActive()
        write()
    }

    private fun directory(root: File, name: String): File {
        checkActive()
        return File(root, name)
    }

    private fun preferences(name: String): DataStore<Preferences> = PreferenceDataStoreFactory.create(
        scope = coroutineScope,
        produceFile = { File(directory(noBackupDirectory, "preferences"), "$name.preferences_pb") },
    )

    internal suspend fun close() {
        messages.close()
        coroutineScope.coroutineContext[Job]!!.cancelAndJoin()
        fileLock.withLock { }
        inboxReplies.close()
        operations.close()
        withContext(Dispatchers.IO) { persistent.close(); cache.close() }
    }
}
