package persona.android.di

import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import persona.feature.agents.AgentsDraftSource
import persona.feature.agents.AgentsOperationsBindings
import persona.feature.agents.AgentsWiringState
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

/** Hub groups in the app layer (docs/android/android.md "Multiple hubs"): ids between a group and the merged lists, and routing. */
class GroupRoutingTest {
    private val primary = GroupNames(null)
    private val other = GroupNames("g0a1b2c3d")

    @Test fun thePrimaryGroupPassesEverythingThroughUnchanged() {
        val machine = Machine("macbook", "")
        val session = Session("macbook", "s1")
        val event = ItemsEvent("macbook", "s1")
        assertSame(machine, primary.machine(machine))
        assertSame(session, primary.session(session))
        assertSame(event, primary.event(event))
        val snapshot = HubPreferencesSnapshot(HubPreferenceSet(listOf("macbook/w1"), listOf("macbook/s1")))
        assertSame(snapshot, primary.preferences(snapshot))
        assertTrue(primary.owns("macbook"))
        assertFalse(primary.owns("macbook~g0a1b2c3d"))
    }

    @Test fun anotherGroupsIdsCarryItsSuffixUpAndLoseItDown() {
        assertEquals(Machine("macbook~g0a1b2c3d", "macbook"), other.machine(Machine("macbook", "")))
        assertEquals("macbook~g0a1b2c3d", other.session(Session("macbook", "s1")).key.machineId)
        val interaction = other.interaction(Interaction("i1", "macbook", "s1"))
        assertEquals(SessionKey("macbook~g0a1b2c3d", "s1"), interaction.sessionKey)
        // Two hubs can use the same interaction id: other groups' ids carry the suffix, and answering strips it.
        assertEquals("i1~g0a1b2c3d", interaction.id)
        assertEquals("i1~g0a1b2c3d", (other.event(InteractionResolvedEvent("i1")) as InteractionResolvedEvent).id)
        assertEquals("i1~g0a1b2c3d", (other.event(NotifyEvent("interaction", "m", "s", interactionId = "i1")) as NotifyEvent).interactionId)
        // So are alert ids (#5614): the same id from two hubs replaces nothing on the other's.
        assertEquals("p1~g0a1b2c3d", (other.event(NotifyEvent("alert", "", "", id = "p1")) as NotifyEvent).id)
        assertEquals("i1", GroupKeys.raw(interaction.id))
        val events: List<ServerEvent> = listOf(ItemsEvent("m", "s"), SessionsEvent("m", listOf(Session("m", "s"))),
            MachinesEvent(listOf(Machine("m"))), RevsEvent("m"), InteractionEvent(Interaction("i", "m", "s")),
            NotifyEvent("done", "m", "s"), DeltaEvent("m", "s"), ScreenEvent("m", "s"), HookEvent("m", "s"),
            TerminalEvent("m", "s"), TerminalControlEvent("m", "s", "active"), ErrorEvent("m", "s"))
        events.map(other::event).forEach { translated -> assertTrue("$translated", translated.toString().contains("m~g0a1b2c3d")) }
        // Events without machine ids are left alone.
        val lark = LarkEvent(listOf("f"))
        assertSame(lark, other.event(lark))
        val preferences = other.preferences(HubPreferencesSnapshot(
            HubPreferenceSet(listOf("m/w"), listOf("m/s"), setOf("m/s")), seen = mapOf("m/s" to "v")))
        assertEquals(listOf("m~g0a1b2c3d/w"), preferences.pins.spaces)
        assertEquals(setOf("m~g0a1b2c3d/s"), preferences.pins.allowed)
        assertEquals(mapOf("m~g0a1b2c3d/s" to "v"), preferences.seen)
        assertEquals(SessionKey("m", "s"), other.raw(SessionKey("m~g0a1b2c3d", "s")))
    }

