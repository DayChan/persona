import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

/// The persona's face, still (the sidebar's card, the profile, Settings): a package's avatar in a circle, or a
/// built-in's PersonaAvatar. `persona` defaults to the one on screen.
struct PersonaAvatar: View {
    @Environment(AppModel.self) private var model
    @Environment(\.displayScale) private var scale
    var size: CGFloat
    var persona: Persona?
    /// The built-in's eyes look around while it works.
    var working = false

    var body: some View {
        let p = persona ?? model.persona
        Group {
            if !p.isBuiltin, let img = model.personas.avatar(p, points: size, scale: scale) {
                Image(uiImage: img)
                    .resizable()
                    .interpolation(.high)
                    .scaledToFill()
                    .frame(width: size, height: size)
                    .clipShape(.circle)
            } else {
                // The built-in in the accent it would have chosen (it brings no colors: under Match Avatar, the style's own),
                // also while another persona is on (Settings' card).
                AssistantAvatar(size: size, design: p.avatarDesign, state: working ? .working : .idle)
            }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

/// The animated dot in the Persona tab's top bar: the persona in a state (docs/apple/persona.md「状态」). A package plays
/// the state's frames in a UIImageView (Core Animation swaps them; nothing in the app runs per frame); a built-in
/// is its PersonaAvatar moving for the state. Both hold still with Reduce Motion and in Low Power Mode.
struct PersonaDot: View {
    @Environment(AppModel.self) private var model
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    var size: CGFloat
    var state: PersonaState
    var persona: Persona?

    var body: some View {
        let p = persona ?? model.persona
        let still = reduceMotion || ProcessInfo.processInfo.isLowPowerModeEnabled
        Group {
            if !p.isBuiltin, let frames = model.personas.frames(p, state) {
                #if os(iOS) || os(visionOS)
                VisibleWork { eligible in
                    PersonaFramesView(frames: frames, animating: !still, eligible: eligible)
                }
                #else
                PersonaFramesView(frames: frames, animating: !still)
                #endif
            } else {
                AssistantAvatar(size: size, design: p.avatarDesign, state: state, still: still)
            }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

extension Persona {
    /// A built-in's drawing; the default's (Mochi) for a package, whose face falls back to it while it is not on the phone.
    var avatarDesign: AssistantAvatar.Design { design.flatMap(AssistantAvatar.Design.init(rawValue:)) ?? .mochi }
}

#if os(iOS) || os(visionOS)
/// Frames in a UIImageView, looping; the first one alone when not animating (frame 0 says the state by itself).
struct PersonaFramesView: UIViewRepresentable {
    var frames: PersonaFrames
    var animating: Bool
    var eligible: Bool

    func makeUIView(context: Context) -> PersonaFramesImageView {
        let v = PersonaFramesImageView()
        v.contentMode = .scaleAspectFit
        v.clipsToBounds = true
        return v
    }

    func updateUIView(_ v: PersonaFramesImageView, context: Context) {
        v.configure(frames: frames, animating: animating, eligible: eligible)
    }

    static func dismantleUIView(_ v: PersonaFramesImageView, coordinator: ()) {
        v.clearFrames()
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: PersonaFramesImageView, context: Context) -> CGSize? {
        CGSize(width: proposal.width ?? 44, height: proposal.height ?? 44)
    }
}

/// Window attachment and eligibility both own playback; removing a representable also releases its decoded frames.
final class PersonaFramesImageView: UIImageView {
    private var frames: PersonaFrames?
    private var animationRequested = false
    private var eligible = false

    func configure(frames: PersonaFrames, animating: Bool, eligible: Bool) {
        guard self.frames !== frames || animationRequested != animating || self.eligible != eligible else { return }
        self.frames = frames
        animationRequested = animating
        self.eligible = eligible
        stopAnimating()
        image = frames.images.first
        updatePlayback()
    }

    override func didMoveToWindow() {
        super.didMoveToWindow()
        updatePlayback()
    }

    private func updatePlayback() {
        guard window != nil, eligible, animationRequested, let frames, frames.images.count > 1 else {
            stopAnimating()
            animationImages = nil
            return
        }
        guard !isAnimating else { return }
        animationImages = frames.images
        animationDuration = frames.duration
        animationRepeatCount = 0
        startAnimating()
    }

    func clearFrames() {
        stopAnimating()
        animationImages = nil
        image = nil
        frames = nil
        eligible = false
        animationRequested = false
    }
}
#endif

/// A persona's colors as small swatches: primary, then the accent (light / dark halves).
struct PersonaSwatches: View {
    var palette: PersonaPalette?

    var body: some View {
        HStack(spacing: Theme.Space.xs) {
            ForEach(Array(pairs.enumerated()), id: \.offset) { _, pair in
                // The light value on the left half, the dark one on the right.
                Capsule()
                    .fill(LinearGradient(stops: [.init(color: Color(hex: pair.light), location: 0.5),
                                                 .init(color: Color(hex: pair.dark), location: 0.5)],
                                         startPoint: .leading, endPoint: .trailing))
                    .frame(width: Theme.Space.xl, height: Theme.Space.m)
                    .overlay(Capsule().strokeBorder(Color.primary.opacity(0.1), lineWidth: 0.5))
            }
        }
        .accessibilityHidden(true)
    }

    private var pairs: [PersonaPalette.Pair] { [palette?.primary, palette?.accent].compactMap { $0 } }
}

#if DEBUG
/// `-openAssistantPage avatars` (with `-tab assistant`): every persona on the phone, its avatar at the sizes it is
/// used and its dot in each state, in light and dark (for screenshots; the visor / circuit / spark / halo comparison below).
struct PersonaGallery: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ScrollView {
            VStack(spacing: Theme.Space.l) {
                ForEach(model.personas.offered.filter { model.personas.isOnPhone($0) }) { p in
                    panel(p, .light)
                    panel(p, .dark)
                }
                AssistantAvatarGallery.Panels()
            }
            .padding()
        }
        .detailTitle("形象")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func panel(_ p: Persona, _ scheme: ColorScheme) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.m) {
                PersonaAvatar(size: 28, persona: p)
                PersonaAvatar(size: 42, persona: p)
                PersonaAvatar(size: 88, persona: p)
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Text(p.name).font(Theme.Font.heading)
                    Text(scheme == .dark ? "深色" : "浅色").font(Theme.Font.label).foregroundStyle(.secondary)
                    PersonaSwatches(palette: p.palette)
                }
                Spacer(minLength: 0)
            }
            HStack(spacing: Theme.Space.s) {
                ForEach(PersonaState.allCases) { s in
                    VStack(spacing: Theme.Space.xs) {
                        PersonaDot(size: 46, state: s, persona: p)
                        Text(s.title).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                    }
                    .frame(maxWidth: .infinity)
                }
            }
        }
        .padding(Theme.Space.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(uiColor: .systemBackground), in: .rect(cornerRadius: Theme.Radius.bubble))
        .environment(\.colorScheme, scheme)
    }
}
#endif
