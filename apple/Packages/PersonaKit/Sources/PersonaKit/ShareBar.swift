import CoreGraphics
import Foundation

/// A bar of parts side by side, each as wide as its share of the values (the Machines page's token shares), unit-tested.
public nonisolated enum ShareBar {
    /// Each part's width in a bar `width` wide with `gap` between parts. A part whose share would be narrower than
    /// `minimum` gets `minimum`, and the others split what is left in proportion, so the parts and the gaps add up to
    /// `width`: a minimum is taken from the large parts, never added past the bar's end. When the bar is too narrow
    /// for every part's minimum, the parts are equal.
    public static func widths(_ values: [Double], width: CGFloat, gap: CGFloat, minimum: CGFloat) -> [CGFloat] {
        guard !values.isEmpty else { return [] }
        let count = CGFloat(values.count)
        let room = max(0, width - gap * (count - 1))
        guard minimum * count < room else { return Array(repeating: room / count, count: values.count) }
        var widths = Array(repeating: minimum, count: values.count)
        var free = Array(values.indices)
        // Each round gives the free parts their share of what the parts held at the minimum leave; any that come out
        // under the minimum are held there next round, which leaves less for the rest. At least one part stays free:
        // the last one gets everything left, which is more than the minimum.
        while true {
            let left = room - minimum * CGFloat(values.count - free.count)
            let total = free.reduce(0) { $0 + max(0, values[$1]) }
            var held: [Int] = []
            for i in free {
                let w = total > 0 ? left * CGFloat(max(0, values[i]) / total) : left / CGFloat(free.count)
                if w < minimum { held.append(i) } else { widths[i] = w }
            }
            if held.isEmpty { return widths }
            for i in held { widths[i] = minimum }
            free.removeAll { held.contains($0) }
        }
    }
}
