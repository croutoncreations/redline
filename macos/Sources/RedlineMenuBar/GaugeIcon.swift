import AppKit
import RedlineKit

enum GaugeIcon {
    static func image(for level: TrayState.Level?) -> NSImage {
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

        let needle = NSBezierPath()
        needle.move(to: center)
        needle.line(to: NSPoint(x: 14.6, y: 12.1))
        needle.lineWidth = 1.6
        needle.lineCapStyle = .round
        NSColor.labelColor.setStroke()
        needle.stroke()

        let hub = NSBezierPath(ovalIn: NSRect(x: 8.2, y: 6.7, width: 2.6, height: 2.6))
        NSColor.labelColor.setFill()
        hub.fill()

        statusColor(for: level).setFill()
        NSBezierPath(ovalIn: NSRect(x: 1.5, y: 1.5, width: 4, height: 4)).fill()
        image.isTemplate = false
        return image
    }

    private static func statusColor(for level: TrayState.Level?) -> NSColor {
        switch level {
        case .comfortable: .systemGreen
        case .constrained: .systemOrange
        case .critical, .degraded: .systemRed
        case .running: .systemBlue
        case nil: .tertiaryLabelColor
        }
    }
}
