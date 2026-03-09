import AppKit
import SwiftUI

/// Single source of truth for the session state machine.
/// Mirrors vscode-extension/src/shadowProcess.ts state transitions.
@MainActor
final class SessionViewModel: ObservableObject {
    @Published var state: SessionState = .idle
    @Published var session = SessionInfo(mode: .host)
    @Published var showJoinSheet = false

    /// Callback for AppDelegate to update the menu bar icon.
    var onStateChange: ((SessionState) -> Void)?

    private let shadowProcess = ShadowProcess()
    private var lastAction: LastAction?
    private var activeSessionStartedAt: Date?
    private var didBecomeActive = false
    private var didTrackStopForCurrentSession = false

    private enum LastAction {
        case start(path: String, readOnly: Bool)
        case join(url: String, path: String)
    }

    init() {
        shadowProcess.onEvent = { [weak self] event in
            Task { @MainActor in
                self?.handleEvent(event)
            }
        }

        shadowProcess.onProcessExit = { [weak self] code, reason in
            Task { @MainActor in
                self?.handleProcessExit(code: code, reason: reason)
            }
        }

        NotificationService.requestPermission()
    }

    // MARK: - Actions

    func startSession(directoryURL: URL, readOnlyJoiners: Bool) {
        guard state == .idle || state == .error else { return }
        let path = directoryURL.path
        session = SessionInfo(mode: .host, directoryPath: path, hostReadOnly: readOnlyJoiners)
        lastAction = .start(path: path, readOnly: readOnlyJoiners)
        resetAnalyticsStateForNewAttempt()
        setState(.starting)
        AnalyticsService.sessionStartRequested(readOnly: readOnlyJoiners)
        shadowProcess.start(directoryPath: path, readOnlyJoiners: readOnlyJoiners)
    }

    func joinSession(url: String, directoryURL: URL) {
        guard state == .idle || state == .error else { return }
        let path = directoryURL.path
        session = SessionInfo(mode: .joiner, joinUrl: url, directoryPath: path)
        lastAction = .join(url: url, path: path)
        resetAnalyticsStateForNewAttempt()
        setState(.starting)
        AnalyticsService.sessionJoinRequested()
        shadowProcess.join(url: url, directoryPath: path)
    }

    func stopSession() {
        guard state != .idle && state != .stopping else { return }
        setState(.stopping)
        shadowProcess.stop()
    }

    func retryLastAction() {
        guard let action = lastAction else { return }
        switch action {
        case .start(let path, let readOnly):
            startSession(directoryURL: URL(fileURLWithPath: path), readOnlyJoiners: readOnly)
        case .join(let url, let path):
            joinSession(url: url, directoryURL: URL(fileURLWithPath: path))
        }
    }

    func copyInvite() {
        if let text = session.joinCommand ?? session.joinUrl {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(text, forType: .string)
            AnalyticsService.inviteCopied()
        }
    }

    /// Called from right-click menu "Start Session" — opens folder picker then starts.
    func startSessionFromMenu() {
        pickDirectory { [weak self] url in
            self?.startSession(directoryURL: url, readOnlyJoiners: false)
        }
    }

    func pickDirectory(completion: @escaping (URL) -> Void) {
        // NSOpenPanel must be run modally for menu bar apps — .begin() doesn't
        // work from inside an NSPopover (the transient popover steals focus).
        // Activate the app first so the panel appears in front.
        NSApp.activate(ignoringOtherApps: true)

        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.canCreateDirectories = true
        panel.allowsMultipleSelection = false
        panel.prompt = "Share"
        panel.message = "Choose a directory to share via Shadow"

        let response = panel.runModal()
        if response == .OK, let url = panel.url {
            completion(url)
        }
    }

    // MARK: - Event Handling

    /// State machine matching vscode-extension/src/shadowProcess.ts:174-222.
    private func handleEvent(_ event: ShadowEvent) {
        switch event.event {
        case ShadowEventName.starting:
            // Already in .starting from spawn
            break

        case ShadowEventName.tunnelReady:
            session.joinUrl = event.joinUrl
            session.joinCommand = event.joinCommand
            markSessionActive()
            AnalyticsService.sessionStarted(mode: session.mode.rawValue, readOnly: session.hostReadOnly)
            setState(.runningHost)
            // Auto-copy to clipboard
            if let cmd = event.joinCommand {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(cmd, forType: .string)
            }
            NotificationService.notifyTunnelReady(joinCommand: event.joinCommand)

        case ShadowEventName.connected:
            markSessionActive()
            AnalyticsService.sessionJoined()
            setState(.runningJoiner)

        case ShadowEventName.snapshotComplete:
            session.fileCount = event.fileCount

        case ShadowEventName.readOnly:
            session.readOnly = true

        case ShadowEventName.fileSent, ShadowEventName.fileReceived:
            if let relPath = event.relPath {
                session.recentFiles.append(relPath)
                if session.recentFiles.count > 5 {
                    session.recentFiles.removeFirst(session.recentFiles.count - 5)
                }
            }

        case ShadowEventName.error:
            session.lastError = event.message
            AnalyticsService.sessionError(
                mode: session.mode.rawValue,
                errorType: AnalyticsService.classifyError(event.message),
                phase: errorPhaseForCurrentState()
            )
            setState(.error)

        case ShadowEventName.warning:
            // Log but don't change state
            break

        case ShadowEventName.stopped:
            if state != .stopping {
                setState(.stopping)
            }

        default:
            break
        }
    }

    private func handleProcessExit(code: Int32, reason: Process.TerminationReason) {
        if state == .stopping {
            trackSessionStoppedIfNeeded()
            resetToIdle()
            NotificationService.notifyStopped()
        } else if state != .idle && state != .error {
            let message = "Process exited unexpectedly (code \(code))"
            session.lastError = message
            AnalyticsService.sessionError(
                mode: session.mode.rawValue,
                errorType: .processExitedUnexpectedly,
                phase: errorPhaseForCurrentState()
            )
            setState(.error)
        }
    }

    private func resetToIdle() {
        setState(.idle)
        session = SessionInfo(mode: .host)
        activeSessionStartedAt = nil
        didBecomeActive = false
        didTrackStopForCurrentSession = false
    }

    private func setState(_ newState: SessionState) {
        guard state != newState else { return }
        state = newState
        onStateChange?(newState)
    }

    private func markSessionActive() {
        if !didBecomeActive {
            activeSessionStartedAt = Date()
            didBecomeActive = true
            didTrackStopForCurrentSession = false
        }
    }

    private func trackSessionStoppedIfNeeded() {
        guard didBecomeActive, !didTrackStopForCurrentSession else { return }
        let duration = max(0, Int(Date().timeIntervalSince(activeSessionStartedAt ?? Date())))
        AnalyticsService.sessionStopped(mode: session.mode.rawValue, durationSeconds: duration)
        didTrackStopForCurrentSession = true
    }

    private func resetAnalyticsStateForNewAttempt() {
        activeSessionStartedAt = nil
        didBecomeActive = false
        didTrackStopForCurrentSession = false
    }

    private func errorPhaseForCurrentState() -> AnalyticsService.ErrorPhase {
        switch state {
        case .starting:
            return session.mode == .host ? .start : .join
        case .stopping:
            return .shutdown
        case .runningHost, .runningJoiner:
            return .runtime
        case .idle, .error:
            return didBecomeActive ? .runtime : (session.mode == .host ? .start : .join)
        }
    }
}
