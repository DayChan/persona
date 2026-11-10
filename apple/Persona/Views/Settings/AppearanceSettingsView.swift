import BeautifulUI
import SwiftUI

// Settings › Appearance (2026-11-01): laid out like Connectors, one row per thing that changes how the app looks
// (Avatar, Theme Color, Interface Style, Light & Dark on iPhone and iPad), each opening its own page; the first three are apart (docs/apple/ios.md「设计语言」).
// Glow While Replying is a switch in place, not a page: it is one on/off choice (iOS only, `ComposerGlow`).
// The home screen icon is the Avatar's, not a row of its own (#3203). Picking anything
// there rebuilds the whole tree (`AppModel.look`); the settings path lives in AppModel, so the
// rebuilt app is back on the same page.

/// The Appearance section of Settings: one row per scene with what it is set to now.
struct AppearanceSection: View {
    @Environment(AppModel.self) private var model
    #if os(iOS)
    private let layoutDisplay = AgentListDisplayChoice()
    #endif
    @StoredValue(ComposerGlowPrefs.enabledKey) private var composerGlow = false

    var body: some View {
        Section {
            Group {
                NavigationLink(value: SettingsPage.persona) {
                    SettingsRow(title: String(localized: "Avatar"), symbol: Theme.Icon.avatar, tint: Theme.persona, value: model.persona.cardTitle, look: .oneLine)
                }
                .settingsListRow(SettingsPage.persona)
                .accessibilityIdentifier("settings-persona")
                NavigationLink(value: SettingsPage.themeColor) {
                    SettingsRow(title: String(localized: "Theme Color"), symbol: Theme.Icon.themeColor, tint: Theme.accent, value: model.themeColor.title, look: .oneLine)
                }
                .settingsListRow(SettingsPage.themeColor)
                .accessibilityIdentifier("settings-color")
                NavigationLink(value: SettingsPage.style) {
                    SettingsRow(title: String(localized: "Interface Style"), symbol: Theme.Icon.style, tint: Theme.Hue.purple, value: styleSummary, look: .oneLine)
                }
                .settingsListRow(SettingsPage.style)
                .accessibilityIdentifier("settings-style")
                NavigationLink(value: SettingsPage.language) {
                    SettingsRow(title: String(localized: "Language"), symbol: Theme.Icon.language, tint: Theme.Hue.green,
                                value: AppLanguage.settingsSummary, look: .oneLine)
                }
                .accessibilityIdentifier("settings-language")
                #if os(iOS)
                // How the Agents list draws its agents on iPhone and iPad (#2530); the Mac and Vision Pro keep their look.
                NavigationLink(value: SettingsPage.layout) {
                    SettingsRow(title: String(localized: "Layout"), symbol: Theme.Icon.listView, tint: Theme.Hue.orange,
                                value: layoutDisplay.display.title, look: .oneLine)
                }
                .settingsListRow(SettingsPage.layout)
                .accessibilityIdentifier("settings-layout-page")
                #endif
                #if os(iOS) || os(visionOS)
                NavigationLink(value: SettingsPage.colorScheme) {
                    SettingsRow(title: String(localized: "Light & Dark"), symbol: Theme.Icon.colorScheme, tint: Theme.Hue.indigo,
                                value: model.colorScheme.title, look: .oneLine)
                }
                .settingsListRow(SettingsPage.colorScheme)
                .accessibilityIdentifier("settings-scheme")
                #endif
                if ComposerGlowPrefs.isAvailable {
                    SettingsSwitchRow(title: String(localized: "Glow While Replying"),
                                      detail: String(localized: "Light up the input box while Persona writes a reply"),
                                      symbol: Theme.Icon.composerGlow, tint: Theme.Hue.purple,
                                      accessibilityID: "settings-composer-glow", isOn: $composerGlow)
                }
            }
            .bareListRow()
        } header: {
            SettingsHeading(String(localized: "Appearance"))
        }
    }

    /// The style, and the text size when it is not the standard one.
    private var styleSummary: String {
        model.textSize == .standard ? model.style.title : String(localized: "\(model.style.title) · Text \(model.textSize.title)")
    }
}

