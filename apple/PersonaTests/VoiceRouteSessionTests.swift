import AVFoundation
import PersonaKit
import Testing
@testable import Persona

/// The call's audio session and the ports `VoiceRoute` decides on (#3015, docs/assistant/voice.md 6.4 "Audio route"); the
/// policy itself is tested in PersonaKit (`VoiceRouteTests`).
@MainActor struct VoiceRouteSessionTests {
    @Test func bluetoothAndTheLoudspeakerAreAllowed() {
        let options = PhoneVoiceAudio.sessionOptions
        #expect(options.contains(.allowBluetoothHFP))
        #expect(options.contains(.allowBluetoothA2DP))
        #expect(options.contains(.defaultToSpeaker))
        #expect(!options.contains(.allowAirPlay), "too much delay for a conversation")
        #expect(!options.contains(.mixWithOthers))
    }

    @Test func portsMapOntoTheRoutePolicy() {
        let expected: [(AVAudioSession.Port, VoiceRoute.Port)] = [
            (.builtInMic, .builtInMic), (.builtInReceiver, .builtInReceiver), (.builtInSpeaker, .builtInSpeaker),
            (.headphones, .headphones), (.headsetMic, .headsetMic), (.bluetoothHFP, .bluetoothHFP),
            (.bluetoothA2DP, .bluetoothA2DP), (.bluetoothLE, .bluetoothLE), (.usbAudio, .usbAudio), (.carAudio, .carAudio),
            (.airPlay, .other), (.HDMI, .other), (.lineIn, .other), (.lineOut, .other),
        ]
        for (port, kind) in expected {
            #expect(PhoneVoiceAudio.kind(port) == kind, "\(port.rawValue)")
        }
    }
}
