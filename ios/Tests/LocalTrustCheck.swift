import CryptoKit
import Foundation

@main
struct LocalTrustCheck {
    static func main() async throws {
        let url = URL(string: CommandLine.arguments[1])!
        let certificate = try Data(contentsOf: URL(fileURLWithPath: CommandLine.arguments[2]))
        let pin = SHA256.hash(data: certificate).map { String(format: "%02x", $0) }.joined()
        for (candidate, shouldConnect) in [(pin, true), (String(repeating: "0", count: 64), false)] {
            let session = URLSession(configuration: .ephemeral, delegate: LocalTLSDelegate(pin: candidate), delegateQueue: nil)
            do {
                let (_, response) = try await session.data(from: url)
                guard shouldConnect, (response as? HTTPURLResponse)?.statusCode == 200 else {
                    fatalError("A mismatched QR certificate was accepted")
                }
            } catch {
                guard !shouldConnect else { throw error }
            }
            session.finishTasksAndInvalidate()
        }
        print("PASS: scanned certificate accepted; mismatched certificate rejected")
    }
}
