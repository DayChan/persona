import SwiftUI

// The terminal tab's operations layer: saved shortcuts, the key bar's buttons, appearance, and suggestions taken from
// what the pane shows (never the computer's shell history).

/// Settings kept on the phone (`@AppStorage`).
enum TerminalPrefs {
    static let shortcuts = "terminalShortcuts" // JSON [String]
    static let keys = "terminalKeys" // the optional key bar buttons shown, comma separated
    static let autoHide = "terminalAutoHideKeys"
    static let fontSize = "terminalFontSize"
    static let font = "terminalFont"
    static let theme = NativeStateMigration.terminalKey
    static let wrap = "terminalWrap"
    /// "direct": the keyboard types into the terminal; "field": a text field under it sends what is written.
    static let inputMode = "terminalInputMode"
    static let tipsSeen = "terminalTipsSeen"
    /// Mac: Option+key sends ESC+key (Meta), like Terminal.app's setting of that name; off by default, as there.
    static let optionAsMeta = "terminalOptionAsMeta"
    /// Mac: who gets the mouse while driving, "program" (the default) or "local" (MacTerminalMouse.Mode).
    static let mouseMode = "terminalMouseMode"
    /// "phone": the phone drives the terminal at its own size while the tab is open; "mirror": it shows the pane at the
    /// computer's size and folds long rows.
    static let size = "terminalSize"
    static let defaultFontSize = 15.0 // as Moshi reads: about 46 columns on a 6.9-inch phone
    static let defaultShortcuts = ["git status", "git diff --stat", "ls -la", "/compact", "/status"]
    static let allKeys: [(id: String, label: String)] = [("esc", "esc"), ("tab", "tab"), ("ctrlc", "⌃C"), ("shortcuts", String(localized: "Quick Commands [Terminal Keys 6f3d83]")),
                                                         ("paste", String(localized: "Paste")), ("files", String(localized: "Photos / Files")), ("alt", "alt"), ("shift", "⇧ shift"), ("pad", String(localized: "Arrows"))]
    /// alt and shift are a tap away in the settings (Moshi shows only ctrl too): the rest fits a phone's width. So is
    /// the photos / files key: holding paste offers the same.
    static let defaultKeys = allKeys.map(\.id).filter { !["alt", "shift", "files"].contains($0) }.joined(separator: ",")

    static func decode(_ json: String) -> [String] {
        guard !json.isEmpty, let list = try? JSONDecoder().decode([String].self, from: Data(json.utf8)) else { return defaultShortcuts }
        return list
    }

    static func encode(_ list: [String]) -> String {
        (try? String(decoding: JSONEncoder().encode(list), as: UTF8.self)) ?? ""
    }
}

/// Saved commands over the key bar: a tap puts one in the text field, a double tap sends it with return.
struct ShortcutsPanel: View {
    var items: [String]
    var hint = String(localized: "Quick commands · tap to fill in, double-tap to send")
    var fill: (String) -> Void
    var send: (String) -> Void
    var edit: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: Theme.Space.s) {
                Text(hint).font(Theme.Font.labelStrong).foregroundStyle(.secondary).lineLimit(1)
                Spacer(minLength: 4)
                Button("Edit [Message Menu 051836]", action: edit).font(Theme.Font.labelStrong).lineLimit(1)
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
            if items.isEmpty {
                Text("No quick commands yet. Tap Edit to add one.").font(Theme.Font.caption).foregroundStyle(.secondary)
                    .padding(.horizontal, Theme.Space.l).padding(.bottom, Theme.Space.m)
            }
            ScrollView {
                VStack(spacing: 0) {
                    ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                        Text(item)
                            .font(Theme.Font.secondary.monospaced())
                            .lineLimit(1)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.horizontal, Theme.Space.l)
                            .padding(.vertical, Theme.Space.m)
                            .contentShape(.rect)
                            .onTapGesture(count: 2) { Haptics.success(); send(item) }
                            .onTapGesture { Haptics.tap(); fill(item) }
                            .accessibilityAddTraits(.isButton)
                            .accessibilityHint("Tap to fill in the input field, double-tap to send")
                            .accessibilityAction(named: "Send Now") { send(item) }
                        Divider().padding(.leading, Theme.Space.l)
                    }
                }
            }
            .frame(maxHeight: 230)
            .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: 360)
        .surface(in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous)) // no shadow over the surface
    }
}

