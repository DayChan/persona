import SwiftUI

/// One modifier on the key bar, the way Moshi does it: a tap arms it for the next key, a quick second tap locks it
/// until tapped again, and holding it keeps it down while the finger stays (let go without pressing anything and it
/// stays armed for the next key).
enum ModifierState: Equatable { case off, once, held, locked }

/// ctrl, alt and shift on the key bar, shared by its keys and the text field under it.
struct TerminalModifiers: Equatable {
    enum Key: CaseIterable { case ctrl, alt, shift }
    var ctrl = ModifierState.off
    var alt = ModifierState.off
    var shift = ModifierState.off
    /// Whether a key went out while one was held (letting go then releases it instead of arming it).
    private var usedWhileHeld = false

    subscript(k: Key) -> ModifierState {
        get { switch k { case .ctrl: ctrl; case .alt: alt; case .shift: shift } }
        set { switch k { case .ctrl: ctrl = newValue; case .alt: alt = newValue; case .shift: shift = newValue } }
    }

    var active: Bool { Key.allCases.contains { self[$0] != .off } }

    /// The key name herdr takes with the modifiers that are down: "ctrl+alt+x", "shift+tab". esc goes out bare.
    func apply(_ key: String) -> String {
        guard key != "esc" else { return key }
        let names: [(Key, String)] = [(.ctrl, "ctrl"), (.alt, "alt"), (.shift, "shift")]
        return (names.filter { self[$0.0] != .off }.map(\.1) + [key]).joined(separator: "+")
    }

    /// A key went out: armed-once modifiers release, held and locked ones stay.
    mutating func pressed() {
        for k in Key.allCases {
            if self[k] == .once { self[k] = .off }
            if self[k] == .held { usedWhileHeld = true }
        }
    }

    /// A tap; `quick` when it follows the previous tap on the same key closely (a double tap).
    mutating func tap(_ k: Key, quick: Bool) {
        switch self[k] {
        case .off: self[k] = .once
        case .once: self[k] = quick ? .locked : .off
        case .held, .locked: self[k] = .off
        }
    }

    /// Held long enough to count as holding it down.
    mutating func hold(_ k: Key) {
        self[k] = .held
        usedWhileHeld = false
    }

    /// Let go after holding.
    mutating func release(_ k: Key) {
        guard self[k] == .held else { return }
        self[k] = usedWhileHeld ? .off : .once
    }

    /// What one character typed in the text field sends while modifiers are down: a key for herdr ("ctrl+c"), or — shift
    /// alone, or a character no key name covers — the text itself (shift capitalized).
    func typed(_ ch: Character) -> (key: String?, text: String?) {
        let ctrlOrAlt = ctrl != .off || alt != .off
        if !ctrlOrAlt { return (nil, shift != .off ? ch.uppercased() : String(ch)) }
        guard ch.isASCII, !ch.isNewline else { return (nil, String(ch)) }
        let name = ch == " " ? "space" : ch == "\t" ? "tab" : ch.lowercased()
        return (apply(name), nil)
    }
}

/// Terminal keys in one row that never wraps (it scrolls sideways when they do not fit): ctrl / alt / shift (tap,
/// double tap, hold), esc, tab, a ⌃C that goes out straight away, the saved commands, paste, photos / files; then, always in reach, a
/// game-pad button that floats a direction pad (↑ ↓ ← → with enter in the middle) above the bar and the keyboard's.
struct TerminalKeyBar: View {
    /// Shared with a text field that should honor them; the bar keeps its own when nil.
    var mods: Binding<TerminalModifiers>? = nil
    /// The optional buttons shown (TerminalPrefs.allKeys ids); ctrl always is.
    var visible: Set<String> = Set(TerminalPrefs.allKeys.map(\.id))
    /// The saved commands' button (nil: none).
    var shortcuts: (() -> Void)? = nil
    var shortcutsOn = false
    var paste: (() -> Void)? = nil
    /// Pick photos / files to upload to the pane's machine and type their paths in (nil: no button).
    var photos: (() -> Void)? = nil
    var files: (() -> Void)? = nil
    /// The keyboard on the terminal: up or down (nil: no button).
    var keyboard: Binding<Bool>? = nil
    var send: ([String]) -> Void
    @State private var own = TerminalModifiers()
    #if DEBUG
    @State private var preset = false
    #endif
    @State private var downAt: [TerminalModifiers.Key: Date] = [:]
    @State private var lastTap: [TerminalModifiers.Key: Date] = [:]
    #if DEBUG
    @State private var pad = UserDefaults.standard.bool(forKey: "showPad")
    #else
    @State private var pad = false
    #endif

