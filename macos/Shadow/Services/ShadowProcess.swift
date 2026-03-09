import Foundation

/// Manages spawning the shadow CLI binary, reading line-delimited JSON from stdout,
/// and forwarding parsed events. Mirrors vscode-extension/src/shadowProcess.ts.
final class ShadowProcess {
    private var process: Process?
    private var stdoutPipe: Pipe?
    private var readTask: Task<Void, Never>?
    private var sawStoppedEvent = false
    private var forceKillWorkItem: DispatchWorkItem?

    var onEvent: ((ShadowEvent) -> Void)?
    var onProcessExit: ((Int32, Process.TerminationReason) -> Void)?
    var onStderr: ((String) -> Void)?

    var isRunning: Bool {
        process?.isRunning ?? false
    }

    /// Locate the bundled shadow binary inside the app's Resources.
    static var bundledBinaryURL: URL? {
        Bundle.main.resourceURL?.appendingPathComponent("shadow")
    }

    /// Start a host session.
    func start(directoryPath: String, readOnlyJoiners: Bool) {
        var args = ["start", ".", "--json"]
        if readOnlyJoiners {
            args.append("--read-only-joiners")
        }
        spawn(args: args, cwd: directoryPath)
    }

    /// Join an existing session.
    func join(url: String, directoryPath: String) {
        spawn(args: ["join", url, "--json"], cwd: directoryPath)
    }

    /// Send SIGINT for graceful shutdown; force SIGKILL after 5s.
    func stop() {
        guard let proc = process, proc.isRunning else { return }
        proc.interrupt() // SIGINT

        let workItem = DispatchWorkItem { [weak self] in
            guard let self, let p = self.process, p.isRunning else { return }
            p.terminate() // SIGKILL
        }
        forceKillWorkItem = workItem
        DispatchQueue.global().asyncAfter(deadline: .now() + 5, execute: workItem)
    }

    /// Force kill immediately (used on app quit).
    func forceKill() {
        forceKillWorkItem?.cancel()
        readTask?.cancel()
        if let proc = process, proc.isRunning {
            proc.terminate()
        }
        process = nil
    }

    // MARK: - Private

    private func spawn(args: [String], cwd: String) {
        // Kill any existing process
        if let existing = process, existing.isRunning {
            existing.terminate()
        }
        forceKillWorkItem?.cancel()
        readTask?.cancel()
        sawStoppedEvent = false

        guard let binaryURL = Self.bundledBinaryURL else {
            let errorEvent = ShadowEvent(
                event: ShadowEventName.error,
                message: "Shadow binary not found in app bundle. Please reinstall.",
                joinUrl: nil, joinCommand: nil, fileCount: nil, relPath: nil,
                timestamp: ISO8601DateFormatter().string(from: Date())
            )
            onEvent?(errorEvent)
            return
        }

        let proc = Process()
        proc.executableURL = binaryURL
        proc.arguments = args
        proc.currentDirectoryURL = URL(fileURLWithPath: cwd)
        proc.environment = ProcessInfo.processInfo.environment.merging(["NO_COLOR": "1"]) { _, new in new }

        let stdout = Pipe()
        let stderr = Pipe()
        proc.standardOutput = stdout
        proc.standardError = stderr
        proc.standardInput = FileHandle.nullDevice

        self.stdoutPipe = stdout
        self.process = proc

        // Handle stderr
        stderr.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            if !data.isEmpty, let text = String(data: data, encoding: .utf8) {
                self?.onStderr?(text)
            }
        }

        // Handle process termination
        proc.terminationHandler = { [weak self] finished in
            guard let self else { return }
            self.forceKillWorkItem?.cancel()
            stderr.fileHandleForReading.readabilityHandler = nil
            DispatchQueue.main.async {
                self.onProcessExit?(finished.terminationStatus, finished.terminationReason)
                self.process = nil
            }
        }

        do {
            try proc.run()
        } catch {
            let errorEvent = ShadowEvent(
                event: ShadowEventName.error,
                message: "Failed to launch shadow: \(error.localizedDescription)",
                joinUrl: nil, joinCommand: nil, fileCount: nil, relPath: nil,
                timestamp: ISO8601DateFormatter().string(from: Date())
            )
            onEvent?(errorEvent)
            return
        }

        // Read stdout lines asynchronously using AsyncBytes
        let fileHandle = stdout.fileHandleForReading
        readTask = Task { [weak self] in
            let decoder = JSONDecoder()
            do {
                for try await line in fileHandle.bytes.lines {
                    if Task.isCancelled { break }
                    guard let self else { break }
                    let trimmed = line.trimmingCharacters(in: .whitespacesAndNewlines)
                    if trimmed.isEmpty { continue }
                    guard let data = trimmed.data(using: .utf8) else { continue }
                    if let event = try? decoder.decode(ShadowEvent.self, from: data) {
                        if event.event == ShadowEventName.stopped {
                            self.sawStoppedEvent = true
                        }
                        await MainActor.run {
                            self.onEvent?(event)
                        }
                    }
                }
            } catch {
                // Stream ended (process exited) or read error — expected on termination
            }
        }
    }
}