/// Settings › Appearance › Avatar: Persona's name, face, status dot, logo and home screen icon (its colors are the Theme Color Match Avatar).
struct PersonaSettingsPage: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        SettingsForm {
            Section {
                PersonaPicker()
                    .listRowInsets(EdgeInsets(top: 10, leading: 0, bottom: 10, trailing: 0))
            } footer: {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    Text(model.isDemo ? String(localized: "Demo mode only has the default avatar.") :
                        String(localized: "Avatars other than Mochi download from the hub when selected. To use an avatar’s colors, choose Match Avatar under Theme Color."))
                    if !model.isDemo, model.hubFeatures.contains(AvatarUploadsModel.feature) {
                        // The hub makes avatars from images (#2405): how to make one, and how to delete one made that way.
                        Text("New from Photo makes an avatar from an image of your own. To delete one you made, use the delete button in its corner.")
                    }
                    #if !os(macOS)
                    if let failure = model.appIcon.failure {
                        Text(failure).foregroundStyle(Theme.danger)
                    }
                    Text("Every avatar uses the default Home Screen icon. Older alternate icons reset to the default.")
                        // The icon on now, or that the system cannot switch it here (AppIconTests).
                        .accessibilityValue(model.appIcon.available ? model.appIcon.current.rawValue : "unavailable")
                        .accessibilityIdentifier("persona-icon-note")
                    #endif
                }
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Avatar"))
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// Settings › Appearance › Theme Color: the sample of the look and the colors.
struct ThemeColorSettingsPage: View {
    var body: some View {
        SettingsForm {
            Section {
                ThemeColorPicker()
                    .listRowInsets(EdgeInsets(top: 10, leading: 0, bottom: 10, trailing: 0))
            } footer: {
                Text("Buttons, links, selections and your own bubbles use the theme color; Clay and Island use a light tint of it for the background. “Match Interface Style” uses each style's own colors.")
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Theme Color"))
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// Settings › Appearance › Interface Style: the style and the text size.
struct StyleSettingsPage: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        SettingsForm {
            Section {
                StylePicker()
                    .listRowInsets(EdgeInsets(top: 10, leading: 0, bottom: 10, trailing: 0))
            } footer: {
                Text("Changes how sheets, cards, buttons, fonts and chat components look across the whole app, including Lark pages in the Inbox.")
            }
            Section {
                Group {
                    if AppStyle.current.usesBeautifulUI {
                        BUIListRow(String(localized: "Text Size")) {
                            IconTile(Theme.Icon.textSize, tint: Theme.Hue.gray)
                        } trailing: {
                            BUISegmented(TextSize.allCases.map { .init($0, $0.title) },
                                         selection: Binding(get: { model.textSize }, set: { model.textSize = $0 }))
                                .fixedSize()
                        }
                    } else {
                        LabeledContent("Text Size") {
                            Picker("Text Size", selection: Binding(get: { model.textSize }, set: { model.textSize = $0 })) {
                                ForEach(TextSize.allCases) { Text($0.title).tag($0) }
                            }
                            .pickerStyle(.segmented)
                            #if os(macOS)
                            .labelsHidden() // the row says Text Size; a Mac form drew it again after the control (#1221)
                            #endif
                            .fixedSize()
                        }
                    }
                }
                .accessibilityIdentifier("text-size")
            } footer: {
                Text("One step smaller or larger than the system's Text Size.")
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Interface Style"))
        .navigationBarTitleDisplayMode(.inline)
    }
}

#if os(iOS) || os(visionOS)
#if DEBUG
/// `probe.scheme` (`-probes YES`): the choice and the look the page is actually drawn in (ColorSchemeIconTests).
private struct SchemeProbe: ViewModifier {
    let choice: ColorSchemeChoice
    @Environment(\.colorScheme) private var shown

    func body(content: Content) -> some View {
        content.screenProbe("scheme", Value(choice: choice.rawValue, shown: shown == .dark ? "dark" : "light"))
    }

    struct Value: Encodable, Equatable {
        var choice: String
        var shown: String
    }
}
#endif

/// Settings › Appearance › Light & Dark: Match System / Light / Dark (`ColorSchemeChoice`), the system's own inline picker.
struct ColorSchemeSettingsPage: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        List {
            Section {
                Picker("Light & Dark", selection: Binding(get: { model.colorScheme }, set: { model.colorScheme = $0 })) {
                    ForEach(ColorSchemeChoice.allCases) { c in
                        Label { Text(c.title).lineLimit(1) } icon: { ThemedIcon(c.symbol) }
                            .tag(c)
                            .accessibilityIdentifier("scheme-\(c.rawValue)")
                    }
                }
                .pickerStyle(.inline)
                .labelsHidden()
            } footer: {
                Text("With Match System, the app switches when your phone switches between light and dark. Pick Light or Dark and the app stays that way; with the default Mochi avatar, the Home Screen icon also switches to the light or dark one (the system asks once). The Dynamic Island, Lock Screen Live Activities, widgets and Apple Watch still follow the system.")
            }
        }
        .themedGroupedPage()
        #if DEBUG
        .modifier(SchemeProbe(choice: model.colorScheme))
        #endif
        .navigationTitle("Light & Dark")
        .navigationBarTitleDisplayMode(.inline)
    }
}
#endif