    @Test fun anotherGroupsFilesReachItsMachineByThePlainId() = runBlocking<Unit> {
        val calls = mutableListOf<String>()
        val files = object : SessionFiles {
            override suspend fun directory(machineId: String, cwd: String, path: String) = error("unused")
            override suspend fun file(machineId: String, cwd: String, path: String) = error("unused")
            override suspend fun status(machineId: String, cwd: String) = error("unused")
            override suspend fun diff(machineId: String, cwd: String, path: String, staged: Boolean) = error("unused")
            override suspend fun log(machineId: String, cwd: String, skip: Int, limit: Int) = error("unused")
            override suspend fun show(machineId: String, cwd: String, hash: String) = error("unused")
            override suspend fun stat(machineId: String, cwd: String, paths: List<String>): SessionPathStats {
                calls += "stat $machineId"; return SessionPathStats()
            }
            override suspend fun download(machineId: String, cwd: String, path: String, into: java.io.File): Long {
                calls += "download $machineId $path"; return 3
            }
            override suspend fun thumbnail(machineId: String, cwd: String, path: String, size: Int): ByteArray? {
                calls += "thumbnail $machineId $path $size"; return byteArrayOf(1)
            }
            override suspend fun convertToPdf(machineId: String, cwd: String, path: String): SessionConvertedFile {
                calls += "convert $machineId $path"; return SessionConvertedFile("/cache/a.pdf")
            }
            override suspend fun filePreview(machineId: String, cwd: String, path: String): SessionFileContent {
                calls += "preview $machineId $path"; return SessionFileContent(path, path, stream = true)
            }
        }
        // The methods with a default implementation are the ones a hand-written forwarder can silently drop.
        val wrapped = files.inGroup(other)
        assertEquals(3L, wrapped.download("macbook~g0a1b2c3d", "/w", "a.mp3", java.io.File("unused")))
        wrapped.stat("macbook~g0a1b2c3d", "/w", listOf("a"))
        wrapped.thumbnail("macbook~g0a1b2c3d", "/w", "a.pdf", 256)
        assertEquals("/cache/a.pdf", wrapped.convertToPdf("macbook~g0a1b2c3d", "/w", "a.docx").path)
        assertTrue(wrapped.filePreview("macbook~g0a1b2c3d", "/w", "a.mp4").stream)
        assertEquals(listOf("download macbook a.mp3", "stat macbook", "thumbnail macbook a.pdf 256", "convert macbook a.docx", "preview macbook a.mp4"), calls)
    }

    @Test fun aGroupsDraftsAreStoredWithItsPlainKeys() = runBlocking<Unit> {
        val stored = mutableListOf<DraftKey>()
        val scope = HubScope("hub", "gen")
        val repository = object : DraftRepository {
            override fun observe(key: DraftKey) = flowOf(DraftSnapshot(scope, key, "x"))
            override suspend fun read(key: DraftKey) = DraftSnapshot(scope, key).also { stored += key }
            override suspend fun save(key: DraftKey, text: String, expectedVersion: Long) = DraftSnapshot(scope, key, text).also { stored += key }
        }
        val merged = SessionKey("m~g0a1b2c3d", "s")
        val wrapped = repository.inGroup(other)
        val read = wrapped.read(DraftKey.Chat(merged))
        assertEquals(DraftKey.Chat(SessionKey("m", "s")), stored.single())
        assertEquals(DraftKey.Chat(merged), read.key)
        assertEquals(DraftKey.Chat(merged), wrapped.observe(DraftKey.Chat(merged)).first().key)
        // Keys that are not a chat's pass through.
        assertEquals(DraftKey.Assistant(), wrapped.save(DraftKey.Assistant(), "t", 0).key)
        assertSame(repository, repository.inGroup(primary))
    }

