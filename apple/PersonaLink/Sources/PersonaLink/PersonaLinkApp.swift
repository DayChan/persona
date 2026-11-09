import SwiftUI

@main
struct PersonaLinkApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    private let model = LinkModel.shared

    var body: some Scene {
        MenuBarExtra {
            MenuView(model: model)
        } label: {
            MenuBarIcon(model: model)
        }
        .menuBarExtraStyle(.window)

        // A `persona://link?…` / `persona://link?…` link opened anywhere (Notes, AirDrop, Mail, Safari) opens this window
        // with the link filled in (Info.plist CFBundleURLTypes); pairing still waits for the 「配对」 (Pair) button.
        Window("配对 / 修改连接", id: "pair") {
            PairView(model: model)
                .onOpenURL { url in
                    model.openLink(url)
                    NSApp.activate(ignoringOtherApps: true)
                }
        }
        .windowResizability(.contentSize)
        .defaultLaunchBehavior(.suppressed)
        .handlesExternalEvents(matching: ["*"])

        // What the hub calls this computer (`persona-link name`, docs/operations/link.md "The computer's name").
        Window("给这台电脑改名", id: "rename") {
            RenameView(model: model)
        }
        .windowResizability(.contentSize)
        .defaultLaunchBehavior(.suppressed)
        // The Web Address window is AppKit's (WebAddressWindow): a clicked notification opens it too, from no view.
    }
}

/// The menu bar icon: a template symbol, so it reads on light and dark menu bars alike; while sessions need you, with
/// their count beside it.
struct MenuBarIcon: View {
    let model: LinkModel

    var body: some View {
        let needs = model.status?.attention?.needsYou ?? 0
        if let count = LinkAttention.countText(needs) {
            Image(nsImage: Self.counted(model.look.symbol, count: count))
                .renderingMode(.template)
                .accessibilityLabel(linkText("Persona Link · Needs You: \(needs)"))
        } else {
            Image(systemName: model.look.symbol)
                .accessibilityLabel("Persona Link")
        }
    }

    /// The symbol and the count drawn into one template image, in the menu bar's font (a menu bar label shows an image
    /// or a text, not both); drawn when shown, so it is sharp at any display's scale.
    static func counted(_ symbol: String, count: String) -> NSImage {
        let size = NSFont.menuBarFont(ofSize: 0).pointSize
        let config = NSImage.SymbolConfiguration(pointSize: size, weight: .regular)
        let icon = NSImage(systemSymbolName: symbol, accessibilityDescription: nil)?.withSymbolConfiguration(config) ?? NSImage()
        let text = NSAttributedString(string: count, attributes: [
            .font: NSFont.monospacedDigitSystemFont(ofSize: size - 1, weight: .semibold), .foregroundColor: NSColor.black,
        ])
        let gap: CGFloat = 3
        let box = NSSize(width: ceil(icon.size.width + gap + text.size().width), height: ceil(max(icon.size.height, text.size().height)))
        let image = NSImage(size: box, flipped: false) { _ in
            icon.draw(in: NSRect(x: 0, y: (box.height - icon.size.height) / 2, width: icon.size.width, height: icon.size.height))
            text.draw(at: NSPoint(x: icon.size.width + gap, y: (box.height - text.size().height) / 2))
            return true
        }
        image.isTemplate = true
        return image
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        let args = CommandLine.arguments
        if let i = args.firstIndex(of: "--snapshot"), i + 1 < args.count {
            // Render the menu and the pairing window, light and dark, and quit (scripts/app/mac-link.sh snapshot).
            MainActor.assumeIsolated { Snapshots.render(to: URL(fileURLWithPath: args[i + 1])) }
            exit(0)
        }
        MainActor.assumeIsolated { LinkModel.shared.start() } // also LinkNotifier's delegate, before a click can arrive
    }

    func applicationWillTerminate(_ notification: Notification) {
        MainActor.assumeIsolated { LinkModel.shared.stopHelper() }
    }
}
