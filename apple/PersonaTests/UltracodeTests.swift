import Foundation
import Testing
@testable import Persona

/// Claude Code's ultracode switch on iOS / Mac / watch (#1307; server #3427, docs/protocol/protocol.md "Claude Code
/// ultracode"): what the session says (older hubs included), what goes to the hub, where the switch shows, how it is
/// drawn and grayed, and the quota under it.
@MainActor
struct UltracodeTests {
    private static let features: Set<String> = [Ultracode.feature]

    private func session(_ extra: String = "", state: String = "idle", id: String = "s1") throws -> Session {
        let effort = extra.contains(#""effort":"#) ? "" : #","effort":"high""#
        return try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"\#(id)","status":"idle","state":"\#(state)","pane_id":"p1"\#(effort)\#(extra)}"#.utf8))
    }

    private func body(_ req: LocalRequest?) throws -> [String: Any] {
        let data = try #require(req?.body)
        return try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    // MARK: Decoding

    @Test func decodesEveryField() throws {
        let s = try session(#","ultracode":true,"ultracode_note":"请升级","ultracode_pending":true,"ultracode_lost_at":"2026-11-01T08:00:00Z""#)
        #expect(s.ultracode == true)
        #expect(s.ultracodeNote == "请升级")
        #expect(s.ultracodePending)
        #expect(s.ultracodeLostAt == JSON.parseDate("2026-11-01T08:00:00Z"))
        #expect(try session(#","ultracode":false"#).ultracode == false, "false is sent too: off, not unknown")
    }

    @Test func anOlderHubSaysNothingWhichIsUnknownNotOff() throws {
        let s = try session()
        #expect(s.ultracode == nil)
        #expect(s.ultracodeNote == nil)
        #expect(!s.ultracodePending)
        #expect(s.ultracodeLostAt == nil)
        #expect(Ultracode.stateWord(s.ultracode) == SessionModes.unknown)
        #expect(!Ultracode.rowMark(s))
    }

    @Test func anOddFieldNeverDropsTheSession() throws {
        let s = try session(#","ultracode":"yes","ultracode_note":7,"ultracode_pending":"no","ultracode_lost_at":"not a time""#)
        #expect(s.sessionID == "s1")
        #expect(s.ultracode == nil)
        #expect(s.ultracodeNote == nil)
        #expect(!s.ultracodePending)
        #expect(s.ultracodeLostAt == nil)
        #expect(try session(#","ultracode_note":"""#).ultracodeNote == nil, "an empty note is no note")
    }

    // MARK: Requests

    @Test func theSwitchGoesAloneAndSaysOnOrOff() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/modes", json: #"{"ok":true}"#)
        let key = SessionKey(machineID: "box", sessionID: "s1")
        try await hub.client.setUltracode(key, on: true)
        try await hub.client.setUltracode(key, on: false)
        try await hub.client.dismissUltracodeLost(key)
        try await hub.client.setModes(key, effort: "high")
        let sent = hub.sent("POST", "/api/v1/sessions/box/s1/modes")
        #expect(sent.count == 4)
        #expect(try body(sent[0]) as NSDictionary == ["ultracode": true] as NSDictionary)
        #expect(try body(sent[1]) as NSDictionary == ["ultracode": false] as NSDictionary)
        #expect(try body(sent[2]) as NSDictionary == ["ultracode_dismiss_lost": true] as NSDictionary)
        #expect(try body(sent[3]) as NSDictionary == ["effort": "high"] as NSDictionary, "an effort change carries no ultracode")
    }

    @Test func aStartSendsOnlyTrue() throws {
        let on = APIClient.StartRequest(machineID: "box", cwd: "/w", launcher: "claude", prompt: nil, resumeSessionID: nil, workspaceID: nil,
                                        async: true, ultracode: true)
        let off = APIClient.StartRequest(machineID: "box", cwd: "/w", launcher: "claude", prompt: nil, resumeSessionID: nil, workspaceID: nil,
                                         async: true)
        let a = try #require(try JSONSerialization.jsonObject(with: JSONEncoder().encode(on)) as? [String: Any])
        let b = try #require(try JSONSerialization.jsonObject(with: JSONEncoder().encode(off)) as? [String: Any])
        #expect(a["ultracode"] as? Bool == true)
        #expect(b["ultracode"] == nil, "off is never sent: a new Claude is off anyway")
    }

    @Test func refusalsAreNamed() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/modes") { _ in
            throw APIError(status: 409, message: "这台电脑上的 node 太旧", fields: ["code": "node_outdated", "error": "这台电脑上的 node 太旧"])
        }
        hub.on("POST", "/api/v1/sessions/box/s2/modes") { _ in
            throw APIError(status: 409, message: "Conflict", fields: ["code": "ultracode_unsupported"])
        }
        do {
            try await hub.client.setUltracode(SessionKey(machineID: "box", sessionID: "s1"), on: true)
            Issue.record("a refusal must reach the caller")
        } catch {
            #expect(Ultracode.refused(error))
            #expect(Ultracode.errorText(error) == "这台电脑上的 node 太旧", "the hub's own words")
        }
        do {
            try await hub.client.setUltracode(SessionKey(machineID: "box", sessionID: "s2"), on: true)
            Issue.record("a refusal must reach the caller")
        } catch {
            #expect(Ultracode.refused(error))
            #expect(Ultracode.errorText(error) == String(localized: "This session can't use ultracode"), "ours when it brought none")
        }
        #expect(!Ultracode.refused(APIError(status: 502, message: "node unreachable")))
    }

    // MARK: Where it shows

    @Test func offeredOnlyOnAClaudeCodeAgentOnAHubThatHasIt() throws {
        let s = try session()
        #expect(Ultracode.offered(s, features: Self.features))
        #expect(!Ultracode.offered(s, features: []), "an older hub")
        #expect(!Ultracode.offered(try session(#","assistant":true"#), features: Self.features), "a Persona conversation")
        #expect(!Ultracode.offered(try session(#","agent":"codex""#), features: Self.features))
        #expect(!Ultracode.offered(try session(#","agent":"traex""#), features: Self.features))
        #expect(!Ultracode.offered(try session(#","agent":"dsh""#), features: Self.features))
        #expect(!Ultracode.offered(try session(state: "offline"), features: Self.features))
        #expect(!Ultracode.offered(try session(#","source_kind":"claude_bg","capabilities":{"read":true}"#), features: Self.features),
                "a read-only source: its effort can't change")
        #expect(Ultracode.offered(try session(state: "working"), features: Self.features), "working: the change waits for the turn")
        #expect(!Ultracode.offered(nil, features: Self.features))
    }

