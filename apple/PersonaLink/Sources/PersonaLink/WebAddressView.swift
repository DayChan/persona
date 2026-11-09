import AppKit
import SwiftUI

/// "Web Address": where "Needs you" rows and notifications open the web app. It wins over the address the hub gives
/// (`web_url`), for a hub without an https public URL, or one whose web app the browser knows at another address.
struct WebAddressView: View {
    let model: LinkModel
    @State private var address: String
    @State private var invalid: Bool
    let close: () -> Void

    init(model: LinkModel, address: String? = nil, close: @escaping () -> Void = {}) {
        self.model = model
        self.close = close
        let a = address ?? model.webAddress
        _address = State(initialValue: a)
        _invalid = State(initialValue: AttentionURL.setting(a) == nil)
    }

    /// What an empty field means: the hub's web address, if it says one.
    private var fallback: String? { AttentionURL.base(setting: nil, webURL: model.status?.attention?.webURL) }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(linkText("Web Address")).font(.headline)
            TextField(fallback ?? "https://hub.example.com", text: $address)
                .textFieldStyle(.roundedBorder)
                .onSubmit { save() }
                .onChange(of: address) { invalid = false }
            Text(explanation)
                .font(.callout).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
            if invalid {
                Label(linkText("Enter an http or https address, such as https://hub.example.com/persona."),
                      systemImage: "exclamationmark.triangle")
                    .font(.callout)
                    .foregroundStyle(.orange)
                    .fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                Spacer()
                Button {
                    close()
                } label: {
                    Text(linkText("Cancel")).lineLimit(1)
                }
                .keyboardShortcut(.cancelAction)
                Button {
                    save()
                } label: {
                    Text(linkText("Save")).lineLimit(1)
                }
                .keyboardShortcut(.defaultAction)
                .disabled(address.trimmingCharacters(in: .whitespacesAndNewlines) == model.webAddress)
            }
        }
        .padding(20)
        .frame(width: 380)
    }

    private var explanation: String {
        if let fallback {
            return linkText("“Needs you” rows and notifications open the web app here. Leave it empty to use \(fallback).")
        }
        return linkText("“Needs you” rows and notifications open the web app here. The hub does not say where it is.")
    }

    private func save() {
        guard let value = AttentionURL.setting(address) else {
            invalid = true
            return
        }
        model.setWebAddress(value)
        close()
    }
}

/// The Web Address window. AppKit's, not a SwiftUI scene: a clicked notification opens it from no view, and the menu
/// opens the same one. Closing it drops a route that waited for the address (`LinkModel.pendingRoute`).
@MainActor enum WebAddressWindow {
    private static var window: NSWindow?
    private static var closing: NSObjectProtocol?

    static func show(model: LinkModel) {
        let window = Self.window ?? make(model: model)
        Self.window = window
        if !window.isVisible { // an open window keeps what is typed in it
            let content = NSHostingController(rootView: WebAddressView(model: model) { [weak window] in window?.close() })
            content.sizingOptions = .preferredContentSize
            window.contentViewController = content
            window.center()
        }
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private static func make(model: LinkModel) -> NSWindow {
        let window = NSWindow(contentRect: .zero, styleMask: [.titled, .closable], backing: .buffered, defer: true)
        window.title = linkText("Web Address")
        window.isReleasedWhenClosed = false
        closing = NotificationCenter.default.addObserver(forName: NSWindow.willCloseNotification, object: window, queue: .main) { _ in
            MainActor.assumeIsolated { model.forgetPendingRoute() }
        }
        return window
    }
}