    @Test fun creatingAnAgentReachesTheGroupWithItsPlainMachineAndComesBackSuffixed() = runBlocking<Unit> {
        var requested: AgentStartRequest? = null
        val service = object : AgentCreationService {
            override val scope = "hub/gen"
            override suspend fun launchers(machineId: String) = listOf(AgentLauncher(machineId))
            override suspend fun directories(machineId: String, path: String) = AgentDirectories(machineId)
            override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult {
                requested = request
                return AgentStartResult.Created(Session(request.machineId, "new"))
            }
            override suspend fun operation(operationId: String) = AgentStartResult.Pending
            override suspend fun launcherModels(machineId: String, launcher: String, refresh: Boolean) =
                LauncherModels(machineId, listOf(AgentModel(launcher)))
        }
        val wrapped = service.inGroup(other)
        assertEquals("m", wrapped.launchers("m~g0a1b2c3d").single().name)
        assertEquals(LauncherModels("m", listOf(AgentModel("claude"))), wrapped.launcherModels("m~g0a1b2c3d", "claude"))
        val created = wrapped.start(AgentStartRequest("m~g0a1b2c3d", "/", "claude"), "op") as AgentStartResult.Created
        assertEquals("m", requested!!.machineId)
        assertEquals(SessionKey("m~g0a1b2c3d", "new"), created.session.key)
    }

    @Test fun aStartsReceiptIsReadFromItsMachinesGroupAfterTheWiringIsRebuilt() = runBlocking<Unit> {
        fun hub(answer: AgentStartResult, asked: MutableList<String>) = object : AgentCreationService {
            override val scope = "hub/gen"
            override suspend fun launchers(machineId: String) = emptyList<AgentLauncher>()
            override suspend fun directories(machineId: String, path: String) = AgentDirectories(path)
            override suspend fun start(request: AgentStartRequest, operationId: String) = AgentStartResult.Unknown
            override suspend fun operation(operationId: String): AgentStartResult { asked += operationId; return answer }
        }
        val homeAsked = mutableListOf<String>()
        val workAsked = mutableListOf<String>()
        val created = AgentStartResult.Created(Session("mac~g0a1b2c3d", "new"))
        val states = listOf(AgentsWiringState("a", creation = hub(AgentStartResult.Unknown, homeAsked)),
            AgentsWiringState("b", creation = hub(created, workAsked)))
        val request = AgentStartRequest("mac~g0a1b2c3d", "/", "claude")
        requireNotNull(mergedAgentsState(listOf(primary, other), states, 0).creation).start(request, "op")
        // A route change rebuilds the merged state (and process death loses it): the pending start is checked anew.
        val rebuilt = requireNotNull(mergedAgentsState(listOf(primary, other), states, 0).creation)
        assertEquals(created, rebuilt.operation("op", request.machineId))
        assertEquals(listOf("op"), workAsked)
        assertTrue(homeAsked.isEmpty())
    }

    @Test fun mergedPinsWriteOnlyTheGroupsWhosePartChanged() = runBlocking<Unit> {
        class Hub(var pins: AgentOperationPreferences, val reachable: Boolean = true) : AgentOperations {
            val saved = mutableListOf<AgentOperationPreferences>()
            override val scope = "s"
            override suspend fun pins(): AgentOperationPreferences { check(reachable); return pins }
            override suspend fun mutes() = AgentOperationPreferences()
            override suspend fun savePins(value: AgentOperationPreferences) { check(reachable); saved += value; pins = value }
            override suspend fun saveMutes(value: AgentOperationPreferences) = Unit
            override suspend fun renameSession(key: SessionKey, title: String?, tabName: String?) = Unit
            override suspend fun renameSpace(machineId: String, workspaceId: String, name: String) = Unit
            override suspend fun closeSession(key: SessionKey) = Unit
        }
        // Each group's operations already speak merged ids (the per-group wiring translates).
        val home = Hub(AgentOperationPreferences(sessions = listOf("a/1")))
        val away = Hub(AgentOperationPreferences(sessions = listOf("b~g0a1b2c3d/2")), reachable = false)
        val assistant = object : AssistantConversations {
            override val assistant = MutableStateFlow(AssistantSnapshot())
            override val pinned = MutableStateFlow(emptySet<SessionKey>())
            override suspend fun refresh() = Unit
            override suspend fun startConversation() = SessionKey("a", "main")
        }
        fun state(operations: AgentOperations) = AgentsWiringState("s", AgentsOperationsBindings(operations, assistant),
            drafts = AgentsDraftSource { emptyFlow() })
        val merged = mergedAgentsState(listOf(primary, other), listOf(state(home), state(away)), 0)
        val operations = requireNotNull(merged.operations).operations
        // The unreachable group contributes nothing it does not know and is never overwritten with nothing.
        val pins = operations.pins()
        assertEquals(listOf("a/1"), pins.sessions)
        operations.savePins(pins.copy(sessions = pins.sessions + "a/2"))
        assertEquals(listOf(AgentOperationPreferences(sessions = listOf("a/1", "a/2"))), home.saved)
        assertTrue(away.saved.isEmpty())
        // Pinning in the group that could not be read fails instead of replacing its list.
        val failed = runCatching { operations.savePins(pins.copy(sessions = pins.sessions + "a/2" + "b~g0a1b2c3d/9")) }
        assertTrue(failed.isFailure)
        assertTrue(away.saved.isEmpty())
        assertEquals("s+s", merged.scope)
    }

