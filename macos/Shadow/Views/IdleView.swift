import SwiftUI

struct IdleView: View {
    @ObservedObject var viewModel: SessionViewModel
    @ObservedObject var updateService: UpdateService
    @State private var readOnlyJoiners = false

    var body: some View {
        VStack(spacing: 16) {
            // Hero
            VStack(spacing: 8) {
                Image(systemName: "circle.righthalf.filled")
                    .font(.system(size: 32, weight: .medium))
                    .foregroundStyle(.primary)

                Text("Shadow")
                    .font(.headline)

                Text("Encrypted real-time collaboration.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .padding(.bottom, 4)

            // Actions
            VStack(spacing: 8) {
                Button {
                    viewModel.pickDirectory { url in
                        viewModel.startSession(directoryURL: url, readOnlyJoiners: readOnlyJoiners)
                    }
                } label: {
                    Text("Start Session")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)

                Button {
                    viewModel.showJoinSheet = true
                } label: {
                    Text("Join Session")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
                .controlSize(.large)
            }

            // Read-only toggle
            Toggle("Joiners are read-only", isOn: $readOnlyJoiners)
                .font(.caption)
                .toggleStyle(.checkbox)

            Divider()

            // Update banner
            updateBanner

            // Footer
            Text("End-to-end encrypted. The relay never sees your code.")
                .font(.caption2)
                .foregroundStyle(.tertiary)
                .multilineTextAlignment(.center)
        }
    }

    @ViewBuilder
    private var updateBanner: some View {
        switch updateService.state {
        case .available(let version):
            HStack(spacing: 6) {
                Image(systemName: "arrow.down.circle")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Text("v\(version) available")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Spacer()
                Button("Update") {
                    updateService.performUpdate()
                }
                .font(.caption2)
                .buttonStyle(.borderless)
            }
        case .downloading:
            HStack(spacing: 6) {
                ProgressView()
                    .controlSize(.mini)
                Text("Updating…")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Spacer()
            }
        case .success(let version):
            HStack(spacing: 6) {
                Image(systemName: "checkmark.circle.fill")
                    .font(.caption2)
                    .foregroundStyle(.green)
                Text("Updated to v\(version)")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Spacer()
            }
        case .failed(let message):
            HStack(spacing: 6) {
                Image(systemName: "exclamationmark.triangle")
                    .font(.caption2)
                    .foregroundStyle(.orange)
                Text("Update failed")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .help(message)
                Spacer()
                Button("Retry") {
                    updateService.checkForUpdate()
                }
                .font(.caption2)
                .buttonStyle(.borderless)
            }
        default:
            EmptyView()
        }
    }
}
