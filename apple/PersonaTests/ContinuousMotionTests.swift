import Foundation
import Testing

/// Every animation that runs on its own, without end, is listed here with why it moves and where it stops for Reduce
/// Motion (docs/apple/ios.md, "Design language", Motion). A new one fails until it is added, so nobody ships a loop
/// without asking whether it should move at all and what it shows with Reduce Motion. An entry whose file no longer
/// has the loop fails too: take it out rather than leave a stale line. Reads the sources, like `HoldSourceTests`.
struct ContinuousMotionTests {
    private static let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    /// The app targets; `Packages/` (BeautifulUI) is a library with its own rules.
    private static let dirs = ["Persona", "PersonaWatch", "PersonaWidgets", "PersonaWatchWidgets", "LiveActivity",
                               "Shared", "Mac", "Vision", "PersonaVisionWidgets", "PersonaLink"]
    private static let loop = try! NSRegularExpression(
        pattern: #"repeatForever|TimelineView\(\.animation|phaseAnimator|repeatCount = \.infinity|startAnimating\(\)"#)

    struct Entry {
        /// How many loops the file starts.
        var count: Int
        var why: String
        /// The file (under `apple/`) that reads the system's Reduce Motion and holds this one still, or nil when the
        /// system draws it and follows Reduce Motion itself.
        var stillIn: String?
    }

    static let registry: [String: Entry] = [
        "Persona/DesignSystem/BreathingRing.swift": Entry(count: 1, why: "Needs you: the dot breathes", stillIn: "Persona/DesignSystem/Components.swift"),
        "Mac/Design/MacBreathingRing.swift": Entry(count: 1, why: "Needs you on the Mac", stillIn: "Persona/DesignSystem/Components.swift"),
        "Persona/DesignSystem/VoiceOrb.swift": Entry(count: 1, why: "A voice call: listening and speaking", stillIn: "Persona/DesignSystem/VoiceOrb.swift"),
        "Persona/DesignSystem/ComposerHoldMic.swift": Entry(count: 1, why: "Recording while the finger holds the mic", stillIn: "Persona/DesignSystem/ComposerHoldMic.swift"),
        "Persona/Views/Assistant/AssistantAvatar.swift": Entry(count: 1, why: "The built-in persona working", stillIn: "Persona/Views/Assistant/AssistantAvatar.swift"),
        "Shared/Design/PersonaFace.swift": Entry(count: 1, why: "The built-in persona's blink", stillIn: "Persona/Views/Assistant/AssistantAvatar.swift"),
        "Persona/Persona/PersonaViews.swift": Entry(count: 1, why: "A downloaded persona's frames for its state", stillIn: "Persona/Persona/PersonaViews.swift"),
        "Mac/Media/PersonaFramesView+Mac.swift": Entry(count: 1, why: "A downloaded persona's frames on the Mac", stillIn: "Persona/Persona/PersonaViews.swift"),
        "Persona/Views/Assistant/AssistantView.swift": Entry(count: 1, why: "The assistant thinking (typing dots)", stillIn: "Persona/Views/Assistant/AssistantView.swift"),
        "Persona/Views/Chat/ChatView.swift": Entry(count: 1, why: "A chat loading (skeleton)", stillIn: "Persona/Views/Chat/ChatView.swift"),
        "Persona/Views/Chat/ImagePager.swift": Entry(count: 1, why: "An image loading (the system spinner)", stillIn: nil),
        "PersonaWatch/WatchPetView.swift": Entry(count: 1, why: "The Milk Frog's page on the watch", stillIn: "PersonaWatch/WatchPetView.swift"),
    ]

    /// Loops per file, comments left out.
    static func found() -> [String: Int] {
        var out: [String: Int] = [:]
        for dir in dirs {
            let urls = FileManager.default.enumerator(at: apple.appending(path: dir), includingPropertiesForKeys: nil)?
                .compactMap { $0 as? URL }.filter { $0.pathExtension == "swift" } ?? []
            for url in urls {
                guard let text = try? String(contentsOf: url, encoding: .utf8) else { continue }
                let code = text.components(separatedBy: "\n")
                    .map { l in l.range(of: "//").map { String(l[..<$0.lowerBound]) } ?? l }
                    .joined(separator: "\n")
                let n = loop.numberOfMatches(in: code, range: NSRange(code.startIndex..., in: code))
                if n > 0 { out[String(url.path.dropFirst(apple.path.count + 1))] = n }
            }
        }
        return out
    }

    @Test func readsTheSources() {
        #expect(FileManager.default.fileExists(atPath: Self.apple.appending(path: "Persona/Views").path))
        #expect(Self.found().count >= Self.registry.count / 2)
    }

    @Test func everyLoopIsRegistered() {
        let found = Self.found()
        let unlisted = found.filter { Self.registry[$0.key]?.count != $0.value }.map { "\($0.key): \($0.value)" }.sorted()
        #expect(unlisted.isEmpty, """
            a continuous animation not in ContinuousMotionTests.registry (or a changed count). Ask first whether it \
            should move on its own at all; if so, register it with its reason and where Reduce Motion holds it still:
            \(unlisted.joined(separator: "\n"))
            """)
        let stale = Self.registry.keys.filter { found[$0] == nil }.sorted()
        #expect(stale.isEmpty, "registered but no longer looping, take the entry out:\n\(stale.joined(separator: "\n"))")
    }

    @Test func everyLoopStopsForReduceMotion() throws {
        for (file, entry) in Self.registry {
            guard let still = entry.stillIn else { continue }
            let text = try String(contentsOf: Self.apple.appending(path: still), encoding: .utf8)
            #expect(text.contains("accessibilityReduceMotion") || text.contains("isReduceMotionEnabled") || text.contains("Motion.reduced"),
                    "\(file) (\(entry.why)): \(still) never reads Reduce Motion")
        }
    }
}
