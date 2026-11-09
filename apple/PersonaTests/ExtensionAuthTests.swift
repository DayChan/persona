import Foundation
import PersonaKit
import Testing
@testable import Persona

/// Extensions that install command-line programs and sign in (#4507, docs/extensions/extensions.md「命令行程序（tools）」「登录（auth）」):
/// the sign-in decodes from the hub's views and its `extension_auth` push (a state this app does not know reads as not
/// known), the install's `tools` step carries its programs and `next: "auth"`, the words of the list's row, the
/// permission and the About lines, and a sign-in a step at a time against a fake hub: a QR code asked again until done,
/// the store following, signing out.
@MainActor
struct ExtensionAuthTests {
    private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
        try JSON.decoder.decode(T.self, from: Data(json.utf8))
    }

    @Test func theSignInDecodes() throws {
        let a = try decode(ExtensionAuth.self, #"""
        {"title":"飞书登录","description":"用飞书确认身份后才能查","page":"login","state":"signed_in","account":"me@example.com",
         "expires_at":"2026-12-01T00:00:00Z","checked_at":"2026-11-06T08:00:00.123Z"}
        """#)
        #expect(a.state == .signedIn && a.isSignedIn && !a.needsSignIn && a.account == "me@example.com" && a.page == "login")
        #expect(a.checkedAt != nil && a.expiresAt != nil)
        #expect(try decode(ExtensionAuth.self, #"{"title":"x","state":"teleported"}"#).state == .unknown)
        #expect(try decode(ExtensionAuth.self, #"{"title":"x","state":"expired"}"#).needsSignIn)
        // On the extension's view, beside the programs it runs with.
        let p = try decode(HubExtension.self, #"""
        {"id":"lookup","name":"查询","kind":"assistant","status":"running","auth":{"title":"登录","state":"signed_out"},
         "tools":[{"name":"node","title":"Node.js","version":"22.23.4","reused":true},{"name":"example-cli","version":"2.3.4"}]}
        """#)
        #expect(p.auth?.state == .signedOut && p.tools?.count == 2 && p.tools?.first?.reused == true)
    }

    @Test func thePushDecodes() {
        let event = #"{"type":"extension_auth","extension":"lookup","auth":{"title":"登录","state":"pending"}}"#
        guard case .extensionAuth(let id, let auth, _)? = ServerEvent.decode(Data(event.utf8)) else {
            Issue.record("not an extension_auth")
            return
        }
        #expect(id == "lookup" && auth?.state == .pending)
        // Another manager's says whose it is (none: the hub's own assistant's).
        let theirs = #"{"type":"extension_auth","manager":"m2","extension":"lookup","auth":{"title":"x","state":"signed_in"}}"#
        guard case .extensionAuth(_, _, let manager)? = ServerEvent.decode(Data(theirs.utf8)) else {
            Issue.record("not an extension_auth")
            return
        }
        #expect(manager == "m2")
        // Something else under `auth` does not lose the event.
        guard case .extensionAuth(_, let none, _)? = ServerEvent.decode(Data(#"{"type":"extension_auth","extension":"x","auth":"?"}"#.utf8)) else {
            Issue.record("lost the event")
            return
        }
        #expect(none == nil)
    }

    @Test func theToolsStepAndNext() throws {
        let op = try decode(ExtensionOperation.self, #"""
        {"id":"op1","kind":"install","extension":"lookup","state":"running","step":"tools",
         "steps":[{"id":"consent","state":"done"},
                  {"id":"tools","state":"running","bytes":1048576,"total":4194304,
                   "tools":[{"name":"node","state":"done","reused":true},{"name":"example-cli","state":"running"},{"name":"x","state":"odd"}]},
                  {"id":"install","state":"pending"}]}
        """#)
        let step = try #require(op.current)
        #expect(step.id == "tools" && step.fraction == 0.25 && step.tools?.count == 3)
        #expect(step.tools?[2].state == .running, "a state this app does not know reads as still going")
        #expect(ExtensionOperationText.step("tools", kind: .install) == "安装命令行")
        #expect(ExtensionOperationText.tool(step.tools![0]) == "用这台机器上的")
        #expect(ExtensionOperationText.tool(step.tools![1]) == "正在安装")
        #expect(!op.needsSignIn)
        let done = try decode(ExtensionOperation.self, #"{"id":"op1","kind":"install","extension":"lookup","state":"succeeded","next":"auth","steps":[]}"#)
        #expect(done.needsSignIn && done.next == "auth")
        // Kept as it came when it goes round (the app's cache of operations).
        #expect(try JSON.decoder.decode(ExtensionOperation.self, from: JSON.encoder.encode(done)).next == "auth")
    }

    @Test func theWords() throws {
        #expect(ExtensionAuthText.state(ExtensionAuth(title: "登录", state: .signedIn, account: "me@example.com")) == "已登录：me@example.com")
        #expect(ExtensionAuthText.state(ExtensionAuth(title: "登录", state: .error, message: "网络不通")) == "出错：网络不通")
        #expect(ExtensionAuthText.state(ExtensionAuth(title: "登录", state: .pending)) == "等待确认")
        #expect(ExtensionAuthText.signInButton(ExtensionAuth(title: "", state: .signedOut)) == "登录")
        #expect(ExtensionAuthText.signInButton(ExtensionAuth(title: "", state: .expired)) == "重新登录")
        #expect(ExtensionAuthText.listSuffix(ExtensionAuth(title: "", state: .signedIn)) == nil)
        let p = try decode(HubExtension.self, #"{"id":"lookup","name":"查询","kind":"assistant","status":"running","auth":{"title":"登录","state":"signed_out"}}"#)
        #expect(ExtensionText.summary(p) == "启动 · 未登录")
        #expect(ExtensionText.summary(p, auth: ExtensionAuth(title: "", state: .signedIn)) == "启动", "the latest word wins")
        var off = p
        off.enabled = false
        off.status = "disabled"
        #expect(ExtensionText.summary(off) == "停用", "a switched off one is not asked to sign in")
        let node = ExtensionToolUse(name: "node", title: "Node.js", version: "22.23.4", reused: true)
        #expect(ExtensionAuthText.toolName(node) == "Node.js（node）" && ExtensionAuthText.toolVersion(node) == "22.23.4 · 机器上已有")
        let cli = ExtensionToolUse(name: "example-cli", version: "2.3.4")
        #expect(ExtensionAuthText.toolName(cli) == "example-cli" && ExtensionAuthText.toolVersion(cli) == "2.3.4")
        // The hub's warning is not said twice when the Sign In section says it.
        let warnings = ["还没登录（飞书登录）：在扩展详情里登录", "Not signed in (飞书登录). Sign in from the extension’s details", "别的"]
        #expect(ExtensionAuthText.warnings(warnings, shown: ExtensionAuth(title: "飞书登录", state: .signedOut)) == ["别的"])
        #expect(ExtensionAuthText.warnings(warnings, shown: nil) == warnings, "an older hub: the warning is all there is")
    }

    @Test func thePermissionLine() throws {
        let tools = try decode([ExtensionToolSpec].self, #"""
        [{"name":"node","title":"Node.js","version":"22.23.4",
          "archive":{"linux-amd64":{"url":"https://nodejs.org/dist/node.tar.gz","sha256":"0","mirrors":["https://cdn.npmmirror.com/node.tar.gz"]},
                     "darwin-arm64":{"url":"https://nodejs.org/dist/node-mac.tar.gz","sha256":"0"}}},
         {"name":"example-cli","version":"2.3.4","npm":{"package":"@example/example-cli","version":"2.3.4","integrity":"sha512-x","registry":"https://registry.example.com"}},
         {"name":"pub","version":"1.0.0","npm":{"package":"pub","version":"1.0.0","integrity":"sha512-x"}}]
        """#)
        #expect(ExtensionAuthText.spec(tools[0]) == "Node.js（node） 22.23.4 · cdn.npmmirror.com、nodejs.org")
        #expect(ExtensionAuthText.spec(tools[1]) == "example-cli 2.3.4 · npm：registry.example.com")
        #expect(ExtensionAuthText.source(tools[2]) == "npm：registry.npmjs.org", "npm's own registry by default")
        #expect(ExtensionText.permissionTitle(ExtensionPermission(key: "tools", title: "安装命令行程序")) == "安装命令行程序")
        #expect(ExtensionText.permissionTitle(ExtensionPermission(key: "tools", title: "安装更多命令行程序")) == "安装更多命令行程序")
        #expect(ExtensionText.permissionTitle(ExtensionPermission(key: "network", title: "访问网络")) == "访问网络")
        // The preview's manifest carries them.
        let pv = try decode(ExtensionPreview.self, #"""
        {"preview_id":"pv1","manifest":{"id":"lookup","version":"1.0.0","tools":[{"name":"node","version":"22.23.4","archive":{}}]},
         "permissions":[{"key":"tools","title":"安装命令行程序","detail":"…"}],"permissions_digest":"d"}
        """#)
        #expect(pv.manifest.tools?.first?.name == "node")
    }

    @Test func theSteps() throws {
        let qr = try decode(ConnectorPrompt.self, #"{"step":"scan","kind":"qr","url":"https://applink.example.com/qr","interval":1,"qr":{"size":1,"rows":["1"]}}"#)
        #expect(ExtensionSignIn.isQR(qr) && ExtensionSignIn.polls(qr) && ExtensionSignIn.link(qr) != nil && !ExtensionSignIn.cannotDraw(qr))
        let device = try decode(ConnectorPrompt.self, #"{"step":"d","kind":"device_code","url":"https://login.example.com/device","interval":2}"#)
        #expect(ExtensionSignIn.isDeviceCode(device), "a device code without a code to enter is a link to open")
        let form = try decode(ConnectorPrompt.self, #"{"step":"token","fields":[{"name":"token","label":"Token","secret":true}]}"#)
        #expect(ExtensionSignIn.isForm(form) && !ExtensionSignIn.polls(form))
        #expect(ExtensionSignIn.cannotDraw(try decode(ConnectorPrompt.self, #"{"step":"x","kind":"teleport"}"#)))
        #expect(ExtensionSignIn.cannotDraw(try decode(ConnectorPrompt.self, #"{"step":"x","kind":"qr"}"#)), "a QR step the hub did not draw")
        #expect(ExtensionSignIn.link(try decode(ConnectorPrompt.self, #"{"kind":"device_code","url":"javascript:alert(1)"}"#)) == nil)
    }

    @Test func signingInAndOut() async throws {
        let hub = FakeHub()
        let base = "/api/v1/plugins/lookup/-/auth"
        let polls = Counter()
        hub.on("POST", base + "/step") { req in
            let body = (try? JSONSerialization.jsonObject(with: req.body ?? Data())) as? [String: Any]
            if (body?["step"] as? String) == "" {
                return Data(#"{"step":"scan","kind":"qr","url":"https://applink.example.com/qr","interval":1,"qr":{"size":1,"rows":["1"]}}"#.utf8)
            }
            return polls.next() >= 2 ? Data(#"{"step":"scan","done":true,"account":"me@example.com"}"#.utf8)
                : Data(#"{"step":"scan","kind":"qr","url":"https://applink.example.com/qr","interval":1,"qr":{"size":1,"rows":["1"]}}"#.utf8)
        }
        hub.on("GET", base, json: #"{"title":"登录","state":"signed_in","account":"me@example.com"}"#)
        hub.on("POST", base + "/logout", json: #"{"title":"登录","state":"signed_out"}"#)
        let store = ExtensionAuthStore()
        let model = ExtensionSignInModel(id: "lookup", deps: .init(api: { hub.client }, store: store))
        await model.start()
        #expect(model.prompt.map(ExtensionSignIn.isQR) == true && model.signingIn)
        #expect(store.auth("lookup", else: nil)?.isSignedIn == true, "the status is asked once the sign-in starts")
        let done = await model.waitForAuthorization()
        #expect(done && !model.signingIn && model.error == nil)
        #expect(hub.sent("POST", base + "/step").count == 4, "the first step, then asked again until the third poll says done")
        #expect(store.auth("lookup", else: nil)?.account == "me@example.com")
        await model.logout()
        #expect(store.auth("lookup", else: nil)?.state == .signedOut)
    }

    @Test func aFailedStepSaysWhy() async {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/lookup/-/auth/step") { _ in throw APIError(status: 409, message: "扩展停用了") }
        let model = ExtensionSignInModel(id: "lookup", deps: .init(api: { hub.client }, store: ExtensionAuthStore()))
        await model.start()
        #expect(model.prompt == nil && model.error == "扩展停用了")
    }

    @Test func anotherHubStartsOver() {
        var hub = "a"
        let store = ExtensionAuthStore(hub: { hub })
        store.received("lookup", ExtensionAuth(title: "", state: .signedIn))
        #expect(store.auth("lookup", else: nil)?.isSignedIn == true)
        hub = "b"
        #expect(store.auth("lookup", else: ExtensionAuth(title: "", state: .signedOut))?.state == .signedOut, "another hub's word is not this one's")
        store.received("other", nil)
        #expect(store.auth("lookup", else: nil) == nil)
    }
}

/// Counts from 0, across the fake hub's queue.
private final class Counter: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    func next() -> Int { lock.withLock { defer { n += 1 }; return n } }
}
