import BeautifulUI
import SwiftUI

/// The voice conversation's orb (docs/assistant/voice.md「设计系统」), in the middle of the voice bar: the fast layer (the realtime
/// voice model) at a glance. It breathes slowly while it listens, swells with the voice it hears, beats with its own voice
/// while Persona speaks, and a ring laps it while the call connects or the bound conversation works. With Reduce Motion
/// (or in Low Power Mode, as `PersonaDot`) every phase is a still frame, and Reduce Motion swaps one phase for the next
/// without a zoom. Everything it draws stays inside its box, the halo too, so it never washes over the words and buttons
/// beside it in the bar. While Persona speaks and `interrupt` is given it is a button, Interrupt Voice: a tap stops the
/// voice, never the agent (Stop Claude is the bar's own button, with its own words). Otherwise it is decoration and
/// VoiceOver skips it: the bar says the state in words beside it.
///
/// Every style draws it from the same roles, and the page only passes the phase: Liquid Glass and the solid styles a lit
/// sphere, pale in the persona's color while it listens and full while it speaks (`Theme.persona`); BeautifulUI a flat
/// disc with a five-bar level meter, as its prompt bar draws dictation. The ring is the persona's while connecting and
/// Working's amber while the agent works (`Theme.busy`); gray is a voice that is off (paused, muted, ended).
///
/// A clock drives it (`TimelineView`), paused whenever nothing moves; a repeating state animation leaked into the layout
/// of what sat around it (`StatusDot`). On the Mac a clock that never stops lays the whole window out every frame
/// (docs/lessons/macos.md): the Mac has no voice mode yet, and its orb should hand the motion to Core Animation.
struct VoiceOrb: View {
    /// What the orb shows. The call engine maps its state onto one: connecting and reconnecting → `connecting`; live →
    /// `listening`, `hearing` while the user's words come in, `speaking` while Persona's voice plays, `working` while it
    /// listens and the bound conversation works; the upstream paused while the agent works → `paused`; the microphone
    /// off while Persona is silent → `muted`; over → `ended`. `idle` is before the call starts.
    nonisolated enum Phase: String, CaseIterable, Sendable {
        case idle, connecting, listening, hearing, speaking, working, paused, muted, ended
    }