    @Test func theMarks() throws {
        #expect(Ultracode.rowMark(try session(#","ultracode":true"#)))
        #expect(!Ultracode.rowMark(try session(#","ultracode":true"#, state: "offline")), "the node remembers it after the process is gone")
        #expect(!Ultracode.rowMark(try session(#","ultracode":true,"assistant":true"#)))
        #expect(!Ultracode.rowMark(try session(#","ultracode":false"#)))
        #expect(Ultracode.chipMark(try session(#","ultracode":true"#)) == .ultracode)
        #expect(Ultracode.chipMark(try session(#","agent":"codex","effort":"ultra","efforts":["high","ultra"]"#)) == .codexUltra)
        #expect(Ultracode.chipMark(try session(#","agent":"codex","effort":"high","efforts":["high","ultra"]"#)) == nil)
        #expect(Ultracode.chipMark(try session()) == nil)
        #expect(Ultracode.infoShown(try session(), features: Self.features))
        #expect(!Ultracode.infoShown(try session(#","agent":"codex""#), features: Self.features))
        #expect(!Ultracode.infoShown(try session(#","ultracode":true"#, state: "offline"), features: Self.features),
                "no On on the info card of a process that is gone, as no mark on its row")
    }

    @Test func theComposerTitleSaysItToVoiceOver() throws {
        var on = try #require(SessionModesMenu.title(try session(#","ultracode":true,"model":"claude-opus-5-5""#), setsModes: true))
        #expect(on.mark == .ultracode)
        #expect(on.spoken.hasSuffix(Ultracode.onWords))
        on.mark = nil
        #expect(!on.spoken.contains(Ultracode.onWords))
        let codex = try #require(SessionModesMenu.title(try session(#","agent":"codex","effort":"ultra","efforts":["ultra"],"model":"gpt-5.5""#), setsModes: true))
        #expect(codex.mark == .codexUltra)
        #expect(!codex.spoken.contains(Ultracode.onWords), "Codex's own word, Ultra, says it")
        #expect(SessionModesMenu.title(try session(#","ultracode":true,"model":"claude-opus-5-5""#), setsModes: false)?.mark == nil)
    }

    @Test func theResumeNotice() throws {
        let lost = #","ultracode":false,"ultracode_lost_at":"2026-11-01T08:00:00Z""#
        #expect(Ultracode.lostShown(try session(lost), features: Self.features))
        #expect(!Ultracode.lostShown(try session(lost), features: []))
        #expect(!Ultracode.lostShown(try session(#","ultracode":true,"ultracode_lost_at":"2026-11-01T08:00:00Z""#), features: Self.features),
                "turned back on")
        #expect(!Ultracode.lostShown(try session(lost + #","assistant":true"#), features: Self.features))
    }

    // MARK: Drawn and grayed

    @Test func theSwitchFollowsTheNodeWithoutAnAsk() throws {
        let off = Ultracode.switchState(try session(#","ultracode":false"#), ask: nil)
        #expect(off == Ultracode.Switch(on: false, disabled: false, pending: false, line: Ultracode.subtitle, caution: false))
        #expect(Ultracode.switchState(try session(#","ultracode":true"#), ask: nil).on)
        #expect(!Ultracode.switchState(try session(), ask: nil).on, "unknown is drawn off")
    }

    @Test func theNodesReasonIsSaidButDoesNotGray() throws {
        let sw = Ultracode.switchState(try session(#","ultracode":false,"ultracode_note":"请升级到 2.1.284""#), ask: nil)
        #expect(sw.line == "请升级到 2.1.284")
        #expect(sw.caution)
        #expect(!sw.disabled, "many reasons are fixed elsewhere: it can be tried again")
        #expect(!Ultracode.quotaWarnShown(sw))
    }

    @Test func grayedOnlyWhileItsOwnRequestIsOut() throws {
        let s = try session(#","ultracode":false"#)
        let sending = Ultracode.switchState(s, ask: Ultracode.Ask(on: true, sending: true))
        #expect(sending.disabled)
        #expect(sending.on, "it stands where it was put")
        #expect(!Ultracode.askSpent(s, ask: Ultracode.Ask(on: true, sending: true)))
    }

    @Test func aQueuedChangeAppliesNextTurn() throws {
        let queued = try session(#","ultracode":false,"ultracode_pending":true"#, state: "working")
        let mine = Ultracode.switchState(queued, ask: Ultracode.Ask(on: true, sending: false, answered: queued))
        #expect(mine.on)
        #expect(mine.pending)
        #expect(mine.line == Ultracode.pendingWords)
        #expect(!mine.disabled, "a queued change never grays it")
        let elsewhere = Ultracode.switchState(queued, ask: nil)
        #expect(!elsewhere.on, "another device's queued change does not move it")
        #expect(elsewhere.line != Ultracode.pendingWords)
        let withNote = Ultracode.switchState(try session(#","ultracode_pending":true,"ultracode_note":"终端里有草稿""#), ask: nil)
        #expect(withNote.line.hasSuffix(" · 终端里有草稿"))
        #expect(!withNote.caution)
    }

    @Test func theAskHoldsUntilTheNodeAnswers() throws {
        let before = try session(#","ultracode":false"#)
        let ask = Ultracode.Ask(on: true, sending: false, answered: before)
        #expect(Ultracode.shownOn(before, ask: ask), "the node's push has not come yet")
        #expect(!Ultracode.askSpent(before, ask: ask))
        let answered = try session(#","ultracode":true"#)
        #expect(Ultracode.askSpent(answered, ask: ask))
        let refused = try session(#","ultracode":false,"ultracode_note":"模型不支持""#)
        #expect(!Ultracode.shownOn(before.withNote("模型不支持"), ask: Ultracode.Ask(on: true, sending: false, answered: before.withNote("模型不支持"))),
                "a reason means the node said no")
        #expect(Ultracode.askSpent(refused, ask: ask))
    }

    @Test func aMenuSaysTheQuotaInTheSectionHeader() throws {
        let quota = Ultracode.Quota(name: "Claude", windows: [.init(kind: .fiveHours, percent: 86), .init(kind: .week, percent: 41)], warn: true)
        let on = try session(#","ultracode":true"#)
        for wraps in [true, false] {
            let words = Ultracode.menuWords(on, ask: nil, quota: quota, headerWraps: wraps)
            #expect(words.subtitle == Ultracode.subtitle)
            #expect(words.header?.hasPrefix(Ultracode.quotaLine(quota)) == true, "the quota leads the header")
            #expect(words.header?.hasSuffix(Ultracode.quotaWarning) == true, "and its warning, as clear as it")
        }
        #expect(Ultracode.menuWords(on, ask: nil, quota: quota, headerWraps: true).header == Ultracode.quotaLine(quota) + "\n" + Ultracode.quotaWarning)
        #expect(Ultracode.menuWords(on, ask: nil, quota: quota, headerWraps: false).header?.contains("\n") == false,
                "a Mac menu draws a header on one line")
        #expect(Ultracode.menuWords(on, ask: nil, quota: nil, headerWraps: true).header == nil, "no quota, no reason: no header")
        let calm = Ultracode.Quota(name: "Claude", windows: [.init(kind: .fiveHours, percent: 20)], warn: false)
        #expect(Ultracode.menuWords(on, ask: nil, quota: calm, headerWraps: true).header == Ultracode.quotaLine(calm))
        let turningOff = try session(#","ultracode":true,"ultracode_pending":true"#, state: "working")
        #expect(Ultracode.menuWords(turningOff, ask: Ultracode.Ask(on: false, sending: false, answered: turningOff), quota: quota, headerWraps: true)
            .header == Ultracode.quotaLine(quota), "no warning while turning it off waits")
    }

    @Test func anIOSMenuSaysTheNodesReasonWholeInTheHeader() throws {
        let note = "请升级到 Claude Code 2.1.284 或更新版本，旧版不支持独立的 ultracode 开关"
        let quota = Ultracode.Quota(name: "Claude", windows: [.init(kind: .fiveHours, percent: 86)], warn: true)
        let refused = try session(#","ultracode":false,"ultracode_note":"\#(note)""#)
        let phone = Ultracode.menuWords(refused, ask: nil, quota: quota, headerWraps: true)
        #expect(phone.header == note + "\n" + Ultracode.quotaLine(quota), "the reason first, whole; no warning under it")
        #expect(phone.subtitle == Ultracode.subtitle, "the subtitle, cut after three lines, keeps what it does")
        #expect(Ultracode.menuWords(refused, ask: nil, quota: nil, headerWraps: true).header == note)
        let mac = Ultracode.menuWords(refused, ask: nil, quota: quota, headerWraps: false)
        #expect(mac.subtitle == note, "a Mac menu wraps a subtitle: the reason stays under the switch")
        #expect(mac.header == Ultracode.quotaLine(quota))

        let draft = "电脑上的输入框里有没发出去的内容，清空后 ultracode 再切换"
        let waiting = try session(#","ultracode":false,"ultracode_pending":true,"ultracode_note":"\#(draft)""#, state: "working")
        let mine = Ultracode.menuWords(waiting, ask: Ultracode.Ask(on: true, sending: false, answered: waiting), quota: nil, headerWraps: true)
        #expect(mine.subtitle == Ultracode.pendingWords)
        #expect(mine.header == draft)
        let theirs = Ultracode.menuWords(waiting, ask: nil, quota: nil, headerWraps: true)
        #expect(theirs.subtitle == Ultracode.queuedWords(ask: nil))
        #expect(theirs.subtitle != Ultracode.pendingWords)
        #expect(Ultracode.menuWords(waiting, ask: nil, quota: nil, headerWraps: false).subtitle == "\(Ultracode.queuedWords(ask: nil)) · \(draft)")
    }

    @Test func theQuotaWarningGoesWithASwitchThatCanBeOn() {
        #expect(Ultracode.quotaWarnShown(.init(on: true, disabled: false, pending: true, line: "", caution: false)))
        #expect(Ultracode.quotaWarnShown(.init(on: false, disabled: false, pending: false, line: "", caution: false)))
        #expect(!Ultracode.quotaWarnShown(.init(on: false, disabled: true, pending: false, line: "", caution: false)), "being turned off")
        #expect(!Ultracode.quotaWarnShown(.init(on: false, disabled: false, pending: true, line: "", caution: false)), "turning off is queued")
    }

    // MARK: New Agent

    @Test func newAgentOffersItForClaudeCodeOnAHubThatHasIt() {
        #expect(Ultracode.startOffered(features: Self.features, agent: "claude"))
        #expect(Ultracode.startOffered(features: Self.features, agent: ""))
        #expect(!Ultracode.startOffered(features: Self.features, agent: "codex"))
        #expect(!Ultracode.startOffered(features: [], agent: "claude"))
    }

    @Test func aKnownOldClaudeCodeIsBlockedAnUnknownOneMayTry() {
        #expect(Ultracode.versionTooOld("2.1.250"))
        #expect(Ultracode.versionTooOld("v2.1.283"))
        #expect(Ultracode.versionTooOld("1.9.999"))
        #expect(!Ultracode.versionTooOld("2.1.284"))
        #expect(!Ultracode.versionTooOld("2.1.284-beta.1"))
        #expect(!Ultracode.versionTooOld("2.2.0"))
        #expect(!Ultracode.versionTooOld("3.0.0"))
        #expect(!Ultracode.versionTooOld(nil))
        #expect(!Ultracode.versionTooOld(""))
        #expect(!Ultracode.versionTooOld("2.1.250 (Claude Code)"), "not a version this rule knows")
        #expect(Ultracode.startBlocked(claudeVersion: "2.1.250")?.contains("2.1.250") == true)
        #expect(Ultracode.startBlocked(claudeVersion: "2.1.250")?.contains("2.1.284") == true)
        #expect(Ultracode.startBlocked(claudeVersion: "2.1.300") == nil)
    }

    // MARK: The quota

    private func account(_ id: String, provider: String = "claude", local: Bool = true, stale: Bool = false,
                         five: Double? = 40, week: Double? = 20, model: String? = nil) -> UsageAccount {
        let soon = Date.now.addingTimeInterval(3600)
        var w: [UsageWindow] = []
        if let five { w.append(UsageWindow(label: "5h", windowMinutes: 300, usedPercent: five, resetsAt: soon, model: model)) }
        if let week { w.append(UsageWindow(label: "7d", windowMinutes: 10080, usedPercent: week, resetsAt: soon, model: model)) }
        return UsageAccount(id: id, provider: provider, name: id, windows: w, sampledAt: .now, stale: stale, local: local)
    }

    @Test func theQuotaIsClaudesAccountReadOnTheComputers() throws {
        #expect(Ultracode.quota([]) == nil, "nothing without data")
        #expect(Ultracode.quota([account("codex", provider: "codex")]) == nil)
        let q = try #require(Ultracode.quota([account("gateway", local: false, five: 95), account("mine", five: 40), account("codex", provider: "codex", five: 99)]))
        #expect(q.name == "mine", "read on the computers before a gateway's")
        #expect(q.windows.map(\.kind) == [.fiveHours, .week])
        #expect(!q.warn)
        let stale = try #require(Ultracode.quota([account("old", stale: true, five: 10), account("fresh", five: 5)]))
        #expect(stale.name == "fresh", "fresh before stale")
        let fullest = try #require(Ultracode.quota([account("a", five: 10), account("b", five: 86)]))
        #expect(fullest.name == "b")
        #expect(fullest.warn, "80% and up")
        #expect(Ultracode.quota([account("scoped", model: "Fable")]) == nil, "one model's allowance is not the plan's")
        let line = Ultracode.quotaLine(fullest)
        #expect(line.contains("b") && line.contains("86%") && line.contains("20%"))
        #expect(!fullest.stale && Ultracode.quotaAge(fullest) == nil)
    }

    /// Numbers the hub marked out of date say so, with their age, as the usage bars do; any fresh account comes first,
    /// even one only a gateway reports (#1906).
    @Test func anOutOfDateQuotaSaysSo() throws {
        let now = Date.now
        var old = account("old", week: 62)
        old.stale = true
        old.sampledAt = now.addingTimeInterval(-2 * 3600)
        let q = try #require(Ultracode.quota([old], now: now))
        #expect(q.stale && q.sampledAt == old.sampledAt)
        #expect(Ultracode.quotaAge(q, now: now) == UsageWindows.staleLabel(old.sampledAt, now: now))
        #expect(Ultracode.quotaAge(q, now: now)?.contains("已过期") == true)
        let words = Ultracode.menuWords(try session(), ask: nil, quota: q, headerWraps: true)
        #expect(words.header?.hasPrefix(Ultracode.quotaLine(q) + "\n" + "已过期") == true, "the menu header says it too, right under the numbers")
        let pool = account("pool", local: false, five: 30)
        #expect(Ultracode.quota([old, pool], now: now)?.name == "pool", "a fresh gateway account before old numbers from a computer")
        var oldPool = account("old pool", local: false, five: 90)
        oldPool.stale = true
        #expect(Ultracode.quota([oldPool, old], now: now)?.name == "old", "both old: the computer's first")
    }

    // MARK: The request through the app

    @Test func aRefusalPutsItBackAndSaysWhyOnce() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/modes") { _ in
            throw APIError(status: 409, message: "node 太旧", fields: ["code": "node_outdated", "error": "node 太旧"])
        }
        let model = AppModel()
        model.apiForTests = hub.client
        model.applySessions([MachineRef(group: "", id: "box"): [try session(#","ultracode":false"#)]])
        let key = SessionKey(machineID: "box", sessionID: "s1")
        #expect(await !model.setUltracode(key, on: true))
        #expect(model.ultracodeAsk(key) == nil, "nothing kept: asking again gets the node's answer again")
        #expect(model.toast?.message.contains("node 太旧") == true)
        #expect(model.toast?.style == .error)
        #expect(Ultracode.readinessUntouched(model), "a node_outdated here is no readiness card")
    }

    @Test func theSwitchStandsWhereItWasPutUntilThePush() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/modes", json: #"{"ok":true}"#)
        let model = AppModel()
        model.apiForTests = hub.client
        let ref = MachineRef(group: "", id: "box")
        model.applySessions([ref: [try session(#","ultracode":false"#, state: "working")]])
        let key = SessionKey(machineID: "box", sessionID: "s1")
        #expect(await model.setUltracode(key, on: true))
        let s = try #require(model.session(key))
        #expect(Ultracode.switchState(s, ask: model.ultracodeAsk(key)).on, "the push has not come yet")
        model.applySessions([ref: [try session(#","ultracode":false,"ultracode_pending":true"#, state: "working")]])
        let queued = Ultracode.switchState(try #require(model.session(key)), ask: model.ultracodeAsk(key))
        #expect(queued.on && queued.line == Ultracode.pendingWords)
        model.applySessions([ref: [try session(#","ultracode":true"#)]])
        #expect(model.ultracodeAsk(key) == nil, "the node answered")
        #expect(model.ultracodeAsks[key] == nil, "and the ask is forgotten, not only hidden")
        #expect(Ultracode.switchState(try #require(model.session(key)), ask: model.ultracodeAsk(key)).on)
        let sent = hub.sent("POST", "/api/v1/sessions/box/s1/modes")
        #expect(sent.count == 1)
        #expect(try body(sent.first) as NSDictionary == ["ultracode": true] as NSDictionary)

        // Later another device turns it off while Claude works: the queue is that device's, not this one's old "on".
        model.applySessions([ref: [try session(#","ultracode":true,"ultracode_pending":true"#, state: "working")]])
        #expect(model.ultracodeAsk(key) == nil, "an answered ask does not come back with someone else's queue")
        let elsewhere = Ultracode.switchState(try #require(model.session(key)), ask: model.ultracodeAsk(key))
        #expect(elsewhere.line == Ultracode.queuedWords(ask: nil))
        #expect(elsewhere.line != Ultracode.pendingWords)
        #expect(elsewhere.on, "where the node saw it, not where the old ask put it")
    }

    @Test func anAskForASessionThatWentIsForgotten() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/modes", json: #"{"ok":true}"#)
        let model = AppModel()
        model.apiForTests = hub.client
        let ref = MachineRef(group: "", id: "box")
        let key = SessionKey(machineID: "box", sessionID: "s1")
        model.applySessions([ref: [try session(#","ultracode":false"#, state: "working")]])
        #expect(await model.setUltracode(key, on: true))
        model.applySessions([ref: [try session(id: "s2")]])
        #expect(model.ultracodeAsks[key] == nil)
    }

    @Test func theQuotaIsLoadedAgainOnceItIsTwoMinutesOld() {
        let now = Date.now
        #expect(UsageWindowsStore.isOld(nil, maxAge: 120, now: now), "never loaded")
        #expect(!UsageWindowsStore.isOld(now.addingTimeInterval(-119), maxAge: 120, now: now))
        #expect(UsageWindowsStore.isOld(now.addingTimeInterval(-120), maxAge: 120, now: now))
        #expect(UsageWindowsStore.isOld(now.addingTimeInterval(-1800), maxAge: 120, now: now), "a chat left open for half an hour")
    }

    @Test func dismissingTheNoticeTakesItAwayAtOnceAndBackOnFailure() async throws {
        let lost = #","ultracode":false,"ultracode_lost_at":"2026-11-01T08:00:00Z""#
        let key = SessionKey(machineID: "box", sessionID: "s1")
        let ok = FakeHub()
        ok.on("POST", "/api/v1/sessions/box/s1/modes", json: #"{"ok":true}"#)
        let model = AppModel()
        model.apiForTests = ok.client
        model.applySessions([MachineRef(group: "", id: "box"): [try session(lost)]])
        await model.dismissUltracodeLost(key)
        #expect(model.session(key)?.ultracodeLostAt == nil)
        #expect(try body(ok.sent("POST", "/api/v1/sessions/box/s1/modes").first) as NSDictionary == ["ultracode_dismiss_lost": true] as NSDictionary)

        let failing = FakeHub()
        failing.on("POST", "/api/v1/sessions/box/s1/modes") { _ in throw APIError(status: 502, message: "node unreachable") }
        let other = AppModel()
        other.apiForTests = failing.client
        other.applySessions([MachineRef(group: "", id: "box"): [try session(lost)]])
        await other.dismissUltracodeLost(key)
        #expect(other.session(key)?.ultracodeLostAt != nil, "back when the hub says no")
        #expect(other.toast?.style == .error)
    }
}

private extension Session {
    func withNote(_ note: String) -> Session {
        var s = self
        s.ultracodeNote = note
        return s
    }
}

private extension Ultracode {
    /// A refused switch is said in a toast, not turned into the main chat's readiness card (`CodedFailure`).
    @MainActor static func readinessUntouched(_ model: AppModel) -> Bool { model.readiness.failure == nil }
}
