import Foundation
import Testing
@testable import PersonaKit

/// The voice call's audio route (#3015, docs/assistant/voice.md 6.4 "Audio route"): the connected device, else the
/// loudspeaker, never the earpiece; changes followed only when a device comes or goes.
struct VoiceRouteTests {
    private typealias Device = VoiceRoute.Device
    private typealias Plan = VoiceRoute.Plan

    private static let mic = Device(port: .builtInMic, id: "mic")
    private static let speaker = Device(port: .builtInSpeaker, id: "speaker")
    private static let receiver = Device(port: .builtInReceiver, id: "receiver")
    private static let airPods = Device(port: .bluetoothHFP, id: "aa:hfp", name: "AirPods Pro")
    private static let airPodsA2DP = Device(port: .bluetoothA2DP, id: "aa:a2dp", name: "AirPods Pro")
    private static let otherHeadset = Device(port: .bluetoothHFP, id: "bb:hfp", name: "Headset")
    private static let btSpeaker = Device(port: .bluetoothA2DP, id: "cc:a2dp", name: "Speaker Box")
    private static let wired = Device(port: .headphones, id: "wired")
    private static let wiredMic = Device(port: .headsetMic, id: "wired-mic")
    private static let car = Device(port: .carAudio, id: "car")
    private static let usb = Device(port: .usbAudio, id: "usb")

    private func route(out: [Device], in inputs: [Device], available: [Device]) -> VoiceRoute.Snapshot {
        VoiceRoute.Snapshot(outputs: out, inputs: inputs, available: available)
    }

    // MARK: Nothing connected

    @Test func nothingConnectedStaysOnTheSpeaker() {
        let r = route(out: [Self.speaker], in: [Self.mic], available: [Self.mic])
        #expect(VoiceRoute.plan(r, at: .start) == .keep)
    }

    @Test func theEarpieceIsMovedToTheSpeaker() {
        let r = route(out: [Self.receiver], in: [Self.mic], available: [Self.mic])
        for moment in [VoiceRoute.Moment.start, .arrived, .left] {
            #expect(VoiceRoute.plan(r, at: moment) == Plan(speaker: true))
        }
    }

    // MARK: Bluetooth

    @Test func airPodsAlreadyOnTheRouteStay() {
        let r = route(out: [Self.airPods], in: [Self.airPods], available: [Self.mic, Self.airPods])
        #expect(VoiceRoute.plan(r, at: .start) == .keep)
    }

    /// The reported bug: AirPods connected, the call on the loudspeaker.
    @Test func connectedAirPodsLeftOutTakeTheCall() {
        let r = route(out: [Self.speaker], in: [Self.mic], available: [Self.mic, Self.airPods])
        #expect(VoiceRoute.plan(r, at: .start) == Plan(input: "aa:hfp"))
        // Also from the earpiece: the headset wins over the speaker override.
        let earpiece = route(out: [Self.receiver], in: [Self.mic], available: [Self.mic, Self.airPods])
        #expect(VoiceRoute.plan(earpiece, at: .start) == Plan(input: "aa:hfp"))
    }

    @Test func listeningOnlyAirPodsBringTheirMicrophone() {
        let r = route(out: [Self.airPodsA2DP], in: [Self.mic], available: [Self.mic, Self.airPods])
        #expect(VoiceRoute.plan(r, at: .start) == Plan(input: "aa:hfp"))
    }

    @Test func aBluetoothSpeakerKeepsThePhoneMicrophone() {
        let r = route(out: [Self.btSpeaker], in: [Self.mic], available: [Self.mic])
        #expect(VoiceRoute.plan(r, at: .start) == .keep)
        // Another headset's microphone would pull the call away from the speaker the system picked last.
        let both = route(out: [Self.btSpeaker], in: [Self.mic], available: [Self.mic, Self.otherHeadset])
        #expect(VoiceRoute.plan(both, at: .start) == .keep)
    }

    @Test func anotherHeadsetsMicrophoneIsNeverAskedFor() {
        let r = route(out: [Self.airPods], in: [Self.mic], available: [Self.mic, Self.otherHeadset])
        #expect(VoiceRoute.plan(r, at: .start) == .keep)
        let byName = Device(port: .bluetoothHFP, id: "aa:hfp-in", name: "AirPods Pro")
        let named = route(out: [Self.airPods], in: [Self.mic], available: [Self.mic, Self.otherHeadset, byName])
        #expect(VoiceRoute.plan(named, at: .start) == Plan(input: "aa:hfp-in"))
    }