    /** A hub's machine management that records what it was asked (plain ids only), as a group's fake hub. */
    private class FakeMachines(val hub: String) : MachineManagement {
        val calls = mutableListOf<String>()
        override suspend fun launchers(machineId: String) = emptyList<AgentLauncher>().also { calls += "launchers $machineId" }
        override suspend fun launcherQuota(machineId: String, name: String, refresh: Boolean) =
            persona.core.contracts.api.LauncherQuotaAnswer(machine = machineId).also { calls += "quota $machineId $name $refresh" }
        override suspend fun reconnect(machineId: String) { calls += "reconnect $machineId" }
        override suspend fun deploy(machineId: String) { calls += "deploy $machineId" }
        override suspend fun herdr(machineId: String) = HerdrState(false, false, true).also { calls += "herdr $machineId" }
        override suspend fun startHerdr(machineId: String) = HerdrState(false, true, true).also { calls += "start herdr $machineId" }
        override suspend fun setEngine(machineId: String, engine: String) = Machine(machineId, "").also { calls += "engine $machineId $engine" }
        override suspend fun claudeSettings(machineId: String) = emptyList<ClaudeSetting>().also { calls += "settings $machineId" }
        override suspend fun setClaudeSetting(machineId: String, key: String, value: kotlinx.serialization.json.JsonElement) =
            emptyList<ClaudeSetting>().also { calls += "set $machineId $key" }
        override suspend fun codexSettings(machineId: String) = emptyList<ClaudeSetting>().also { calls += "codex settings $machineId" }
        override suspend fun setCodexSetting(machineId: String, key: String, value: kotlinx.serialization.json.JsonElement) =
            emptyList<ClaudeSetting>().also { calls += "set codex $machineId $key" }
        override suspend fun doctor(machineId: String) =
            persona.core.contracts.api.DoctorReport(dir = hub).also { calls += "doctor $machineId" }
        override suspend fun fixDoctor(machineId: String) =
            persona.core.contracts.api.DoctorReport(fixed = listOf(hub)).also { calls += "fix $machineId" }
        override suspend fun browser(machineId: String) =
            persona.core.contracts.MachineBrowser(note = hub).also { calls += "browser $machineId" }
        override suspend fun setBrowser(machineId: String, enabled: Boolean) =
            persona.core.contracts.MachineBrowser(enabled = enabled).also { calls += "browser $machineId $enabled" }
        override suspend fun browserJob(machineId: String, action: String) =
            persona.core.contracts.MachineBrowser(note = action).also { calls += "browser $machineId $action" }
        override suspend fun usageWindows() =
            listOf(persona.core.contracts.api.UsageAccount(hub)).also { calls += "usage" }
    }

