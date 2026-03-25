import SwiftUI

struct ActiveSessionView: View {
    @ObservedObject var viewModel: SessionViewModel

    private var session: SessionInfo { viewModel.session }
    private var isHost: Bool { session.mode == .host }

    var body: some View {
        VStack(spacing: 14) {
            // Status header
            HStack(spacing: 10) {
                Circle()
                    .fill(.green)
                    .frame(width: 8, height: 8)

                Text(isHost ? "Sharing" : "Joined")
                    .font(.headline)

                Spacer()
            }
            .padding(.vertical, 10)
            .padding(.horizontal, 12)
            .background(.quaternary.opacity(0.5))
            .clipShape(RoundedRectangle(cornerRadius: 8))

            // Info rows
            VStack(spacing: 0) {
                if let dirName = session.directoryName {
                    infoRow("Directory", dirName)
                }
                infoRow("Role", isHost ? "Host" : "Joiner")
                if isHost {
                    infoRow("Permissions", session.hostReadOnly ? "Joiners read-only" : "Everyone can edit")
                }
                if !isHost && session.readOnly {
                    infoRow("Access", "Read-only")
                }
                infoRow("Files synced", "\((session.fileCount ?? 0) + session.liveSyncCount)")
                if let lastSync = session.lastSyncTime {
                    infoRow("Last sync", lastSync.relativeDescription)
                }
                infoRow("Security", "E2E encrypted")
            }

            // Recent activity
            if !session.recentFiles.isEmpty {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Recent activity")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    ForEach(session.recentFiles.suffix(5), id: \.self) { file in
                        HStack(spacing: 4) {
                            Image(systemName: "arrow.left.arrow.right")
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                            Text(file)
                                .font(.caption2)
                                .lineLimit(1)
                                .truncationMode(.middle)
                        }
                    }
                }
                .padding(.vertical, 4)
            }

            // Open in editor
            if let dir = session.directoryPath {
                let editors = EditorService.availableEditors()
                if !editors.isEmpty {
                    Menu {
                        ForEach(editors) { editor in
                            Button(editor.name) {
                                EditorService.open(editor, directory: dir)
                            }
                        }
                    } label: {
                        Label("Open In…", systemImage: "square.and.arrow.up.on.square")
                            .frame(maxWidth: .infinity)
                    }
                    .menuStyle(.borderedButton)
                    .controlSize(.large)
                }
            }

            // Copy invite (host only)
            if isHost, session.joinCommand != nil || session.joinUrl != nil {
                Button {
                    viewModel.copyInvite()
                } label: {
                    Label("Copy Invite Link", systemImage: "doc.on.doc")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
            }

            // Stop button
            Button(role: .destructive) {
                viewModel.stopSession()
            } label: {
                Text(isHost ? "Stop Session" : "Leave Session")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.bordered)
            .controlSize(.large)
        }
    }

    private func infoRow(_ key: String, _ value: String) -> some View {
        HStack {
            Text(key)
                .foregroundStyle(.secondary)
            Spacer()
            Text(value)
        }
        .font(.caption)
        .padding(.vertical, 5)
        .overlay(alignment: .bottom) {
            Divider()
        }
    }
}

private extension Date {
    var relativeDescription: String {
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .abbreviated
        return formatter.localizedString(for: self, relativeTo: Date())
    }
}
