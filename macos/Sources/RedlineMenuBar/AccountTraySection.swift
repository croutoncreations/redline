import RedlineKit
import SwiftUI

/// The popover's multi-account block for one provider family: an overview
/// line, a compact row per account in the service's order, and one row at a
/// time expanded for details.
///
/// Keyboard: the list itself is the one Tab stop (whether or not Full
/// Keyboard Access is on); ↑/↓ move the highlighted row, Return toggles its
/// details (Space too on macOS 14+), Escape collapses an open row and
/// otherwise falls through to close the popover. On macOS 13 the system focus
/// ring is drawn around the list as well as the row highlight. VoiceOver
/// reads each row as one button with its full sentence and an
/// expanded/collapsed value.
///
/// Expanding a row is view state only. It never asks the service to refresh
/// usage, so it cannot touch any CLI's stored login.
struct AccountTraySection: View {
    let snapshot: DashboardSnapshot
    let provider: String
    @Binding var expandedID: String?
    let pausing: Set<String>
    let setPaused: (String, Bool) -> Void
    /// Fixed clock for tests and renders; nil follows the real clock.
    var now: Date? = nil

    var body: some View {
        // Relative wording ("due", countdowns) is recomputed every minute even
        // when no new snapshot arrives.
        TimelineView(.everyMinute) { context in
            if let tray = AccountTray(snapshot: snapshot, provider: provider, now: now ?? context.date) {
                AccountTrayList(
                    tray: tray,
                    expandedID: $expandedID,
                    pausing: pausing,
                    setPaused: setPaused
                )
            }
        }
    }
}

struct AccountTrayList: View {
    let tray: AccountTray
    @Binding var expandedID: String?
    let pausing: Set<String>
    let setPaused: (String, Bool) -> Void

    /// The row the keyboard is on. Separate from the expanded row: moving
    /// with the arrows does not open anything until Return or Space.
    @State private var highlightedID: String?
    @FocusState private var listFocused: Bool

