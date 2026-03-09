import SwiftUI

/// Root popover view that switches on session state.
struct PopoverContentView: View {
    @ObservedObject var viewModel: SessionViewModel

    var body: some View {
        Group {
            switch viewModel.state {
            case .idle:
                IdleView(viewModel: viewModel)
            case .starting:
                StartingView(label: "Starting session...")
            case .stopping:
                StartingView(label: "Stopping session...")
            case .runningHost, .runningJoiner:
                ActiveSessionView(viewModel: viewModel)
            case .error:
                ErrorView(viewModel: viewModel)
            }
        }
        .frame(width: 280)
        .padding(16)
        .sheet(isPresented: $viewModel.showJoinSheet) {
            JoinInputView(viewModel: viewModel)
        }
    }
}
