// The coordinator API client: wire models, the device key, and request signing.
// Mirrors internal/remote/protocol.go. All times are unix seconds.
import CryptoKit
import Foundation
import LocalAuthentication
import Observation
import Security

struct Device: Codable, Identifiable, Hashable {
    let autoApproveRoutine: Bool?
    let id: String
    let name: String
    let kind: String  // phone, desktop, cloud
    let os: String
    let tools: [String]?
    let workspaces: [String]?
    let apps: [String]?
    let lastSeen: Int
    let reachable: Bool
    let revoked: Bool
}

/// What a computer asks for when it wants to be enrolled.
struct Enrollment: Codable {
    let autoApproveRoutine: Bool?
    let name: String
    let kind: String
    let os: String
    let tools: [String]?
    let workspaces: [String]?
    let apps: [String]?
}

struct JobOption: Codable, Identifiable, Hashable {
    let id: String
    let name: String
}

struct JobAction: Codable, Identifiable, Hashable {
    let id: String
    let tool: String
    let summary: String
    let status: String
    let challenge: String?
    let approvalExpires: Int?
    let exitCode: Int?
    let output: String?
}

struct JobEvent: Codable, Identifiable, Hashable {
    let seq: Int
    let message: String
    let at: Int
    var id: Int { seq }
}

struct Job: Codable, Identifiable, Hashable {
    let screen: ComputerScreen?
    let id: String
    let transcript: String
    let state: String
    let detail: String
    let runnerName: String?
    let result: String?
    let question: String?
    let options: [JobOption]?
    let actions: [JobAction]?
    let events: [JobEvent]?
    let createdAt: Int

    var isTerminal: Bool {
        ["succeeded", "failed", "cancelled", "expired", "needs_reconciliation"].contains(state)
    }
    var pendingApproval: JobAction? { actions?.first { $0.status == "awaiting_approval" } }
}

struct ComputerScreen: Codable, Hashable {
    let data: String
    let width: Int
    let height: Int
}

struct APIError: LocalizedError {
    let status: Int
    let message: String
    var errorDescription: String? { message }
}

/// The phone's Ed25519 identity. The private key stays in the Keychain on this device.
enum DeviceKey {
    private static let query: [String: Any] = [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: "com.blooper.remote",
        kSecAttrAccount as String: "device-key",
    ]

    static func loadOrCreate() -> Curve25519.Signing.PrivateKey {
        var read = query
        read[kSecReturnData as String] = true
        var found: CFTypeRef?
        if SecItemCopyMatching(read as CFDictionary, &found) == errSecSuccess,
           let data = found as? Data,
           let key = try? Curve25519.Signing.PrivateKey(rawRepresentation: data) {
            return key
        }
        let key = Curve25519.Signing.PrivateKey()
        var add = query
        add[kSecValueData as String] = key.rawRepresentation
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        SecItemDelete(query as CFDictionary)
        SecItemAdd(add as CFDictionary, nil)
        return key
    }
}

struct ProviderConnection: Codable {
    let provider: String
    let model: String
    let connected: Bool
    let deepgramConnected: Bool
}

@MainActor
@Observable
final class Client {
    private(set) var server: String? = ComputerTrust.load()?.server ?? UserDefaults.standard.string(forKey: "server")
    private var pin: String? = ComputerTrust.load()?.pin
    private(set) var computerName = ComputerTrust.load()?.name ?? "My computer"
    private(set) var computers = ComputerTrust.all()
    private(set) var connected = false
    private(set) var providerConnection: ProviderConnection?
    var isPaired: Bool { server != nil }