    /// Collapsed row height at normal text size; five fit without scrolling.
    static let rowHeight: CGFloat = 48
    static let rowSpacing: CGFloat = 4

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            header
            columnHeader
            list
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(tray.displayName) accounts")
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 7) {
                Image(nsImage: ProviderArtwork.image(for: tray.provider, template: false, size: 15))
                    .resizable().frame(width: 15, height: 15)
                    .accessibilityHidden(true)
                Text(tray.displayName.uppercased() + " ACCOUNTS")
                    .font(.system(size: 9, weight: .bold, design: .monospaced))
                    .tracking(1.2)
                    .foregroundStyle(.secondary)
            }
            Text(tray.overview)
                .font(.system(size: 12, weight: .semibold))
                .fixedSize(horizontal: false, vertical: true)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(tray.displayName) accounts: \(AccountTrayRow.spoken(tray.overview))")
        .accessibilityAddTraits(.isHeader)
    }

    private var columnHeader: some View {
        HStack(spacing: 8) {
            Text("Account").frame(maxWidth: .infinity, alignment: .leading)
            Text("5-hour").frame(width: AccountTrayRowView.windowWidth, alignment: .trailing)
            Text("Weekly").frame(width: AccountTrayRowView.windowWidth, alignment: .trailing)
            Text("Banked").frame(width: AccountTrayRowView.creditsWidth, alignment: .trailing)
                .help("Banked resets: count, then the soonest expiry")
        }
        .font(.system(size: 9, weight: .medium))
        .foregroundStyle(.tertiary)
        .padding(.horizontal, 10)
        .accessibilityHidden(true)
    }

    private var rows: some View {
        VStack(spacing: Self.rowSpacing) {
            ForEach(tray.rows) { row in
                VStack(spacing: 0) {
                    AccountTrayRowView(
                        row: row,
                        expanded: expandedID == row.id,
                        toggle: { toggle(row.id) }
                    )
                    // The list is the Tab stop; rows stay buttons for VoiceOver.
                    .focusable(false)
                    if expandedID == row.id {
                        AccountTrayDetailView(
                            row: row,
                            pausing: pausing.contains(row.id),
                            setPaused: { setPaused(row.id, $0) }
                        )
                    }
                }
                .background(
                    Color.primary.opacity(expandedID == row.id ? 0.075 : 0.045),
                    in: RoundedRectangle(cornerRadius: 9)
                )
                .overlay {
                    // Keyboard position only; a mouse click draws no ring.
                    RoundedRectangle(cornerRadius: 9)
                        .stroke(listFocused && highlightedID == row.id ? Color.accentColor.opacity(0.8) : .clear, lineWidth: 1.5)
                }
                .id(row.id)
            }
        }
    }

    @ViewBuilder private var list: some View {
        let scrolls = tray.rows.count > AccountTray.visibleRows
        ScrollViewReader { proxy in
            Group {
                if scrolls {
                    // Beyond five accounts the rows scroll in a frame sized
                    // for five, so the sections below do not move as accounts
                    // are added. An open row's details are added to the frame
                    // so they are never squeezed into it.
                    ScrollView { rows }
                        .frame(height: frameHeight)
                        .accessibilityLabel("\(tray.rows.count) accounts, scrollable")
                } else {
                    rows
                }
            }
            .focusable()
            .focused($listFocused)
            .focusEffectDisabledIfAvailable()
            .onMoveCommand { direction in
                switch direction {
                case .up: move(-1, proxy: proxy, scrolls: scrolls)
                case .down: move(1, proxy: proxy, scrolls: scrolls)
                default: break
                }
            }
            // Escape collapses an open row; with none open it is not handled
            // here, so it still closes the popover.
            .onExitCommand(perform: expandedID == nil ? nil : { expandedID = nil })
            .onKeyPressIfAvailable { toggle(highlightedID ?? tray.rows.first?.id) }
            .background {
                // macOS 13 has no onKeyPress: Return reaches the list through
                // a hidden default-style shortcut while the list has focus.
                if #unavailable(macOS 14.0), listFocused {
                    Button("") { toggle(highlightedID ?? tray.rows.first?.id) }
                        .keyboardShortcut(.return, modifiers: [])
                        .focusable(false)
                        .frame(width: 0, height: 0)
                        .opacity(0)
                        .accessibilityHidden(true)
                }
            }
            .onChangeCompat(of: expandedID) { id in
                // Only the inner list scrolls. With five or fewer rows there is
                // no inner scroll, and scrolling would move the whole popover.
                guard scrolls, let id else { return }
                withAnimation(.easeOut(duration: 0.15)) { proxy.scrollTo(id, anchor: .top) }
            }
        }
    }

    /// Five collapsed rows, plus the open row's details when one is open.
    private var frameHeight: CGFloat {
        let five = CGFloat(AccountTray.visibleRows) * (Self.rowHeight + Self.rowSpacing)
        guard let row = tray.expandedRow(expandedID) else { return five }
        return five + AccountTrayDetailView.estimatedHeight(row)
    }

    /// Clicks and keys share one position: the toggled row becomes the
    /// highlighted one, so the next arrow moves from where the user is.
    private func toggle(_ id: String?) {
        guard let id else { return }
        expandedID = expandedID == id ? nil : id
        highlightedID = id
    }

    private func move(_ offset: Int, proxy: ScrollViewProxy, scrolls: Bool) {
        guard let target = tray.neighbour(of: highlightedID ?? expandedID, offset: offset) else { return }
        highlightedID = target
        if expandedID != nil { expandedID = target }
        // No anchor: scrolls (the inner list, or the popover with five or
        // fewer rows) only as far as needed to bring the row into view.
        withAnimation(.easeOut(duration: 0.1)) { proxy.scrollTo(target) }
    }
}

private extension View {
    /// The list's own highlight replaces the system focus ring.
    @ViewBuilder func focusEffectDisabledIfAvailable() -> some View {
        if #available(macOS 14.0, *) { self.focusEffectDisabled() } else { self }
    }

    /// Return and Space toggle the highlighted row on macOS 14+ (on 13 a
    /// hidden Return shortcut does it).
    @ViewBuilder func onKeyPressIfAvailable(_ action: @escaping () -> Void) -> some View {
        if #available(macOS 14.0, *) {
            self.onKeyPress(keys: [.return, .space]) { _ in
                action()
                return .handled
            }
        } else {
            self
        }
    }

    /// onChange without the macOS 14 deprecation warning.
    @ViewBuilder func onChangeCompat<V: Equatable>(of value: V, perform action: @escaping (V) -> Void) -> some View {
        if #available(macOS 14.0, *) {
            self.onChange(of: value) { _, new in action(new) }
        } else {
            self.onChange(of: value, perform: action)
        }
    }
}

struct AccountTrayRowView: View {
    let row: AccountTrayRow
    let expanded: Bool
    let toggle: () -> Void

    static let windowWidth: CGFloat = 62
    static let creditsWidth: CGFloat = 58

