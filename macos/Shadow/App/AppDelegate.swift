import AppKit
import SwiftUI

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private var popover: NSPopover!
    private let viewModel = SessionViewModel()

    func applicationDidFinishLaunching(_ notification: Notification) {
        AnalyticsService.setup()
        AnalyticsService.appLaunched()

        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)

        if let button = statusItem.button {
            updateMenuBarIcon(state: .idle)
            button.action = #selector(togglePopover)
            button.target = self
            button.sendAction(on: [.leftMouseUp, .rightMouseUp])
        }

        let contentView = PopoverContentView(viewModel: viewModel)
        popover = NSPopover()
        popover.contentSize = NSSize(width: 300, height: 400)
        popover.behavior = .transient
        popover.contentViewController = NSHostingController(rootView: contentView)

        // Observe state changes to update menu bar icon
        viewModel.onStateChange = { [weak self] state in
            self?.updateMenuBarIcon(state: state)
        }
    }

    @objc private func togglePopover() {
        guard let button = statusItem.button else { return }

        let event = NSApp.currentEvent
        if event?.type == .rightMouseUp {
            showContextMenu(relativeTo: button)
            return
        }

        if popover.isShown {
            popover.performClose(nil)
        } else {
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
            // Ensure popover window becomes key so it gets focus
            popover.contentViewController?.view.window?.makeKey()
        }
    }

    private func showContextMenu(relativeTo button: NSStatusBarButton) {
        let menu = NSMenu()

        switch viewModel.state {
        case .idle, .error:
            menu.addItem(NSMenuItem(title: "Start Session...", action: #selector(menuStart), keyEquivalent: ""))
            menu.addItem(NSMenuItem(title: "Join Session...", action: #selector(menuJoin), keyEquivalent: ""))
        case .runningHost, .runningJoiner:
            if viewModel.session.joinCommand != nil {
                menu.addItem(NSMenuItem(title: "Copy Invite Link", action: #selector(menuCopyInvite), keyEquivalent: ""))
            }
            menu.addItem(NSMenuItem(title: "Stop Session", action: #selector(menuStop), keyEquivalent: ""))
        case .starting, .stopping:
            let item = NSMenuItem(title: "Working...", action: nil, keyEquivalent: "")
            item.isEnabled = false
            menu.addItem(item)
        }

        menu.addItem(NSMenuItem.separator())
        menu.addItem(NSMenuItem(title: "Quit Shadow", action: #selector(menuQuit), keyEquivalent: "q"))

        for item in menu.items {
            item.target = self
        }

        statusItem.menu = menu
        statusItem.button?.performClick(nil)
        statusItem.menu = nil // Reset so left-click shows popover again
    }

    @objc private func menuStart() {
        viewModel.startSessionFromMenu()
    }

    @objc private func menuJoin() {
        if popover.isShown { popover.performClose(nil) }
        if let button = statusItem.button {
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
            popover.contentViewController?.view.window?.makeKey()
        }
        viewModel.showJoinSheet = true
    }

    @objc private func menuCopyInvite() {
        viewModel.copyInvite()
    }

    @objc private func menuStop() {
        viewModel.stopSession()
    }

    @objc private func menuQuit() {
        viewModel.stopSession()
        // Give the process a moment to SIGINT
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) {
            NSApp.terminate(nil)
        }
    }

    private func updateMenuBarIcon(state: SessionState) {
        guard let button = statusItem.button else { return }

        // Use SF Symbols for the menu bar icon with state coloring
        let config = NSImage.SymbolConfiguration(pointSize: 16, weight: .medium)

        switch state {
        case .idle, .starting, .stopping:
            let image = NSImage(systemSymbolName: "circle.righthalf.filled", accessibilityDescription: "Shadow")!
                .withSymbolConfiguration(config)!
            image.isTemplate = true
            button.image = image
        case .runningHost, .runningJoiner:
            let image = NSImage(systemSymbolName: "circle.righthalf.filled", accessibilityDescription: "Shadow - Active")!
                .withSymbolConfiguration(config)!
            image.isTemplate = false
            // Tint green by drawing into a new image
            let tinted = NSImage(size: image.size, flipped: false) { rect in
                NSColor.systemGreen.set()
                image.draw(in: rect)
                NSColor.systemGreen.set()
                rect.fill(using: .sourceAtop)
                return true
            }
            button.image = tinted
        case .error:
            let image = NSImage(systemSymbolName: "circle.righthalf.filled", accessibilityDescription: "Shadow - Error")!
                .withSymbolConfiguration(config)!
            image.isTemplate = false
            let tinted = NSImage(size: image.size, flipped: false) { rect in
                NSColor.systemRed.set()
                image.draw(in: rect)
                NSColor.systemRed.set()
                rect.fill(using: .sourceAtop)
                return true
            }
            button.image = tinted
        }
    }
}
