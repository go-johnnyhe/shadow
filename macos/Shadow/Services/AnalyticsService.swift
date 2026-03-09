import Foundation
import PostHog

/// Lightweight PostHog wrapper — manual events only, no autocapture.
enum AnalyticsService {
    enum ErrorPhase: String {
        case start
        case join
        case runtime
        case shutdown
    }

    enum ErrorType: String {
        case processLaunchFailed = "process_launch_failed"
        case processExitedUnexpectedly = "process_exited_unexpectedly"
        case tunnelFailed = "tunnel_failed"
        case connectionFailed = "connection_failed"
        case snapshotFailed = "snapshot_failed"
        case binaryMissing = "binary_missing"
        case invalidInput = "invalid_input"
        case permissionDenied = "permission_denied"
        case unknown = "unknown"
    }

    static func setup() {
        let config = PostHogConfig(apiKey: Secrets.posthogApiKey)

        // Disable everything invasive — manual events only
        config.captureScreenViews = false
        config.captureApplicationLifecycleEvents = false
        config.preloadFeatureFlags = false
        config.sendFeatureFlagEvent = false

        PostHogSDK.shared.setup(config)
    }

    // MARK: - Session Events

    static func sessionStartRequested(readOnly: Bool) {
        PostHogSDK.shared.capture("session_start_requested", properties: [
            "read_only": readOnly,
        ])
    }

    static func sessionJoinRequested() {
        PostHogSDK.shared.capture("session_join_requested")
    }

    static func sessionStarted(mode: String, readOnly: Bool) {
        PostHogSDK.shared.capture("session_started", properties: [
            "mode": mode,
            "read_only": readOnly,
        ])
    }

    static func sessionJoined() {
        PostHogSDK.shared.capture("session_joined")
    }

    static func sessionStopped(mode: String, durationSeconds: Int) {
        PostHogSDK.shared.capture("session_stopped", properties: [
            "mode": mode,
            "duration_seconds": durationSeconds,
        ])
    }

    static func sessionError(mode: String, errorType: ErrorType, phase: ErrorPhase?) {
        var properties: [String: Any] = [
            "mode": mode,
            "error_type": errorType.rawValue,
        ]
        if let phase {
            properties["phase"] = phase.rawValue
        }
        PostHogSDK.shared.capture("session_error", properties: properties)
    }

    // MARK: - Key Actions

    static func inviteCopied() {
        PostHogSDK.shared.capture("invite_copied")
    }

    static func appLaunched() {
        PostHogSDK.shared.capture("app_launched")
    }

    static func classifyError(_ message: String) -> ErrorType {
        let normalized = message.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()

        if normalized.contains("binary not found") || normalized.contains("reinstall") {
            return .binaryMissing
        }
        if normalized.contains("failed to launch shadow") || normalized.contains("process error") {
            return .processLaunchFailed
        }
        if normalized.contains("process exited unexpectedly") {
            return .processExitedUnexpectedly
        }
        if normalized.contains("failed to create tunnel") || normalized.contains("tunnel") {
            return .tunnelFailed
        }
        if normalized.contains("error making connection")
            || normalized.contains("connecting to session")
            || normalized.contains("local connection failed")
            || normalized.contains("connection lost") {
            return .connectionFailed
        }
        if normalized.contains("snapshot failed") {
            return .snapshotFailed
        }
        if normalized.contains("invalid")
            || normalized.contains("expected exactly one session url")
            || normalized.contains("missing e2e key")
            || normalized.contains("session url is required") {
            return .invalidInput
        }
        if normalized.contains("permission") || normalized.contains("not permitted") {
            return .permissionDenied
        }
        return .unknown
    }
}
