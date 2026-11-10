import Foundation

/// Where a voice call's audio goes (docs/assistant/voice.md 6.4 "Audio route", #3015): the device the user connected (a
/// Bluetooth or wired headset, CarPlay, a USB interface) plays the voice, and its microphone records when it has one;
/// with nothing connected, the loudspeaker, never the earpiece. A device plugged in, unplugged, connected or lost during
/// the call is followed. Any other route change (the user's own pick in Control Center, the call's own preferred input
/// or override) is left as it is, so the call never fights the user. Pure: `PhoneVoiceAudio` reads the audio session into
/// a `Snapshot` and applies the `Plan`; `VoiceRouteTests`.
public nonisolated enum VoiceRoute {
    /// The audio ports the policy tells apart (the app maps `AVAudioSession.Port` onto them).
    public enum Port: Equatable, Hashable, Sendable {
        case builtInMic
        /// The earpiece.
        case builtInReceiver
        /// The loudspeaker.
        case builtInSpeaker
        /// Wired headphones, or a wired headset's earpieces.
        case headphones
        /// A wired headset's microphone.
        case headsetMic
        /// Bluetooth hands-free: the voice and a microphone (AirPods, headsets, a car without CarPlay).
        case bluetoothHFP
        /// Bluetooth for listening only: no microphone.
        case bluetoothA2DP
        /// Bluetooth Low Energy audio for listening (hearing aids).
        case bluetoothLE
        case usbAudio
        case carAudio
        /// Anything else (AirPlay, HDMI, line in or out…): external, but never asked for by the policy.
        case other

        /// The phone's own earpiece, loudspeaker or microphone.
        public var isBuiltIn: Bool { self == .builtInMic || self == .builtInReceiver || self == .builtInSpeaker }
    }

    /// A port of the current route, or an input the session offers.
    public struct Device: Equatable, Sendable {
        public var port: Port
        /// The session's `uid`: the plan names a microphone by it.
        public var id: String
        /// The device's name (`portName`): a Bluetooth device's earpieces and microphone share it.
        public var name: String

        public init(port: Port, id: String, name: String = "") {
            self.port = port
            self.id = id
            self.name = name
        }
    }

    /// The session as it is: `currentRoute`'s outputs and inputs, and `availableInputs`.
    public struct Snapshot: Equatable, Sendable {
        public var outputs: [Device]
        public var inputs: [Device]
        public var available: [Device]

        public init(outputs: [Device], inputs: [Device], available: [Device]) {
            self.outputs = outputs
            self.inputs = inputs
            self.available = available
        }
    }

    /// What a route change was about (`AVAudioSession.RouteChangeReason`, mapped by the app).
    public enum Change: Equatable, Sendable {
        /// `newDeviceAvailable`: headphones plugged in, a Bluetooth device connected.
        case deviceArrived
        /// `oldDeviceUnavailable`: headphones unplugged, a Bluetooth device gone.
        case deviceLeft
        /// Anything else: a pick in Control Center, the call's own preferred input or override, a category change…
        case other
    }

    /// When the route is decided.
    public enum Moment: Equatable, Sendable {
        /// The call starts, or the route is still settling after it started.
        case start
        case arrived
        case left
        /// Nothing to decide: what the route is now stays.
        case other
    }

    /// What to change; `keep` changes nothing.
    public struct Plan: Equatable, Sendable {
        /// The microphone to prefer (`setPreferredInput`), by `Device.id`. Asking for a Bluetooth hands-free or a wired
        /// headset microphone moves the voice to that device too.
        public var input: String?
        /// Send the voice to the loudspeaker (`overrideOutputAudioPort(.speaker)`): nothing is connected and the route
        /// is on the earpiece.
        public var speaker: Bool

        public init(input: String? = nil, speaker: Bool = false) {
            self.input = input
            self.speaker = speaker
        }

        public static let keep = Plan()
    }

    /// The decision for the route as it is now. Applying it and asking again gives `keep`. `refused` is a microphone
    /// asked for before that the route did not take: it is not asked for again.
    public static func plan(_ route: Snapshot, at moment: Moment, refused: String? = nil) -> Plan {
        guard moment != .other else { return .keep }
        if let output = route.outputs.first(where: { !$0.port.isBuiltIn }) {
            // A connected device plays: its own microphone with it, when it has one.
            guard let mic = microphone(of: output, in: route.available), mic.id != refused, !records(from: mic, route)
            else { return .keep }
            return Plan(input: mic.id)
        }
        // The phone plays. A headset the system left out takes the call (asking for its microphone brings the voice
        // along), unless an external microphone already records.
        if !route.inputs.contains(where: { !$0.port.isBuiltIn }),
           let mic = headset(in: route.available.filter { $0.id != refused }) {
            return Plan(input: mic.id)
        }
        // Nothing connected: the loudspeaker, never the earpiece.
        return route.outputs.contains(where: { $0.port == .builtInReceiver }) ? Plan(speaker: true) : .keep
    }

    /// The microphone that belongs with an external output: the same device's, else none (the phone's records).
    static func microphone(of output: Device, in available: [Device]) -> Device? {
        switch output.port {
        case .bluetoothHFP:
            // Only the same device's: asking for another headset's microphone would move the call to it.
            return available.first { $0.port == .bluetoothHFP && ($0.id == output.id || sameDevice($0, output)) }
        case .usbAudio, .carAudio:
            let same = available.filter { $0.port == output.port }
            return same.first { $0.id == output.id } ?? same.first
        case .headphones:
            return available.first { $0.port == .headsetMic }
        case .bluetoothA2DP, .bluetoothLE:
            // Listening only: the same device's hands-free microphone if it has one (the voice follows it); a speaker or
            // headphones without one keep the phone's microphone.
            return available.first { $0.port == .bluetoothHFP && sameDevice($0, output) }
        case .builtInMic, .builtInReceiver, .builtInSpeaker, .headsetMic, .other:
            return nil
        }
    }

    /// A connected device's microphone while the phone plays: CarPlay, then a wired headset, a Bluetooth hands-free, USB.
    static func headset(in available: [Device]) -> Device? {
        for port in [Port.carAudio, .headsetMic, .bluetoothHFP, .usbAudio] {
            if let mic = available.first(where: { $0.port == port }) { return mic }
        }
        return nil
    }

    private static func records(from mic: Device, _ route: Snapshot) -> Bool {
        route.inputs.contains { $0.id == mic.id }
    }

    private static func sameDevice(_ a: Device, _ b: Device) -> Bool {
        !a.name.isEmpty && a.name == b.name
    }

    /// One call's route over time. While the route settles after the start every change counts as the start (a
    /// Bluetooth hands-free microphone appears only a moment after the session goes active; recording waits up to two
    /// seconds for it); after that only a device coming or going is acted on. A microphone asked for once and not taken
    /// is not asked for again until a device comes or goes, so the call never loops on a route that will not change.
    public struct Follower: Sendable {
        public static let settle: Duration = .seconds(2)

        private let settled: ContinuousClock.Instant
        private var asked: String?

        public init(startedAt now: ContinuousClock.Instant) {
            settled = now + Self.settle
        }

        /// At the start, and again once the engine is built.
        public mutating func starting(_ route: Snapshot) -> Plan {
            decide(route, at: .start)
        }

        /// After a route change.
        public mutating func changed(_ route: Snapshot, _ change: Change, at now: ContinuousClock.Instant) -> Plan {
            switch change {
            case .deviceArrived:
                asked = nil
                return decide(route, at: .arrived)
            case .deviceLeft:
                asked = nil
                return decide(route, at: .left)
            case .other:
                return decide(route, at: now < settled ? .start : .other)
            }
        }

        private mutating func decide(_ route: Snapshot, at moment: Moment) -> Plan {
            // Asked for and still not recording: the route did not take it.
            let plan = VoiceRoute.plan(route, at: moment, refused: asked)
            if let input = plan.input { asked = input }
            return plan
        }
    }
}