/// Terminal Settings: shortcuts, the key bar, appearance, and what the gestures do.
struct TerminalSettingsView: View {
    @Environment(\.dismiss) private var dismiss
    @AppStorage(TerminalPrefs.shortcuts) private var shortcutsJSON = ""
    @AppStorage(TerminalPrefs.keys) private var keys = TerminalPrefs.defaultKeys
    @AppStorage(TerminalPrefs.autoHide) private var autoHide = false
    @AppStorage(TerminalPrefs.wrap) private var wrap = true
    @AppStorage(TerminalPrefs.fontSize) private var fontSize = TerminalPrefs.defaultFontSize
    @AppStorage(TerminalPrefs.font) private var fontID = TerminalFont.jetbrains.rawValue
    @AppStorage(TerminalPrefs.theme) private var themeID = TerminalTheme.defaultID
    @AppStorage(TerminalPrefs.inputMode) private var inputMode = "direct"
    @AppStorage(TerminalPrefs.size) private var size = "phone"
    #if os(macOS)
    @AppStorage(TerminalPrefs.optionAsMeta) private var optionAsMeta = false
    @AppStorage(TerminalPrefs.mouseMode) private var mouseMode = MacTerminalMouse.Mode.program.rawValue
    #endif
    @State private var newShortcut = ""