    private var m: Binding<TerminalModifiers> { mods ?? $own }

    var body: some View {
        // One low glass capsule holding every key (as Moshi's toolbar: 44 high, 36-point keys 6 apart, 5 / 4 inside):
        // the terminal keeps the height.
        HStack(spacing: Theme.Space.sm) {
            ViewThatFits(in: .horizontal) {
                keys
                ScrollView(.horizontal, showsIndicators: false) { keys }
            }
            Spacer(minLength: 0)
            if visible.contains("pad") { padButton }
            if let keyboard { keyboardButton(keyboard) }
        }
        .padding(.horizontal, Theme.Space.sm)
        .padding(.vertical, Theme.Space.xs)
        .scaledFrame(height: Theme.Size.tapTarget)
        .surface(in: .capsule)
        #if DEBUG
        // `-showKeyMods locked,once,held`: ctrl, alt, shift in those states, for screenshots.
        .onAppear {
            guard !preset, let v = UserDefaults.standard.string(forKey: "showKeyMods") else { return }
            preset = true
            let states: [String: ModifierState] = ["off": .off, "once": .once, "held": .held, "locked": .locked]
            for (k, name) in zip(TerminalModifiers.Key.allCases, v.split(separator: ",")) {
                m.wrappedValue[k] = states[String(name)] ?? .off
            }
        }
        #endif
        .overlay(alignment: .bottomTrailing) {
            if pad {
                DirectionPad { press($0) }
                    .offset(y: -52)
                    .transition(.scale(scale: 0.6, anchor: .bottomTrailing).combined(with: .opacity))
            }
        }
    }

