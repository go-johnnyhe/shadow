import SwiftUI

struct JoinInputView: View {
    @ObservedObject var viewModel: SessionViewModel
    @State private var urlInput = ""
    @State private var directoryURL: URL?
    @State private var validationError: String?

    private var normalizedURL: String {
        var s = urlInput.trimmingCharacters(in: .whitespacesAndNewlines)
        // Strip "shadow join '...'" wrapper
        if let range = s.range(of: #"^shadow\s+join\s+"#, options: [.regularExpression, .caseInsensitive]) {
            s.removeSubrange(range)
        }
        s = s.trimmingCharacters(in: .whitespacesAndNewlines)
        s = s.trimmingCharacters(in: CharacterSet(charactersIn: "'\""))
        return s.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var isValid: Bool {
        let url = normalizedURL
        guard !url.isEmpty else { return false }
        guard url.hasPrefix("http://") || url.hasPrefix("https://") else { return false }
        guard url.contains("#") else { return false }
        return directoryURL != nil
    }

    var body: some View {
        VStack(spacing: 16) {
            Text("Join Session")
                .font(.headline)

            VStack(alignment: .leading, spacing: 6) {
                Text("Paste the Shadow URL or join command:")
                    .font(.caption)
                    .foregroundStyle(.secondary)

                TextField("shadow join 'https://...'", text: $urlInput)
                    .textFieldStyle(.roundedBorder)
                    .onChange(of: urlInput) { _ in
                        validateInput()
                    }

                if let error = validationError, !urlInput.isEmpty {
                    Text(error)
                        .font(.caption2)
                        .foregroundStyle(.red)
                }
            }

            VStack(alignment: .leading, spacing: 6) {
                Text("Save files to:")
                    .font(.caption)
                    .foregroundStyle(.secondary)

                HStack {
                    Text(directoryURL?.lastPathComponent ?? "No folder selected")
                        .font(.caption)
                        .foregroundStyle(directoryURL == nil ? .tertiary : .primary)
                        .lineLimit(1)
                        .truncationMode(.middle)

                    Spacer()

                    Button("Choose...") {
                        pickDirectory()
                    }
                    .controlSize(.small)
                }
                .padding(8)
                .background(.quaternary.opacity(0.5))
                .clipShape(RoundedRectangle(cornerRadius: 6))
            }

            HStack {
                Button("Cancel") {
                    viewModel.showJoinSheet = false
                }
                .keyboardShortcut(.cancelAction)

                Spacer()

                Button("Join") {
                    guard let dir = directoryURL else { return }
                    viewModel.showJoinSheet = false
                    viewModel.joinSession(url: normalizedURL, directoryURL: dir)
                }
                .buttonStyle(.borderedProminent)
                .disabled(!isValid)
                .keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 340)
    }

    private func validateInput() {
        let url = normalizedURL
        if url.isEmpty {
            validationError = nil
        } else if !url.hasPrefix("http://") && !url.hasPrefix("https://") {
            validationError = "URL must start with http:// or https://"
        } else if !url.contains("#") {
            validationError = "URL must include an encryption key (#...)"
        } else {
            validationError = nil
        }
    }

    private func pickDirectory() {
        NSApp.activate(ignoringOtherApps: true)

        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.canCreateDirectories = true
        panel.allowsMultipleSelection = false
        panel.prompt = "Select"
        panel.message = "Choose a folder to save joined files"

        let response = panel.runModal()
        if response == .OK, let url = panel.url {
            directoryURL = url
        }
    }
}
