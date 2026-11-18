import SwiftUI

/// Isolated UI-test host: never reads a user's paired computers or credentials.
@main
struct ScannerHarness: App {
    @State private var scanning = false
    @State private var received = 0

    var body: some Scene {
        WindowGroup {
            VStack {
                Button("Open scanner") { scanning = true }
                Text("Codes received: \(received)")
            }
            .sheet(isPresented: $scanning) {
                ScannerSheet { _ in received += 1 }
            }
        }
    }
}
