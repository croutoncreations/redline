import AppKit
import RedlineKit
import SwiftUI

@MainActor
final class RunLogWindowController {
    private let client: RedlineAPIClient
    private var windows: [String: NSWindowController] = [:]

    init(client: RedlineAPIClient) { self.client = client }

    func show(run: RunSummary) {
        if let existing = windows[run.id] {
            existing.showWindow(nil)
            existing.window?.makeKeyAndOrderFront(nil)
            NSApplication.shared.activate(ignoringOtherApps: true)
            return
        }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 820, height: 560),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "Run Logs · \(run.taskID)"
        window.minSize = NSSize(width: 560, height: 360)
        window.contentViewController = NSHostingController(rootView: RunLogView(client: client, run: run))
        window.center()
        let controller = NSWindowController(window: window)
        windows[run.id] = controller
        controller.showWindow(nil)
        window.makeKeyAndOrderFront(nil)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }
}

private struct RunLogView: View {
    let client: RedlineAPIClient
    let run: RunSummary
    @State private var stream = RunLogStream.stdout
    @State private var content = "Loading…"
    @State private var detail = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(run.taskID).font(.headline)
                    Text("\(run.providerAccountID) · \(run.state) · \(run.id)").font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Picker("Stream", selection: $stream) {
                    Text("Run stdout").tag(RunLogStream.stdout)
                    Text("Run stderr").tag(RunLogStream.stderr)
                    Divider()
                    Text("Prepare stdout").tag(RunLogStream.prepareStdout)
                    Text("Prepare stderr").tag(RunLogStream.prepareStderr)
                    Text("Finalize stdout").tag(RunLogStream.finalizeStdout)
                    Text("Finalize stderr").tag(RunLogStream.finalizeStderr)
                }
                .frame(width: 180)
                Button { Task { await load() } } label: { Image(systemName: "arrow.clockwise") }
            }
            if let summary = run.summary, !summary.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    Text("RESULT").font(.caption2.weight(.semibold)).foregroundStyle(.secondary)
                    Text(LinkedResultText.make(summary)).font(.callout).textSelection(.enabled)
                    if !run.artifacts.isEmpty {
                        HStack {
                            ForEach(run.artifacts) { artifact in
                                if let value = artifact.url, let url = URL(string: value) {
                                    Link(artifact.label, destination: url)
                                } else {
                                    Text(artifact.label).foregroundStyle(.secondary)
                                }
                            }
                        }.font(.caption)
                    }
                }
                .padding(10)
                .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 7))
            }
            if !detail.isEmpty { Text(detail).font(.caption).foregroundStyle(.secondary) }
            ScrollView([.horizontal, .vertical]) {
                Text(content.isEmpty ? "This log is empty." : content)
                    .font(.system(size: 11, design: .monospaced))
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .topLeading)
                    .padding(10)
            }
            .background(Color(nsColor: .textBackgroundColor), in: RoundedRectangle(cornerRadius: 7))
        }
        .padding(18)
        .task(id: stream) { await load() }
    }

    private func load() async {
        content = "Loading…"
        detail = ""
        do {
            let log = try await client.runLogs(runID: run.id, stream: stream)
            content = log.content
            detail = log.truncated ? "Showing the latest 32 KB of \(log.sizeBytes) bytes." : "\(log.sizeBytes) bytes"
        } catch {
            content = "Log unavailable"
            detail = error.localizedDescription
        }
    }
}
