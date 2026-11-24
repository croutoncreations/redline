import RedlineKit
import SwiftUI

/// The popover card for an OpenAI API credits account: dollars of
/// promotional credit, never a weekly, monthly or 5-hour window. Opening the
/// details is view state only; it never refreshes usage.
struct APICreditsCardView: View {
    let account: ProviderSummary
    @Binding var expanded: Bool
    let pausing: Bool
    let setPaused: (Bool) -> Void
    /// Fixed clock for tests and renders; nil follows the real clock.
    var now: Date? = nil

    var body: some View {
        TimelineView(.everyMinute) { context in
            if let card = APICreditsCard(account: account, now: now ?? context.date) {
                content(card)
            }
        }
    }

    private func content(_ card: APICreditsCard) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 7) {
                Image(nsImage: ProviderArtwork.image(for: account.provider, template: true, size: 17))
                    .resizable().frame(width: 17, height: 17)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 1) {
                    Text(card.title).font(.system(size: 12, weight: .semibold)).lineLimit(1)
                    if let label = card.label {
                        Text(label).font(.system(size: 10)).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                .layoutPriority(1)
                Spacer(minLength: 4)
                Text(card.headline)
                    .font(.system(size: 15, weight: .bold, design: .rounded))
                    .monospacedDigit()
                    .foregroundStyle(headlineColor(card))
                    .lineLimit(1)
                    .minimumScaleFactor(0.7)
            }
            // Open, the "Promotional credit" meter carries the bar and expiry.
            if expanded && hasDetails(card) {
                details(card)
            } else {
                if let progress = card.progress {
                    ProgressView(value: progress, total: 1).tint(toneColor(card.tone))
                }
                secondaryLine(card)
            }
            HStack(spacing: 6) {
                Button {
                    setPaused(!card.paused)
                } label: {
                    if pausing {
                        ProgressView().controlSize(.mini)
                    } else {
                        Label(
                            card.paused ? "Resume scheduling" : "Pause scheduling",
                            systemImage: card.paused ? "play.fill" : "pause.fill")
                    }
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .disabled(pausing)
                Spacer()
                if hasDetails(card) {
                    Button {
                        expanded.toggle()
                    } label: {
                        Label(
                            expanded ? "Hide details" : "Details",
                            systemImage: expanded ? "chevron.up" : "chevron.down"
                        )
                        .labelStyle(.titleAndIcon)
                    }
                    .buttonStyle(.borderless)
                    .controlSize(.small)
                    .accessibilityValue(expanded ? "Expanded" : "Collapsed")
                }
            }
            .font(.system(size: 10, weight: .medium))
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.primary.opacity(0.055), in: RoundedRectangle(cornerRadius: 11))
    }

    /// Signed out, there is nothing behind "Details" but the fix already shown.
    private func hasDetails(_ card: APICreditsCard) -> Bool {
        card.decision != nil || card.promoLine != nil || card.rule != nil || card.paid != nil
            || !card.grants.isEmpty
    }

    private func secondaryLine(_ card: APICreditsCard) -> some View {
        Label(card.secondary, systemImage: secondarySymbol(card))
            .font(.system(size: 10))
            .foregroundStyle(card.attention ? Color.orange : Color.secondary)
            .fixedSize(horizontal: false, vertical: true)
            .textSelection(.enabled)
    }

    @ViewBuilder private func details(_ card: APICreditsCard) -> some View {
        VStack(alignment: .leading, spacing: 7) {
            if let decision = card.decision {
                Text(decision)
                    .font(.system(size: 10, weight: .medium))
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let promo = card.promoLine {
                VStack(alignment: .leading, spacing: 4) {
                    detailRow("Promotional credit", promo)
                    if let progress = card.progress {
                        ProgressView(value: progress, total: 1).tint(toneColor(card.tone))
                    }
                    secondaryLine(card)
                }
            } else {
                if let progress = card.progress {
                    ProgressView(value: progress, total: 1).tint(toneColor(card.tone))
                }
                secondaryLine(card)
            }
            if let rule = card.rule {
                Text(rule)
                    .font(.system(size: 10))
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let paid = card.paid {
                detailRow("Paid credit", paid)
            }
            if !card.grants.isEmpty {
                VStack(alignment: .leading, spacing: 4) {
                    Text("GRANTS")
                        .font(.system(size: 9, weight: .bold, design: .monospaced))
                        .tracking(1.2)
                        .foregroundStyle(.secondary)
                    ForEach(card.grants, id: \.self) { grant in
                        HStack(alignment: .firstTextBaseline, spacing: 8) {
                            Text(grant.label).font(.system(size: 10))
                            Spacer(minLength: 4)
                            Text([grant.value, grant.expires].compactMap { $0 }.joined(separator: " · "))
                                .font(.system(size: 10))
                                .monospacedDigit()
                                .foregroundStyle(.secondary)
                        }
                        .accessibilityElement(children: .combine)
                    }
                }
            }
        }
    }

    private func detailRow(_ label: String, _ value: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(label).font(.system(size: 10)).foregroundStyle(.secondary)
            Spacer(minLength: 4)
            Text(value).font(.system(size: 10, weight: .medium)).monospacedDigit()
                .multilineTextAlignment(.trailing)
        }
        .accessibilityElement(children: .combine)
    }

    private func headlineColor(_ card: APICreditsCard) -> Color {
        switch card.status {
        case .ok: card.progress == 0 ? .secondary : .primary
        case .signedOut, .error: .red
        case .stale, .noData: .orange
        }
    }

    private func toneColor(_ tone: APICreditsCard.Tone) -> Color {
        switch tone {
        case .normal: .green
        case .warning: .orange
        case .danger: .red
        }
    }

    private func secondarySymbol(_ card: APICreditsCard) -> String {
        switch card.status {
        case .signedOut: "key.fill"
        case .stale, .error, .noData: "exclamationmark.triangle.fill"
        case .ok: card.progress == 0 ? "minus.circle" : "hourglass"
        }
    }
}
