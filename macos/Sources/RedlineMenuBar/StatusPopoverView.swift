import RedlineKit
import SwiftUI

struct StatusPopoverActions {
    let showDashboard: @MainActor () -> Void
    let openBrowser: @MainActor () -> Void
    let showRunLogs: @MainActor (RunSummary) -> Void
    let checkForUpdates: @MainActor () -> Void
    let enableNotifications: @MainActor () -> Void
    let showAppSetup: @MainActor () -> Void
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
                    failureSection
                    providerGrid
                    activitySection
                    queueSection
                    recentRunsSection
                    latestDecision
                }
                .padding(18)
            }
            Divider()
            footer
        }
        .frame(width: 420, height: 640)
        .background(.ultraThinMaterial)
    }

    @ViewBuilder private var failureSection: some View {
        if let failure = snapshot?.unresolvedFailure {
            VStack(alignment: .leading, spacing: 8) {
                Label("Job failed", systemImage: "exclamationmark.triangle.fill")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(.red)
                Text(taskName(failure.taskID))
                    .font(.system(size: 13, weight: .semibold))
                Text(failureMessage(failure))
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                HStack(spacing: 12) {
                    Button("View logs") { actions.showRunLogs(failure) }
                        .buttonStyle(.bordered)
                    Button {
                        Task {
                            await model.recoverFailedTask(
                                failure.taskID,
                                providerID: failure.providerAccountID,
                                providerPaused: failedProviderIsPaused(failure)
                            )
                        }
                    } label: {
                        if model.tasksBeingControlled.contains(failure.taskID) {
                            ProgressView().controlSize(.small)
                        } else {
                            Text(failedProviderIsPaused(failure) ? "Resume & retry" : "Retry job")
                        }
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(.red)
                    .disabled(model.tasksBeingControlled.contains(failure.taskID))
                    Button("Enable alerts…", action: actions.enableNotifications)
                        .buttonStyle(.borderless)
                }
                .font(.system(size: 10, weight: .medium))
            }
            .padding(12)
            .background(Color.red.opacity(0.09), in: RoundedRectangle(cornerRadius: 11))
            .overlay(RoundedRectangle(cornerRadius: 11).stroke(Color.red.opacity(0.35)))
        }
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
            Button {
                Task { await model.setPaused(!provider.paused, providerID: provider.id) }
            } label: {
                if model.providersBeingControlled.contains(provider.id) {
                    ProgressView().controlSize(.mini)
                } else {
                    Label(provider.paused ? "Resume scheduling" : "Pause scheduling", systemImage: provider.paused ? "play.fill" : "pause.fill")
                }
            }
            .font(.system(size: 10, weight: .medium))
            .buttonStyle(HoverActionButtonStyle(tint: provider.paused ? .green : .red))
            .disabled(model.providersBeingControlled.contains(provider.id))
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
            if let error = model.actionError {
                Label(error, systemImage: "exclamationmark.circle").font(.system(size: 11)).foregroundStyle(.red)
            }
        }
    }

    @ViewBuilder private var recentRunsSection: some View {
        let runs = Array((snapshot?.runs ?? []).prefix(3))
        if !runs.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                sectionLabel("RECENT RUNS")
                ForEach(runs) { run in
                    HStack(spacing: 9) {
                        Image(systemName: runSymbol(run.state))
                            .foregroundStyle(run.state == "failed" ? .red : run.state == "completed" ? .green : .blue)
                            .frame(width: 14)
                        VStack(alignment: .leading, spacing: 1) {
                            Text(taskName(run.taskID)).font(.system(size: 12, weight: .medium)).lineLimit(1)
                            Text("\(run.providerAccountID) · \(run.state)").font(.system(size: 10)).foregroundStyle(.secondary)
                        }
                        Spacer()
                        Button("Logs") { actions.showRunLogs(run) }
                            .font(.system(size: 10)).buttonStyle(.borderless)
                    }
                }
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
            Button("Open Redline", action: actions.showDashboard).buttonStyle(.borderedProminent).tint(.red)
            Button { Task { await model.refresh() } } label: { Image(systemName: "arrow.clockwise") }
                .buttonStyle(.bordered).disabled(model.isRefreshing)
            Spacer()
            Button("Notifications…", action: actions.enableNotifications)
                .buttonStyle(.bordered)
                .help("Enable or manage job notifications")
            Menu {
                Button("Open in Browser", action: actions.openBrowser)
                Button("Check for Updates…", action: actions.checkForUpdates)
                Button("App Setup…", action: actions.showAppSetup)
                Divider()
                Button("Quit Redline", action: actions.quit)
            } label: { Image(systemName: "ellipsis.circle") }
            .menuStyle(.borderlessButton)
            .fixedSize()
        }
        .padding(.horizontal, 18)
        .padding(.vertical, 12)
    }

    private func taskName(_ taskID: String) -> String {
        snapshot?.tasks.first { $0.id == taskID }?.name ?? taskID
    }

    private func failureMessage(_ run: RunSummary) -> String {
        let error = run.error ?? ""
        if error != "harness exited with code 1" {
            return error.isEmpty ? "The agent stopped unexpectedly. Open the run logs for details." : error
        }
        switch snapshot?.tasks.first(where: { $0.id == run.taskID })?.harnessType {
        case "claude-code":
            return "Claude Code stopped unexpectedly. Open the logs for details. If its session expired, run `claude auth login`, then retry."
        case "codex-cli":
            return "Codex CLI stopped unexpectedly. Open the logs for details. If its session expired, run `codex login`, then retry."
        default:
            return "The agent stopped unexpectedly. Open the run logs for details."
        }
    }

    private func failedProviderIsPaused(_ run: RunSummary) -> Bool {
        snapshot?.providers.first { $0.id == run.providerAccountID }?.paused == true
    }

    private func runSymbol(_ state: String) -> String {
        switch state {
        case "completed": "checkmark.circle.fill"
        case "failed": "xmark.circle.fill"
        default: "bolt.circle.fill"
        }
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

private struct HoverActionButtonStyle: ButtonStyle {
    let tint: Color

    func makeBody(configuration: Configuration) -> some View {
        HoverActionButton(configuration: configuration, tint: tint)
    }

    private struct HoverActionButton: View {
        let configuration: ButtonStyleConfiguration
        let tint: Color
        @Environment(\.isEnabled) private var isEnabled
        @State private var isHovering = false

        var body: some View {
            configuration.label
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 8)
                .padding(.vertical, 6)
                .foregroundStyle(isHovering ? tint : .secondary)
                .background(
                    tint.opacity(configuration.isPressed ? 0.2 : isHovering ? 0.11 : 0),
                    in: RoundedRectangle(cornerRadius: 6)
                )
                .overlay {
                    RoundedRectangle(cornerRadius: 6)
                        .stroke(tint.opacity(isHovering ? 0.45 : 0), lineWidth: 1)
                }
                .contentShape(Rectangle())
                .opacity(isEnabled ? 1 : 0.55)
                .animation(.easeOut(duration: 0.12), value: isHovering)
                .onHover { isHovering = $0 }
        }
    }
}
