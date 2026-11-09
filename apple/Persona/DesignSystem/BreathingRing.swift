#if os(iOS) || os(visionOS)
import QuartzCore
import SwiftUI
import UIKit

/// The needs-you ring runs in the compositor, not a SwiftUI display clock. Visibility owns its lifetime.
struct BreathingRing: UIViewRepresentable {
    var color: Color
    var eligible: Bool

    func makeUIView(context: Context) -> BreathingRingView { BreathingRingView() }

    func updateUIView(_ view: BreathingRingView, context: Context) {
        view.configure(color: color.resolve(in: context.environment).cgColor,
                       displayScale: context.environment.displayScale, eligible: eligible)
    }

    static func dismantleUIView(_ view: BreathingRingView, coordinator: ()) {
        view.stop()
    }
}

final class BreathingRingView: UIView {
    let ring = CAShapeLayer()
    static let key = "breath"
    private var eligible = false

    override init(frame: CGRect) {
        super.init(frame: frame)
        isUserInteractionEnabled = false
        ring.fillColor = nil
        ring.lineWidth = StatusDot.ringWidth
        ring.opacity = 0
        layer.addSublayer(ring)
    }

    @available(*, unavailable) required init?(coder: NSCoder) { fatalError() }

    override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? { nil }

    func configure(color: CGColor, displayScale: CGFloat, eligible: Bool) {
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        ring.strokeColor = color
        ring.contentsScale = displayScale
        CATransaction.commit()
        self.eligible = eligible
        updateAnimation()
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        // A local, circular path centered in the bounds, including rectangular bounds and nonzero origins.
        let side = min(bounds.width, bounds.height)
        let local = CGRect(origin: .zero, size: bounds.size)
        let circle = CGRect(x: local.midX - side / 2, y: local.midY - side / 2, width: side, height: side)
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        ring.bounds = local
        ring.position = CGPoint(x: bounds.midX, y: bounds.midY)
        ring.path = CGPath(ellipseIn: circle, transform: nil)
        CATransaction.commit()
        updateAnimation()
    }

    override func didMoveToWindow() {
        super.didMoveToWindow()
        updateAnimation()
    }

    private func updateAnimation() {
        guard eligible, window != nil, bounds.width > 0, bounds.height > 0 else {
            ring.removeAnimation(forKey: Self.key)
            return
        }
        // Color, scale, and ordinary row updates do not reset a running breath.
        guard ring.animation(forKey: Self.key) == nil else { return }
        let now = ring.convertTime(CACurrentMediaTime(), from: nil)
        ring.add(Self.animation(at: now, wallTime: Date.timeIntervalSinceReferenceDate), forKey: Self.key)
    }

    func stop() {
        eligible = false
        ring.removeAnimation(forKey: Self.key)
    }

    /// Map the original wall-clock phase onto the layer's local clock. Resuming joins the current breath.
    static func animation(at now: CFTimeInterval, wallTime: TimeInterval) -> CAAnimationGroup {
        let scale = CABasicAnimation(keyPath: "transform.scale")
        scale.fromValue = 1
        scale.toValue = 1 + StatusDot.breathScale
        scale.duration = StatusDot.breath
        scale.timingFunction = CAMediaTimingFunction(name: .linear)
        let fade = CABasicAnimation(keyPath: "opacity")
        fade.fromValue = 1
        fade.toValue = 0
        fade.duration = StatusDot.breath
        fade.timingFunction = CAMediaTimingFunction(name: .linear)
        let group = CAAnimationGroup()
        group.animations = [scale, fade]
        group.duration = StatusDot.breath
        group.repeatCount = .infinity
        // Exactly 1 - (1 - p)^2: a quadratic elevated to a cubic. Ease the group, not its children twice.
        group.timingFunction = CAMediaTimingFunction(controlPoints: 1 / 3, 2 / 3, 2 / 3, 1)
        let phase = wallTime.truncatingRemainder(dividingBy: StatusDot.breath)
        group.beginTime = now - (phase < 0 ? phase + StatusDot.breath : phase)
        // This asks for 30 FPS; the system controls the actual compositor cadence.
        group.preferredFrameRateRange = CAFrameRateRange(minimum: Theme.Motion.breathMinimumFPS,
                                                        maximum: Theme.Motion.breathPreferredFPS,
                                                        preferred: Theme.Motion.breathPreferredFPS)
        return group
    }
}
#endif
