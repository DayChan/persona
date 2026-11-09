#if os(iOS) || os(visionOS)
import ImageIO
import QuartzCore
import SwiftUI
import Testing
import UIKit
import UniformTypeIdentifiers
@testable import Persona

@MainActor struct BreathingRingTests {
    @Test func groupKeepsTheOriginalCurveDurationAndWallPhase() throws {
        let group = BreathingRingView.animation(at: 50, wallTime: 123.25)
        #expect(group.beginTime == 49.75)
        #expect(group.duration == StatusDot.breath)
        #expect(group.repeatCount == .infinity)
        #expect(group.preferredFrameRateRange.preferred == Theme.Motion.breathPreferredFPS)
        let animations = try #require(group.animations as? [CABasicAnimation])
        #expect(animations.map(\.keyPath) == ["transform.scale", "opacity"])
        #expect((animations[0].fromValue as? NSNumber)?.doubleValue == 1)
        #expect((animations[0].toValue as? NSNumber)?.doubleValue == 1 + Double(StatusDot.breathScale))
        #expect((animations[1].fromValue as? NSNumber)?.doubleValue == 1)
        #expect((animations[1].toValue as? NSNumber)?.doubleValue == 0)
        let linear = CAMediaTimingFunction(name: .linear)
        for animation in animations {
            #expect(animation.duration == StatusDot.breath)
            for index in 0...3 {
                var actual = [Float](repeating: 0, count: 2), expected = actual
                animation.timingFunction?.getControlPoint(at: index, values: &actual)
                linear.getControlPoint(at: index, values: &expected)
                #expect(actual == expected)
            }
        }
        let easing = try #require(group.timingFunction)
        var first = [Float](repeating: 0, count: 2), second = first
        easing.getControlPoint(at: 1, values: &first)
        easing.getControlPoint(at: 2, values: &second)
        #expect(abs(first[0] - 1 / 3) < 0.00001 && abs(first[1] - 2 / 3) < 0.00001)
        #expect(abs(second[0] - 2 / 3) < 0.00001 && second[1] == 1)
    }

    @Test func layoutIsCircularLocalAndDoesNotRestartTheAnimation() throws {
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 200, height: 200))
        let view = BreathingRingView(frame: CGRect(x: 20, y: 30, width: 40, height: 20))
        view.bounds.origin = CGPoint(x: 7, y: 11)
        view.configure(color: UIColor.red.cgColor, displayScale: 3, eligible: true)
        #expect(view.ring.animation(forKey: BreathingRingView.key) == nil, "unattached views cannot animate")
        window.addSubview(view)
        view.layoutIfNeeded()
        let before = try #require(view.ring.animation(forKey: BreathingRingView.key))
        #expect(view.ring.path?.boundingBoxOfPath == CGRect(x: 10, y: 0, width: 20, height: 20))
        #expect(view.ring.position == CGPoint(x: 27, y: 21))
        #expect(view.ring.contentsScale == 3)
        #expect(view.ring.lineWidth == StatusDot.ringWidth)
        #expect(view.hitTest(.zero, with: nil) == nil)
        view.configure(color: UIColor.blue.cgColor, displayScale: 2, eligible: true)
        view.setNeedsLayout()
        view.layoutIfNeeded()
        let after = try #require(view.ring.animation(forKey: BreathingRingView.key))
        #expect(before.beginTime == after.beginTime, "ordinary updates preserve the animation's phase")
        #expect(view.ring.animationKeys() == [BreathingRingView.key], "no implicit layout or color animations")
        view.configure(color: UIColor.blue.cgColor, displayScale: 2, eligible: false)
        #expect(view.ring.animation(forKey: BreathingRingView.key) == nil)
        view.configure(color: UIColor.blue.cgColor, displayScale: 2, eligible: true)
        #expect(view.ring.animation(forKey: BreathingRingView.key) != nil)
        view.removeFromSuperview()
        #expect(view.ring.animation(forKey: BreathingRingView.key) == nil)
        window.addSubview(view)
        #expect(view.ring.animation(forKey: BreathingRingView.key) != nil)
        BreathingRing.dismantleUIView(view, coordinator: ())
        #expect(view.ring.animation(forKey: BreathingRingView.key) == nil)
        view.removeFromSuperview()
    }
}

@MainActor struct PersonaFramesVisibilityTests {
    private func frames() throws -> PersonaFrames {
        let url = FileManager.default.temporaryDirectory.appending(path: "visibility-frames-\(UUID().uuidString).png")
        defer { try? FileManager.default.removeItem(at: url) }
        let destination = try #require(CGImageDestinationCreateWithURL(url as CFURL, UTType.png.identifier as CFString, 2, nil))
        CGImageDestinationSetProperties(destination, [kCGImagePropertyPNGDictionary: [kCGImagePropertyAPNGLoopCount: 0]] as CFDictionary)
        for index in 0..<2 {
            let image = UIGraphicsImageRenderer(size: CGSize(width: 12, height: 12), format: .init(for: .init(displayScale: 1))).image { context in
                UIColor(white: CGFloat(index), alpha: 1).setFill()
                context.fill(CGRect(x: 0, y: 0, width: 12, height: 12))
            }
            CGImageDestinationAddImage(destination, try #require(image.cgImage),
                                      [kCGImagePropertyPNGDictionary: [kCGImagePropertyAPNGDelayTime: 0.125]] as CFDictionary)
        }
        #expect(CGImageDestinationFinalize(destination))
        let frames = try #require(PersonaFrames(url: url, fps: nil))
        #expect(frames.images.count == 2)
        return frames
    }

    @Test func playbackNeedsAWindowAndEligibilityAndDismantleReleasesFrames() throws {
        let frames = try frames()
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 200, height: 200))
        let view = PersonaFramesImageView(frame: CGRect(x: 0, y: 0, width: 40, height: 40))
        view.configure(frames: frames, animating: true, eligible: true)
        #expect(!view.isAnimating && view.animationImages == nil)
        window.addSubview(view)
        #expect(view.isAnimating)
        #expect(view.animationImages?.count == 2)
        view.configure(frames: frames, animating: true, eligible: false)
        #expect(!view.isAnimating && view.animationImages == nil)
        #expect(view.image === frames.images[0])
        view.configure(frames: frames, animating: true, eligible: true)
        #expect(view.isAnimating)
        view.configure(frames: frames, animating: false, eligible: true)
        #expect(!view.isAnimating && view.animationImages == nil, "Reduce Motion keeps only frame zero")
        view.configure(frames: frames, animating: true, eligible: true)
        view.removeFromSuperview()
        #expect(!view.isAnimating && view.animationImages == nil)
        window.addSubview(view)
        #expect(view.isAnimating)
        PersonaFramesView.dismantleUIView(view, coordinator: ())
        #expect(!view.isAnimating && view.animationImages == nil && view.image == nil)
        view.removeFromSuperview()
        window.addSubview(view)
        #expect(!view.isAnimating && view.image == nil, "teardown cannot restart retained playback")
        view.removeFromSuperview()
    }
}
#endif