    @Test fun anotherGroupsMachineIsManagedOnItsOwnHub() = runBlocking<Unit> {
        // #634: reconnect, deploy, engine and Claude Code settings went to the selected group's hub for every machine
        val home = FakeMachines("home")
        val work = FakeMachines("work")
        val managed = mergedMachineManagement(object : MachineRoutes {
            override suspend fun <T> on(machineId: String, call: suspend (MachineManagement) -> T): T =
                if (other.owns(machineId)) call(work.inGroup(other)) else call(home.inGroup(primary))
        })
        managed.reconnect("macbook~g0a1b2c3d")
        managed.deploy("macbook~g0a1b2c3d")
        managed.launchers("macbook~g0a1b2c3d")
        managed.claudeSettings("macbook~g0a1b2c3d")
        // the machine that read the quota is named back in the merged ids
        assertEquals("macbook~g0a1b2c3d", managed.launcherQuota("macbook~g0a1b2c3d", "cc_main", refresh = true).machine)
        managed.setClaudeSetting("macbook~g0a1b2c3d", "model", kotlinx.serialization.json.JsonPrimitive("opus"))
        managed.codexSettings("macbook~g0a1b2c3d")
        managed.setCodexSetting("macbook~g0a1b2c3d", "model", kotlinx.serialization.json.JsonPrimitive("gpt-5.5"))
        assertEquals(HerdrState(false, false, true), managed.herdr("macbook~g0a1b2c3d"))
        assertEquals(HerdrState(false, true, true), managed.startHerdr("macbook~g0a1b2c3d"))
        val changed = managed.setEngine("macbook~g0a1b2c3d", "orca")
        assertEquals(listOf("reconnect macbook", "deploy macbook", "launchers macbook", "settings macbook",
            "quota macbook cc_main true", "set macbook model", "codex settings macbook", "set codex macbook model",
            "herdr macbook", "start herdr macbook", "engine macbook orca"), work.calls)
        // the machine it hands back is the merged list's, so the page finds it again
        assertEquals("macbook~g0a1b2c3d", changed.id)
        // the primary group's machine with the same plain id stays on the primary hub, unchanged
        managed.reconnect("macbook")
        assertEquals(listOf("reconnect macbook"), home.calls)
        assertEquals("macbook", managed.setEngine("macbook", "herdr").id)
        val plain = FakeMachines("solo")
        assertSame(plain, plain.inGroup(primary))
    }

    @Test fun selfCheckAndBrowserReachTheMachinesGroupAndUsageEveryGroupsHub() = runBlocking<Unit> {
        val home = FakeMachines("home")
        val work = FakeMachines("work")
        val routes = object : MachineRoutes {
            override suspend fun <T> on(machineId: String, call: suspend (MachineManagement) -> T): T =
                if (other.owns(machineId)) call(work.inGroup(other)) else call(home.inGroup(primary))
        }
        val managed = mergedMachineManagement(routes, usage = { listOf(GroupUsageRead("g0a1b2c3d", work.inGroup(other)::usageWindows)) })
        assertEquals("work", managed.doctor("macbook~g0a1b2c3d").dir)
        assertEquals(listOf("work"), managed.fixDoctor("macbook~g0a1b2c3d").fixed)
        assertEquals("work", managed.browser("macbook~g0a1b2c3d").note)
        assertTrue(managed.setBrowser("macbook~g0a1b2c3d", true).enabled)
        assertEquals("install", managed.browserJob("macbook~g0a1b2c3d", "install").note)
        assertEquals(listOf("work"), managed.usageWindows().map { it.id })
        assertEquals(listOf("doctor macbook", "fix macbook", "browser macbook", "browser macbook true",
            "browser macbook install", "usage"), work.calls)
        // the primary group's machine with the same plain id stays on its own hub
        assertEquals("home", managed.doctor("macbook").dir)
        assertEquals("home", managed.browser("macbook").note)
        assertEquals(listOf("doctor macbook", "browser macbook"), home.calls)
        // usage limits belong to a hub: without an open group there are none, and no machine's hub is asked
        assertEquals(emptyList<persona.core.contracts.api.UsageAccount>(), mergedMachineManagement(routes).usageWindows())
        assertEquals(listOf("doctor macbook", "browser macbook"), home.calls)
    }

