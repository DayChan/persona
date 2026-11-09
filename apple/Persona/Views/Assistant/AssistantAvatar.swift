import SwiftUI

/// Mochi's animated face, shared with the still drawing in the Live Activity.
struct AssistantAvatar: View {
    enum Design: String, CaseIterable, Identifiable {
        case mochi
        var id: String { rawValue }
        var title: String { String(localized: "Mochi") }
    }

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    var size: CGFloat
    var design: Design = .mochi
    var state: PersonaState = .idle
    /// Hold still in Reduce Motion and Low Power Mode.
    var still = false

    #if os(macOS)
    @Environment(\.tabIsActive) private var inFront
    #endif

    private var held: Bool { still || reduceMotion }
    private func moving(eligible: Bool) -> Bool {
        !held && eligible && [.thinking, .working, .needsYou].contains(state)
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("PersonaAvatar")
        #endif
        #if os(macOS)
        // Preserve the Mac's existing continuous-animation branch.
        TimelineView(.animation(minimumInterval: Theme.Motion.personaFrameInterval, paused: !moving(eligible: inFront))) { tl in
            face(at: tl.date, eligible: inFront)
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
        #else
        VisibleWork { eligible in
            TimelineView(VisiblePeriodicSchedule(interval: Theme.Motion.personaFrameInterval, paused: !moving(eligible: eligible))) { tl in
                face(at: tl.date, eligible: eligible)
            }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
        #endif
    }

    private func face(at date: Date, eligible: Bool) -> some View {
        PersonaFace(size: size, design: PersonaFace.Design(rawValue: design.rawValue) ?? .mochi, state: state,
                    time: date.timeIntervalSinceReferenceDate, moving: moving(eligible: eligible),
                    blink: held || !eligible ? nil : Theme.Motion.blink)
    }
}

#if DEBUG
/// The remaining built-in face at its display sizes, in both appearances.
enum AssistantAvatarGallery {
    struct Panels: View {
        var body: some View {
            VStack(spacing: Theme.Space.l) {
                panel(.light)
                panel(.dark)
            }
        }

        private func panel(_ scheme: ColorScheme) -> some View {
            HStack(spacing: Theme.Space.l) {
                Text(AssistantAvatar.Design.mochi.title).font(Theme.Font.secondaryStrong)
                AssistantAvatar(size: Theme.Size.avatar)
                AssistantAvatar(size: PersonaStore.thumbnailPoints)
                Spacer(minLength: 0)
            }
            .padding(Theme.Space.l)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color(uiColor: .systemBackground), in: .rect(cornerRadius: Theme.Radius.bubble))
            .environment(\.colorScheme, scheme)
        }
    }
}
#endif
