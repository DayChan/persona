#if DEBUG
// i18n-ignore-file: DEBUG-only made-up recording for screenshots
import Foundation

/// Demo and screenshots use made-up text only and never start the microphone; with a hub paired they sync into its Knowledge as usual (UI tests check this step with the mock hub).
enum RecordingSamples {
    /// Once per launch: the page appears again after its recording was deleted, and must not bring it back.
    nonisolated(unsafe) private static var installed = false

    static func installIfAsked() -> UUID? {
        guard UserDefaults.standard.bool(forKey: "showRecordingSample"), !installed else { return nil }
        installed = true
        let id = UUID(uuidString: "00000000-0000-0000-0000-000000000001")!
        RecordingLibrary.shared.restore(id)
        var entry = RecordingEntry(id: id, createdAt: Date(timeIntervalSince1970: 1_800_000_000), source: .watch,
                                   title: "项目讨论", active: false, expectedSegments: 1)
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 90, sentences: [
            RecordingSentence(start: 0, end: 12, text: "这次讨论确定三个重点：先验证核心流程，再补充测试，最后检查体验。"),
            RecordingSentence(start: 28, end: 40, text: "下周整理一次进展，把未解决的问题列出来。"),
            RecordingSentence(start: 65, end: 78, text: "需要把今天的结论变成待办，避免遗漏。"),
        ])]
        entry.marks = [28]
        try? RecordingLibrary.shared.save(entry)
        return id
    }

    /// The meeting being recorded (`-showMeetingSample recording|paused|interrupted`).
    static let liveMeeting = UUID(uuidString: "00000000-0000-0000-0000-000000000002")!
    /// A finished meeting, transcribed (`-showMeetingSample minutes|pending` opens it).
    static let pastMeeting = UUID(uuidString: "00000000-0000-0000-0000-000000000003")!

    /// `-showMeetingSample recording|paused|interrupted`: a meeting 12:34 into its recording, as the recording page and its
    /// Live Activity show it (no microphone); `minutes` / `pending`: a finished meeting whose page opens (its minutes from
    /// the hub, or still being written). Both recordings are made up.
    static func showMeetingIfAsked() {
        #if os(iOS)
        guard let mode = UserDefaults.standard.string(forKey: "showMeetingSample") else {
            // A run without them: an earlier run's made-up meetings go, so other tests see only their own recordings.
            for id in [liveMeeting, pastMeeting] where RecordingLibrary.shared.entry(id) != nil { try? RecordingLibrary.shared.remove(id) }
            return
        }
        let start = Date(timeIntervalSince1970: 1_800_003_600)
        let meeting = RecordingMeeting(eventID: "sample-weekly", title: "产品周会", start: start, end: start.addingTimeInterval(3600),
                                       location: "3 楼会议室", attendees: ["张三", "李四", "王五"])
        RecordingLibrary.shared.restore(pastMeeting)
        var past = RecordingEntry(id: pastMeeting, createdAt: start, source: .phone, title: "产品周会", active: false, expectedSegments: 2)
        past.meeting = meeting
        past.segments = [
            RecordingSegment(index: 0, start: 0, duration: 60, sentences: [
                RecordingSentence(start: 2, end: 14, text: "今天先过一下新版录音入口，目标是一步就能开始录。"),
                RecordingSentence(start: 21, end: 38, text: "锁屏和灵动岛要能看到时长，也能直接停止。"),
                RecordingSentence(start: 44, end: 58, text: "决定：默认用中文转写，中英混说也要能用。"),
            ]),
            RecordingSegment(index: 1, start: 60, duration: 60, sentences: [
                RecordingSentence(start: 63, end: 80, text: "张三负责周五前补上长录音的真机测试。"),
                RecordingSentence(start: 86, end: 101, text: "还没定的是 Mac 上要不要也支持录音。"),
                RecordingSentence(start: 105, end: 118, text: "下周同一时间再对一次进展。"),
            ]),
        ]
        past.marks = [44]
        try? RecordingLibrary.shared.save(past)
        if mode == "minutes" || mode == "pending" { return }

        RecordingLibrary.shared.restore(liveMeeting)
        var live = RecordingEntry(id: liveMeeting, createdAt: .now.addingTimeInterval(-754), source: .phone, title: "产品周会", active: true)
        live.meeting = meeting
        live.segments = [RecordingSegment(index: 0, start: 0, duration: 30, sentences: [
            RecordingSentence(start: 1, end: 9, text: "好，我们开始，今天先看录音入口的进展。"),
        ])]
        live.marks = [65, 300]
        try? RecordingLibrary.shared.save(live)
        RecordingCapture.shared.showSample(liveMeeting, elapsed: 754, paused: mode == "paused", interrupted: mode == "interrupted")
        #endif
    }
}
#endif