    @Test fun subscriptionLimitsListEveryGroupsAccountsEachFromItsOwnHub() = runBlocking<Unit> {
        // #1845: only the selected group's hub was asked, so the other group's computers looked as if they had no usage
        val routes = object : MachineRoutes {
            override suspend fun <T> on(machineId: String, call: suspend (MachineManagement) -> T): T = error("no machine call")
        }
        val shared = UsageAccount("claude:team", name = "team")
        var work: suspend () -> List<UsageAccount> = { listOf(UsageAccount("codex:work"), shared) }
        var home: suspend () -> List<UsageAccount> = { listOf(UsageAccount("claude:home"), shared) }
        val managed = mergedMachineManagement(routes, usage = {
            listOf(GroupUsageRead("", { home() }), GroupUsageRead("g0a1b2c3d", { work() }))
        })
        // In the groups' order; an account both hubs report shows once in each, with no merging across groups.
        assertEquals(listOf("claude:home", "claude:team", "codex:work", "claude:team"), managed.usageWindows().map { it.id })

        // A hub that can't be read just now keeps its last answer while the other shows its new one.
        work = { throw HubFailure(HubError.Unreachable, status = 503) }
        home = { listOf(UsageAccount("claude:home", name = "renewed")) }
        assertEquals(listOf("renewed", "", "team"), managed.usageWindows().map { it.name })
        // An old hub without the route has none; a group that went away under the read has none either.
        work = { throw HubFailure(HubError.Server, status = 404) }
        assertEquals(listOf("claude:home"), managed.usageWindows().map { it.id })
        home = { throw kotlinx.coroutines.CancellationException("Pairing changed") }
        work = { listOf(UsageAccount("codex:work")) }
        assertEquals(listOf("codex:work"), managed.usageWindows().map { it.id })
    }

    @Test fun anotherGroupsTerminalSendsPlainIdsAndHearsMergedOnes() = runBlocking {
        val sent = mutableListOf<String>()
        val hub = object : TerminalTransport {
            override val connection = MutableStateFlow(ConnectionState.Online)
            override val features = MutableStateFlow(setOf("terminal_stream"))
            override val events = flowOf<ServerEvent>(TerminalEvent("desk", "pane:w1:p2"))
            override fun subscribe(key: SessionKey, epoch: String?, after: Int, size: TerminalSize?, takeover: Boolean) = sent.add("subscribe $key")
            override fun unsubscribe(key: SessionKey) { sent.add("unsubscribe $key") }
            override fun input(key: SessionKey, text: String, size: TerminalSize?, scroll: Int, at: TerminalCellPosition?, click: TerminalCellPosition?) =
                sent.add("input $key $text")
            override suspend fun screen(key: SessionKey) = throw UnsupportedOperationException()
        }
        val merged = hub.inGroup(other)
        val pane = SessionKey("desk~g0a1b2c3d", "pane:w1:p2")
        merged.subscribe(pane, null, 0, null)
        merged.input(pane, "ls")
        merged.unsubscribe(pane)
        // The pane address passes as it is; only the machine loses its suffix.
        val plain = SessionKey("desk", "pane:w1:p2")
        assertEquals(listOf("subscribe $plain", "input $plain ls", "unsubscribe $plain"), sent)
        assertEquals(TerminalEvent("desk~g0a1b2c3d", "pane:w1:p2"), merged.events.first())
        assertSame(hub.features, merged.features)
        assertSame(hub, hub.inGroup(primary))
    }

    @Test fun fleetsAddUpAndTheMostUrgentPhaseWins() {
        val one = FleetState(phase = "working", working = 2, items = listOf(FleetItem("a")))
        assertSame(one, mergeFleets(listOf(one), one))
        val merged = mergeFleets(listOf(one, FleetState(phase = "needsYou", needsYou = 1, unread = 3, items = listOf(FleetItem("b")))), one)
        assertEquals("needsYou", merged.phase)
        assertEquals(1, merged.needsYou)
        assertEquals(2, merged.working)
        assertEquals(3, merged.unread)
        assertEquals(listOf("a", "b"), merged.items.map { it.id })
    }

