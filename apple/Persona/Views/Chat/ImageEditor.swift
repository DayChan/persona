import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

/// A small markup editor: freehand pen and rectangles in a few colors, undo, clear. The marks are kept in image
/// coordinates and burned into the bitmap on Done.
struct ImageEditor: View {
    @Environment(\.dismiss) private var dismiss
    let image: UIImage
    /// Title of the done button (what happens with the result: "Add to Message", "Done").
    var doneTitle: String = String(localized: "Done [Extension System Presentation c0b3fb]")
    var onDone: (UIImage) -> Void

    enum Tool: String, CaseIterable { case pen, box }
    struct Mark: Identifiable {
        var id = UUID()
        var tool: Tool
        var color: UIColor
        var points: [CGPoint] // image coordinates; a box is its first and last point
    }

    @State private var tool: Tool = .box
    @State private var color: UIColor = .systemRed
    @State private var marks: [Mark] = []
    @State private var current: Mark?

    private static let colors: [UIColor] = [.systemRed, .systemYellow, .systemGreen, .systemBlue, .white, .black]

    var body: some View {
        NavigationStack {
            GeometryReader { geo in
                let fit = Self.fitRect(image.size, in: geo.size)
                ZStack {
                    Color.black.ignoresSafeArea()
                    Image(uiImage: image).resizable().frame(width: fit.width, height: fit.height).position(x: fit.midX, y: fit.midY)
                    Canvas { ctx, _ in
                        for m in marks + (current.map { [$0] } ?? []) { draw(m, in: &ctx, fit: fit) }
                    }
                    .frame(width: geo.size.width, height: geo.size.height)
                    .contentShape(.rect)
                    .gesture(DragGesture(minimumDistance: 0)
                        .onChanged { v in
                            let p = Self.toImage(v.location, fit: fit, size: image.size)
                            if current == nil { current = Mark(tool: tool, color: color, points: [p]) } else {
                                current!.points.append(p)
                            }
                        }
                        .onEnded { _ in
                            if let c = current, c.points.count > 1 { marks.append(c) }
                            current = nil
                        })
                }
            }
            .safeAreaInset(edge: .bottom) { toolbar }
            .navigationTitle("Edit Image")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(.black, for: .navigationBar)
            .toolbarColorScheme(.dark, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button(doneTitle) {
                        onDone(render())
                        dismiss()
                    }
                    .fontWeight(.semibold)
                    .lineLimit(1)
                }
            }
        }
        .preferredColorScheme(.dark)
    }

    private var toolbar: some View {
        VStack(spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.l) {
                ForEach(Self.colors, id: \.self) { c in
                    Button { color = c } label: {
                        Circle().fill(Color(uiColor: c)).scaledFrame(width: Theme.Size.iconL, height: Theme.Size.iconL)
                            .overlay(Circle().stroke(.white, lineWidth: color == c ? 3 : 1).opacity(color == c ? 1 : 0.4))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Colors [Image Editor 070a80]")
                }
            }
            HStack(spacing: Theme.Space.m) {
                toolButton(.box, String(localized: "Box"), "rectangle")
                toolButton(.pen, String(localized: "Pen"), "scribble")
                Spacer(minLength: 0)
                Button { _ = marks.popLast() } label: { ThemedIcon(Theme.Icon.undo).scaledFrame(width: Theme.Size.icon) }
                    .themedButton().disabled(marks.isEmpty).accessibilityLabel("Undo")
                Button { marks = [] } label: { ThemedIcon(Theme.Icon.delete).scaledFrame(width: Theme.Size.icon) }
                    .themedButton().disabled(marks.isEmpty).accessibilityLabel("Clear [Image Editor bce237]")
            }
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.m)
        .background(.black)
    }

    private func toolButton(_ t: Tool, _ title: String, _ symbol: String) -> some View {
        Button { tool = t } label: {
            Label(title, systemImage: symbol).lineLimit(1).font(Theme.Font.secondaryStrong)
        }
        .themedButton(prominent: tool == t)
    }

    private func draw(_ m: Mark, in ctx: inout GraphicsContext, fit: CGRect) {
        let scale = fit.width / image.size.width
        let pts = m.points.map { CGPoint(x: fit.minX + $0.x * scale, y: fit.minY + $0.y * scale) }
        guard let first = pts.first, let last = pts.last else { return }
        var path = Path()
        switch m.tool {
        case .pen: path.addLines(pts)
        case .box: path.addRect(CGRect(x: min(first.x, last.x), y: min(first.y, last.y), width: abs(last.x - first.x), height: abs(last.y - first.y)))
        }
        ctx.stroke(path, with: .color(Color(uiColor: m.color)), style: StrokeStyle(lineWidth: 3.5, lineCap: .round, lineJoin: .round))
    }

    /// The image with the marks burned in, at its own resolution.
    private func render() -> UIImage {
        guard !marks.isEmpty else { return image }
        let width = max(image.size.width, image.size.height) / 220 // the on-screen stroke at the image's scale
        return UIGraphicsImageRenderer(size: image.size).image { ctx in
            #if os(iOS) || os(visionOS)
            image.draw(at: .zero)
            #else
            // The renderer's context is flipped (top-left origin); NSImage only honours that when asked.
            image.draw(in: CGRect(origin: .zero, size: image.size), from: .zero, operation: .copy, fraction: 1, respectFlipped: true, hints: nil)
            #endif
            let cg = ctx.cgContext
            cg.setLineCap(.round)
            cg.setLineJoin(.round)
            cg.setLineWidth(width)
            for m in marks {
                cg.setStrokeColor(m.color.cgColor)
                guard let first = m.points.first, let last = m.points.last else { continue }
                switch m.tool {
                case .pen: cg.addLines(between: m.points)
                case .box: cg.addRect(CGRect(x: min(first.x, last.x), y: min(first.y, last.y), width: abs(last.x - first.x), height: abs(last.y - first.y)))
                }
                cg.strokePath()
            }
        }
    }

    static func fitRect(_ size: CGSize, in box: CGSize) -> CGRect {
        guard size.width > 0, size.height > 0 else { return .zero }
        let s = min(box.width / size.width, box.height / size.height)
        let w = size.width * s, h = size.height * s
        return CGRect(x: (box.width - w) / 2, y: (box.height - h) / 2, width: w, height: h)
    }

    static func toImage(_ p: CGPoint, fit: CGRect, size: CGSize) -> CGPoint {
        guard fit.width > 0 else { return .zero }
        let s = size.width / fit.width
        return CGPoint(x: min(max((p.x - fit.minX) * s, 0), size.width), y: min(max((p.y - fit.minY) * s, 0), size.height))
    }
}
