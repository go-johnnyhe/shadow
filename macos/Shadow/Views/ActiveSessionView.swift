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
                if let count = session.fileCount {
                    infoRow("Files synced", "\(count)")
                }
                infoRow("Security", "E2E encrypted")
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
