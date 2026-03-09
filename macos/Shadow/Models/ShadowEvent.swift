import Foundation

/// Codable mirror of Go's JSONEvent struct (cmd/jsonevents.go:24-32).
struct ShadowEvent: Codable {
    let event: String
    let message: String
    let joinUrl: String?
    let joinCommand: String?
    let fileCount: Int?
    let relPath: String?
    let timestamp: String

    enum CodingKeys: String, CodingKey {
        case event, message, timestamp
        case joinUrl = "join_url"
        case joinCommand = "join_command"
        case fileCount = "file_count"
        case relPath = "rel_path"
    }
}

/// Event name constants matching Go's jsonevents.go:10-21.
enum ShadowEventName {
    static let starting = "starting"
    static let tunnelReady = "tunnel_ready"
    static let connected = "connected"
    static let snapshotComplete = "snapshot_complete"
    static let stopped = "stopped"
    static let fileSent = "file_sent"
    static let fileReceived = "file_received"
    static let readOnly = "read_only"
    static let warning = "warning"
    static let error = "error"
}
