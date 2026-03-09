import SwiftUI

struct IdleView: View {
    @ObservedObject var viewModel: SessionViewModel
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

            // Footer
            Text("End-to-end encrypted. The relay never sees your code.")
                .font(.caption2)
                .foregroundStyle(.tertiary)
                .multilineTextAlignment(.center)
        }
    }
}