    var phase: Phase
    /// 0…1: the microphone's level while it listens or hears, Persona's voice while it speaks (the engine's levels, about
    /// 20 a second). Read only while a moving orb is drawn, so a new level redraws the orb and nothing around it.
    var level: () -> Double = { 0 }
    /// The box at the default text size, halo included; it grows with the text like every `Theme.Size`.
    var size: CGFloat = Theme.Size.voiceOrb
    /// What a tap does while Persona speaks: stop the voice. Without it the orb is never a button.
    var interrupt: (() -> Void)?

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.buiTheme) private var bui

    /// The sphere (or disc) inside its box: the rest is room to swell and glow into.
    static let sphere: CGFloat = 0.7
    /// The lap ring around it, as a multiple of the sphere, and how much of the ring its arc covers.
    static let ring: CGFloat = 1.3
    static let arc: CGFloat = 0.22

    private var moving: Bool { phase.moves && !reduceMotion && !ProcessInfo.processInfo.isLowPowerModeEnabled }
    private var isButton: Bool { phase == .speaking && interrupt != nil }

    var body: some View {
        let box = size * Theme.sizeScale(typeSize)
        // One Button whatever the phase, so a phase change animates the picture instead of swapping views; the guard and
        // `disabled` keep a keyboard (the Mac's, Full Keyboard Access) from interrupting a voice that is not speaking. Its
        // own style: `.plain` faded the disabled orb, which is every orb but the one that interrupts.
        Button { if isButton { interrupt?() } } label: {
            TimelineView(.animation(minimumInterval: Theme.Motion.voiceFrame, paused: !moving)) { context in
                let pose = VoiceOrbPose.make(phase, at: moving ? context.date.timeIntervalSinceReferenceDate : nil,
                                             level: moving && phase.hearsLevel ? level() : 0)
                picture(pose, box: box)
            }
            .scaledFrame(width: size, height: size)
            .contentShape(.circle)
            // Reduce Motion: the next phase's frame at once, not a zoom into it.
            .animation(reduceMotion ? nil : Theme.Motion.state, value: phase)
        }
        .buttonStyle(Press())
        .disabled(!isButton)
        .allowsHitTesting(isButton)
        .accessibilityHidden(!isButton)
        .accessibilityLabel("Interrupt Voice")
        .accessibilityHint("Stops only the voice; the work in progress carries on")
        .accessibilityIdentifier("voice-orb")
    }

    private func picture(_ pose: VoiceOrbPose, box: CGFloat) -> some View {
        let disc = box * Self.sphere
        return ZStack {
            if AppStyle.current.usesBeautifulUI {
                flat(pose, disc: disc, box: box)
            } else {
                lit(pose, disc: disc, box: box)
            }
            if let lap = pose.lap {
                lapRing(lap, disc: disc, box: box)
            }
        }
    }

    // MARK: Liquid Glass and the solid styles: a lit sphere

    private func lit(_ pose: VoiceOrbPose, disc: CGFloat, box: CGFloat) -> some View {
        let persona = Theme.persona
        let halo = Self.halo(pose)
        return ZStack {
            // Its own light, from under the sphere's edge out to the box's. The glow follows the sphere through where it
            // starts; the halo itself never scales, so it ends at the box's edge however far the sphere swells.
            Circle()
                .fill(RadialGradient(colors: [persona.opacity(pose.glow), persona.opacity(pose.glow * 0.35), .clear],
                                     center: .center, startRadius: box * halo.start, endRadius: box * halo.end))
                .frame(width: box, height: box)
            Circle()
                .fill(LinearGradient(colors: Self.litColors(phase), startPoint: .topLeading, endPoint: .bottomTrailing))
                // The sheen, up and to the left.
                .overlay {
                    Circle().fill(RadialGradient(colors: [Theme.onSolid.opacity(Self.sheen(phase)), .clear],
                                                 center: UnitPoint(x: 0.34, y: 0.28), startRadius: 0, endRadius: disc / 2))
                }
                .overlay { Circle().strokeBorder(Theme.cardStroke, lineWidth: Theme.Size.hairline) }
                .overlay { glyph(disc: disc, color: Theme.onSolid) }
                .frame(width: disc, height: disc)
                .scaleEffect(pose.scale)
        }
    }

    /// The sphere's colors, top left to bottom right. Paused and muted are a plain gray a little darker toward the
    /// bottom, so their white glyph reads (≥ 3:1, `VoiceOrbTests`); listening is the persona's pale, speaking the full.
    static func litColors(_ phase: Phase) -> [Color] {
        let persona = Theme.persona
        let gray = Theme.Hue.gray
        return switch phase {
        case .speaking: [persona.blended(with: .white, by: 0.3), persona, persona.blended(with: .black, by: 0.12)]
        case .paused, .muted: [gray, gray.blended(with: .black, by: 0.1)]
        case .ended: [gray.opacity(0.3), gray.opacity(0.45)]
        default: [persona.blended(with: .white, by: 0.62), persona.blended(with: .white, by: 0.25)]
        }
    }

    /// The white sheen's strength; none under a glyph, where it would wash the white glyph out.
    static func sheen(_ phase: Phase) -> Double { phase.glyph == nil ? 0.42 : 0 }

    /// The halo's gradient as shares of the box: it starts under the sphere's edge (following its scale) and fades out
    /// at the box's edge, never past it.
    static func halo(_ pose: VoiceOrbPose) -> (start: CGFloat, end: CGFloat) {
        (sphere * 0.45 * pose.scale, 0.5)
    }

    // MARK: BeautifulUI: a flat disc with a level meter

    private func flat(_ pose: VoiceOrbPose, disc: CGFloat, box: CGFloat) -> some View {
        let persona = Theme.persona
        let fill: Color = phase == .speaking ? persona : phase.moves || phase == .idle ? persona.opacity(0.12) : bui.colors.field
        let edge: Color = phase.moves || phase == .idle ? persona.opacity(0.3) : bui.colors.line
        return ZStack {
            // The pulse ring: it beats while Persona speaks, swells with what it hears, breathes while it listens.
            if pose.lap == nil, pose.glow > 0 {
                Circle()
                    .strokeBorder(persona.opacity(pose.glow), lineWidth: Theme.Size.line)
                    .frame(width: disc, height: disc)
                    .scaleEffect(pose.pulseRing)
            }
            Circle()
                .fill(fill)
                .overlay { Circle().strokeBorder(edge, lineWidth: Theme.Size.line) }
                .overlay {
                    if pose.bars.isEmpty {
                        glyph(disc: disc, color: bui.colors.ink2)
                    } else {
                        meter(pose.bars, box: box)
                    }
                }
                .frame(width: disc, height: disc)
        }
    }

    /// Five bars, the tallest in the middle; on the full persona disc while it speaks in white, or near black where
    /// white would not read (a light persona color), elsewhere in the persona's color, gray when the voice is over.
    private func meter(_ bars: [Double], box: CGFloat) -> some View {
        let persona = Theme.persona
        let speaking = phase == .speaking
        let quiet = !(phase.moves || phase == .idle)
        let k = box / Theme.Size.voiceOrb
        let ink = bui.colors.ink3
        return Canvas { ctx, area in
            let w = Theme.Size.voiceLevelBar * k, gap = Theme.Size.voiceLevelGap * k, tallest = Theme.Size.voiceLevelHeight * k
            let n = CGFloat(bars.count)
            let color = speaking ? Self.ink(on: persona, in: ctx.environment) : quiet ? ink : persona
            var x = (area.width - (n * w + (n - 1) * gap)) / 2
            for share in bars {
                let h = max(w, tallest * share)
                ctx.fill(Path(roundedRect: CGRect(x: x, y: (area.height - h) / 2, width: w, height: h), cornerRadius: w / 2),
                         with: .color(color))
                x += w + gap
            }
        }
    }

    /// White on `fill`, or near black where white would fall under 3:1 (a pale persona color, yellow).
    static func ink(on fill: Color, in environment: EnvironmentValues) -> Color {
        let c = fill.resolve(in: environment)
        let luminance = 0.2126 * Double(c.linearRed) + 0.7152 * Double(c.linearGreen) + 0.0722 * Double(c.linearBlue)
        return 1.05 / (luminance + 0.05) >= 3 ? Theme.onSolid : Color.black.opacity(0.82)
    }

    /// The orb as it is, enabled or not (the picture says the phase); pressed, it dims as a plain button does.
    private struct Press: ButtonStyle {
        func makeBody(configuration: Configuration) -> some View {
            configuration.label.opacity(configuration.isPressed ? 0.7 : 1)
        }
    }

    // MARK: Both

    /// What sits on a still orb whose voice is off: Paused's pause, Muted's slashed mic.
    @ViewBuilder private func glyph(disc: CGFloat, color: Color) -> some View {
        if let symbol = phase.glyph {
            ThemedIcon(symbol).font(.system(size: disc * 0.36, weight: .semibold)).foregroundStyle(color)
                .transition(.opacity)
        }
    }

    /// An arc lapping a faint track around the orb: the persona's while the call connects, amber while the agent works.
    private func lapRing(_ lap: Double, disc: CGFloat, box: CGFloat) -> some View {
        let color = phase == .working ? Theme.busy : Theme.persona
        let width = Theme.Size.rule * box / Theme.Size.voiceOrb
        return ZStack {
            Circle().stroke(color.opacity(0.2), lineWidth: width)
            Circle()
                .trim(from: 0, to: Self.arc)
                .stroke(color, style: StrokeStyle(lineWidth: width, lineCap: .round))
                .rotationEffect(.degrees(lap * 360 - 90))
        }
        .frame(width: disc * Self.ring, height: disc * Self.ring)
    }
}