    private var keys: some View {
        HStack(spacing: Theme.Space.sm) {
            modifier(.ctrl, "Ctrl", name: "Control")
            if visible.contains("esc") { key("Esc", name: "esc") { press("esc") } }
            if visible.contains("tab") { key("Tab", name: "tab") { press("tab") } }
            if visible.contains("ctrlc") { key("⌃C", name: String(localized: "Send Control-C")) { send(["ctrl+c"]) } }
            if visible.contains("shortcuts"), let shortcuts {
                Button { Haptics.tap(); shortcuts() } label: {
                    KeyCap(symbol: shortcutsOn ? "bolt.fill" : "bolt", state: shortcutsOn ? .once : .off)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Quick Commands [Terminal Keys 6f3d83]")
            }
            if visible.contains("paste"), let paste {
                if let photos, let files {
                    // A tap pastes the clipboard's text as ever; held, it offers photos and files too.
                    Menu {
                        Button { paste() } label: { Label("Paste Text", systemImage: Theme.Icon.paste) }
                        fileItems(photos: photos, files: files)
                    } label: {
                        KeyCap(symbol: Theme.Icon.paste, state: .off)
                    } primaryAction: {
                        Haptics.tap()
                        paste()
                    }
                    .menuStyle(.button)
                    .buttonStyle(.plain)
                    .menuIndicator(.hidden)
                    .accessibilityLabel("Paste")
                    .accessibilityHint("Press and hold to paste photos or files")
                    .accessibilityIdentifier("terminal-paste")
                } else {
                    Button { Haptics.tap(); paste() } label: { KeyCap(symbol: Theme.Icon.paste, state: .off) }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Paste")
                }
            }
            if visible.contains("files"), let photos, let files {
                Menu {
                    fileItems(photos: photos, files: files)
                } label: {
                    KeyCap(symbol: Theme.Icon.attachment, state: .off)
                }
                .menuStyle(.button)
                .buttonStyle(.plain)
                .menuIndicator(.hidden)
                .accessibilityLabel("Paste Photos or Files")
                .accessibilityIdentifier("terminal-files")
            }
            if visible.contains("alt") { modifier(.alt, "Alt", name: "Option") }
            if visible.contains("shift") { modifier(.shift, "⇧", name: "Shift") }
        }
    }

    @ViewBuilder private func fileItems(photos: @escaping () -> Void, files: @escaping () -> Void) -> some View {
        Button { photos() } label: { Label("Photos", systemImage: Theme.Icon.photos) }
        Button { files() } label: { Label("Files", systemImage: Theme.Icon.file) }
    }

    private func keyboardButton(_ up: Binding<Bool>) -> some View {
        Button {
            Haptics.tap()
            up.wrappedValue.toggle()
        } label: {
            KeyCap(symbol: up.wrappedValue ? Theme.Icon.dismissKeyboard : Theme.Icon.keyboard, state: .off)
        }
        .buttonStyle(.plain)
        // A double tap is return (as in Moshi); a single one waits for it not to be one.
        .highPriorityGesture(TapGesture(count: 2).onEnded { Haptics.tap(); send(["enter"]) })
        .accessibilityLabel(up.wrappedValue ? String(localized: "Hide Keyboard") : String(localized: "Keyboard"))
        .accessibilityIdentifier("terminal-keyboard")
    }

    private var padButton: some View {
        Button {
            Haptics.tap()
            withAnimation(.spring(duration: 0.3, bounce: 0.3)) { pad.toggle() }
        } label: {
            KeyCap(symbol: pad ? "gamecontroller.fill" : "gamecontroller", state: pad ? .once : .off)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Arrows")
        .accessibilityAddTraits(pad ? .isSelected : [])
    }

    private func key(_ label: String, name: String? = nil, action: @escaping () -> Void) -> some View {
        Button {
            Haptics.tap()
            action()
        } label: {
            KeyCap(label: label, state: .off)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(name ?? label)
    }

    /// Down and up are tracked by hand: a tap, a quick second tap and a hold all mean something different. Touches
    /// come from UIKit: a SwiftUI drag gesture misses the second touch of a quick double tap.
    private func modifier(_ k: TerminalModifiers.Key, _ label: String, name: String) -> some View {
        let state = m.wrappedValue[k]
        return KeyCap(label: label, state: state)
            .overlay(TouchArea(down: { down(k) }, up: { up(k) }, cancel: { cancelled(k) }))
            .accessibilityElement()
            .accessibilityLabel(name)
            .accessibilityValue(state == .locked ? String(localized: "Locked") : state == .off ? "" : String(localized: "Pressed"))
            .accessibilityAddTraits(.isButton)
            .accessibilityAddTraits(state == .off ? [] : .isSelected)
            .accessibilityAction { m.wrappedValue.tap(k, quick: false) }
    }

    private func down(_ k: TerminalModifiers.Key) {
        guard downAt[k] == nil else { return }
        let at = Date()
        downAt[k] = at
        Task {
            try? await Task.sleep(for: .milliseconds(Self.holdAfter))
            guard downAt[k] == at else { return }
            Haptics.tap()
            m.wrappedValue.hold(k)
        }
    }

    private func up(_ k: TerminalModifiers.Key) {
        downAt[k] = nil
        if m.wrappedValue[k] == .held {
            m.wrappedValue.release(k)
            return
        }
        let now = Date()
        let quick = lastTap[k].map { now.timeIntervalSince($0) < Self.doubleTap } ?? false
        lastTap[k] = now
        Haptics.tap()
        m.wrappedValue.tap(k, quick: quick)
    }

    private func cancelled(_ k: TerminalModifiers.Key) {
        downAt[k] = nil
        m.wrappedValue.release(k)
    }

    static let holdAfter = 350 // ms
    static let doubleTap: TimeInterval = 0.35

    /// Modifiers apply to the next key, then the armed-once ones let go.
    private func press(_ key: String) {
        send([m.wrappedValue.apply(key)])
        m.wrappedValue.pressed()
    }
}

#if os(iOS) || os(visionOS)
/// Reports every touch down and up on it (each finger on its own key), nothing else.
private struct TouchArea: UIViewRepresentable {
    var down: () -> Void
    var up: () -> Void
    /// The touch became something else (the bar scrolled): not a tap.
    var cancel: () -> Void

    func makeUIView(context: Context) -> TouchView { TouchView() }
    func updateUIView(_ v: TouchView, context: Context) { v.down = down; v.up = up; v.cancel = cancel }

    final class TouchView: UIView {
        var down: (() -> Void)?
        var up: (() -> Void)?
        var cancel: (() -> Void)?
        override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent?) { down?() }
        override func touchesEnded(_ touches: Set<UITouch>, with event: UIEvent?) { up?() }
        override func touchesCancelled(_ touches: Set<UITouch>, with event: UIEvent?) { cancel?() }
    }
}
#else
/// Reports every mouse press and release on it (the Mac's version of the touch area above), nothing else.
private struct TouchArea: NSViewRepresentable {
    var down: () -> Void
    var up: () -> Void
    /// Never sent by a mouse (a press always ends in a release); kept so the key bar is the same on both.
    var cancel: () -> Void

    func makeNSView(context: Context) -> TouchView { TouchView() }
    func updateNSView(_ v: TouchView, context: Context) { v.down = down; v.up = up; v.cancel = cancel }

    final class TouchView: NSView {
        var down: (() -> Void)?
        var up: (() -> Void)?
        var cancel: (() -> Void)?
        override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
        override func mouseDown(with event: NSEvent) { down?() }
        override func mouseUp(with event: NSEvent) { up?() }
    }
}
#endif

/// A key of the bar: armed / held keys are tinted and outlined, a locked one is filled with a bar under its label.
struct KeyCap: View {
    var label = ""
    var symbol: String? = nil
    var state: ModifierState

    var body: some View {
        Group {
            if let symbol {
                ThemedIcon(symbol).font(Theme.Font.secondaryMedium).dynamicTypeSize(...DynamicTypeSize.large).scaledFrame(width: Theme.Size.wellL)
            } else {
                Text(label)
                    .font(Font(TerminalFont.jetbrains.font(size: 14, bold: false))) // the terminal's own JetBrains Mono
                    .lineLimit(1)
                    .fixedSize()
                    .padding(.horizontal, Theme.Space.s)
            }
        }
        .foregroundStyle(state == .locked ? Color.white : state == .off ? Color.primary : Theme.accent)
        .scaledFrame(minWidth: Theme.Size.wellL, minHeight: Theme.Size.wellL)
        // As in Moshi: plain labels and icons on the bar; only an armed or locked key gets a fill.
        .background(state == .locked ? Theme.accent : state == .off ? Color.clear : Theme.accent.opacity(0.18), in: .capsule)
        .overlay(Capsule().stroke(state == .once || state == .held ? Theme.accent : .clear, lineWidth: 1.5))
        .overlay(alignment: .bottom) {
            if state == .locked { Capsule().fill(.white).scaledFrame(width: Theme.Size.markXS, height: Theme.Size.rule).padding(.bottom, Theme.Space.sm) }
        }
        .scaleEffect(state == .held ? 0.94 : 1)
        .animation(.snappy(duration: 0.15), value: state)
        .contentShape(.capsule)
    }
}

/// Five keys laid out like a game pad.
struct DirectionPad: View {
    var press: (String) -> Void
    private let size: CGFloat = 52

    var body: some View {
        Grid(horizontalSpacing: 6, verticalSpacing: 6) {
            GridRow {
                Color.clear.frame(width: size, height: size)
                pad(Theme.Icon.up, "up")
                Color.clear.frame(width: size, height: size)
            }
            GridRow {
                pad("arrow.left", "left")
                pad("return", "enter", prominent: true)
                pad("arrow.right", "right")
            }
            GridRow {
                Color.clear.frame(width: size, height: size)
                pad(Theme.Icon.down, "down")
                Color.clear.frame(width: size, height: size)
            }
        }
        .padding(Theme.Space.m)
        .surface(in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous)) // no shadow over the surface
    }

    @ViewBuilder private func pad(_ symbol: String, _ key: String, prominent: Bool = false) -> some View {
        let label = ThemedIcon(symbol).font(Theme.Font.title).frame(width: size, height: size)
        if prominent {
            Button { Haptics.tap(); press(key) } label: { label }
                .themedButton(prominent: true).clipShape(.circle).accessibilityLabel(key)
        } else {
            Button { Haptics.tap(); press(key) } label: { label }
                .themedButton().clipShape(.circle).accessibilityLabel(key)
        }
    }
}
