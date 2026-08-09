import Foundation
import CryptoKit

/// Manages checking for and applying updates to the bundled shadow binary.
@MainActor
final class UpdateService: ObservableObject {
    enum State: Equatable {
        case idle
        case checking
        case upToDate(String)
        case available(String)
        case downloading
        case success(String)
        case failed(String)
    }

    @Published private(set) var state: State = .idle

    private var checkTask: Task<Void, Never>?
    private var updateTask: Task<Void, Never>?
    private var dismissTask: Task<Void, Never>?

    private static let releaseURL = "https://api.github.com/repos/go-johnnyhe/shadow/releases/latest"

    #if arch(arm64)
    private static let arch = "arm64"
    #else
    private static let arch = "amd64"
    #endif

    // MARK: - Public

    func checkForUpdate() {
        checkTask?.cancel()
        checkTask = Task {
            state = .checking
            do {
                let (current, latest) = try await fetchVersions()

                if Task.isCancelled { return }

                if current != "dev" && current == latest {
                    state = .upToDate(latest)
                    autoDismiss(after: 3)
                } else {
                    state = .available(latest)
                }
            } catch {
                if Task.isCancelled { return }
                // Silent failure — don't bother user if check fails
                state = .idle
            }
        }
    }

    func performUpdate() {
        guard case .available(let version) = state else { return }
        updateTask?.cancel()
        updateTask = Task {
            state = .downloading
            do {
                try await downloadAndReplace(version: version)
                if Task.isCancelled { return }
                state = .success(version)
                autoDismiss(after: 4)
            } catch {
                if Task.isCancelled { return }
                state = .failed(error.localizedDescription)
                autoDismiss(after: 6)
            }
        }
    }

    // MARK: - Private

    private func fetchVersions() async throws -> (current: String, latest: String) {
        let current = try currentVersion()
        let latest = try await latestVersion()
        return (current, latest)
    }

    private func currentVersion() throws -> String {
        guard let binaryURL = ShadowProcess.bundledBinaryURL else {
            return "dev"
        }

        let proc = Process()
        proc.executableURL = binaryURL
        proc.arguments = ["--version"]
        let pipe = Pipe()
        proc.standardOutput = pipe
        proc.standardError = FileHandle.nullDevice
        proc.standardInput = FileHandle.nullDevice

        try proc.run()
        proc.waitUntilExit()

        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        let output = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""

        // Output format: "shadow version: X.Y.Z" or "shadow version: dev"
        if let range = output.range(of: "version: ") {
            let version = String(output[range.upperBound...]).trimmingCharacters(in: .whitespacesAndNewlines)
            return version.hasPrefix("v") ? String(version.dropFirst()) : version
        }
        return "dev"
    }

    private func latestVersion() async throws -> String {
        var request = URLRequest(url: URL(string: Self.releaseURL)!)
        request.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
        request.timeoutInterval = 10

        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            throw UpdateError.apiError
        }