    private var shortcuts: Binding<[String]> {
        Binding(get: { TerminalPrefs.decode(shortcutsJSON) }, set: { shortcutsJSON = TerminalPrefs.encode($0) })
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    SegmentedChoice(String(localized: "Terminal Size"), [("phone", TerminalSizeText.own), ("mirror", String(localized: "Computer Size"))], selection: $size)
                } header: {
                    Text("Size [Image Viewer 246df7]")
                } footer: {
                    Text(size == "phone" ? TerminalSizeText.ownFooter : TerminalSizeText.mirrorFooter)
                }
                Section {
                    TerminalPreview(fontSize: fontSize, font: TerminalFont(rawValue: fontID) ?? .jetbrains,
                                    theme: .named(themeID), wrap: wrap)
                        .frame(height: TerminalPreview.height(fontSize))
                        .listRowInsets(EdgeInsets())
                    ForEach(TerminalTheme.all, id: \.id) { t in
                        Button { NativeStateMigration.writeTerminalID(t.id, defaults: .standard) } label: { ThemeRow(theme: t, on: t.id == themeID) }
                            .buttonStyle(.plain)
                    }
                } header: {
                    Text("Colors [Terminal Extras 2ae75e]")
                } footer: {
                    Text("Ink and Midnight don't change with the system; Persona and Solarized follow its light / dark appearance.")
                }
                Section {
                    Picker("Font", selection: $fontID) {
                        ForEach(TerminalFont.allCases, id: \.rawValue) { Text($0.name).tag($0.rawValue) }
                    }
                    Stepper(value: $fontSize, in: 7...20, step: 0.5) {
                        Text("Text Size \(fontSize, specifier: "%.1f")").monospacedDigit()
                    }
                    Toggle(TerminalSizeText.wrap, isOn: $wrap)
                } header: {
                    Text("text")
                } footer: {
                    Text(TerminalHelpText.textFooter)
                }
                Section {
                    SegmentedChoice(String(localized: "Input Mode"), [("direct", String(localized: "Type Directly")), ("field", String(localized: "Input Field"))], selection: $inputMode)
                } header: {
                    Text("Enter")
                } footer: {
                    Text(inputMode == "field" ? String(localized: "Write in the field below the terminal and press Return to send it all at once. The key bar's ctrl / alt / ⇧ also apply to the next character you type in the field.")
                         : TerminalHelpText.directFooter)
                }
                #if os(macOS)
                Section {
                    Toggle("Use Option as Meta Key", isOn: $optionAsMeta)
                } header: {
                    Text("Keyboard")
                } footer: {
                    Text(optionAsMeta ? String(localized: "Option+letter sends Esc+letter, so Option+B / Option+F move by word in the shell; Option no longer types special characters like ™ or é. Input methods work as usual.")
                         : String(localized: "Option types special characters as usual (Option+2 is ™). When this is on, Option+key sends Esc+key, like the setting of the same name in the Terminal app."))
                }
                Section {
                    SegmentedChoice(String(localized: "Mouse While Driving"), [(MacTerminalMouse.Mode.program.rawValue, String(localized: "To Program")), (MacTerminalMouse.Mode.local.rawValue, String(localized: "Select Locally"))],
                                    selection: $mouseMode)
                } header: {
                    Text("Mouse")
                } footer: {
                    Text(MacTerminalMouse.mode(mouseMode) == .program
                         ? String(localized: "While driving, clicks, drags and right and middle clicks go to the program on the computer (only programs with mouse support, like vim or Claude Code, receive them). Hold Shift to select text and open the menu here; ⌘-click still opens links.")
                         : String(localized: "While driving, select text and right-click for the menu here; a single click still clicks the program. Hold Shift to hand the mouse to the program."))
                }
                #endif
                Section {
                    ForEach(Array(shortcuts.wrappedValue.enumerated()), id: \.offset) { i, s in
                        Text(s).font(Theme.Font.body.monospaced()).lineLimit(1)
                        #if os(macOS)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .contentShape(.rect)
                            .contextMenu { shortcutMenu(i) }
                        #endif
                    }
                    .onDelete { shortcuts.wrappedValue.remove(atOffsets: $0) }
                    .onMove { shortcuts.wrappedValue.move(fromOffsets: $0, toOffset: $1) }
                    HStack {
                        TextField(String(localized: "New quick command"), text: $newShortcut)
                            .font(Theme.Font.body.monospaced())
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .onSubmit(add)
                        Button("Add [Beautiful U I Bridge 7a8a11]", action: add).lineLimit(1).disabled(newShortcut.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                } header: {
                    Text("Quick Commands [Terminal Keys 6f3d83]")
                } footer: {
                    Text(TerminalHelpText.shortcutsFooter)
                }
                Section {
                    Toggle(isOn: .constant(true)) { Text("ctrl") }.disabled(true)
                    ForEach(TerminalPrefs.allKeys, id: \.id) { k in
                        Toggle(k.label, isOn: keyBinding(k.id))
                    }
                    Toggle("Hide Key Bar When Not Typing", isOn: $autoHide)
                } header: {
                    Text("Key Bar")
                } footer: {
                    Text(TerminalHelpText.keysFooter)
                }
                #if os(macOS)
                Section("Actions [Terminal Extras ed31fb]") {
                    ForEach(TerminalHelpText.gestures) { Label($0.text, systemImage: $0.symbol) }
                }
                .font(Theme.Font.secondary)
                .foregroundStyle(.secondary)
                #else
                Section("Gestures") {
                    Label("Tap the terminal: the tap goes to the terminal (Claude's “Jump to bottom”, questions at the top)", systemImage: Theme.Icon.tap)
                    Label("Drag up or down: scroll the terminal (Claude scrolls back through the chat)", systemImage: "arrow.up.and.down")
                    Label("Key bar's keyboard button: shows the keyboard; double-tap for Return", systemImage: Theme.Icon.keyboard)
                    Label("Pinch: change text size", systemImage: Theme.Icon.enlarge)
                    Label("Two-finger swipe left or right: switch agents in this space", systemImage: Theme.Icon.switchAgents)
                    Label("Two-finger swipe up or down: switch to the next space", systemImage: Theme.Icon.upDown)
                    Label("Double-tap the terminal: paste the clipboard", systemImage: Theme.Icon.paste)
                    Label("Long press: select, copy", systemImage: Theme.Icon.insert)
                    Label("Dictation: the mic on the keyboard", systemImage: Theme.Icon.dictate)
                }
                .font(Theme.Font.secondary)
                .foregroundStyle(.secondary)
                #endif
            }
            .themedPage()
            .navigationTitle("Terminal Settings")
            .navigationBarTitleDisplayMode(.inline)
            .pageToolbar {
                ToolbarItem(placement: .topBarLeading) { EditButton() }
                ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() } }
            }
        }
    }

    #if os(macOS)
    /// A Mac list has no edit mode (`EditButton` is empty there): a command moves or goes from its context menu.
    @ViewBuilder private func shortcutMenu(_ i: Int) -> some View {
        let list = shortcuts.wrappedValue
        Button { shortcuts.wrappedValue = MacShortcutOrder.move(list, at: i, by: -1) } label: { Label("Move Up", systemImage: Theme.Icon.moveUp) }
            .disabled(i == 0)
        Button { shortcuts.wrappedValue = MacShortcutOrder.move(list, at: i, by: 1) } label: { Label("Move Down", systemImage: Theme.Icon.moveDown) }
            .disabled(i == list.count - 1)
        Divider()
        Button(role: .destructive) { shortcuts.wrappedValue.remove(at: i) } label: { Label("Delete", systemImage: Theme.Icon.delete) }
    }
    #endif

    private func add() {
        let s = newShortcut.trimmingCharacters(in: .whitespaces)
        guard !s.isEmpty else { return }
        shortcuts.wrappedValue.append(s)
        newShortcut = ""
    }

    private func keyBinding(_ id: String) -> Binding<Bool> {
        Binding(get: { keys.split(separator: ",").contains(Substring(id)) }, set: { on in
            var set = keys.split(separator: ",").map(String.init).filter { $0 != id }
            if on { set.append(id) }
            keys = TerminalPrefs.allKeys.map(\.id).filter(set.contains).joined(separator: ",")
        })
    }
}