    @Test fun anotherGroupsRequestComesBeforeThisGroupsWorkingAgents() {
        // The ongoing notification and the widget draw only the first few items.
        val home = FleetState(phase = "working", working = 3, items = listOf(FleetItem("mini/a", kind = "working"),
            FleetItem("mini/b", kind = "working"), FleetItem("mini/c", kind = "working")))
        val work = FleetState(phase = "needsYou", needsYou = 1, working = 1, items = listOf(FleetItem("macbook~g0a1b2c3d/s2", kind = "working"),
            FleetItem("macbook~g0a1b2c3d/s1", kind = "question")))
        val merged = mergeFleets(listOf(home, work), home)
        assertEquals("macbook~g0a1b2c3d/s1", merged.items.first().id)
        assertEquals(listOf("macbook~g0a1b2c3d/s1", "mini/a", "mini/b", "mini/c", "macbook~g0a1b2c3d/s2"), merged.items.map { it.id })
    }

    @Test fun anotherGroupsFleetItemsCarryTheSuffixInTheirSessionAndInteraction() {
        val fleet = FleetState(phase = "needsYou", needsYou = 1,
            items = listOf(FleetItem("macbook/s1", kind = "permission", interaction = "i1")))
        assertSame(fleet, primary.fleet(fleet))
        val item = other.fleet(fleet).items.single()
        // The notification opens and answers by these: the plain ids would reach the primary group's hub (Gone).
        assertEquals(SessionKey("macbook~g0a1b2c3d", "s1"), persona.core.domain.fleet.FleetSummary.key(item))
        assertEquals("i1~g0a1b2c3d", item.interaction)
    }

    @Test fun aFetchedFleetIsTranslatedAndMergedWithTheOtherGroupsPushedOne() {
        val fleets = mutableMapOf<String, FleetState>()
        // The primary group's pushed fleet, then the other group's fetched one in its own plain ids.
        fleets.putFleet("home", primary, FleetState(phase = "working", working = 2, items = listOf(FleetItem("mini/a", kind = "working"))),
            listOf("home", "work"), "home")
        val merged = fleets.putFleet("work", other, FleetState(phase = "needsYou", needsYou = 1,
            items = listOf(FleetItem("macbook/s1", kind = "permission", interaction = "i1"))), listOf("home", "work"), "home")
        assertEquals("needsYou", merged.phase)
        assertEquals(1, merged.needsYou)
        assertEquals(2, merged.working)
        assertEquals(listOf("macbook~g0a1b2c3d/s1", "mini/a"), merged.items.map { it.id })
        assertEquals("i1~g0a1b2c3d", merged.items.first().interaction)
        // A group that went away drops out of the merge.
        assertEquals(listOf("macbook~g0a1b2c3d/s1"), fleets.putFleet("work", other, FleetState(needsYou = 1,
            items = listOf(FleetItem("macbook/s1"))), listOf("work"), "work").items.map { it.id })
    }

    @Test fun aGroupThatGoesAwayTakesItsEntriesOutOfTheMergedFleet() {
        val fleets = mutableMapOf<String, FleetState>()
        fleets.putFleet("home", primary, FleetState(phase = "working", working = 1, items = listOf(FleetItem("mini/a", kind = "working"))),
            listOf("home", "work"), "home")
        assertEquals(1, fleets.putFleet("work", other, FleetState(phase = "needsYou", needsYou = 1,
            items = listOf(FleetItem("macbook/s1", kind = "permission", interaction = "i1"))), listOf("home", "work"), "home").needsYou)
        // Disconnecting the other group: its permission request (and Allow button, answered by no hub now) leaves.
        val merged = fleets.retainFleets(listOf("home"), "home")!!
        assertEquals("working", merged.phase)
        assertEquals(0, merged.needsYou)
        assertEquals(listOf("mini/a"), merged.items.map { it.id })
        assertEquals(setOf("home"), fleets.keys)
        assertNull(fleets.retainFleets(listOf("home"), "home"))
        // The last known fleet going leaves an empty one, not the stale one.
        val alone = mutableMapOf("work" to other.fleet(FleetState(phase = "needsYou", needsYou = 1, items = listOf(FleetItem("macbook/s1")))))
        assertEquals(FleetState(), alone.retainFleets(listOf("home"), "home"))
    }
}
