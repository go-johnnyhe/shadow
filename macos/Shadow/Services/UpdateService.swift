import Foundation

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

        let archiveURLString = "https://github.com/go-johnnyhe/shadow/releases/download/v\(version)/shadow_\(version)_darwin_\(Self.arch).tar.gz"
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

        // Atomic replace: remove old, move new
        let backupURL = binaryURL.appendingPathExtension("bak")
        try? FileManager.default.removeItem(at: backupURL)

        do {
            // Move current binary to backup
            try FileManager.default.moveItem(at: binaryURL, to: backupURL)
            // Move new binary into place
            try FileManager.default.moveItem(at: extractedBinary, to: binaryURL)
            // Clean up backup
            try? FileManager.default.removeItem(at: backupURL)
        } catch {
            // Restore backup if replacement failed
            try? FileManager.default.moveItem(at: backupURL, to: binaryURL)
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
    case extractFailed
    case binaryNotInArchive
    case permissionDenied
    case replaceFailed(String)

    var errorDescription: String? {
        switch self {
        case .apiError: return "Could not reach GitHub"
        case .binaryNotFound: return "Shadow binary not found in bundle"
        case .invalidURL: return "Invalid download URL"
        case .downloadFailed: return "Download failed"
        case .extractFailed: return "Failed to extract archive"
        case .binaryNotInArchive: return "Binary not found in archive"
        case .permissionDenied: return "Permission denied — try moving Shadow.app to Applications"
        case .replaceFailed(let msg): return "Replace failed: \(msg)"
        }
    }
}
