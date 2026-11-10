import Foundation
import Testing
@testable import Persona

/// The subscription windows' pure part (docs/server/usage-windows.md), the same cases as the web's lib/usage-windows.test.ts.
struct UsageWindowsTests {
    let now = Date(timeIntervalSince1970: 1_793_000_000)

    private func win(_ label: String, _ mins: Int, used: Double, left: TimeInterval?, model: String? = nil) -> UsageWindow {
        UsageWindow(label: label, windowMinutes: mins, usedPercent: used, resetsAt: left.map { now.addingTimeInterval($0) }, model: model)
    }

    @Test func decodesTheHubsAnswer() throws {
        let json = """
        {"accounts":[{"id":"claude-1","provider":"claude","name":"work-claude","plan":"Max","sampled_at":"2026-11-05T09:08:06Z",
          "local":true,"machines":["MacBook"],"sources":["Relay"],"problem":"unreachable","stale":true,
          "windows":[{"label":"5h","window_minutes":300,"used_percent":86,"resets_at":"2026-11-05T13:08:06.123456Z"},
                     {"label":"7d","window_minutes":10080,"used_percent":100,"model":"Fable"}]},
         {"id":"codex-1","provider":"codex","name":"Codex Pro","windows":[]}],"checked_at":"2026-11-05T09:08:20Z"}
        """
        struct R: Decodable { var accounts: [UsageAccount] }
        let r = try JSON.decoder.decode(R.self, from: Data(json.utf8))
        let a = r.accounts[0]
        #expect(a.local && a.stale && a.sources == ["Relay"] && a.machines == ["MacBook"] && a.problem == "unreachable")
        #expect(a.sampledAt == JSON.parseDate("2026-11-05T09:08:06Z"))
        #expect(a.windows[0].resetsAt != nil && a.windows[0].windowMinutes == 300)
        #expect(a.windows[1].model == "Fable" && a.windows[1].resetsAt == nil)
        #expect(r.accounts[1].sampledAt == nil && !r.accounts[1].local && r.accounts[1].sources.isEmpty && r.accounts[1].machines.isEmpty)
    }

    @Test func windowLengthFromTheLabel() {
        #expect(UsageWindows.minutes(win("5h", 0, used: 0, left: nil)) == 300)
        #expect(UsageWindows.minutes(win("7d", 0, used: 0, left: nil)) == 10080)
        #expect(UsageWindows.minutes(win("45m", 0, used: 0, left: nil)) == 45)
        #expect(UsageWindows.minutes(win("1w", 0, used: 0, left: nil)) == 10080)
        #expect(UsageWindows.minutes(win("weekly", 10080, used: 0, left: nil)) == 10080)
        #expect(UsageWindows.minutes(win("?", -3, used: 0, left: nil)) == 0)
    }

    @Test func pace() {
        // Half of a 5h window gone: 50% expected.
        let half = UsageWindows.pace(win("5h", 300, used: 50, left: 150 * 60), now: now)
        #expect(half == UsageWindows.Pace(expected: 50, state: .on))
        #expect(UsageWindows.pace(win("5h", 300, used: 56, left: 150 * 60), now: now)?.state == .over)
        #expect(UsageWindows.pace(win("5h", 300, used: 44, left: 150 * 60), now: now)?.state == .under)
        #expect(UsageWindows.pace(win("5h", 300, used: 55, left: 150 * 60), now: now)?.state == .on) // 5 points is within
        // The first 5% is not judged; nor without a reset time, nor a reset farther off than the window.
        #expect(UsageWindows.pace(win("5h", 300, used: 30, left: 290 * 60), now: now) == nil)
        #expect(UsageWindows.pace(win("5h", 300, used: 30, left: nil), now: now) == nil)
        #expect(UsageWindows.pace(win("5h", 300, used: 30, left: 400 * 60), now: now) == nil)
    }