    // MARK: Wired

    @Test func aWiredHeadsetsMicrophoneFollowsItsEarpieces() {
        let r = route(out: [Self.wired], in: [Self.mic], available: [Self.mic, Self.wiredMic])
        #expect(VoiceRoute.plan(r, at: .arrived) == Plan(input: "wired-mic"))
        let recording = route(out: [Self.wired], in: [Self.wiredMic], available: [Self.mic, Self.wiredMic])
        #expect(VoiceRoute.plan(recording, at: .arrived) == .keep)
    }

    @Test func headphonesWithoutAMicrophoneKeepThePhones() {
        let r = route(out: [Self.wired], in: [Self.mic], available: [Self.mic])
        #expect(VoiceRoute.plan(r, at: .arrived) == .keep)
    }

    @Test func aWiredHeadsetLeftOutTakesTheCall() {
        let r = route(out: [Self.speaker], in: [Self.mic], available: [Self.mic, Self.wiredMic])
        #expect(VoiceRoute.plan(r, at: .start) == Plan(input: "wired-mic"))
    }

    // MARK: CarPlay, USB, priority

    @Test func carPlayKeepsTheCar() {
        let r = route(out: [Self.car], in: [Self.car], available: [Self.car, Self.mic])
        #expect(VoiceRoute.plan(r, at: .start) == .keep)
        let phoneMic = route(out: [Self.car], in: [Self.mic], available: [Self.mic, Self.car])
        #expect(VoiceRoute.plan(phoneMic, at: .start) == Plan(input: "car"))
    }

    @Test func leftOutDevicesInOrderCarWiredBluetoothUSB() {
        let all = [Self.mic, Self.usb, Self.airPods, Self.wiredMic, Self.car]
        #expect(VoiceRoute.plan(route(out: [Self.speaker], in: [Self.mic], available: all), at: .start) == Plan(input: "car"))
        let noCar = Array(all.dropLast())
        #expect(VoiceRoute.plan(route(out: [Self.speaker], in: [Self.mic], available: noCar), at: .start) == Plan(input: "wired-mic"))
        let bluetoothOrUSB = [Self.mic, Self.usb, Self.airPods]
        #expect(VoiceRoute.plan(route(out: [Self.speaker], in: [Self.mic], available: bluetoothOrUSB), at: .start) == Plan(input: "aa:hfp"))
    }

    @Test func anExternalMicrophoneAlreadyRecordingStays() {
        let r = route(out: [Self.speaker], in: [Self.usb], available: [Self.mic, Self.usb, Self.airPods])
        #expect(VoiceRoute.plan(r, at: .arrived) == .keep)
        let earpiece = route(out: [Self.receiver], in: [Self.usb], available: [Self.mic, Self.usb])
        #expect(VoiceRoute.plan(earpiece, at: .start) == Plan(speaker: true))
    }

    // MARK: The user's choice, and no loops

    @Test func otherChangesAreLeftAlone() {
        // The user picked the speaker (or the earpiece) in Control Center with AirPods connected.
        let speaker = route(out: [Self.speaker], in: [Self.mic], available: [Self.mic, Self.airPods])
        let earpiece = route(out: [Self.receiver], in: [Self.mic], available: [Self.mic, Self.airPods])
        #expect(VoiceRoute.plan(speaker, at: .other) == .keep)
        #expect(VoiceRoute.plan(earpiece, at: .other) == .keep)
    }

    @Test func applyingThePlanSettlesIt() {
        // Asked for the AirPods' microphone: the route moves there, and the next look keeps it.
        let after = route(out: [Self.airPods], in: [Self.airPods], available: [Self.mic, Self.airPods])
        #expect(VoiceRoute.plan(after, at: .start) == .keep)
        // Overridden to the speaker: kept.
        #expect(VoiceRoute.plan(route(out: [Self.speaker], in: [Self.mic], available: [Self.mic]), at: .start) == .keep)
    }

