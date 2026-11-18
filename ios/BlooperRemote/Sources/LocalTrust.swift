import CryptoKit
import Foundation
import Security

/// A QR pin authenticates the computer's exact TLS certificate. It is never
/// accepted as a reason to trust a different certificate or redirect destination.
final class LocalTLSDelegate: NSObject, URLSessionDelegate, URLSessionTaskDelegate {
    let pin: String?
    init(pin: String?) { self.pin = pin }

    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard let pin else { completionHandler(.performDefaultHandling, nil); return }
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust,
              let certificates = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let certificate = certificates.first else {
            completionHandler(.cancelAuthenticationChallenge, nil)
            return
        }
        let data = SecCertificateCopyData(certificate) as Data
        let actual = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        guard actual == pin else {
            completionHandler(.cancelAuthenticationChallenge, nil)
            return
        }
        // The scanned pin binds this exact identity, including when a LAN IP
        // changes. Basic X.509 validation retains certificate validity checks.
        SecTrustSetPolicies(trust, SecPolicyCreateBasicX509())
        SecTrustSetAnchorCertificates(trust, [certificate] as CFArray)
        SecTrustSetAnchorCertificatesOnly(trust, true)
        guard SecTrustEvaluateWithError(trust, nil) else {
            completionHandler(.cancelAuthenticationChallenge, nil)
            return
        }
        completionHandler(.useCredential, URLCredential(trust: trust))
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil) // Signed requests and identity trust never follow redirects.
    }
}

struct PairedComputer: Codable, Identifiable, Hashable {
    let server: String
    let pin: String?
    let name: String
    var id: String { pin ?? server }
}

enum ComputerTrust {
    private struct State: Codable {
        var computers: [PairedComputer]
        var selectedID: String
    }
    private static let query: [String: Any] = [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: "com.blooper.remote",
        kSecAttrAccount as String: "computer-trust",
    ]
    private static func state() -> State {
        var read = query
        read[kSecReturnData as String] = true
        var found: CFTypeRef?
        guard SecItemCopyMatching(read as CFDictionary, &found) == errSecSuccess, let data = found as? Data else { return State(computers: [], selectedID: "") }
        if let state = try? JSONDecoder().decode(State.self, from: data) { return state }
        // Preserve a computer paired by the first desktop release.
        if let old = try? JSONDecoder().decode([String: String].self, from: data), let server = old["server"] {
            let computer = PairedComputer(server: server, pin: old["pin"], name: URL(string: server)?.host ?? "My computer")
            return State(computers: [computer], selectedID: computer.id)
        }
        return State(computers: [], selectedID: "")
    }
    static func all() -> [PairedComputer] { state().computers }
    static func load() -> PairedComputer? {
        let value = state()
        return value.computers.first { $0.id == value.selectedID } ?? value.computers.first
    }
    static func save(server: String, pin: String?, name: String) throws {
        var value = state()
        let computer = PairedComputer(server: server, pin: pin, name: name)
        value.computers.removeAll { $0.id == computer.id }
        value.computers.append(computer)
        value.selectedID = computer.id
        try persist(value)
    }
    static func select(_ computer: PairedComputer) throws {
        var value = state()
        guard value.computers.contains(computer) else { return }
        value.selectedID = computer.id
        try persist(value)
    }
    static func remove(server: String) throws {
        var value = state()
        value.computers.removeAll { $0.server == server }
        if !value.computers.contains(where: { $0.id == value.selectedID }) { value.selectedID = value.computers.first?.id ?? "" }
        try persist(value)
    }
    private static func persist(_ value: State) throws {
        let data = try JSONEncoder().encode(value)
        let status = SecItemUpdate(query as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        if status == errSecSuccess { return }
        guard status == errSecItemNotFound else { throw APIError(status: 0, message: "Could not update your paired computers in Keychain.") }
        var add = query
        add[kSecValueData as String] = data
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        guard SecItemAdd(add as CFDictionary, nil) == errSecSuccess else {
            throw APIError(status: 0, message: "Could not save this computer’s trusted identity in Keychain.")
        }
    }
}
