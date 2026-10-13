import RedlineKit
import SwiftUI

/// Cursor and Grok Bot usage in the tray dropdown. There are no pause, queue
/// or refresh controls here, and nothing in this section affects the gauge or
/// menu-bar title. When a Cursor provider account exists its monthly pools
/// gate Cursor jobs (shown on that account), so the label says "usage detail"
/// rather than claiming Redline ignores them.
struct CursorUsageSection: View {
    let report: CursorUsageReport
    var scheduled: Bool = false

    var body: some View {
        // Re-evaluate freshness on this clock even when no new data arrives.
        TimelineView(.periodic(from: .now, by: 30)) { context in
            content(now: context.date)
        }
    }

    private func content(now: Date) -> some View {
        let stale = report.isStale(now: now)
        let freshness = report.freshness(now: now)
        return VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 7) {
                Text("C")
                    .font(.system(size: 11, weight: .bold, design: .monospaced))
                    .frame(width: 17, height: 17)
                    .background(Color.primary.opacity(0.1), in: RoundedRectangle(cornerRadius: 4))
                Text("Cursor").font(.system(size: 12, weight: .semibold))
                if let plan = report.plan {
                    Text(plan)
                        .font(.system(size: 8, weight: .semibold, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 5).padding(.vertical, 1)
                        .overlay(Capsule().stroke(Color.secondary.opacity(0.4)))
                }
                Text(scheduled ? "USAGE DETAIL" : "USAGE ONLY")
                    .font(.system(size: 8, weight: .bold, design: .monospaced))
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 5).padding(.vertical, 1)
                    .overlay(Capsule().strokeBorder(style: StrokeStyle(lineWidth: 1, dash: [2])).foregroundStyle(.secondary))
                    .help(scheduled
                        ? "Monthly pools schedule Cursor jobs on the Cursor account. Grok Bot, on-demand spend and credits never do."
                        : "Shown for reference. Redline does not schedule or submit work to Cursor or Grok Bot.")
                Spacer()
                Text(freshness.label)
                    .font(.system(size: 10, weight: .medium))
                    .foregroundStyle(freshnessColor(freshness))
            }
            Text(metaLine(now: now))
                .font(.system(size: 9))
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            ForEach(Array((report.errors + [report.refreshError].compactMap { $0 }).prefix(2).enumerated()), id: \.offset) { _, error in
                Label(error, systemImage: "exclamationmark.triangle.fill")
                    .font(.system(size: 9))
                    .foregroundStyle(.orange)
                    .lineLimit(2)
            }
            if report.meters.isEmpty && report.balances.isEmpty {
                Text(report.fetchedAt == nil ? "Waiting for the first Cursor usage read." : "Cursor reported no usage meters.")
                    .font(.system(size: 10))
                    .foregroundStyle(.secondary)
            }
            ForEach(CursorMeter.Group.allCases, id: \.self) { group in
                let meters = report.meters(in: group)
                if !meters.isEmpty {
                    VStack(alignment: .leading, spacing: 6) {
                        groupLabel(group.heading)
                        ForEach(meters) { meter in meterRow(meter, stale: stale, now: now) }
                    }
                }
            }
            if !report.balances.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    groupLabel("CREDITS")
                    ForEach(report.balances) { balance in balanceRow(balance, stale: stale, now: now) }
                }
            }
            if !report.notReported.isEmpty && !(report.meters.isEmpty && report.balances.isEmpty) {
                Text("Not reported: \(report.notReported.joined(separator: ", "))")
                    .font(.system(size: 9))
                    .foregroundStyle(.secondary)
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.primary.opacity(0.035), in: RoundedRectangle(cornerRadius: 11))
        .overlay(RoundedRectangle(cornerRadius: 11).strokeBorder(style: StrokeStyle(lineWidth: 1, dash: [3])).foregroundStyle(Color.secondary.opacity(0.35)))
        .accessibilityElement(children: .contain)
        .accessibilityLabel(scheduled
            ? "Cursor usage detail. Monthly pools schedule Cursor jobs; Grok Bot, on-demand and credits never do."
            : "Cursor usage, usage only, not scheduled")
    }

    private func metaLine(now: Date) -> String {
        var parts: [String] = []
        if let fetched = CursorUsageFormat.date(report.fetchedAt) {
            let source = report.sourceLabel.map { "\($0) · " } ?? ""
            parts.append("\(source)read \(CursorUsageFormat.relative(fetched, now: now))")
        } else {
            parts.append("Not read yet")
        }
        parts.append("not a queue; Redline can't submit work here")
        return parts.joined(separator: " · ")
    }

    private func meterRow(_ meter: CursorMeter, stale: Bool, now: Date) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack {
                Text("\(stale ? "Last known " : "")\(meter.label)").font(.system(size: 11))
                Spacer()
                Text(meter.valueText)
                    .font(.system(size: 11, weight: .semibold, design: .monospaced))
                    .foregroundStyle(meter.isExhausted ? .red : .primary)
            }
            if let percent = meter.remainingPercent {
                // Same direction and thresholds as the provider gauges.
                ProgressView(value: percent, total: 100)
                    .tint(meter.isExhausted || percent < 15 ? .red : percent < 35 ? .orange : .green)
                    .accessibilityLabel("\(meter.label) \(Int(percent.rounded()))% remaining")
            }
            Text(CursorUsageFormat.resetLine(for: meter, now: now))
                .font(.system(size: 9))
                .foregroundStyle(.secondary)
        }
    }

    private func balanceRow(_ balance: CursorBalance, stale: Bool, now: Date) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack {
                Text("\(stale ? "Last known " : "")\(balance.label)").font(.system(size: 11))
                Spacer()
                Text(balance.valueText).font(.system(size: 11, weight: .semibold, design: .monospaced))
            }
            if let expiry = balance.nextExpiry(now: now) {
                Text("Next expires \(CursorUsageFormat.relative(expiry, now: now)) · \(CursorUsageFormat.absolute(expiry))")
                    .font(.system(size: 9))
                    .foregroundStyle(.secondary)
            }
        }
    }

    private func groupLabel(_ text: String) -> some View {
        Text(text).font(.system(size: 8, weight: .bold, design: .monospaced)).tracking(1).foregroundStyle(.secondary)
    }

    private func freshnessColor(_ freshness: CursorUsageReport.Freshness) -> Color {
        switch freshness {
        case .upToDate: .green
        case .outOfDate, .partial: .orange
        case .noData: .secondary
        }
    }
}
