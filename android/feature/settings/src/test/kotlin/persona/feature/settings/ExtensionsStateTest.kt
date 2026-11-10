package persona.feature.settings

import persona.core.contracts.api.*
import persona.core.designsystem.Status
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.yield
import persona.core.contracts.MachineComputer
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4

private class FakeExtensions(var detail: ExtensionDetail) : ExtensionApiStub() {
    val configs = mutableListOf<JsonObject>()
    val secrets = mutableListOf<Pair<String, String?>>()
    var list: List<HubExtension> = listOf(detail.extension)
    var refuse: ExtensionConfigRejected? = null
    var fail: Exception? = null
    val autoUpgrades = mutableListOf<Boolean>()
    val uninstalls = mutableListOf<Boolean>()
    /** Enabled extensions that depend on it: refused (409 has_dependents) without cascade. */
    var dependents: List<ExtensionDependent> = emptyList()
    val cascades = mutableListOf<String>()

    private fun answer(): ExtensionDetail { fail?.let { throw it }; return detail }
    override suspend fun extensions() = fail?.let { throw it } ?: list
    override suspend fun extension(id: String) = answer()
    override suspend fun setEnabled(id: String, enabled: Boolean, cascade: Boolean): ExtensionDetail {
        if (!enabled) refuseDependents("off", cascade)
        detail = detail.copy(extension = detail.extension.copy(enabled = enabled, status = if (enabled) "running" else "disabled"))
        return answer()
    }
    override suspend fun setConfig(id: String, config: JsonObject): ExtensionDetail {
        configs += config
        refuse?.let { throw it }
        detail = detail.copy(config = config)
        return answer()
    }
    override suspend fun setSecret(id: String, name: String, value: String): ExtensionDetail {
        secrets += name to value
        detail = detail.copy(secrets = detail.secrets.map { if (it.name == name) it.copy(set = true) else it })
        return answer()
    }
    override suspend fun setAutoUpgrade(id: String, auto: Boolean): ExtensionDetail {
        autoUpgrades += auto
        fail?.let { throw it }
        detail = detail.copy(extension = detail.extension.copy(autoUpgrade = auto))
        return detail
    }
    override suspend fun uninstall(id: String, keepData: Boolean, cascade: Boolean) {
        refuseDependents("uninstall", cascade)
        uninstalls += keepData; fail?.let { throw it }
    }
    private fun refuseDependents(call: String, cascade: Boolean) {
        cascades += "$call cascade=$cascade"
        if (dependents.isNotEmpty() && !cascade) throw ExtensionHasDependents(dependents, "B depends on it")
    }
    override suspend fun clearSecret(id: String, name: String): ExtensionDetail {
        secrets += name to null
        detail = detail.copy(secrets = detail.secrets.map { if (it.name == name) it.copy(set = false) else it })
        return answer()
    }
}

