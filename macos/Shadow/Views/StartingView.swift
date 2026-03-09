import SwiftUI

struct StartingView: View {
    let label: String
    @State private var pulsing = false

    var body: some View {
        VStack(spacing: 16) {
            HStack(spacing: 10) {
                Circle()
                    .fill(.yellow)
                    .frame(width: 8, height: 8)
                    .opacity(pulsing ? 0.3 : 1.0)
                    .animation(.easeInOut(duration: 1.0).repeatForever(autoreverses: true), value: pulsing)

                Text(label)
                    .font(.headline)
            }
            .padding(.vertical, 10)
            .padding(.horizontal, 12)
            .background(.quaternary.opacity(0.5))
            .clipShape(RoundedRectangle(cornerRadius: 8))

            if label.contains("Starting") {
                Text("Setting up your encrypted tunnel.\nThis usually takes a few seconds.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }
        }
        .onAppear { pulsing = true }
    }
}
