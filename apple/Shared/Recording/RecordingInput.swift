import Foundation
#if os(iOS) || os(watchOS)
import AVFoundation
#endif

/// Which microphone a phone recording listens to: the iPhone's own mic, or a
/// Bluetooth HFP input (Meta glasses, Bluetooth headsets) when one is connected.
nonisolated enum RecordingInput: String, CaseIterable, Sendable {
    case phone
    case bluetooth

    var title: String {
        switch self {
        case .phone: String(localized: "Phone Microphone")
        case .bluetooth: String(localized: "Glasses / Bluetooth Headset")
        }
    }

    /// AppStorage key that remembers the user's last choice.
    static let storageKey = "recording.input"

    #if os(iOS) || os(watchOS)
    /// Index of the input port to prefer for `input`, or nil when no port fits.
    static func preferredIndex(_ ports: [AVAudioSession.Port], for input: RecordingInput) -> Int? {
        switch input {
        case .phone: ports.firstIndex(of: .builtInMic)
        case .bluetooth: ports.firstIndex(of: .bluetoothHFP)
        }
    }
    #endif

    #if os(iOS)
    /// True when a Bluetooth audio device (headset or glasses) is part of the current route.
    static var bluetoothConnected: Bool {
        let route = AVAudioSession.sharedInstance().currentRoute
        let types: Set<AVAudioSession.Port> = [.bluetoothHFP, .bluetoothA2DP, .bluetoothLE]
        return (route.inputs + route.outputs).contains { types.contains($0.portType) }
    }

    /// The microphone a recording started outside the Recordings page uses: the one chosen there, the Bluetooth one only
    /// while it is still connected.
    static var chosen: RecordingInput {
        guard bluetoothConnected else { return .phone }
        return UserDefaults.standard.string(forKey: storageKey).flatMap(RecordingInput.init(rawValue:)) ?? .phone
    }
    #endif
}