/// A few lines of a Claude session in the chosen look, for the settings.
struct TerminalPreview: View {
    var fontSize: Double
    var font: TerminalFont
    var theme: TerminalTheme
    var wrap: Bool

    private static let sample: TerminalContent = {
        let ansi = "\u{1B}[38;2;215;119;87m⏺\u{1B}[0m " + String(localized: "OK, running the tests first:", comment: "Sample terminal output in the terminal settings preview") + "\r\n"
            + "  ⎿  \u{1B}[32mok\u{1B}[0m  ./screen  \u{1B}[2m0.41s\u{1B}[0m\r\n"
            + "\u{1B}[38;2;136;136;136m" + String(repeating: "─", count: 60) + "\u{1B}[0m\r\n"
            + "❯ \u{1B}[7m \u{1B}[0m\r\n"
            + "\u{1B}[38;2;136;136;136m" + String(repeating: "─", count: 60) + "\u{1B}[0m\r\n"
            + "  \u{1B}[35m⏵⏵ auto mode on\u{1B}[0m \u{1B}[2m(shift+tab to cycle)\u{1B}[0m"
        return TerminalContent(rows: TerminalGrid.rows(fromANSI: ansi), cols: 60, cursor: nil)
    }()

    /// Room for its six rows at a size.
    static func height(_ size: Double) -> CGFloat { min(280, 6 * ceil(size * 1.42) + 22) }

    var body: some View {
        TerminalView(content: Self.sample, wrap: wrap, fontSize: fontSize, font: font, theme: theme)
            .allowsHitTesting(false)
            .accessibilityHidden(true)
    }
}

/// A theme in the list: its name and its colors.
struct ThemeRow: View {
    @Environment(\.colorScheme) private var scheme
    var theme: TerminalTheme
    var on: Bool

