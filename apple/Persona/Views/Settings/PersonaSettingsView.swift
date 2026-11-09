import SwiftUI

/// Settings › Appearance › Avatar (docs/apple/persona.md): Mochi and the hub's Milk Frog package, in a
/// grid like the styles, and the chosen one's top-bar dot in each state under it. Choosing one changes Persona's name, face and dot; the app's
/// colors only under Theme Color › Match Avatar (`ThemeColorPicker`). The tree is rebuilt, as for a style; Appearance stays open because
/// the settings path is AppModel's. A package is downloaded first and kept on the phone.
struct PersonaPicker: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dynamicTypeSize) private var typeSize

    private var store: PersonaStore { model.personas }
    /// A card's picture area: the 64 pt face with room around it.
    private static let cardHeight: CGFloat = PersonaStore.thumbnailPoints + Theme.Space.xxl
    private var columns: [GridItem] {
        let n = typeSize.isAccessibilitySize ? 2 : 3
        let item = GridItem(.flexible(), spacing: 12, alignment: .top)
        return Array(repeating: item, count: n)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.l) {
            LazyVGrid(columns: columns, alignment: .leading, spacing: 14) {
                ForEach(model.isDemo ? [Persona.builtin] : store.offered) { p in
                    card(p)
                }
            }
            states
            if let error = store.error, !model.isDemo {
                Label(error, systemImage: Theme.Icon.error)
                    .font(Theme.Font.caption)
                    .foregroundStyle(Theme.cautionText)
            }
        }
        .padding(.horizontal, Theme.Space.l)
        .task { await store.refresh(model.api) }
    }

    /// The chosen one's dot in each state, moving as in the top bar: in a row; at the accessibility text sizes two a row,
    /// where five in a row cut every name to "…" and three ran them together (#1796).
    private var states: some View {
        Group {
            if typeSize.isAccessibilitySize {
                LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: Theme.Space.xxs, alignment: .top), count: 2),
                          spacing: Theme.Space.s) { stateCells }
            } else {
                HStack(spacing: Theme.Space.xxs) { stateCells }
            }
        }
        .padding(.vertical, Theme.Space.s)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(model.persona.name)'s five states in the top bar: idle, thinking, working, waiting for you, offline")
        .accessibilityIdentifier("persona-states")
    }

    @ViewBuilder private var stateCells: some View {
        ForEach(PersonaState.allCases) { s in
            VStack(spacing: Theme.Space.xs) {
                PersonaDot(size: Theme.Size.avatar, state: s)
                Text(s.title).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1).minimumScaleFactor(0.8)
            }
            .frame(maxWidth: .infinity)
        }
    }

    private func card(_ p: Persona) -> some View {
        let selected = p.id == model.persona.id
        let here = store.isOnPhone(p)
        return Button {
            guard !selected else { return }
            Task {
                await store.choose(p, api: model.api)
                if store.current.id == p.id { Haptics.success() }
            }
        } label: {
            VStack(spacing: Theme.Space.s) {
                ZStack {
                    RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
                        .fill((p.palette?.primary?.color ?? Theme.accent).opacity(0.12))
                    PersonaAvatar(size: PersonaStore.thumbnailPoints, persona: p)
                        .overlay(alignment: .bottomTrailing) {
                            // Not on the phone yet: choosing it downloads it.
                            if store.downloading.contains(p.id) {
                                ProgressView().padding(Theme.Space.xxs).background(.regularMaterial, in: .circle)
                            } else if !here {
                                ThemedIcon("arrow.down.circle.fill")
                                    .font(Theme.Font.heading)
                                    .foregroundStyle(.white, Theme.accent)
                                    .accessibilityHidden(true)
                            }
                        }
                }
                .frame(maxWidth: .infinity)
                .frame(height: Self.cardHeight)
                .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
                    .stroke(selected ? Theme.accent : Color.primary.opacity(0.1), lineWidth: selected ? 2.5 : 1))
                VStack(spacing: Theme.Space.xxs) {
                    HStack(spacing: Theme.Space.xs) {
                        if selected { ThemedIcon(Theme.Icon.successFilled).foregroundStyle(Theme.accent) }
                        Text(p.cardTitle).font(Theme.Font.captionStrong).lineLimit(1)
                    }
                    if p.isBuiltin {
                        Text(p.builtinNote).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                    } else if let about = p.manifest?.about, !about.isEmpty {
                        Text(about).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1).minimumScaleFactor(0.8)
                    } else {
                        PersonaSwatches(palette: p.palette).frame(height: Theme.Space.l)
                    }
                }
            }
            .frame(maxWidth: .infinity)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(p.id == Persona.builtinID ? String(localized: "\(p.cardTitle), default avatar")
                            : p.isBuiltin ? p.cardTitle : here ? p.name : String(localized: "\(p.name), downloads when selected"))
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier("persona-\(p.id)")
    }
}