    @Test func aRefusedMicrophoneIsNotAskedForAgain() {
        let r = route(out: [Self.receiver], in: [Self.mic], available: [Self.mic, Self.airPods])
        #expect(VoiceRoute.plan(r, at: .start, refused: "aa:hfp") == Plan(speaker: true))
        let next = route(out: [Self.speaker], in: [Self.mic], available: [Self.mic, Self.airPods, Self.wiredMic])
        #expect(VoiceRoute.plan(next, at: .start, refused: "wired-mic") == Plan(input: "aa:hfp"))
    }

    // MARK: Over a call

    private let t0 = ContinuousClock.now

    @Test func theStartAsksOnceAndThenFallsBackToTheSpeaker() {
        var follower = VoiceRoute.Follower(startedAt: t0)
        let stuck = route(out: [Self.receiver], in: [Self.mic], available: [Self.mic, Self.airPods])
        #expect(follower.starting(stuck) == Plan(input: "aa:hfp"))
        // The engine is built and the route did not move: not again, and never the earpiece.
        #expect(follower.starting(stuck) == Plan(speaker: true))
    }

    @Test func aHandsFreeMicrophoneThatComesUpLateIsTakenWhileSettling() {
        var follower = VoiceRoute.Follower(startedAt: t0)
        let early = route(out: [Self.speaker], in: [Self.mic], available: [Self.mic])
        #expect(follower.starting(early) == .keep)
        let late = route(out: [Self.speaker], in: [Self.mic], available: [Self.mic, Self.airPods])
        #expect(follower.changed(late, .other, at: t0 + .milliseconds(800)) == Plan(input: "aa:hfp"))
    }

    @Test func afterSettlingTheUsersPickStays() {
        var follower = VoiceRoute.Follower(startedAt: t0)
        let onAirPods = route(out: [Self.airPods], in: [Self.airPods], available: [Self.mic, Self.airPods])
        #expect(follower.starting(onAirPods) == .keep)
        let picked = route(out: [Self.speaker], in: [Self.mic], available: [Self.mic, Self.airPods])
        #expect(follower.changed(picked, .other, at: t0 + .seconds(5)) == .keep)
    }

    @Test func pluggingAndUnpluggingDuringACallIsFollowed() {
        var follower = VoiceRoute.Follower(startedAt: t0)
        let alone = route(out: [Self.speaker], in: [Self.mic], available: [Self.mic])
        #expect(follower.starting(alone) == .keep)
        let plugged = route(out: [Self.wired], in: [Self.mic], available: [Self.mic, Self.wiredMic])
        #expect(follower.changed(plugged, .deviceArrived, at: t0 + .seconds(30)) == Plan(input: "wired-mic"))
        let unplugged = route(out: [Self.receiver], in: [Self.mic], available: [Self.mic])
        #expect(follower.changed(unplugged, .deviceLeft, at: t0 + .seconds(60)) == Plan(speaker: true))
    }

    @Test func aDeviceComingOrGoingClearsARefusal() {
        var follower = VoiceRoute.Follower(startedAt: t0)
        let stuck = route(out: [Self.speaker], in: [Self.mic], available: [Self.mic, Self.airPods])
        #expect(follower.starting(stuck) == Plan(input: "aa:hfp"))
        #expect(follower.changed(stuck, .other, at: t0 + .seconds(1)) == .keep)
        // AirPods reconnected: asked for again.
        #expect(follower.changed(stuck, .deviceArrived, at: t0 + .seconds(40)) == Plan(input: "aa:hfp"))
    }

    @Test func bluetoothGoneMidCallFallsBackToTheSpeakerOrTheNextHeadset() {
        var follower = VoiceRoute.Follower(startedAt: t0)
        let onAirPods = route(out: [Self.airPods], in: [Self.airPods], available: [Self.mic, Self.airPods])
        #expect(follower.starting(onAirPods) == .keep)
        let gone = route(out: [Self.speaker], in: [Self.mic], available: [Self.mic])
        #expect(follower.changed(gone, .deviceLeft, at: t0 + .seconds(20)) == .keep)
        let wiredStill = route(out: [Self.speaker], in: [Self.mic], available: [Self.mic, Self.wiredMic])
        #expect(follower.changed(wiredStill, .deviceLeft, at: t0 + .seconds(25)) == Plan(input: "wired-mic"))
    }
}