    var body: some View {
        Button(action: toggle) {
            HStack(alignment: .center, spacing: 8) {
                VStack(alignment: .leading, spacing: 3) {
                    Text(row.title)
                        .font(.system(size: 12, weight: .semibold))
                        .lineLimit(1)
                        .truncationMode(.tail)
                    HStack(spacing: 5) {
                        if let plan = row.plan {
                            Text(plan)
                                .font(.system(size: 9, weight: .medium, design: .monospaced))
                                .foregroundStyle(.secondary)
                                .padding(.horizontal, 4).padding(.vertical, 1)
                                .background(Color.primary.opacity(0.08), in: RoundedRectangle(cornerRadius: 4))
                                .fixedSize()
                        }
                        Circle().fill(stateColor).frame(width: 6, height: 6)
                        Text(row.compactState)
                            .font(.system(size: 10))
                            .foregroundStyle(row.status == .ok ? Color.secondary : stateColor)
                            .lineLimit(1)
                            .layoutPriority(1)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                windowCell(row.session)
                windowCell(row.weekly)
                creditsCell
                Image(systemName: expanded ? "chevron.up" : "chevron.down")
                    .font(.system(size: 9, weight: .semibold))
                    .foregroundStyle(.tertiary)
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 7)
            .frame(minHeight: AccountTrayList.rowHeight)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(row.spoken)
        .accessibilityValue(expanded ? "Expanded" : "Collapsed")
        .accessibilityHint(expanded ? "Hides details" : "Shows details")
        .accessibilityAddTraits(.isButton)
        .help(expanded ? "Hide details" : "Show details for \(row.title)")
    }

    /// Count on the first line, the soonest expiry on the second.
    private var creditsCell: some View {
        let parts = (row.credits ?? "—").components(separatedBy: " · ")
        return VStack(alignment: .trailing, spacing: 2) {
            Text(parts[0])
                .font(.system(size: 11, weight: .semibold, design: .rounded))
                .monospacedDigit()
                .foregroundStyle(row.status == .ok && row.credits != nil ? .primary : .secondary)
                .lineLimit(1)
                .minimumScaleFactor(0.8)
            Text(parts.count > 1 ? parts[1] : " ")
                .font(.system(size: 9))
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .minimumScaleFactor(0.75)
        }
        .frame(width: Self.creditsWidth, alignment: .trailing)
    }

    private func windowCell(_ window: AccountTrayWindow) -> some View {
        VStack(alignment: .trailing, spacing: 2) {
            Text(window.value)
                .font(.system(size: 12, weight: .semibold, design: .rounded))
                .monospacedDigit()
                .foregroundStyle(window.outOfDate || window.percent == nil ? Color.secondary : percentColor(window.percent))
                .lineLimit(1)
                .minimumScaleFactor(0.8)
            Text(window.reset.isEmpty ? " " : window.reset)
                .font(.system(size: 9))
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .minimumScaleFactor(0.75)
        }
        .frame(width: Self.windowWidth, alignment: .trailing)
    }

    private var stateColor: Color {
        switch row.status {
        case .ok: return row.paused ? .secondary : row.dispatching ? .green : .secondary
        case .stale, .noData: return .orange
        case .signedOut, .error: return .red
        }
    }

    private func percentColor(_ percent: Int?) -> Color {
        guard let percent else { return .secondary }
        if percent <= 20 { return .red }
        if percent <= 40 { return .orange }
        return .primary
    }
}

struct AccountTrayDetailView: View {
    let row: AccountTrayRow
    let pausing: Bool
    let setPaused: (Bool) -> Void

    /// Room the details need below their row, for the fixed frame used with
    /// more than five accounts: a title, each detail's lines (about 46
    /// characters per line), the button and padding.
    static func estimatedHeight(_ row: AccountTrayRow) -> CGFloat {
        // About 46 characters fit the value column at 10pt.
        let lines = row.details.reduce(0) { $0 + max(1, Int((Double($1.value.count) / 46).rounded(.up))) }
        return 30 + CGFloat(lines) * 17 + 40
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            Divider().padding(.bottom, 3)
            // The full name, which the compact row may shorten.
            Text(row.title)
                .font(.system(size: 11, weight: .semibold))
                .fixedSize(horizontal: false, vertical: true)
            ForEach(row.details, id: \.self) { detail in
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text(detail.label)
                        .font(.system(size: 10))
                        .foregroundStyle(.secondary)
                        .frame(width: 84, alignment: .leading)
                    Text(detail.value)
                        .font(.system(size: 10, design: detail.label == "Account" ? .monospaced : .default))
                        .fixedSize(horizontal: false, vertical: true)
                        .textSelection(.enabled)
                    Spacer(minLength: 0)
                }
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("\(detail.label): \(AccountTrayRow.spoken(detail.value))")
            }
            Button {
                setPaused(!row.paused)
            } label: {
                if pausing {
                    ProgressView().controlSize(.mini)
                } else {
                    Label(row.paused ? "Resume scheduling" : "Pause scheduling",
                          systemImage: row.paused ? "play.fill" : "pause.fill")
                }
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
            .disabled(pausing)
            .padding(.top, 3)
            .accessibilityLabel("\(row.paused ? "Resume" : "Pause") scheduling for \(row.title)")
        }
        .padding(.horizontal, 10)
        .padding(.bottom, 9)
    }
}