/** Robolectric for the words: the app's strings in Simplified Chinese (robolectric.properties). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ExtensionsStateTest {
    private val res get() = ApplicationProvider.getApplicationContext<android.content.Context>().resources

    private val form = listOf(
        ExtensionField("base_url", "string", title = "服务地址", format = "uri", default = JsonPrimitive("https://notes.example.com"), required = true),
        ExtensionField("folders", "array", items = ExtensionItems("string")),
        ExtensionField("every", "integer", default = JsonPrimitive(30)),
        ExtensionField("ratio", "number"),
        ExtensionField("mode", "string", enum = listOf(ExtensionChoice(JsonPrimitive("fast"), "只要新的"), ExtensionChoice(JsonPrimitive("safe")))),
        ExtensionField("proxy", "object", title = "代理", fields = listOf(ExtensionField("host", "string"), ExtensionField("port", "integer"))),
    )
    private val notes = ExtensionDetail(
        HubExtension("example-notes", "Example Notes", "connectors", "needs_setup", version = "1.2.0", source = "installed", enabled = true),
        form = form, config = buildJsonObject { putJsonArray("folders") { add("工作") } },
        secrets = listOf(ExtensionSecret("api_key", "API Key", required = true)),
    )

    @Test fun wordsForStatesSourcesAndTheSettingsRow() {
        fun p(status: String) = HubExtension("x", "X", "connectors", status)
        assertEquals(listOf("启动", "启动", "停用"), listOf(p("running"), p("stopped"), p("disabled")).map { ExtensionText.state(res, it) })
        assertEquals(listOf("正在运行", "暂未运行，用到时自动启动", null), listOf(p("running"), p("stopped"), p("error")).map { ExtensionText.runNote(res, it) })
        assertEquals(Status.Unread, ExtensionText.status(p("stopped")))
        assertEquals("这台 hub 用不了", ExtensionText.state(res, p("incompatible")))
        assertEquals("状态未知", ExtensionText.state(res, p("new_state")))
        assertTrue(ExtensionText.isProblem(p("error")) && ExtensionText.isProblem(p("incompatible")))
        assertEquals(Status.NeedsYou, ExtensionText.status(p("needs_setup")))
        assertEquals(Status.Unread, ExtensionText.status(p("running")))
        assertEquals("内置", ExtensionText.source(res, "builtin"))
        assertNull(ExtensionText.source(res, null))
        assertEquals("连接器", ExtensionText.point(res, "connectors"))
        assertEquals("界面", ExtensionText.point(res, "ui"))
        assertEquals("future_point", ExtensionText.point(res, "future_point"))
        assertEquals("需要设置 · 1.2.0 · 已安装", ExtensionText.stateLine(res, notes.extension))
        assertNull(ExtensionText.listSummary(res, null))
        assertEquals("没有", ExtensionText.listSummary(res, emptyList()))
        assertEquals("3 个", ExtensionText.listSummary(res, listOf(p("running"), p("disabled"), p("stopped"))))
        assertEquals("3 个 · 2 个要处理", ExtensionText.listSummary(res, listOf(p("running"), p("needs_setup"), p("error"))))
    }

    @Test fun fieldTitlesPlaceholdersAndSecrets() {
        assertEquals("服务地址", ExtensionText.fieldTitle(res, form[0]))
        assertEquals("host（必填）", ExtensionText.fieldTitle(res, ExtensionField("host", required = true)))
        assertEquals("默认：https://notes.example.com", ExtensionText.placeholder(res, form[0]))
        assertEquals("默认：只要新的", ExtensionText.placeholder(res, form[4].copy(default = JsonPrimitive("fast"))))
        assertEquals("未设置", ExtensionText.placeholder(res, form[3]))
        assertEquals("未设置（必填）", ExtensionText.secretState(res, notes.secrets[0]))
        assertEquals("已设置", ExtensionText.secretState(res, notes.secrets[0].copy(set = true)))
        assertEquals("未设置", ExtensionText.secretState(res, ExtensionSecret("x", "X")))
    }

    /** x-confirm asks only before a switch is turned on, and only when the field has a sentence. */
    @Test fun aSwitchAsksItsConfirmOnlyWhenTurnedOn() {
        val asking = ExtensionField("agent", "boolean", confirm = "Replies go out as you.")
        assertTrue(asking.asksFirst(true))
        assertFalse(asking.asksFirst(false))
        assertFalse(ExtensionField("agent", "boolean").asksFirst(true))
        assertFalse(asking.copy(confirm = " ").asksFirst(true))
    }

    @Test fun theDraftSetsByPathAndDropsEmptyGroups() {
        var draft = JsonObject(emptyMap())
        draft = ExtensionDraft.set(draft, listOf("proxy", "host"), JsonPrimitive("h"))
        assertEquals(JsonPrimitive("h"), ExtensionDraft.value(draft, listOf("proxy", "host")))
        draft = ExtensionDraft.set(draft, listOf("proxy", "host"), null)
        assertFalse("proxy" in draft)
        assertNull(ExtensionDraft.value(buildJsonObject { put("a", JsonNull) }, listOf("a")))
        val compact = ExtensionDraft.compact(buildJsonObject {
            put("empty", ""); putJsonArray("none") {}; put("keep", "x"); put("zero", 0); put("off", false)
            putJsonObject("group") { put("blank", "") }; putJsonObject("kept") { put("n", 1) }
        })
        assertEquals(buildJsonObject { put("keep", "x"); put("zero", 0); put("off", false); putJsonObject("kept") { put("n", 1) } }, compact)
        assertEquals(setOf("base_url", "folders", "every", "ratio", "mode", "proxy.host", "proxy.port"), ExtensionDraft.paths(form))
    }

    @Test fun numbersAreTypedAsTextUntilTheyReadAsOne() {
        assertEquals(JsonPrimitive(30L) to null, ExtensionDraft.number(res, " 30 ", integer = true))
        assertEquals(null to "要是一个整数", ExtensionDraft.number(res, "1.5", integer = true))
        assertEquals(JsonPrimitive(1.5) to null, ExtensionDraft.number(res, "1.5", integer = false))
        assertEquals(JsonPrimitive(2L) to null, ExtensionDraft.number(res, "2.0", integer = false))
        assertEquals(null to "要是一个数", ExtensionDraft.number(res, "abc", integer = false))
        assertEquals(null to null, ExtensionDraft.number(res, "  ", integer = true))
    }

    @Test fun editsAreSavedWholeAndCompacted() = runBlocking {
        val api = FakeExtensions(notes)
        val model = ExtensionModel(api, "example-notes", res)
        model.load()
        assertFalse(model.state.value.dirty)
        assertTrue(model.state.value.editable)
        model.edit(listOf("base_url"), JsonPrimitive("https://x.example.com"))
        model.edit(listOf("folders"), null)
        model.edit(listOf("proxy", "host"), JsonPrimitive(""))
        model.editNumber(listOf("every"), "45", integer = true)
        assertTrue(model.state.value.canSave)
        assertTrue(model.save())
        assertEquals(buildJsonObject { put("base_url", "https://x.example.com"); put("every", 45) }, api.configs.single())
        assertFalse(model.state.value.dirty)
        assertTrue(model.state.value.numberText.isEmpty())
    }

    @Test fun aNumberThatIsNotOneKeepsSavingOff() = runBlocking {
        val model = ExtensionModel(FakeExtensions(notes), "example-notes", res)
        model.load()
        model.editNumber(listOf("proxy", "port"), "80x", integer = true)
        assertEquals("要是一个整数", model.state.value.fieldError(listOf("proxy", "port")))
        assertTrue(model.state.value.dirty)
        assertFalse(model.state.value.canSave)
        assertFalse(model.save())
        model.editNumber(listOf("proxy", "port"), "8080", integer = true)
        assertNull(model.state.value.fieldError(listOf("proxy", "port")))
        assertTrue(model.state.value.canSave)
    }

    @Test fun aRefusedFieldShowsBesideItAndTheDraftStays() = runBlocking {
        val api = FakeExtensions(notes).apply { refuse = ExtensionConfigRejected("base_url", "要是一个网址") }
        val model = ExtensionModel(api, "example-notes", res)
        model.load()
        model.edit(listOf("base_url"), JsonPrimitive("not a url"))
        assertFalse(model.save())
        assertEquals("要是一个网址", model.state.value.fieldError(listOf("base_url")))
        assertEquals(JsonPrimitive("not a url"), model.state.value.value(listOf("base_url")))
        assertNull(model.state.value.error)
        // Editing that field clears the hub's word on it.
        model.edit(listOf("base_url"), JsonPrimitive("https://ok.example.com"))
        assertNull(model.state.value.fieldError(listOf("base_url")))

        // A field the form does not draw goes on top instead.
        api.refuse = ExtensionConfigRejected("", "配置不对")
        assertFalse(model.save())
        assertEquals("没有保存：配置不对", model.state.value.error)
    }

    @Test fun anUnsavedEditSurvivesTheSwitchAndSecrets() = runBlocking {
        val api = FakeExtensions(notes)
        val model = ExtensionModel(api, "example-notes", res)
        model.load()
        model.edit(listOf("mode"), JsonPrimitive("fast"))
        model.setEnabled(false)
        assertEquals(false, model.state.value.detail?.extension?.enabled)
        assertEquals(JsonPrimitive("fast"), model.state.value.value(listOf("mode")))
        assertTrue(model.setSecret("api_key", "  s3cret \n"))
        assertEquals("api_key" to "s3cret", api.secrets.single())
        assertTrue(model.state.value.detail!!.secrets.single().set)
        assertFalse(model.setSecret("api_key", "   "))
        assertTrue(model.clearSecret("api_key"))
        assertFalse(model.state.value.detail!!.secrets.single().set)
        assertEquals(JsonPrimitive("fast"), model.state.value.value(listOf("mode")))
    }

    @Test fun failuresSayWhatDidNotHappen() = runBlocking {
        val api = FakeExtensions(notes).apply { fail = HubFailure(HubError.Server, "扩展不存在", status = 404) }
        val model = ExtensionModel(api, "example-notes", res)
        model.load()
        assertEquals("读取失败：扩展不存在", model.state.value.error)
        api.fail = null
        model.load()
        assertNull(model.state.value.error)
        api.fail = HubFailure(HubError.Server, "hub 拒绝了", status = 500)
        model.setEnabled(false)
        assertEquals("没有改成：hub 拒绝了", model.state.value.error)
    }

    @Test fun aHubJsonExtensionIsNotEditable() = runBlocking {
        val model = ExtensionModel(FakeExtensions(ExtensionDetail(HubExtension("voice", "语音", "voice", "running", source = "hub.json"), readOnly = true)), "voice", res)
        model.load()
        assertFalse(model.state.value.editable)
        assertFalse(model.state.value.canSave)
    }

    @Test fun theListLoadsAndReportsFailures() = runBlocking {
        val api = FakeExtensions(notes)
        val list = ExtensionListModel(api, res)
        list.load()
        assertEquals(listOf("example-notes"), list.state.value.extensions?.map { it.id })
        api.fail = HubFailure(HubError.Server, "坏了", status = 500)
        list.load()
        assertEquals("读取失败：坏了", list.state.value.error)
        assertEquals(1, list.state.value.extensions?.size)
    }

    @Test fun theListKnowsWhetherTheHubHasMarkets() = runBlocking {
        val api = object : ExtensionApiStub() {
            override suspend fun extensions() = listOf(notes.extension)
            override suspend fun listing() = ExtensionListing(extensions(), canInstall = true, markets = true)
        }
        val list = ExtensionListModel(api, res)
        list.load()
        assertTrue(list.state.value.markets)
        // An older contract (no listing of its own) reads as no markets.
        val old = ExtensionListModel(object : ExtensionApiStub() { override suspend fun extensions() = listOf(notes.extension) }, res)
        old.load()
        assertFalse(old.state.value.markets)
    }

    private val machinesField = ExtensionField("machines", "array", title = "在哪些 Mac 上用",
        items = ExtensionItems("string", format = "machine", os = listOf("darwin")), confirm = "扩展开着时这台 Mac 不会自动锁屏。")
    private val cua = ExtensionDetail(
        HubExtension("cua", "操控电脑", "computer", "running", enabled = true,
            nodes = listOf(ExtensionNodeState("mac-a", version = "0.3.0", state = "running"))),
        form = listOf(machinesField, ExtensionField("note", "string")), config = buildJsonObject { putJsonArray("machines") { add("mac-a"); add("gone") } },
        nodeMachines = "machines", origin = "market:m1/cua",
    )

    @Test fun upgradeWords() {
        val u = ExtensionUpgrade("example-ui", market = "m1", from = "1.0.0", to = "1.2.0", state = "needs_permission",
            addedPermissions = listOf(ExtensionPermission("net", "访问更多网络", "api.example.com")))
        assertEquals("要多 1 项权限：访问更多网络（api.example.com）", ExtensionText.upgradeDetail(res, u))
        assertEquals("去确认", ExtensionText.upgradeAction(res, u))
        assertTrue(ExtensionText.upgradeAttention(u))
        assertEquals("去升级", ExtensionText.upgradeAction(res, u.copy(state = "failed")))
        assertEquals("没装上：校验不过", ExtensionText.upgradeDetail(res, u.copy(state = "failed", detail = "校验不过")))
        assertNull(ExtensionText.upgradeAction(res, u.copy(state = "waiting_idle")))
        assertEquals("扩展空闲时自动升级", ExtensionText.upgradeDetail(res, u.copy(state = "waiting_idle")))
        assertEquals("上次升级后起不来，已回到 1.0.0，不再自动升级到这个版本", ExtensionText.upgradeDetail(res, u.copy(state = "skipped")))
        // A version the user rolled back from by hand (#1826) says so, not "didn't start".
        assertEquals("你从这个版本回滚到了 1.0.0，不会再自动升级到它", ExtensionText.upgradeDetail(res, u.copy(state = "skipped", skippedBy = "user")))
        assertEquals("已自动升级到 1.2.0（原来是 1.0.0）", ExtensionText.upgradeNote(res, ExtensionUpgradeNote("upgraded", "1.0.0", "1.2.0")))
        assertEquals("1.1.0 起不来，已回到 1.0.0", ExtensionText.upgradeNote(res, ExtensionUpgradeNote("rolled_back", "1.0.0", "1.1.0")))
        assertNull(ExtensionText.upgradeNote(res, ExtensionUpgradeNote("needs_permission", "1.0.0", "1.2.0")))
        val ext = HubExtension("example-ui", "Example UI", "ui", "running", upgrade = u)
        assertEquals(ExtensionText.state(res, ext) + " · 新版本待确认", ExtensionText.listLine(res, ext))
        assertEquals(ExtensionText.state(res, ext), ExtensionText.listLine(res, ext.copy(upgrade = u.copy(state = "available"))))
    }

    // Pausing execution never changes the manager's persisted preference.
    @Test fun theAutoUpgradeSwitchKeepsItsSavedValueWhilePaused() {
        val ext = HubExtension("example-ui", "Example UI", "ui", "running")
        for ((own, paused, shown) in listOf(
            Triple(true, null, true),
            Triple(false, null, false),
            Triple(true, "disabled", true),
            Triple(false, "disabled", false),
            Triple(true, "market_removed", true),
            Triple(false, "market_removed", false),
        )) {
            assertEquals("own $own, paused $paused", shown, ExtensionText.autoUpgradeShown(ext.copy(autoUpgrade = own, autoUpgradePaused = paused)))
        }
        val decoded = Json { ignoreUnknownKeys = true }.decodeFromString<HubExtension>(
            """{"id":"x","name":"X","kind":"ui","status":"disabled","enabled":false,"auto_upgrade":true,"auto_upgrade_paused":"disabled"}""")
        assertEquals("disabled", decoded.autoUpgradePaused)
        assertTrue(ExtensionText.autoUpgradeShown(decoded))
    }

    @Test fun aMachineListIsASwitchPerMachineSavedAtOnce() = runBlocking {
        val api = FakeExtensions(cua)
        val model = ExtensionModel(api, "cua", res)
        model.load()
        val machines = listOf(ExtensionMachine("mac-a", "工作 Mac", "darwin", true), ExtensionMachine("mac-b", "家里 Mac", "darwin", false),
            ExtensionMachine("box", "Linux 机器", "linux", true))
        val selected = ExtensionMachines.selected(model.state.value, machinesField, listOf("machines"))
        val rows = ExtensionMachines.switches(machinesField, machines, selected)
        // Only Macs, and a value naming no machine still shows (to turn off).
        assertEquals(listOf("mac-a", "mac-b", "gone"), rows.map { it.id })
        assertFalse(rows.last().known)
        assertEquals("在线 · 程序在运行 · 0.3.0", ExtensionText.machineStatus(res, true, cua.extension.nodes!!.first(), picksProgram = true, on = true))
        assertEquals("离线", ExtensionText.machineStatus(res, false, null, picksProgram = true, on = false))
        assertEquals("在线 · 等 hub 把程序发过去", ExtensionText.machineStatus(res, true, null, picksProgram = true, on = true))

        // An unsaved edit elsewhere stays a draft; the switch goes to the hub alone.
        model.edit(listOf("note"), JsonPrimitive("草稿"))
        assertTrue(model.saveNow(listOf("machines"), ExtensionMachines.toggled(selected, "mac-b", true)))
        assertEquals(buildJsonObject { putJsonArray("machines") { add("mac-a"); add("gone"); add("mac-b") } }, api.configs.last())
        assertEquals(JsonPrimitive("草稿"), model.state.value.value(listOf("note")))
        assertTrue(model.state.value.dirty)

        // Refused: the switch goes back and says why.
        api.refuse = ExtensionConfigRejected("machines", "没有这台机器")
        assertFalse(model.saveNow(listOf("machines"), null))
        assertEquals(JsonArray(listOf("mac-a", "gone", "mac-b").map(::JsonPrimitive)), model.state.value.value(listOf("machines")))
        assertEquals("没有保存：没有这台机器", model.state.value.error)
        // Turning the last one off is "not set".
        assertNull(ExtensionMachines.toggled(listOf("mac-a"), "mac-a", false))
    }

    @Test fun anExtensionOperatingComputersSaysHowCuaDriverIsOnEachMac() {
        val ready = MachineComputer(enabled = true, installed = true, daemon = true, accessibility = "granted", screenRecording = "granted", desktop = "active", ready = true)
        fun status(c: MachineComputer?, online: Boolean = true, computers: Boolean = true) =
            ExtensionText.machineStatus(res, online, null, picksProgram = false, on = false, computer = c, computers = computers)
        assertEquals("在线 · Cua 可以用", status(ready))
        assertEquals("在线 · Cua 没装", status(MachineComputer(enabled = true, note = "装 Cua Driver")))
        assertEquals("在线 · Cua 没在运行", status(ready.copy(daemon = false, ready = false, note = "打开 CuaDriver")))
        assertEquals("在线 · Cua 缺权限", status(ready.copy(screenRecording = "missing", ready = false, note = "给录屏权限")))
        assertEquals("在线 · 锁屏了", status(ready.copy(desktop = "locked", ready = false, note = "解锁")))
        assertEquals("在线 · Cua 已装", status(ready.copy(desktop = "away", ready = false, note = "登录")))
        // Never asked (no verdict, no note), offline, or an extension that does not operate computers: presence only.
        assertEquals("在线", status(MachineComputer(enabled = true)))
        assertEquals("离线", status(ready, online = false))
        assertEquals("在线", status(ready, computers = false))
        // The machine's computer reaches its switch.
        val rows = ExtensionMachines.switches(machinesField, listOf(ExtensionMachine("mac-a", "工作 Mac", "darwin", true, ready)), emptyList())
        assertEquals(ready, rows.single().computer)
    }

    @Test fun aFailedAutoUpgradeSwitchSaysWhichWay() = runBlocking {
        val api = FakeExtensions(cua.copy(extension = cua.extension.copy(autoUpgrade = true)))
        val model = ExtensionModel(api, "cua", res)
        model.load()
        api.fail = HubFailure(HubError.Server, "hub 忙", status = 503)
        assertFalse(model.setAutoUpgrade(false))
        assertEquals("没有关掉自动升级：hub 忙", model.state.value.error)
        assertFalse(model.setAutoUpgrade(true))
        assertEquals("没有打开自动升级：hub 忙", model.state.value.error)
    }

    @Test fun theListFollowsWhatItsPageChanged() = runBlocking {
        val api = FakeExtensions(cua)
        val list = ExtensionListModel(api, res)
        list.load()
        assertEquals(listOf("cua"), list.state.value.extensions?.map { it.id })
        val follow = launch(start = CoroutineStart.UNDISPATCHED) { ExtensionChanges.of(api).collect { list.load() } }
        // Another hub's page changing something does not reload this list.
        ExtensionChanges.changed(FakeExtensions(cua))
        val model = ExtensionModel(api, "cua", res)
        model.load()
        api.list = emptyList()
        assertTrue(model.uninstall(keepData = true))
        withTimeout(5_000) { while (list.state.value.extensions?.isNotEmpty() == true) yield() }
        assertEquals(emptyList<HubExtension>(), list.state.value.extensions)
        follow.cancel()
    }

    @Test fun autoUpgradeAndUninstallFromTheExtensionsPage() = runBlocking {
        val api = FakeExtensions(cua.copy(extension = cua.extension.copy(autoUpgrade = true)))
        val model = ExtensionModel(api, "cua", res)
        model.load()
        assertTrue(model.state.value.detail!!.fromMarket)
        assertTrue(model.setAutoUpgrade(false))
        assertEquals(listOf(false), api.autoUpgrades)
        assertEquals(false, model.state.value.detail!!.extension.autoUpgrade)
        assertTrue(model.uninstall(keepData = true))
        assertEquals(listOf(true), api.uninstalls)
        api.fail = HubFailure(HubError.Server, "还有扩展依赖它", status = 409)
        assertFalse(model.uninstall(keepData = false))
        assertEquals("没有卸载：还有扩展依赖它", model.state.value.error)
    }

    @Test fun dependentsAreAskedAboutThenSwitchedOffWithIt() = runBlocking {
        // #5192: switching off or removing one others depend on asks, naming them, then goes on with cascade.
        val api = FakeExtensions(notes).apply { dependents = listOf(ExtensionDependent("ext-b", "B"), ExtensionDependent("ext-c")) }
        val model = ExtensionModel(api, "example-notes", res)
        model.load()
        assertFalse(model.setEnabled(false))
        assertEquals(ExtensionCascade(listOf("B", "ext-c")), model.state.value.cascade)
        assertNull(model.state.value.error)
        assertEquals(true, model.state.value.detail!!.extension.enabled)
        model.dismissCascade()
        assertNull(model.state.value.cascade)
        assertFalse(model.setEnabled(false))
        assertTrue(model.confirmCascade())
        assertNull(model.state.value.cascade)
        assertEquals(false, model.state.value.detail!!.extension.enabled)
        assertEquals(listOf("off cascade=false", "off cascade=false", "off cascade=true"), api.cascades)

        assertFalse(model.uninstall(keepData = false))
        assertEquals(ExtensionCascade(listOf("B", "ext-c"), keepData = false), model.state.value.cascade)
        assertNull(model.state.value.error)
        assertTrue(model.confirmCascade())
        assertEquals(listOf(false), api.uninstalls)
        assertEquals("uninstall cascade=true", api.cascades.last())
        assertEquals("B、ext-c 依赖 Example Notes，会一起停用。", ExtensionText.cascade(res, ExtensionCascade(listOf("B", "ext-c")), "Example Notes"))
    }
}