    var body: some View {
        let side = theme.side(scheme == .dark)
        HStack(spacing: Theme.Space.m) {
            Text(theme.name).lineLimit(1)
            Spacer(minLength: 8)
            HStack(spacing: Theme.Space.xs) {
                ForEach([side.background, side.foreground, side.ansi[1], side.ansi[2], side.ansi[4]], id: \.self) { c in
                    Circle().fill(Color(hex: c)).scaledFrame(width: Theme.Size.markS, height: Theme.Size.markS)
                        .overlay(Circle().stroke(Color.primary.opacity(0.15), lineWidth: 0.5))
                }
            }
            ThemedIcon(Theme.Icon.done).font(Theme.Font.heading).foregroundStyle(Theme.accent).opacity(on ? 1 : 0)
        }
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(on ? [.isButton, .isSelected] : .isButton)
    }
}

/// Completions for the terminal's text field from what the pane shows: commands typed after a prompt earlier on, and
/// words (paths, names) for the one being typed. Latest first.
enum TerminalCompletion {
    static let prompts = ["$ ", "% ", "# ", "❯ ", "➜ ", "> ", "» "]

    static func suggestions(for input: String, rows: [TermRow], limit: Int = 6) -> [String] {
        guard !input.isEmpty else { return [] }
        let lines = rows.suffix(600).map(TerminalGrid.plain).reversed()
        var out: [String] = []
        func add(_ s: String) {
            if s != input, !out.contains(s), out.count < limit { out.append(s) }
        }
        for line in lines {
            if let cmd = command(in: line), cmd.hasPrefix(input) { add(cmd) }
        }
        let word = input.split(separator: " ", omittingEmptySubsequences: false).last.map(String.init) ?? ""
        if word.count >= 2 {
            let head = String(input.dropLast(word.count))
            for line in lines {
                for token in line.split(whereSeparator: { $0 == " " || $0 == "\"" || $0 == "'" || $0 == "(" || $0 == ")" })
                where token.count > word.count && token.count <= 120 && token.hasPrefix(word) {
                    add(head + token)
                }
            }
        }
        return out
    }

    /// What follows a prompt at the start of a line ("~/app $ git status" → "git status").
    static func command(in line: String) -> String? {
        let head = line.prefix(60)
        for p in prompts {
            guard let r = head.range(of: p) else { continue }
            let cmd = line[r.upperBound...].trimmingCharacters(in: .whitespaces)
            return cmd.isEmpty || cmd.count > 200 ? nil : cmd
        }
        return nil
    }
}

