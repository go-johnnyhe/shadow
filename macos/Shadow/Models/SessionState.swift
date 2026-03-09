import Foundation

/// Session state machine matching vscode-extension/src/types.ts:2-9.
enum SessionState: String {
    case idle
    case starting
    case runningHost = "running_host"
    case runningJoiner = "running_joiner"
    case stopping
    case error
}