nonisolated extension VoiceOrb.Phase {
    /// Moves by itself while motion is allowed: breathes, swells, beats, or a ring laps it. The call is on, and the orb
    /// wears the persona's color.
    var moves: Bool {
        switch self {
        case .connecting, .listening, .hearing, .speaking, .working: true
        case .idle, .paused, .muted, .ended: false
        }
    }

    /// Follows a level: the microphone's, or Persona's own voice.
    var hearsLevel: Bool {
        switch self {
        case .listening, .hearing, .speaking, .working: true
        case .idle, .connecting, .paused, .muted, .ended: false
        }
    }

    /// The symbol on an orb whose voice is off, in place of its meter.
    var glyph: String? {
        switch self {
        case .paused: Theme.Icon.pause
        case .muted: Theme.Icon.voiceMute
        default: nil
        }
    }
}

/// One frame of the orb, worked out from its phase, the time and the level; pure, so the tests can hold it to the tokens.
/// `at: nil` is the still frame (Reduce Motion, or a phase that does not move): the same at any time and any level.
nonisolated struct VoiceOrbPose: Equatable, Sendable {
    /// The sphere's scale (Liquid Glass and the solid styles).
    var scale: Double
    /// How strongly it glows: the sphere's halo, the BeautifulUI disc's pulse ring.
    var glow: Double
    /// The BeautifulUI disc's pulse ring, a multiple of the disc.
    var pulseRing: Double
    /// The level meter's bars, each a share of the tallest; none where a glyph sits instead.
    var bars: [Double]
    /// How far round the lap ring is, a share of a turn (connecting, working); nil without a ring.
    var lap: Double?

    /// The bars' heights at full level: the tallest in the middle.
    static let profile: [Double] = [0.55, 0.8, 1, 0.8, 0.55]

    static func make(_ phase: VoiceOrb.Phase, at t: TimeInterval?, level raw: Double) -> VoiceOrbPose {
        let level = min(max(raw, 0), 1)
        let breath = t.map { swing($0, oneWay: Theme.Motion.voiceBreath) } ?? 0
        let beat = t.map { swing($0, oneWay: Theme.Motion.voiceBeat) } ?? 0
        func bars(_ height: (Int) -> Double) -> [Double] { profile.indices.map { min(1, max(0, height($0) * profile[$0])) } }
        switch phase {
        case .idle:
            return VoiceOrbPose(scale: 0.9, glow: 0, pulseRing: 1, bars: bars { _ in 0 }, lap: nil)
        case .connecting:
            return VoiceOrbPose(scale: 0.92, glow: 0.12, pulseRing: 1, bars: bars { _ in 0.12 },
                                lap: t.map { turn($0, period: Theme.Motion.voiceSpin) } ?? 0)
        case .listening, .working:
            // Still, nothing is heard: the resting breath.
            let heard = t == nil ? 0 : level
            return VoiceOrbPose(scale: 1 + 0.03 * breath + 0.18 * heard, glow: 0.28 + 0.2 * heard,
                                pulseRing: 1.1 + 0.02 * (breath + 1) + 0.12 * heard,
                                bars: bars { i in 0.2 + 0.05 * breath + 0.8 * heard * wobble(t, i) },
                                lap: phase == .working ? t.map { turn($0, period: Theme.Motion.voiceLap) } ?? 0 : nil)
        case .hearing:
            // Still, a voice halfway up.
            let heard = t == nil ? 0.5 : level
            return VoiceOrbPose(scale: 1.02 + 0.02 * breath + 0.3 * heard, glow: 0.35 + 0.3 * heard,
                                pulseRing: 1.1 + 0.25 * heard, bars: bars { i in 0.16 + 0.84 * heard * wobble(t, i) }, lap: nil)
        case .speaking:
            let voice = t == nil ? 0.5 : level
            return VoiceOrbPose(scale: 1.04 + 0.05 * beat + 0.12 * voice, glow: 0.5 + 0.15 * voice,
                                pulseRing: 1.12 + 0.04 * (beat + 1) + 0.1 * voice,
                                bars: bars { i in
                                    guard let t else { return 0.7 }
                                    // Each bar on its own beat, as an equalizer: taller as the voice gets louder.
                                    return (0.3 + 0.7 * abs(sin(.pi * t / Theme.Motion.voiceBeat + Double(i) * 1.1))) * (0.55 + 0.45 * voice)
                                },
                                lap: nil)
        case .paused, .muted:
            return VoiceOrbPose(scale: phase == .muted ? 0.92 : 0.9, glow: 0, pulseRing: 1, bars: [], lap: nil)
        case .ended:
            return VoiceOrbPose(scale: 0.84, glow: 0, pulseRing: 1, bars: bars { _ in 0 }, lap: nil)
        }
    }

    /// −1…1 and back, `oneWay` seconds each way, eased at both ends; −1 at the start of a cycle.
    static func swing(_ t: TimeInterval, oneWay: TimeInterval) -> Double {
        let p = abs(t / oneWay).truncatingRemainder(dividingBy: 2)
        let x = p < 1 ? p : 2 - p
        return x * x * (3 - 2 * x) * 2 - 1
    }

    /// 0..<1, one turn every `period` seconds.
    static func turn(_ t: TimeInterval, period: TimeInterval) -> Double {
        abs(t / period).truncatingRemainder(dividingBy: 1)
    }

    /// A bar's own flutter, so a level does not lift the meter as one block; 1 when still.
    private static func wobble(_ t: TimeInterval?, _ i: Int) -> Double {
        guard let t else { return 1 }
        return 0.8 + 0.2 * sin(t * 9 + Double(i) * 1.7)
    }
}

