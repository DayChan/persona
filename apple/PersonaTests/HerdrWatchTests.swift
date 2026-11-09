import Foundation
import Testing
@testable import Persona

/// The Agents list's 「herdr 没在运行」 rows (#1916): which machines get one, what a failed read leaves, and On.
@MainActor struct HerdrWatchTests {
    private func machine(_ id: String, online: Bool = true, engine: MachineEngine = .herdr) -> Machine {
        var m = Machine(id: id, name: id, kind: "ssh", online: online)
        m.engine = engine
        return m
    }

    private func answer(running: Bool, known: Bool = true) -> String {
        #"{"autostart":false,"running":\#(running),"known":\#(known)}"#
    }

    @Test func onlyOnlineClassicHerdr() {
        #expect(HerdrWatch.watched(machine("a")))
        #expect(HerdrWatch.watched(machine("a", engine: .herdrBoth)))
        #expect(!HerdrWatch.watched(machine("a", engine: .herdrApp)))
        #expect(!HerdrWatch.watched(machine("a", engine: .orca)))
        #expect(!HerdrWatch.watched(machine("a", online: false)))
    }

    @Test func aRowOnlyWhenKnownNotRunning() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/down/herdr", json: answer(running: false))
        hub.on("GET", "/api/v1/machines/up/herdr", json: answer(running: true))
        hub.on("GET", "/api/v1/machines/unsure/herdr", json: answer(running: false, known: false))
        let watch = HerdrWatch()
        let machines = [machine("down"), machine("up"), machine("unsure"), machine("app", engine: .herdrApp)]
        await watch.refresh(machines) { _ in hub.client }
        #expect(watch.stopped == [machine("down").ref])
        #expect(hub.sent("GET", "/api/v1/machines/app/herdr").isEmpty, "Herdr App has no such server: not asked")
    }

    @Test func aLostReadKeepsTheRowAndAnOlderHubIsNotAskedAgain() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/down/herdr", json: answer(running: false))
        let watch = HerdrWatch()
        let machines = [machine("down"), machine("old")] // "old": the hub has no route (404)
        await watch.refresh(machines) { _ in hub.client }
        #expect(watch.stopped == [machine("down").ref])

        hub.on("GET", "/api/v1/machines/down/herdr") { _ in throw APIError(status: 502, message: "node did not answer") }
        await watch.refresh(machines) { _ in hub.client }
        #expect(watch.stopped == [machine("down").ref], "one lost poll must not take the row away")
        #expect(hub.sent("GET", "/api/v1/machines/old/herdr").count == 1)

        // Gone offline: no row, whatever was read before.
        await watch.refresh([machine("down", online: false)]) { _ in hub.client }
        #expect(watch.stopped.isEmpty)
    }

    @Test func aStartTakesTheRowAwayEvenIfTheHubStillSaysDown() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/mac/herdr", json: answer(running: false))
        hub.on("POST", "/api/v1/machines/mac/herdr/start", json: answer(running: true))
        let watch = HerdrWatch()
        let mac = machine("mac")
        await watch.refresh([mac]) { _ in hub.client }
        #expect(watch.stopped == [mac.ref])

        await watch.start(mac, api: hub.client)
        #expect(watch.stopped.isEmpty)
        #expect(watch.starting.isEmpty)
        #expect(watch.failures.isEmpty)
        // A hub that only knew what node.info said keeps answering "not running": the start's answer stands.
        await watch.refresh([mac]) { _ in hub.client }
        #expect(watch.stopped.isEmpty)

        // Once the hub says it runs, a later stop shows the row again.
        hub.on("GET", "/api/v1/machines/mac/herdr", json: answer(running: true))
        await watch.refresh([mac]) { _ in hub.client }
        hub.on("GET", "/api/v1/machines/mac/herdr", json: answer(running: false))
        await watch.refresh([mac]) { _ in hub.client }
        #expect(watch.stopped == [mac.ref])
    }

    @Test func aFailedStartSaysWhyAndKeepsTheRow() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/mac/herdr", json: answer(running: false))
        hub.on("POST", "/api/v1/machines/mac/herdr/start") { _ in
            throw APIError(status: 502, message: "herdr server exited (status 1)")
        }
        let watch = HerdrWatch()
        let mac = machine("mac")
        await watch.refresh([mac]) { _ in hub.client }
        await watch.start(mac, api: hub.client)
        #expect(watch.stopped == [mac.ref])
        #expect(watch.failures[mac.ref]?.text == "herdr server exited (status 1)", "an unknown failure keeps its words")
        #expect(watch.starting.isEmpty)
    }

    @Test func aPrecancelledRefreshDoesNotAskOrPruneRows() async {
        let hub = FakeHub()
        let first = machine("first")
        hub.on("GET", "/api/v1/machines/first/herdr", json: answer(running: false))
        let watch = HerdrWatch()
        await watch.refresh([first]) { _ in hub.client }
        let task = Task {
            withUnsafeCurrentTask { $0?.cancel() }
            await watch.refresh([machine("next")]) { _ in hub.client }
        }
        await task.value
        #expect(hub.sent("GET", "/api/v1/machines/next/herdr").isEmpty)
        #expect(watch.stopped == [first.ref], "a cancelled refresh must not prune or publish")
    }

    @Test(arguments: [0, 404, 502])
    func cancellationDuringFirstResponseStopsTheBatchAndDoesNotPublish(errorStatus: Int) async {
        let hub = FakeHub()
        let first = machine("first"), second = machine("second")
        let path = "/api/v1/machines/first/herdr"
        hub.on("GET", path, json: answer(running: false))
        hub.on("GET", "/api/v1/machines/second/herdr", json: answer(running: false))
        let watch = HerdrWatch()
        await watch.refresh([first]) { _ in hub.client }
        let data = Data(answer(running: true).utf8)
        hub.on("GET", path) { _ in
            // A transport can complete (or answer an error) even after its caller was cancelled.
            withUnsafeCurrentTask { $0?.cancel() }
            if errorStatus != 0 { throw APIError(status: errorStatus, message: "fixture: cancelled response") }
            return data
        }
        let task = Task { await watch.refresh([first, second]) { _ in hub.client } }
        await task.value
        #expect(hub.sent("GET", path).count == 2)
        #expect(hub.sent("GET", "/api/v1/machines/second/herdr").isEmpty, "no next GET after cancellation")
        #expect(watch.stopped == [first.ref], "no late row publication")

        hub.on("GET", path, json: answer(running: true))
        await watch.refresh([first, second]) { _ in hub.client }
        #expect(hub.sent("GET", path).count == 3, "a cancelled 404 must not mark a machine unsupported")
        #expect(watch.stopped == [second.ref])
    }

    @Test func knownFailuresInWords() {
        #expect(HerdrFailure.says("herdr server stopped right after starting: exit status 1") == "herdr 起来后马上退出了（退出码 1）")
        #expect(HerdrFailure.says("herdr server stopped right after starting: signal: killed") == "herdr 起来后马上被结束了（killed）")
        #expect(HerdrFailure.says("herdr server stopped right after starting: exited") == "herdr 起来后马上退出了")
        #expect(HerdrFailure.says("herdr is not installed (no herdr command in PATH or the login shell)").hasPrefix("这台电脑上没装 herdr"))
        #expect(HerdrFailure.says("herdr did not answer on /tmp/x.sock within 20s") == "herdr 启动了，但 20 秒内没有应答")
        #expect(HerdrFailure.says("unknown method herdr.server.start").contains("版本太旧"))
        #expect(HerdrFailure.says("MacBook: machine is offline") == "这台电脑现在离线")
        #expect(HerdrFailure.says("context deadline exceeded").hasPrefix("等了很久"))
        #expect(HerdrFailure.says("这台电脑没有 herdr") == "这台电脑没有 herdr")
    }

    @Test func theRequestIDIsInTheDetailNotTheRow() {
        let f = HerdrFailure(APIError(status: 502, message: "herdr server stopped right after starting: exit status 1", requestID: "r-1"))
        #expect(f.text == "herdr 起来后马上退出了（退出码 1）")
        #expect(!f.text.contains("r-1"))
        #expect(f.detail.contains("exit status 1") && f.detail.contains("请求 r-1"))
    }
}