/// Completion chips over the text field.
struct SuggestionStrip: View {
    var items: [String]
    var pick: (String) -> Void

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: Theme.Space.sm) {
                ForEach(items, id: \.self) { s in
                    Button { Haptics.tap(); pick(s) } label: {
                        Text(s).font(Theme.Font.code).lineLimit(1)
                            .padding(.horizontal, Theme.Space.m).padding(.vertical, Theme.Space.sm)
                            .background(Theme.Fill.chip, in: .capsule)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .scrollClipDisabled()
    }
}

/// The terminal's size wording: the phone's size on iOS, the window's on the Mac (a Mac takes over at its panel's size).
enum TerminalSizeText {
    #if os(macOS)
    static let own = String(localized: "Window Size")
    static let ownFooter = String(localized: "When the terminal opens, this window takes it over: the computer's pane takes the window's rows and columns, and Claude lays out to the window's width. When you leave the terminal or the connection drops, the pane returns to the computer's size.")
    static let mirrorFooter = String(localized: "Leaves the computer's pane as it is and shows it at the computer's width; long lines wrap in the window (or scroll sideways).")
    static let wrap = String(localized: "Wrap long lines to the window width")
    static let takeOver = String(localized: "For example Ovis or another herdr client. Taking over disconnects it and resizes the terminal to this window; when you leave the terminal, the pane returns to the computer's size.")
    static let icon = Theme.Icon.laptop
    static let takeOverIcon = Theme.Icon.takeOverWindow
    #else
    static let own = String(localized: "iPhone Size")
    static let ownFooter = String(localized: "When the terminal opens, the iPhone takes it over: the computer's pane takes the iPhone's rows and columns, and Claude lays out to the iPhone's width. When you leave the terminal, the app goes to the background, or the connection drops, the pane returns to the computer's size.")
    static let mirrorFooter = String(localized: "Leaves the computer's pane as it is and shows it at the computer's width; long lines wrap on the iPhone (or swipe sideways).")
    static let wrap = String(localized: "Wrap long lines to the iPhone width")
    static let takeOver = String(localized: "For example Ovis or another herdr client. Taking over disconnects it and resizes the terminal to the iPhone; when you leave the terminal, the pane returns to the computer's size.")
    static let icon = Theme.Icon.phone
    static let takeOverIcon = Theme.Icon.handoff
    #endif
}

/// The terminal's help wording: touches on iOS; on the Mac a click, the keyboard, the wheel and the trackpad.
enum TerminalHelpText {
    struct Gesture: Identifiable {
        var text: String
        var symbol: String
        var id: String { text }
    }

    #if os(macOS)
    static let loadOlder = String(localized: "Scroll up to load earlier output")
    static let noWrap = String(localized: "No Wrap · Scroll Sideways")
    static let noWrapValue = String(localized: "Off, scroll sideways")
    static let textFooter = String(localized: "⌘+ / ⌘− / ⌘0 or pinching on the trackpad also change the text size. Chinese and other wide characters use the system font, two cells each.")
    static let directFooter = String(localized: "Click the terminal to type directly: every key goes to the terminal (input methods work too), and ⌘ shortcuts stay with the app. The key bar's keyboard button moves input to or away from the terminal; double-click it for Return. A click on the terminal is also a click inside it (such as Claude's “Jump to bottom”).")
    static let shortcutsFooter = String(localized: "In the key bar's bolt: click to type, double-click to run with Return. Right-click a command to move it up, move it down, or delete it.")
    static let keysFooter = String(localized: "ctrl is always there; the keyboard button and arrows are at the far right. When the keys don't fit, the key bar scrolls sideways.")
    static let gestures: [Gesture] = [
        Gesture(text: String(localized: "Click the terminal: start typing; the click also goes to the terminal (Claude's “Jump to bottom”, questions at the top)"), symbol: Theme.Icon.click),
        Gesture(text: String(localized: "Scroll wheel or trackpad: scroll the terminal (Claude scrolls back through the chat)"), symbol: Theme.Icon.upDown),
        Gesture(text: String(localized: "Drag to select, ⌘C to copy; the context menu has Copy, Paste, and Select All"), symbol: Theme.Icon.contextMenu),
        Gesture(text: String(localized: "⌘V: paste the clipboard (asks first for multiple lines)"), symbol: Theme.Icon.paste),
        Gesture(text: String(localized: "⌘+ / ⌘− / ⌘0, trackpad pinch: change text size"), symbol: Theme.Icon.enlarge),
        Gesture(text: String(localized: "Three-finger swipe on the trackpad: switch agents in this space (when “Swipe between pages” is set to three fingers in System Settings › Trackpad)"), symbol: Theme.Icon.swipe),
    ]
    #else
    static let loadOlder = String(localized: "Swipe up to load earlier output")
    static let noWrap = String(localized: "No Wrap · Swipe Sideways")
    static let noWrapValue = String(localized: "Off, swipe sideways")
    static let textFooter = String(localized: "Pinching the terminal with two fingers also changes the text size. Chinese and other wide characters use the system font, two cells each.")
    static let directFooter = String(localized: "Tap the keyboard button at the right of the key bar to show the keyboard; every key goes straight to the terminal (like on a computer, with input methods and dictation). Double-tap the keyboard button for Return. Tapping the terminal taps inside it (such as Claude's “Jump to bottom”) and doesn't show the keyboard.")
    static let shortcutsFooter = String(localized: "In the key bar's bolt: tap to type, double-tap to run with Return. Tap Edit at the top left to delete or reorder.")
    static let keysFooter = String(localized: "ctrl is always there; the keyboard button and arrows are at the far right. When the keys don't fit, swipe the key bar sideways.")
    #endif
    #if os(macOS)

    /// Everything above, for the wording check (MacTerminalWordingTests).
    static var all: [String] {
        [loadOlder, noWrap, noWrapValue, textFooter, directFooter, shortcutsFooter, keysFooter] + gestures.map(\.text)
    }
    #endif
}