    @Test func resetLabel() {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "UTC")!
        cal.locale = Locale(identifier: "en_US_POSIX")
        // now is 2026-10-26 07:33:20 UTC.
        #expect(UsageWindows.resetLabel(now.addingTimeInterval(-1), now: now, calendar: cal) == "即将重置")
        #expect(UsageWindows.resetLabel(now.addingTimeInterval(44 * 60 + 10), now: now, calendar: cal) == "45m 后重置")
        #expect(UsageWindows.resetLabel(now.addingTimeInterval(2 * 86400 + 3 * 3600 + 30), now: now, calendar: cal) == "2d 3h 后重置")
        let today = UsageWindows.resetLabel(now.addingTimeInterval(3 * 3600), now: now, calendar: cal)
        #expect(today.hasSuffix(" 重置") && !today.contains("后") && today.contains("10:33"))
        #expect(UsageWindows.resetLabel(nil, now: now, calendar: cal) == "")
    }

    @Test func sampleAge() {
        #expect(UsageWindows.sampleAge(now.addingTimeInterval(-12), now: now) == "12 秒前")
        #expect(UsageWindows.sampleAge(now.addingTimeInterval(-190), now: now) == "3 分钟前")
        #expect(UsageWindows.sampleAge(now.addingTimeInterval(-3 * 3600 - 5), now: now) == "3 小时前")
        #expect(UsageWindows.sampleAge(now.addingTimeInterval(-2 * 86400), now: now) == "2 天前")
        #expect(UsageWindows.sampleAge(nil, now: now) == "")
        let stale = UsageAccount(id: "a", provider: "codex", name: "A", windows: [], sampledAt: now.addingTimeInterval(-7200), stale: true)
        #expect(UsageWindows.ageLabel(stale, now: now) == "已过期 · 2 小时前")
    }

    @Test func liveDropsOldSamplesAndResetWindows() {
        let fresh = UsageAccount(id: "a", provider: "codex", name: "A",
                                 windows: [win("5h", 300, used: 10, left: -60), win("7d", 10080, used: 20, left: 3600)], sampledAt: now)
        let allReset = UsageAccount(id: "b", provider: "codex", name: "B", windows: [win("5h", 300, used: 10, left: -1)], sampledAt: now)
        let old = UsageAccount(id: "c", provider: "codex", name: "C", windows: [win("7d", 10080, used: 1, left: 3600)],
                               sampledAt: now.addingTimeInterval(-4 * 86400))
        let live = UsageWindows.live([fresh, allReset, old], now: now)
        #expect(live.map(\.id) == ["a"])
        #expect(live[0].windows.map(\.label) == ["7d"])
    }

    /// Kept numbers age by `stale_at` (#1905): a group whose refresh fails keeps its last answer, and an account the hub
    /// called fresh then fades and reads "Outdated" once its time has passed; the bars put it last.
    @Test func liveJudgesStaleByStaleAt() throws {
        let json = """
        {"accounts":[{"id":"k","provider":"codex","name":"kept","sampled_at":"2026-11-05T09:00:00Z","stale_at":"2026-11-05T09:30:00Z",
          "windows":[{"label":"7d","window_minutes":10080,"used_percent":80}]}]}
        """
        struct R: Decodable { var accounts: [UsageAccount] }
        let kept = try #require(try JSON.decoder.decode(R.self, from: Data(json.utf8)).accounts.first)
        let staleAt = try #require(kept.staleAt)
        #expect(!kept.stale && staleAt == JSON.parseDate("2026-11-05T09:30:00Z"))
        #expect(UsageWindows.live([kept], now: staleAt.addingTimeInterval(-1)).first?.stale == false)
        let later = staleAt.addingTimeInterval(3600)
        let aged = try #require(UsageWindows.live([kept], now: later).first)
        #expect(aged.stale && UsageWindows.ageLabel(aged, now: later).hasPrefix("已过期"))
        let other = UsageAccount(id: "o", provider: "claude", name: "other",
                                 windows: [UsageWindow(label: "7d", windowMinutes: 10080, usedPercent: 10, resetsAt: later.addingTimeInterval(3600), model: nil)], sampledAt: later)
        #expect(UsageWindows.top(UsageWindows.live([kept, other], now: later)).map(\.id) == ["o", "k"], "stale ones last")
        var older = kept // an older hub sends no stale_at: its own mark stands
        older.staleAt = nil
        #expect(UsageWindows.live([older], now: later).first?.stale == false)
    }

    @Test func fullestAndTop() {
        let scoped = UsageAccount(id: "s", provider: "claude", name: "S",
                                  windows: [win("5h", 300, used: 30, left: 60), win("7d", 10080, used: 100, left: 60, model: "Fable")], sampledAt: now)
        #expect(UsageWindows.fullest(scoped) == 30) // a model running out is not the account running out
        let onlyModel = UsageAccount(id: "m", provider: "claude", name: "M", windows: [win("7d", 10080, used: 70, left: 60, model: "X")], sampledAt: now)
        #expect(UsageWindows.fullest(onlyModel) == 70)
        let high = UsageAccount(id: "h", provider: "codex", name: "H", windows: [win("5h", 300, used: 90, left: 60)], sampledAt: now)
        let staleFull = UsageAccount(id: "z", provider: "codex", name: "Z", windows: [win("5h", 300, used: 99, left: 60)], sampledAt: now, stale: true)
        #expect(UsageWindows.top([scoped, staleFull, onlyModel, high]).map(\.id) == ["h", "m"])
        #expect(UsageWindows.top([staleFull, scoped], n: 5).map(\.id) == ["s", "z"])
    }

    @Test func sourcesPlanAndProblem() {
        var a = UsageAccount(id: "a", provider: "codex", name: "Codex Pro", plan: "pro", windows: [], sampledAt: now, local: true, sources: ["Relay"])
        #expect(UsageWindows.sourceNames(a) == ["电脑", "Relay"]) // an older hub: no names, never "this device" (#1907)
        a.machines = ["MacBook", "Studio"]
        #expect(UsageWindows.sourceNames(a) == ["MacBook", "Studio", "Relay"])
        #expect(UsageWindows.planNote(a) == "") // the name says it
        a.name = "team-seat-2"
        #expect(UsageWindows.planNote(a) == "pro")
        #expect(UsageWindows.problemText(a) == "")
        a.problem = "unreachable"
        #expect(UsageWindows.problemText(a).contains("上次"))
        a.problem = "rejected" // the key was turned away: says where to fix it, not that the network is down (#1902)
        #expect(UsageWindows.problemText(a).contains("用量来源") && !UsageWindows.problemText(a).contains("暂时读不到"))
        a.problem = "failed"
        #expect(UsageWindows.problemText(a) == "读网关出错，显示的是上次的数字")
        a.problem = "something-newer"
        #expect(UsageWindows.problemText(a) == "")
        #expect(UsageWindows.isWarn(80) && !UsageWindows.isWarn(79.9))
        #expect(UsageWindows.percentText(85.6) == "86%")
        #expect(UsageWindows.fraction(140) == 1 && UsageWindows.fraction(-3) == 0)
    }
}
