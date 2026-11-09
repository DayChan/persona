import Foundation
import Testing
@testable import Persona

/// Settings › Extensions' logic (ExtensionsModel, ExtensionModel): what the hub sends decodes, the form's draft is edited by path and
/// saved as a whole without the emptied fields, a field the hub refuses comes back beside that field.
@MainActor
struct ExtensionsModelTests {
    private static let detail = #"""
    {"id":"example-notes","name":"Example Notes","kind":"connectors","status":"needs_setup","version":"1.2.0","source":"installed",
     "enabled":true,"contributes":{"connectors":["examplenotes"]},"needs_setup":["密钥「API Key」"],"previous_version":"1.1.0",
     "form":{"fields":[
       {"name":"base_url","type":"string","title":"服务地址","format":"uri","default":"https://notes.example.com","required":true},
       {"name":"folders","type":"array","items":{"name":"items","type":"string"}},
       {"name":"every_minutes","type":"integer","minimum":5,"default":30},
       {"name":"mode","type":"string","enum":[{"value":"fast","title":"只要新的"},{"value":"safe"}]},
       {"name":"proxy","type":"object","title":"代理","fields":[{"name":"host","type":"string"},{"name":"port","type":"integer"}]}]},
     "config":{"folders":["工作"]},
     "secrets":[{"name":"api_key","title":"API Key","required":true,"set":false}],
     "permissions":[{"key":"runs_code","title":"在 hub 机器上运行程序","detail":"…"}]}
    """#

    private func make(_ hub: FakeHub) -> ExtensionModel { ExtensionModel(id: "example-notes", deps: .init(api: { hub.client })) }

    @Test func listDecodesAndSummarizes() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins", json: #"{"plugins":[{"id":"voice","name":"语音","kind":"voice","status":"running"},"# +
               #"{"id":"example-notes","name":"Example Notes","kind":"connectors","status":"needs_setup","source":"installed","enabled":true}],"can_install":true}"#)
        let page = ExtensionsModel(deps: .init(api: { hub.client }))
        await page.load()
        #expect(page.error == nil && page.plugins?.count == 2)
        #expect(ExtensionText.listSummary(page.plugins) == "2 个 · 1 个要处理")
        #expect(ExtensionText.summary(page.plugins![0]) == "启动")
        #expect(ExtensionText.listSummary([]) == "没有")
    }

    /// On or off to the user (#1684): running and idle are both On, and only the detail's small print tells them apart;
    /// the other states keep their words.
    @Test func statesReadAsOnOrOff() throws {
        func plugin(_ status: String) throws -> HubExtension {
            try JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"p","name":"P","kind":"connectors","status":"\#(status)"}"#.utf8))
        }
        let words = try ["running", "stopped", "disabled", "starting", "needs_setup", "incompatible", "error", "new"].map { try ExtensionText.state(plugin($0)) }
        #expect(words == ["启动", "启动", "停用", "正在启动", "需要设置", "这台 hub 用不了", "出错了", "状态未知"])
        #expect(try ExtensionText.runNote(plugin("running")) == "正在运行")
        #expect(try ExtensionText.runNote(plugin("stopped")) == "暂未运行，用到时自动启动")
        for status in ["disabled", "starting", "needs_setup", "incompatible", "error"] {
            #expect(try ExtensionText.runNote(plugin(status)) == nil)
        }
    }

    @Test func detailDecodesTheForm() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/example-notes/-/", json: Self.detail)
        let page = make(hub)
        await page.load()
        let d = try #require(page.detail)
        #expect(d.form.map(\.name) == ["base_url", "folders", "every_minutes", "mode", "proxy"])
        #expect(d.form[0].required == true && d.form[0].default == .string("https://notes.example.com"))
        #expect(d.form[3].enum?.map(\.label) == ["只要新的", "safe"])
        #expect(d.form[4].fields?.map(\.name) == ["host", "port"])
        #expect(d.secrets.first?.set == false && d.previousVersion == "1.1.0" && !d.readOnly)
        #expect(d.plugin.needsSetup == ["密钥「API Key」"])
        #expect(page.draft == ["folders": .array([.string("工作")])] && !page.dirty)
    }

    @Test func theDraftIsSavedAsAWholeWithoutEmptiedFields() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/example-notes/-/", json: Self.detail)
        var sent: JSONValue?
        hub.on("PUT", "/api/v1/plugins/example-notes/-/config") { req in
            sent = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            return Data(Self.detail.utf8)
        }
        let page = make(hub)
        await page.load()
        page.set(.string("db.example.com"), at: ["proxy", "host"])
        page.set(.string(""), at: ["mode"])
        page.set(nil, at: ["folders"])
        page.numberText["every_minutes"] = "1o"
        #expect(page.dirty && !page.canSave && page.fieldError(["every_minutes"]) == "要是一个数")
        page.numberText["every_minutes"] = "15"
        page.set(.number(15), at: ["every_minutes"])
        #expect(page.canSave)
        #expect(await page.save())
        #expect(sent == .object(["config": .object(["proxy": .object(["host": .string("db.example.com")]), "every_minutes": .number(15)])]))
        // An emptied group goes too.
        page.set(nil, at: ["proxy", "host"])
        #expect(page.draft["proxy"] == nil)
    }

    /// A machine's switch (format machine) is saved at once on top of what the hub has: other edits stay in the draft,
    /// unsaved, and the switch's field is not left to Save.
    @Test func aMachineSwitchIsSavedAtOnceAlone() async throws {
        let hub = FakeHub()
        let cua = #"{"id":"cua","name":"操控电脑（Cua）","kind":"computer","status":"running","enabled":true,"contributes":{"computer":["cua"]},"# +
            #""form":{"fields":[{"name":"machines","type":"array","items":{"name":"items","type":"string","format":"machine","os":["darwin"]}},"# +
            #"{"name":"confirm","type":"boolean"}]},"config":{"machines":["macbook"]},"secrets":[],"permissions":[]}"#
        hub.on("GET", "/api/v1/plugins/cua/-/", json: cua)
        var sent: JSONValue?
        hub.on("PUT", "/api/v1/plugins/cua/-/config") { req in
            sent = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            return Data(cua.replacingOccurrences(of: #""machines":["macbook"]"#, with: #""machines":["macbook","studio"]"#).utf8)
        }
        let page = ExtensionModel(id: "cua", deps: .init(api: { hub.client }))
        await page.load()
        let field = try #require(page.detail?.form.first)
        #expect(field.items?.format == "machine" && field.items?.os == ["darwin"])
        page.set(.bool(true), at: ["confirm"]) // being edited, not saved
        await page.saveNow(.array([.string("macbook"), .string("studio")]), at: ["machines"])
        #expect(sent == .object(["config": .object(["machines": .array([.string("macbook"), .string("studio")])])]),
                "only the switch's field, on top of what the hub had")
        #expect(page.value(["machines"]) == .array([.string("macbook"), .string("studio")]))
        #expect(page.value(["confirm"]) == .bool(true) && page.dirty, "the other edit is still there to 保存")
    }

    /// The footer says which switches take effect at once and that the rest waits for Save.
    @Test func theFooterSaysWhatIsSavedAtOnce() {
        func field(_ name: String, _ type: String, machines: Bool = false) -> ExtensionField {
            ExtensionField(name: name, type: type, title: name == "machines" ? "能操控的电脑" : nil,
                        items: machines ? ExtensionItems(type: "string", format: "machine") : nil)
        }
        #expect(ExtensionText.saveFooter([field("url", "string")])?.hasPrefix("改完点右上角「保存」") == true)
        #expect(ExtensionText.saveFooter([field("machines", "array", machines: true)]) == nil)
        #expect(ExtensionText.saveFooter([field("machines", "array", machines: true), field("confirm", "boolean")])?
            .hasPrefix("「能操控的电脑」的开关即时生效；其他项改完点右上角「保存」") == true)
    }

    @Test func aRefusedFieldComesBackBesideIt() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/example-notes/-/", json: Self.detail)
        hub.on("PUT", "/api/v1/plugins/example-notes/-/config") { _ in
            throw APIError(status: 400, message: "proxy.port: 要是一个整数",
                           fields: ["code": "invalid_config", "field": "proxy.port", "message": "要是一个整数"])
        }
        let page = make(hub)
        await page.load()
        page.set(.number(1.5), at: ["proxy", "port"])
        #expect(await page.save() == false)
        #expect(page.fieldError(["proxy", "port"]) == "要是一个整数" && page.error == nil)
        #expect(page.value(["proxy", "port"]) == .number(1.5), "the draft is kept")
    }

    /// A failed immediate save leaves its field's refusal visible; saving that field again clears it and takes the
    /// hub's new value. Other fields' errors are not discarded by an unrelated machine switch.
    @Test func aSuccessfulMachineRetryClearsItsOldFieldError() async throws {
        let hub = FakeHub()
        let cua = #"{"id":"cua","name":"Cua","kind":"computer","status":"running","enabled":true,"# +
            #""form":{"fields":[{"name":"machines","type":"array","max_items":1,"items":{"type":"string","format":"machine"}},"# +
            #"{"name":"confirm","type":"boolean"}]},"config":{"machines":["macbook"],"confirm":false},"secrets":[],"permissions":[]}"#
        hub.on("GET", "/api/v1/plugins/cua/-/", json: cua)
        var attempts = 0
        hub.on("PUT", "/api/v1/plugins/cua/-/config") { _ in
            attempts += 1
            if attempts == 1 {
                throw APIError(status: 400, message: "machines: refused", fields: ["code": "invalid_config", "field": "machines", "message": "Choose one Mac"])
            }
            return Data(cua.replacingOccurrences(of: #""machines":["macbook"]"#, with: #""machines":["studio"]"#).utf8)
        }
        let page = ExtensionModel(id: "cua", deps: .init(api: { hub.client }))
        await page.load()
        page.set(.bool(true), at: ["confirm"]) // another unsaved edit survives both attempts
        await page.saveNow(.array([.string("macbook"), .string("studio")]), at: ["machines"])
        #expect(page.fieldError(["machines"]) == "Choose one Mac")
        #expect(page.value(["machines"]) == .array([.string("macbook")]))
        await page.saveNow(.array([.string("studio")]), at: ["machines"])
        #expect(page.fieldError(["machines"]) == nil)
        #expect(page.value(["machines"]) == .array([.string("studio")]))
        #expect(page.value(["confirm"]) == .bool(true) && page.dirty)
    }

    @Test func aSecretIsSentAndOnlyItsStateComesBack() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/example-notes/-/", json: Self.detail)
        var body: String?
        hub.on("PUT", "/api/v1/plugins/example-notes/-/secrets/api_key") { req in
            body = String(decoding: req.body ?? Data(), as: UTF8.self)
            return Data(Self.detail.replacingOccurrences(of: #""set":false"#, with: #""set":true"#).utf8)
        }
        let page = make(hub)
        await page.load()
        #expect(await page.setSecret("api_key", value: "   ") == .failed, "nothing to save")
        #expect(await page.setSecret("api_key", value: " k3y \n") == .saved)
        #expect(body == #"{"value":"k3y"}"#)
        #expect(page.detail?.secrets.first?.set == true)
    }

    /// A plugin with a program on computers: the field picking them, and how the program is on each (#1830).
    @Test func nodeProgramsDecodeAndSayHowTheyAre() throws {
        let json = #"""
        {"id": "example-helper", "name": "Example 助手", "kind": "providers", "status": "running", "node_machines": "computers",
         "nodes": [{"machine_id": "macbook", "version": "1.0.0", "state": "running"},
                   {"machine_id": "studio", "state": "error", "error": "exit status 3"}],
         "form": {"fields": []}, "permissions": []}
        """#
        let d = try JSONDecoder().decode(HubExtensionDetail.self, from: Data(json.utf8))
        #expect(d.nodeMachines == "computers")
        #expect(d.plugin.nodes?.map(\.machineID) == ["macbook", "studio"])
        #expect(ExtensionNodeState.status(d.plugin.nodes?[0]) == "程序在运行 · 1.0.0")
        #expect(ExtensionNodeState.status(d.plugin.nodes?[1]) == "程序出错：退出码 3")
        #expect(ExtensionNodeState.exitText("1.0.0 没有起来：exit status 3") == "1.0.0 没有起来：退出码 3")
        #expect(ExtensionNodeState.exitText("signal: killed") == "被信号结束（killed）")
        #expect(ExtensionNodeState.status(nil) == "等 hub 把程序发过去")
        #expect(ExtensionNodeState.status(ExtensionNodeState(machineID: "x", state: "outdated")).contains("personad 太旧"))
    }

}