    private let key = DeviceKey.loadOrCreate()
    private let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return d
    }()

    private var deviceID: String {
        SHA256.hash(data: key.publicKey.rawRepresentation).prefix(8).map { String(format: "%02x", $0) }.joined()
    }

    /// Sends a request signed over method, URI, timestamp, and body hash.
    /// `uri` must already be percent-encoded: the server verifies it byte for byte.
    private func send(_ method: String, _ uri: String, body: Data = Data(), contentType: String = "application/json", to base: String? = nil, certificatePin: String? = nil) async throws -> Data {
        guard let resolvedBase = base ?? server, let url = URL(string: resolvedBase + uri) else {
            throw APIError(status: 0, message: "This phone is not paired with a server.")
        }
        let timestamp = String(Int(Date().timeIntervalSince1970))
        let bodyHash = SHA256.hash(data: body).map { String(format: "%02x", $0) }.joined()
        let signature = try key.signature(for: Data("\(method)\n\(uri)\n\(timestamp)\n\(bodyHash)".utf8))

        var request = URLRequest(url: url)
        request.httpMethod = method
        if method != "GET" { request.httpBody = body }
        request.setValue(contentType, forHTTPHeaderField: "Content-Type")
        request.setValue(deviceID, forHTTPHeaderField: "X-Blooper-Device")
        request.setValue(timestamp, forHTTPHeaderField: "X-Blooper-Time")
        request.setValue(signature.base64EncodedString(), forHTTPHeaderField: "X-Blooper-Sig")

        let delegate = LocalTLSDelegate(pin: base == nil ? pin : certificatePin)
        let session = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
        defer { session.finishTasksAndInvalidate() }
        let data: Data
        let response: URLResponse
        do { (data, response) = try await session.data(for: request) }
        catch {
            if base == nil, server == resolvedBase { connected = false }
            throw error
        }
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        if base == nil, server == resolvedBase { connected = status != 401 && status != 403 }
        guard (200..<300).contains(status) else {
            let message = (try? JSONDecoder().decode([String: String].self, from: data))?["error"]
            throw APIError(status: status, message: message ?? "The server answered with status \(status).")
        }
        return data
    }

    private func json(_ value: [String: Any]) -> Data {
        (try? JSONSerialization.data(withJSONObject: value)) ?? Data()
    }

    /// Codes and IDs go into URL paths, so keep only characters that need no escaping.
    private func safe(_ pathPart: String) -> String {
        pathPart.uppercased().filter { $0.isASCII && ($0.isLetter || $0.isNumber) }
    }

    // MARK: Pairing and devices

    func pair(server: String, code: String, name: String, pin: String? = nil, computerName: String? = nil) async throws {
        var base = server.trimmingCharacters(in: .whitespacesAndNewlines)
        while base.hasSuffix("/") { base.removeLast() }
        guard let url = URL(string: base), ["http", "https"].contains(url.scheme ?? ""), url.host != nil,
              url.user == nil, url.password == nil, url.query == nil, url.fragment == nil,
              url.path.isEmpty || url.path == "/" else {
            throw APIError(status: 0, message: "Enter the computer’s HTTP or HTTPS address.")
        }
        if let pin {
            guard url.scheme == "https", pin.count == 64, pin.allSatisfy({ $0.isHexDigit && $0.isASCII }) else {
                throw APIError(status: 0, message: "That pairing code has an invalid computer identity. Create a new code on your computer.")
            }
        }
        let body = json([
            "code": safe(code),
            "name": name,
            "public_key": key.publicKey.rawRepresentation.base64EncodedString(),
        ])
        _ = try await send("POST", "/v1/pair", body: body, to: base, certificatePin: pin?.lowercased())
        try ComputerTrust.save(server: base, pin: pin?.lowercased(), name: computerName ?? URL(string: base)?.host ?? "My computer")
        UserDefaults.standard.removeObject(forKey: "server")
        self.pin = pin?.lowercased()
        self.server = base
        self.computerName = ComputerTrust.load()?.name ?? "My computer"
        computers = ComputerTrust.all()
        connected = true
    }

    /// Forgets the server on this phone. Revoke the phone from another device to cut it off server-side.
    func unpair() async throws {
        try? await unregisterPush()
        if let server { try ComputerTrust.remove(server: server) }
        UserDefaults.standard.removeObject(forKey: "server")
        server = ComputerTrust.load()?.server
        pin = ComputerTrust.load()?.pin
        computerName = ComputerTrust.load()?.name ?? "My computer"
        computers = ComputerTrust.all()
        connected = false
        providerConnection = nil
    }

    func selectComputer(_ computer: PairedComputer) throws {
        try ComputerTrust.select(computer)
        server = computer.server
        pin = computer.pin
        computerName = computer.name
        connected = false
        providerConnection = nil
    }

    /// Discovery is only an address hint. Verify the saved QR identity over TLS
    /// and the existing signed device enrollment before persisting a new URL.
    func rediscover(pin discoveredPin: String, server candidate: String) async {
        guard pin == discoveredPin, candidate != server, !connected else { return }
        do {
            _ = try await send("POST", "/v1/heartbeat", to: candidate, certificatePin: discoveredPin)
            guard pin == discoveredPin else { return }
            try ComputerTrust.save(server: candidate, pin: discoveredPin, name: computerName)
            server = candidate
            computers = ComputerTrust.all()
            connected = true
        } catch { /* Ignore unverified or unreachable discovery hints. */ }
    }

    /// Run while the app is active. A disconnected or revoked phone never stays green.
    func heartbeat() async {
        let origin = server
        do {
            _ = try await send("POST", "/v1/heartbeat")
            let connection = try decoder.decode(ProviderConnection.self, from: await send("GET", "/v1/providers"))
            if server == origin { providerConnection = connection }
        } catch { if server == origin { connected = false } }
    }

    func devices() async throws -> [Device] {
        try decoder.decode([Device].self, from: await send("GET", "/v1/devices"))
    }

    func revoke(_ device: Device) async throws -> [Device] {
        try decoder.decode([Device].self, from: await send("DELETE", "/v1/devices/\(device.id)"))
    }

    func enrollment(code: String) async throws -> Enrollment {
        try decoder.decode(Enrollment.self, from: await send("GET", "/v1/enroll/\(safe(code))"))
    }

    func approveEnrollment(code: String) async throws {
        _ = try await send("POST", "/v1/enroll/\(safe(code))/approve")
    }

    /// Job IDs are local routing metadata, not credentials. Preserve the source
    /// computer so an alert from a self-hosted server opens the correct profile.
    private func rememberJobs(_ jobs: [Job], ownerID: String?) {
        guard let ownerID else { return }
        var owners = UserDefaults.standard.dictionary(forKey: "jobComputers") as? [String: String] ?? [:]
        for job in jobs { owners[job.id] = ownerID }
        UserDefaults.standard.set(owners, forKey: "jobComputers")
    }

    func computerForJob(_ id: String) -> PairedComputer? {
        let owners = UserDefaults.standard.dictionary(forKey: "jobComputers") as? [String: String] ?? [:]
        return computers.first { $0.id == owners[id] }
    }

    // MARK: Push notifications

    func registerPush(token: String) async throws {
        _ = try await send("POST", "/v1/push-token", body: json(["token": token]))
    }

    func unregisterPush() async throws {
        guard isPaired else { return }
        _ = try await send("DELETE", "/v1/push-token")
    }

    // MARK: Jobs

    /// Uploads recorded audio to the coordinator, which forwards it to Deepgram.
    func transcribe(file: URL) async throws -> String {
        let data = try await send("POST", "/v1/transcribe", body: Data(contentsOf: file), contentType: "audio/mp4")
        return (try JSONDecoder().decode([String: String].self, from: data))["transcript"] ?? ""
    }

    /// `requestID` makes a retried submission map to the same job.
    func submit(transcript: String, target: String, requestID: String) async throws -> Job {
        let ownerID = ComputerTrust.load()?.id
        let body = json(["transcript": transcript, "target": target, "client_request_id": requestID])
        let job = try decoder.decode(Job.self, from: await send("POST", "/v1/jobs", body: body))
        rememberJobs([job], ownerID: ownerID)
        return job
    }

    func jobs() async throws -> [Job] {
        let ownerID = ComputerTrust.load()?.id
        let jobs = try decoder.decode([Job].self, from: await send("GET", "/v1/jobs"))
        rememberJobs(jobs, ownerID: ownerID)
        return jobs
    }

    /// With `after`, the server holds the request until the job has an event past that sequence number.
    func job(_ id: String, after: Int? = nil) async throws -> Job {
        let query = after.map { "?after=\($0)" } ?? ""
        return try decoder.decode(Job.self, from: await send("GET", "/v1/jobs/\(id)\(query)"))
    }

    func clarify(_ job: Job, answer: String) async throws {
        _ = try await send("POST", "/v1/jobs/\(job.id)/clarify", body: json(["answer": answer]))
    }

    func decide(_ job: Job, action: JobAction, approve: Bool) async throws {
        let body = json(["challenge": action.challenge ?? "", "approve": approve])
        _ = try await send("POST", "/v1/jobs/\(job.id)/approval", body: body)
    }

    func cancel(_ job: Job) async throws {
        _ = try await send("POST", "/v1/jobs/\(job.id)/cancel")
    }
}

/// Face ID, Touch ID, or the passcode. Used before approving an action or enrolling a computer.
/// A phone with no passcode has nothing to verify against, so it passes.
func confirmOwner(_ reason: String) async -> Bool {
    let context = LAContext()
    guard context.canEvaluatePolicy(.deviceOwnerAuthentication, error: nil) else { return true }
    return (try? await context.evaluatePolicy(.deviceOwnerAuthentication, localizedReason: reason)) ?? false
}
