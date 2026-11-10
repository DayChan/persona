import Foundation
import Testing
@testable import Persona

/// What This Computer Can Do (#3018): the node's report read as features, the same table as the web's `node-health.test.ts`.
@MainActor struct NodeHealthTests {
    private static let names = ["node.sock", "runtime.sock", "claude-settings.json", "claude-hooks", "assistant-settings.json",
                                "assistant-hooks", "mcp.json", "agent-plugin", "command-paths", "claude-version", "herdr",
                                "global-permission-hooks"]

    private func report(_ overrides: [String: DoctorCheck] = [:], fixed: [String] = []) -> DoctorReport {
        DoctorReport(checks: Self.names.map { overrides[$0] ?? DoctorCheck(name: $0, status: .ok, detail: "ok") }, fixed: fixed)
    }

    private func state(_ features: [NodeHealth.Feature], _ id: String) -> NodeHealth.FeatureState? {
        features.first { $0.id == id }?.state
    }

    @Test func decodesTheNodeReport() throws {
        let json = #"""
        {"ok":false,"dir":"/x","checks":[
          {"name":"node.sock","status":"ok","state":"","detail":"已应答","hint":"","items":[]},
          {"name":"claude-hooks","status":"warn","state":"stale","detail":"过期","hint":"运行 --fix","items":["a","b"]},
          {"name":"future","status":"weird"}
        ],"fixed":null}
        """#
        let r = try JSON.decoder.decode(DoctorReport.self, from: Data(json.utf8))
        #expect(r.checks.count == 3 && r.fixed.isEmpty && !r.ok)
        #expect(r.checks[1].fixable && r.checks[1].items == ["a", "b"])
        #expect(r.checks[2].status == .warn && r.checks[2].detail.isEmpty)
    }

    @Test func healthyIsEverythingReady() {
        let f = NodeHealth.derive(report())
        #expect(f.map(\.id) == ["approvals", "status", "terminal", "mcp", "skills", "runtimes"])
        #expect(f.allSatisfy { $0.state == .ready && $0.note.isEmpty })
        #expect(NodeHealth.summarize(f) == .init(attention: 0, title: "一切正常", off: ""))
        #expect(NodeHealth.fixable(report()).isEmpty)
    }

    @Test func staleGeneratedFilesArePartialWithAFix() {
        let r = report(["claude-hooks": DoctorCheck(name: "claude-hooks", status: .warn, state: "stale", detail: "过期", hint: "personad node doctor --fix")])
        let f = NodeHealth.derive(r)
        #expect(state(f, "approvals") == .partial)
        #expect(f.first { $0.id == "approvals" }?.note.contains("修复") == true)
        #expect(NodeHealth.summarize(f).title == "1 项功能需要处理")
        #expect(NodeHealth.fixable(r).map(\.name) == ["claude-hooks"])
    }

    @Test func aFailedSocketBreaksEveryFeatureOnIt() {
        let f = NodeHealth.derive(report(["node.sock": DoctorCheck(name: "node.sock", status: .fail, detail: "连不上", hint: "重启 node")]))
        #expect(state(f, "approvals") == .broken && state(f, "status") == .broken && state(f, "mcp") == .broken)
        #expect(state(f, "skills") == .ready)
        #expect(f.first { $0.id == "status" }?.note == "重启 node")
        #expect(NodeHealth.summarize(f).attention == 3)
    }

    @Test func optionalFeaturesAreOffUnlessTheEngineNeedsThem() {
        let r = report(["herdr": DoctorCheck(name: "herdr", status: .warn, detail: "没有在监听"),
                        "runtime.sock": DoctorCheck(name: "runtime.sock", status: .warn, detail: "没有")])
        let orca = NodeHealth.derive(r, engine: .orca)
        #expect(state(orca, "terminal") == .off && state(orca, "runtimes") == .off)
        #expect(NodeHealth.summarize(orca) == .init(attention: 0, title: "一切正常", off: "没有设置：终端与引擎、运行时接入"))
        #expect(state(NodeHealth.derive(r, engine: .herdr), "terminal") == .broken)
        #expect(state(NodeHealth.derive(r, engine: .herdrBoth), "terminal") == .broken)
    }

    @Test func unknownChecksAreNotHidden() {
        var r = report()
        r.checks.append(DoctorCheck(name: "new-thing", status: .warn, detail: "新的"))
        let f = NodeHealth.derive(r)
        #expect(f.last?.id == "other" && f.last?.state == .partial)
        r.checks.removeLast()
        r.checks.append(DoctorCheck(name: "new-thing", status: .ok))
        #expect(!NodeHealth.derive(r).contains { $0.id == "other" })
    }

    @Test func anOlderNodeLeavesOutWhatItDoesNotReport() {
        let f = NodeHealth.derive(DoctorReport(checks: [DoctorCheck(name: "node.sock", status: .ok)]))
        #expect(f.map(\.id) == ["approvals", "status", "mcp"])
    }

    @Test func theUsersOwnHooksAreInformation() {
        let r = report(["global-permission-hooks": DoctorCheck(name: "global-permission-hooks", status: .warn, detail: "有 1 个同步钩子")])
        #expect(NodeHealth.info(r).map(\.name) == ["global-permission-hooks"])
        #expect(NodeHealth.summarize(NodeHealth.derive(r)).attention == 0)
    }

    @Test func fixResultSaysWhatWasWritten() {
        #expect(NodeHealth.fixResult(report(fixed: ["a", "b"])) == ("已重新生成 2 项", false))
        #expect(NodeHealth.fixResult(report()) == ("没有需要重新生成的文件", false))
        var r = report()
        r.checks.append(DoctorCheck(name: "fix", status: .fail, detail: "重新生成失败：只读"))
        #expect(NodeHealth.fixResult(r) == ("重新生成失败：只读", true))
    }

    @Test func whyASelfCheckIsUnavailable() {
        #expect(NodeHealth.unavailable(APIError(status: 502, message: "unknown method node.doctor")) == .oldNode)
        #expect(NodeHealth.unavailable(APIError(status: 404, message: "Not Found")) == .oldHub)
        #expect(NodeHealth.unavailable(APIError(status: 404, message: "unknown machine", details: .object(["error": .string("unknown machine")]))) == nil)
        #expect(NodeHealth.unavailable(APIError(status: 403, message: "a computer's self-check needs a full device")) == .forbidden)
        #expect(NodeHealth.unavailable(APIError(status: 503, message: "offline")) == .offline)
        #expect(NodeHealth.unavailable(APIError(status: 500, message: "boom")) == nil)
    }

    @Test func deployFollowsTheHubsAnswer() {
        // Older hubs send neither field: the kind decides, as before.
        #expect(NodeHealth.canDeploy(Machine(id: "a", name: "a", kind: "ssh", online: true)))
        #expect(!NodeHealth.canDeploy(Machine(id: "a", name: "a", kind: "local", online: true)))
        #expect(!NodeHealth.canDeploy(Machine(id: "a", name: "a", kind: "link", online: true)))
        #expect(NodeHealth.deployReason(Machine(id: "a", name: "a", kind: "ssh", online: true)) == nil)

        // Newer hubs answer themselves: a link the hub can update, and an ssh one it refuses, with its reason.
        var link = Machine(id: "b", name: "b", kind: "link", online: true)
        link.canDeploy = true
        #expect(NodeHealth.canDeploy(link))
        var ssh = Machine(id: "c", name: "c", kind: "ssh", online: true)
        ssh.canDeploy = false
        ssh.deployReason = "没有这个平台的构建"
        #expect(!NodeHealth.canDeploy(ssh))
        #expect(NodeHealth.deployReason(ssh) == "没有这个平台的构建")
    }

    @Test func aDeployNamesItsStage() throws {
        #expect(MachineDeployPhase.of(Machine(id: "a", name: "a", kind: "link", online: true)) == nil)
        #expect(MachineDeployPhase(rawValue: "waiting_idle") == .waitingIdle)
        #expect(MachineDeployPhase.waitingIdle.message == "正在等待这台电脑上的 Agent 空闲…")
        #expect(MachineDeployPhase.transferring.message == "正在把 Persona 传到这台电脑…")
        #expect(MachineDeployPhase.restarting.message == "正在重启这台电脑上的 Persona…")
        #expect(MachineDeployPhase.confirming.message == "正在等待 Persona 重新连接并确认版本…")

        let json = #"{"id":"a","name":"a","kind":"link","online":true,"can_deploy":true,"deploy_phase":"waiting_idle"}"#
        let m = try JSON.decoder.decode(Machine.self, from: Data(json.utf8))
        #expect(m.canDeploy == true && MachineDeployPhase.of(m) == .waitingIdle)
    }

    // MARK: Model

    private struct Boom: Error {}

    @Test func modelChecksAndFixes() async {
        var fixes = 0
        let stale = report(["mcp.json": DoctorCheck(name: "mcp.json", status: .warn, state: "missing")])
        let m = NodeHealthModel(deps: .init(check: { stale }, fix: { fixes += 1; return self.report(fixed: ["mcp.json"]) }))
        await m.check()
        #expect(m.report.map(NodeHealth.fixable)?.count == 1 && !m.busy)
        let r = await m.fix()
        #expect(fixes == 1 && r == ("已重新生成 1 项", false))
        #expect(m.report.map(NodeHealth.fixable)?.isEmpty == true && !m.busy && !m.fixing)
    }

    @Test func modelSaysWhyItCouldNotCheck() async {
        let old = NodeHealthModel(deps: .init(check: { throw APIError(status: 502, message: "unknown method node.doctor") }, fix: { throw Boom() }))
        await old.check()
        guard case .unavailable(.oldNode) = old.load else { Issue.record("expected old node"); return }
        let broken = NodeHealthModel(deps: .init(check: { throw APIError(status: 500, message: "boom") }, fix: { throw Boom() }))
        await broken.check()
        guard case .error(let text) = broken.load else { Issue.record("expected an error"); return }
        #expect(text.contains("boom"))
    }

    @Test func aFailedFixKeepsTheReport() async {
        let m = NodeHealthModel(deps: .init(check: { self.report() }, fix: { throw APIError(status: 500, message: "写不了") }))
        await m.check()
        let r = await m.fix()
        #expect(r.failed && r.text.hasPrefix("修复失败：") && m.report != nil)
    }

    @Test func resetForgetsTheReport() async {
        let m = NodeHealthModel(deps: .init(check: { self.report() }, fix: { self.report() }))
        await m.check()
        m.reset()
        guard case .idle = m.load else { Issue.record("expected idle"); return }
    }
}