        let release = try JSONDecoder().decode(GitHubRelease.self, from: data)
        let tag = release.tagName
        return tag.hasPrefix("v") ? String(tag.dropFirst()) : tag
    }

    private func downloadAndReplace(version: String) async throws {
        guard let binaryURL = ShadowProcess.bundledBinaryURL else {
            throw UpdateError.binaryNotFound
        }

        let archiveName = "shadow_\(version)_darwin_\(Self.arch).tar.gz"
        let releaseBaseURL = "https://github.com/go-johnnyhe/shadow/releases/download/v\(version)"
        let archiveURLString = "\(releaseBaseURL)/\(archiveName)"
        guard let archiveURL = URL(string: archiveURLString) else {
            throw UpdateError.invalidURL
        }

        // Download to temp file
        let (downloadURL, response) = try await URLSession.shared.download(for: URLRequest(url: archiveURL))
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            throw UpdateError.downloadFailed
        }

        // Extract using /usr/bin/tar to a temp directory
        let tmpDir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: tmpDir, withIntermediateDirectories: true)

        defer {
            try? FileManager.default.removeItem(at: tmpDir)
        }

        // Move downloaded .tar.gz into our tmp dir so tar can access it
        let tgzPath = tmpDir.appendingPathComponent("shadow.tar.gz")
        try FileManager.default.moveItem(at: downloadURL, to: tgzPath)

        guard let checksumsURL = URL(string: "\(releaseBaseURL)/checksums.txt") else {
            throw UpdateError.invalidURL
        }
        let (checksumsData, checksumsResponse) = try await URLSession.shared.data(from: checksumsURL)
        guard let checksumsHTTP = checksumsResponse as? HTTPURLResponse, checksumsHTTP.statusCode == 200 else {
            throw UpdateError.checksumDownloadFailed
        }
        let expectedChecksum = try checksum(for: archiveName, in: checksumsData)
        let actualChecksum = try sha256(of: tgzPath)
        guard actualChecksum == expectedChecksum.lowercased() else {
            throw UpdateError.checksumMismatch
        }

        let tar = Process()
        tar.executableURL = URL(fileURLWithPath: "/usr/bin/tar")
        tar.arguments = ["-xzf", tgzPath.path, "-C", tmpDir.path]
        tar.standardOutput = FileHandle.nullDevice
        tar.standardError = FileHandle.nullDevice
        try tar.run()
        tar.waitUntilExit()

        guard tar.terminationStatus == 0 else {
            throw UpdateError.extractFailed
        }

        // Find the extracted shadow binary
        let extractedBinary = tmpDir.appendingPathComponent("shadow")
        guard FileManager.default.fileExists(atPath: extractedBinary.path) else {
            throw UpdateError.binaryNotInArchive
        }

        // Set executable permissions
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: extractedBinary.path)

        // Stage and replace on one file system so a failed update keeps the old binary.
        let destinationDirectory = binaryURL.deletingLastPathComponent()
        let stagedBinary = destinationDirectory.appendingPathComponent(".shadow-update-\(UUID().uuidString)")
        let backupName = ".shadow-backup-\(UUID().uuidString)"
        let backupURL = destinationDirectory.appendingPathComponent(backupName)
        try FileManager.default.copyItem(at: extractedBinary, to: stagedBinary)
        defer {
            try? FileManager.default.removeItem(at: stagedBinary)
        }

        do {
            _ = try FileManager.default.replaceItemAt(
                binaryURL,
                withItemAt: stagedBinary,
                backupItemName: backupName,
                options: []
            )
            try? FileManager.default.removeItem(at: backupURL)
        } catch {
            if !FileManager.default.fileExists(atPath: binaryURL.path),
               FileManager.default.fileExists(atPath: backupURL.path) {
                do {
                    try FileManager.default.moveItem(at: backupURL, to: binaryURL)
                } catch {
                    throw UpdateError.rollbackFailed(backupURL.path)
                }
            }
            if (error as NSError).domain == NSCocoaErrorDomain &&
               (error as NSError).code == NSFileWriteNoPermissionError {
                throw UpdateError.permissionDenied
            }
            throw UpdateError.replaceFailed(error.localizedDescription)
        }
    }

    private func autoDismiss(after seconds: UInt64) {
        dismissTask?.cancel()
        dismissTask = Task {
            try? await Task.sleep(nanoseconds: seconds * 1_000_000_000)
            if !Task.isCancelled {
                state = .idle
            }
        }
    }

    private func checksum(for archiveName: String, in data: Data) throws -> String {
        guard let text = String(data: data, encoding: .utf8) else {
            throw UpdateError.checksumMissing
        }
        for line in text.split(whereSeparator: \Character.isNewline) {
            let fields = line.split(whereSeparator: \Character.isWhitespace)
            guard fields.count == 2, String(fields[1]) == archiveName else { continue }
            let checksum = String(fields[0])
            guard checksum.count == 64, checksum.allSatisfy({ $0.isHexDigit }) else {
                throw UpdateError.checksumMissing
            }
            return checksum.lowercased()
        }
        throw UpdateError.checksumMissing
    }

    private func sha256(of url: URL) throws -> String {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        var hasher = SHA256()
        while true {
            let data = try handle.read(upToCount: 1024 * 1024) ?? Data()
            if data.isEmpty { break }
            hasher.update(data: data)
        }
        return hasher.finalize().map { String(format: "%02x", $0) }.joined()
    }
}

// MARK: - Models

private struct GitHubRelease: Decodable {
    let tagName: String

    enum CodingKeys: String, CodingKey {
        case tagName = "tag_name"
    }
}

private enum UpdateError: LocalizedError {
    case apiError
    case binaryNotFound
    case invalidURL
    case downloadFailed
    case checksumDownloadFailed
    case checksumMissing
    case checksumMismatch
    case extractFailed
    case binaryNotInArchive
    case permissionDenied
    case replaceFailed(String)
    case rollbackFailed(String)

    var errorDescription: String? {
        switch self {
        case .apiError: return "Could not reach GitHub"
        case .binaryNotFound: return "Shadow binary not found in bundle"
        case .invalidURL: return "Invalid download URL"
        case .downloadFailed: return "Download failed"
        case .checksumDownloadFailed: return "Could not download release checksum"
        case .checksumMissing: return "Release checksum is missing"
        case .checksumMismatch: return "Release checksum verification failed"
        case .extractFailed: return "Failed to extract archive"
        case .binaryNotInArchive: return "Binary not found in archive"
        case .permissionDenied: return "Permission denied — try moving Shadow.app to Applications"
        case .replaceFailed(let msg): return "Replace failed: \(msg)"
        case .rollbackFailed(let path): return "Update failed. The previous binary is at \(path)"
        }
    }
}
