import AppKit
import RedlineKit

enum GaugeIcon {
    static func image(
        activity: TrayState.Activity?,
        remainingPercent: Int?,
        offline: Bool = false
    ) -> NSImage {
        let image = NSImage(size: NSSize(width: 19, height: 19))
        image.lockFocus()
        defer { image.unlockFocus() }

        let center = NSPoint(x: 9.5, y: 8)
        let track = NSBezierPath()
        track.appendArc(withCenter: center, radius: 6.5, startAngle: 155, endAngle: 25, clockwise: true)
        track.lineWidth = 2
        NSColor.secondaryLabelColor.setStroke()
        track.stroke()

        let redline = NSBezierPath()
        redline.appendArc(withCenter: center, radius: 6.5, startAngle: 48, endAngle: 25, clockwise: true)
        redline.lineWidth = 2.5
        NSColor.systemRed.setStroke()
        redline.stroke()

        let usedFraction = 1 - (Double(remainingPercent ?? 100) / 100)
        let angle = 155 + ((25 - 155) * min(max(usedFraction, 0), 1))
        let radians = angle * .pi / 180
        let needle = NSBezierPath()
        needle.move(to: center)
        needle.line(to: NSPoint(x: center.x + 6.3 * cos(radians), y: center.y + 6.3 * sin(radians)))
        needle.lineWidth = 1.6
        needle.lineCapStyle = .round
        NSColor.labelColor.setStroke()
        needle.stroke()

        let hub = NSBezierPath(ovalIn: NSRect(x: 8.2, y: 6.7, width: 2.6, height: 2.6))
        NSColor.labelColor.setFill()
        hub.fill()

        statusColor(activity: activity, offline: offline).setFill()
        NSBezierPath(ovalIn: NSRect(x: 1.5, y: 1.5, width: 4, height: 4)).fill()
        image.isTemplate = false
        return image
    }

    private static func statusColor(activity: TrayState.Activity?, offline: Bool) -> NSColor {
        if offline { return .systemRed }
        return switch activity {
        case .waiting: .systemGreen
        case .running: .systemBlue
        case .attention: .systemOrange
        case nil: .tertiaryLabelColor
        }
    }
}
