import RedlineKit
import SwiftUI

struct StatusPopoverActions {
    let showDashboard: @MainActor () -> Void
    let openBrowser: @MainActor () -> Void
    let quit: @MainActor () -> Void
}

struct StatusPopoverView: View {
    @ObservedObject var model: PopoverViewModel
    let actions: StatusPopoverActions

    private var snapshot: DashboardSnapshot? { model.snapshot }
    private var trayState: TrayState? { snapshot.map(TrayState.init) }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    providerGrid
                    activitySection
                    queueSection
                    latestDecision
                }
                .padding(18)
            }
            Divider()
            footer
        }
        .frame(width: 420, height: 560)
        .background(.ultraThinMaterial)
    }

    private var header: some View {
        HStack(spacing: 11) {
            Image(nsImage: GaugeIcon.image(activity: trayState?.activity, remainingPercent: trayState?.lowestWeeklyPercent))
                .resizable()
                .frame(width: 26, height: 26)
            VStack(alignment: .leading, spacing: 2) {
                Text(activityTitle).font(.system(size: 15, weight: .semibold))
                Text(activityDetail).font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer()
            if model.isRefreshing {
                ProgressView().controlSize(.small)
            } else {
                Circle().fill(activityColor).frame(width: 8, height: 8)
            }
        }
        .padding(.horizontal, 18)
        .padding(.vertical, 15)
    }

    @ViewBuilder private var providerGrid: some View {
        let providers = (snapshot?.providers ?? []).sorted {
            providerRank($0.provider) < providerRank($1.provider)
        }
        if !providers.isEmpty {
            HStack(spacing: 10) {
                ForEach(providers) { provider in
                    providerCard(provider)
                }
            }
        } else {
            emptyRow("Usage data is not available yet.", symbol: "chart.bar.xaxis")
        }
    }

    private func providerCard(_ provider: ProviderSummary) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 7) {
                Image(nsImage: ProviderArtwork.image(
                    for: provider.provider,
                    template: provider.provider.lowercased() != "claude",
                    size: 17
                ))
                    .resizable().frame(width: 17, height: 17)
                Text(provider.displayName).font(.system(size: 12, weight: .semibold))
                Spacer()
                Text(provider.weeklyPercent.map { "\($0)%" } ?? "—")
                    .font(.system(size: 15, weight: .bold, design: .rounded))
            }
            ProgressView(value: Double(provider.weeklyPercent ?? 0), total: 100)
                .tint(progressColor(provider.weeklyPercent))
            HStack {
                Text("Weekly").foregroundStyle(.secondary)
                Spacer()
                if let reset = resetSummary(provider) { Text(reset).foregroundStyle(.secondary) }
            }
            .font(.system(size: 10))
        }
        .padding(12)
        .frame(maxWidth: .infinity)
        .background(Color.primary.opacity(0.055), in: RoundedRectangle(cornerRadius: 11))
    }

    private var activitySection: some View {
        VStack(alignment: .leading, spacing: 9) {
            sectionLabel("SYSTEM")
            HStack(spacing: 12) {
                Label(snapshot?.scheduler.enabled == true ? "Scheduler on" : "Scheduler off", systemImage: "clock.arrow.circlepath")
                Spacer()
                Label("\(snapshot?.health.activeRuns ?? 0) running", systemImage: "bolt.fill")
            }
            .font(.system(size: 12, weight: .medium))
            if let error = model.errorMessage {
                Label(error, systemImage: "wifi.exclamationmark").font(.system(size: 11)).foregroundStyle(.red)
            } else if let health = snapshot?.health, health.status != "healthy" {
                let currentFailure = snapshot?.latestAttemptsByProvider.contains { $0.outcome == "error" } == true
                Label(
                    currentFailure
                        ? "A provider's latest dispatch check failed"
                        : "\(health.dispatchErrors) earlier dispatch errors; latest checks are succeeding",
                    systemImage: currentFailure ? "exclamationmark.triangle.fill" : "clock.badge.exclamationmark"
                )
                .font(.system(size: 11))
                .foregroundStyle(currentFailure ? .red : .orange)
            }
        }
    }

    @ViewBuilder private var queueSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            sectionLabel("NEXT IN QUEUE")
            let tasks = Array(
                (snapshot?.tasks ?? [])
                    .filter { $0.state == "queued" }
                    .prefix(3)
            )
            if tasks.isEmpty {
                emptyRow("No queued jobs", symbol: "checkmark.circle")
            } else {
                ForEach(Array(tasks.enumerated()), id: \.element.id) { index, task in
                    HStack(spacing: 9) {
                        Text("\(index + 1).")
                            .frame(width: 16, alignment: .trailing)
                            .font(.system(size: 10, weight: .semibold, design: .monospaced))
                            .foregroundStyle(.secondary)
                        VStack(alignment: .leading, spacing: 1) {
                            Text(task.name).font(.system(size: 12, weight: .medium)).lineLimit(1)
                            Text("\(task.providerAccountID) · \(task.dispatchTier.replacingOccurrences(of: "_", with: " "))")
                                .font(.system(size: 10)).foregroundStyle(.secondary)
                        }
                        Spacer()
                    }
                    .padding(.vertical, 3)
                }
            }
        }
    }

    @ViewBuilder private var latestDecision: some View {
        let decisions = latestProviderDecisions
        if !decisions.isEmpty {
            VStack(alignment: .leading, spacing: 7) {
                sectionLabel("LATEST DECISIONS")
                ForEach(decisions, id: \.attempt.id) { item in
                    HStack(alignment: .top, spacing: 9) {
                        Image(nsImage: ProviderArtwork.image(
                            for: item.provider.provider,
                            template: item.provider.provider.lowercased() != "claude",
                            size: 13
                        ))
                        .resizable().frame(width: 13, height: 13)
                        VStack(alignment: .leading, spacing: 2) {
                            Text("\(item.provider.displayName) · \(item.attempt.decision ?? item.attempt.outcome.uppercased())")
                                .font(.system(size: 12, weight: .semibold))
                            Text(item.attempt.reason ?? "No reason reported").font(.system(size: 10)).foregroundStyle(.secondary)
                        }
                    }
                }
            }
        }
    }

    private var footer: some View {
        HStack(spacing: 10) {
            Button("Open Dashboard", action: actions.showDashboard).buttonStyle(.borderedProminent).tint(.red)
            Button { Task { await model.refresh() } } label: { Image(systemName: "arrow.clockwise") }
                .buttonStyle(.bordered).disabled(model.isRefreshing)
            Spacer()
            Menu {
                Button("Open in Browser", action: actions.openBrowser)
                Divider()
                Button("Quit Redline", action: actions.quit)
            } label: { Image(systemName: "ellipsis.circle") }
            .menuStyle(.borderlessButton)
            .fixedSize()
        }
        .padding(.horizontal, 18)
        .padding(.vertical, 12)
    }

    private func sectionLabel(_ text: String) -> some View {
        Text(text).font(.system(size: 9, weight: .bold, design: .monospaced)).tracking(1.2).foregroundStyle(.secondary)
    }

    private func emptyRow(_ text: String, symbol: String) -> some View {
        Label(text, systemImage: symbol).font(.system(size: 11)).foregroundStyle(.secondary)
    }

    private var activityTitle: String {
        guard model.errorMessage == nil else { return "Redline is offline" }
        return switch trayState?.activity {
        case .running: "Running a queued job"
        case .attention: "Redline needs attention"
        case .waiting: "Waiting for spare capacity"
        case nil: "Starting Redline"
        }
    }

    private var activityDetail: String {
        if model.errorMessage != nil { return "The local service could not be reached" }
        if trayState?.activity == .attention { return "The service is online, but recent operations failed" }
        if trayState?.activity == .running { return "An admitted task is currently active" }
        return "Monitoring usage and the dispatch queue"
    }

    private var activityColor: Color {
        guard model.errorMessage == nil else { return .red }
        return switch trayState?.activity {
        case .running: .blue
        case .attention: .orange
        case .waiting: .green
        case nil: .secondary
        }
    }

    private func progressColor(_ percent: Int?) -> Color {
        guard let percent else { return .secondary }
        if percent <= 20 { return .red }
        if percent <= 40 { return .orange }
        return .green
    }

    private var latestProviderDecisions: [(provider: ProviderSummary, attempt: AttemptSummary)] {
        let attempts = snapshot?.latestAttemptsByProvider ?? []
        return (snapshot?.providers ?? [])
            .sorted { providerRank($0.provider) < providerRank($1.provider) }
            .compactMap { provider in
                attempts.first { $0.providerAccountID == provider.id }.map { (provider, $0) }
            }
    }

    private func resetSummary(_ provider: ProviderSummary) -> String? {
        if let short = provider.shortPercent {
            let reset = relativeReset(provider.snapshot?.short?.resetsAt).map { " · \($0)" } ?? ""
            return "5h \(short)%\(reset)"
        }
        return relativeReset(provider.snapshot?.weekly?.resetsAt).map { "Resets \($0)" }
    }

    private func relativeReset(_ timestamp: String?) -> String? {
        guard let timestamp else { return nil }
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let plain = ISO8601DateFormatter()
        guard let date = fractional.date(from: timestamp) ?? plain.date(from: timestamp) else { return nil }
        let seconds = max(0, date.timeIntervalSinceNow)
        if seconds < 3600 { return "in \(max(1, Int(ceil(seconds / 60))))m" }
        if seconds < 86400 { return "in \(Int(ceil(seconds / 3600)))h" }
        return "in \(Int(ceil(seconds / 86400)))d"
    }

    private func providerRank(_ provider: String) -> Int {
        switch provider.lowercased() {
        case "codex": 0
        case "claude": 1
        default: 2
        }
    }
}