#if DEBUG
extension View {
    /// `-showVoiceOrbs YES`: every phase of the voice orb in a sheet, with a made-up voice for the level, and a larger
    /// pair; no hub, no microphone. For screenshots in each style (`-ui.style`) and appearance (`-ui.scheme`).
    func voiceOrbSample() -> some View { modifier(VoiceOrbSample()) }
}

private struct VoiceOrbSample: ViewModifier {
    @State private var shown = UserDefaults.standard.bool(forKey: "showVoiceOrbs")

    func body(content: Content) -> some View {
        content.sheet(isPresented: $shown) { VoiceOrbGallery() }
    }
}

private struct VoiceOrbGallery: View {
    @State private var interrupted = 0

    /// A voice rising and falling, standing in for the engine's levels.
    private static func level() -> Double {
        let t = Date.now.timeIntervalSinceReferenceDate
        return 0.5 + 0.5 * sin(t * 5.3) * sin(t * 1.7)
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: Theme.Space.m) {
                    LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: Theme.Space.m), count: 3), spacing: Theme.Space.m) {
                        ForEach(VoiceOrb.Phase.allCases, id: \.self) { phase in
                            VStack(spacing: Theme.Space.s) {
                                VoiceOrb(phase: phase, level: Self.level, interrupt: phase == .speaking ? { interrupted += 1 } : nil)
                                VStack(spacing: Theme.Space.xxs) {
                                    Text(phase.sampleTitle).font(Theme.Font.labelStrong).lineLimit(1).minimumScaleFactor(0.8)
                                    Text(phase.rawValue).font(Theme.Font.code).foregroundStyle(.secondary)
                                }
                            }
                            .frame(maxWidth: .infinity)
                            .card()
                            .accessibilityElement(children: .contain)
                            .accessibilityIdentifier("voice-orb-\(phase.rawValue)")
                        }
                    }
                    HStack(spacing: Theme.Space.xl) {
                        VoiceOrb(phase: .listening, level: Self.level, size: Theme.Size.box120)
                        VoiceOrb(phase: .speaking, level: Self.level, size: Theme.Size.box120)
                    }
                    .frame(maxWidth: .infinity)
                    .card()
                    if interrupted > 0 {
                        Text("已打断 \(interrupted) 次").font(Theme.Font.secondary).accessibilityIdentifier("voice-orb-interrupted")
                    }
                }
                .padding(Theme.Space.page)
            }
            .themedGroupedPage()
            .navigationTitle("语音光球")
        }
    }
}

private extension VoiceOrb.Phase {
    var sampleTitle: String {
        switch self {
        case .idle: "还没开始"
        case .connecting: "连接中"
        case .listening: "在听"
        case .hearing: "听到你说"
        case .speaking: "正在说"
        case .working: "Claude 在做"
        case .paused: "语音已暂停"
        case .muted: "已静音"
        case .ended: "已结束"
        }
    }
}
#endif
