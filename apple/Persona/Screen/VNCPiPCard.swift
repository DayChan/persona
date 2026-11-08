import CoreVideo
import SwiftUI

#if os(iOS)
/// What the small window shows while there is no picture of the Mac (#1839): its name and Connecting… with a turning arc
/// from the tap on (so it comes up at once, not once connected), or why it could not connect and how to try again
/// (its ▶, or back to the app). Drawn into frames for its source (VNCPiP); the Mac's first frame replaces it.
struct VNCPiPCard: View {
    enum State: Equatable {
        case connecting
        /// The kept sign-in was refused: the page asks for a new one.
        case signIn
        case failed(String)

        /// What the card says for this phase of the connection; nil once the Mac's picture is there.
        init?(_ phase: RFBSession.Phase, pictured: Bool) {
            switch phase {
            case .live where pictured: return nil
            case .connecting, .live: self = .connecting
            case .signIn: self = .signIn
            case .closed(let why): self = .failed(why ?? String(localized: "Connection lost"))
            }
        }

        /// For Picture in Picture Diagnostics (and the source's accessibility value under DEBUG -showPiPSource).
        var line: String {
            switch self {
            case .connecting: String(localized: "Connecting")
            case .signIn: String(localized: "Sign in again")
            case .failed(let why): String(localized: "Not connected: \(why)")
            }
        }
    }

    let state: State
    let name: String
    /// The card's size, in the Mac's proportions (the small window keeps it when the picture comes).
    let size: CGSize
    /// How far the arc has turned, 0 to 1 (connecting).
    var turn: Double = 0

    var body: some View {
        VStack(spacing: Theme.Space.s) {
            mark
            Text(name).font(Theme.Font.heading).foregroundStyle(.primary).lineLimit(1)
            Text(status).font(Theme.Font.secondary).foregroundStyle(.secondary).multilineTextAlignment(.center).lineLimit(2)
            if let hint { hint.font(Theme.Font.caption).foregroundStyle(.secondary).multilineTextAlignment(.center).lineLimit(2) }
        }
        .minimumScaleFactor(0.7)
        .padding(Theme.Space.l)
        .frame(width: size.width, height: size.height)
        .background(Color.black)
        .environment(\.colorScheme, .dark)
    }

    @ViewBuilder private var mark: some View {
        switch state {
        case .connecting:
            Circle()
                .trim(from: 0, to: 0.7)
                .stroke(Theme.accent, style: StrokeStyle(lineWidth: Theme.Size.ruleWide, lineCap: .round))
                .frame(width: Theme.Size.icon, height: Theme.Size.icon)
                .rotationEffect(.degrees(turn * 360))
        case .signIn:
            ThemedIcon(Theme.Icon.locked).font(Theme.Font.title).foregroundStyle(Theme.caution)
        case .failed:
            ThemedIcon(Theme.Icon.error).font(Theme.Font.title).foregroundStyle(Theme.danger)
        }
    }

    private var status: String {
        switch state {
        case .connecting: String(localized: "Connecting… [Connection Status ee9ebe]")
        case .signIn: String(localized: "Saved password no longer works")
        case .failed(let why): why
        }
    }

    private var hint: Text? {
        let back = ThemedIcon(Theme.Icon.pictureInPictureExit)
        switch state {
        case .connecting: return nil
        case .signIn: return Text("Tap \(back) to go back to the app and sign in again")
        case .failed: return Text("Tap \(Image(systemName: Theme.Icon.pictureInPicturePlay)) to reconnect, or \(back) to go back to the app")
        }
    }

    /// The card as a frame for the small window's source: BGRA, at 3× its size.
    @MainActor static func frame(_ state: State, name: String, size: CGSize, turn: Double = 0) -> CVPixelBuffer? {
        let renderer = ImageRenderer(content: VNCPiPCard(state: state, name: name, size: size, turn: turn))
        renderer.scale = 3
        guard let image = renderer.cgImage else { return nil }
        let attrs: [String: Any] = [kCVPixelBufferIOSurfacePropertiesKey as String: [:], kCVPixelBufferCGImageCompatibilityKey as String: true,
                                    kCVPixelBufferCGBitmapContextCompatibilityKey as String: true]
        var pb: CVPixelBuffer?
        guard CVPixelBufferCreate(nil, image.width, image.height, kCVPixelFormatType_32BGRA, attrs as CFDictionary, &pb) == kCVReturnSuccess,
              let pb else { return nil }
        CVPixelBufferLockBaseAddress(pb, [])
        defer { CVPixelBufferUnlockBaseAddress(pb, []) }
        guard let context = CGContext(data: CVPixelBufferGetBaseAddress(pb), width: image.width, height: image.height, bitsPerComponent: 8,
                                      bytesPerRow: CVPixelBufferGetBytesPerRow(pb), space: CGColorSpaceCreateDeviceRGB(),
                                      bitmapInfo: CGImageAlphaInfo.noneSkipFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue)
        else { return nil }
        context.draw(image, in: CGRect(x: 0, y: 0, width: image.width, height: image.height))
        return pb
    }
}
#endif
