import Foundation

/// Mutable session metadata matching vscode-extension/src/types.ts:23-32.
struct SessionInfo {
    var mode: SessionMode
    var joinUrl: String? = nil
    var joinCommand: String? = nil
    var directoryPath: String? = nil
    var fileCount: Int? = nil
    var readOnly: Bool = false
    var hostReadOnly: Bool = false
    var lastError: String? = nil
    var recentFiles: [String] = []

    var directoryName: String? {
        guard let path = directoryPath else { return nil }
        return (path as NSString).lastPathComponent
    }

    enum SessionMode: String {
        case host
        case joiner
    }
}
