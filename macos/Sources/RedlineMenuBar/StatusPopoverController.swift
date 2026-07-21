import AppKit
import SwiftUI

@MainActor
final class StatusPopoverController {
    private let popover = NSPopover()
    private let model: PopoverViewModel
    private let actions: StatusPopoverActions
    private var previewWindow: NSWindowController?

    init(model: PopoverViewModel, actions: StatusPopoverActions) {
        self.model = model
        self.actions = actions
        popover.behavior = .transient
        popover.animates = true
        popover.contentSize = NSSize(width: 420, height: 640)
        popover.contentViewController = NSHostingController(rootView: StatusPopoverView(model: model, actions: actions))
    }

    func toggle(relativeTo button: NSStatusBarButton) {
        if popover.isShown {
            popover.performClose(nil)
        } else {
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
            Task { await model.refresh() }
        }
    }

    func show(relativeTo button: NSStatusBarButton) {
        guard !popover.isShown else { return }
        popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
        Task { await model.refresh() }
    }

    func showPreviewWindow() {
        if previewWindow == nil {
            let window = NSWindow(
                contentRect: NSRect(x: 0, y: 0, width: 420, height: 640),
                styleMask: [.titled, .closable],
                backing: .buffered,
                defer: false
            )
            window.title = "Redline Quick Panel Preview"
            window.contentViewController = NSHostingController(rootView: StatusPopoverView(model: model, actions: actions))
            window.center()
            previewWindow = NSWindowController(window: window)
        }
        previewWindow?.showWindow(nil)
        previewWindow?.window?.makeKeyAndOrderFront(nil)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }
}
