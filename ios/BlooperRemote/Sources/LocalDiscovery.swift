import Foundation

/// Bonjour helps previously paired devices find a changed address. TXT records
/// are not trusted identities: Client verifies the saved certificate pin first.
@MainActor
final class LocalDiscovery: NSObject, @preconcurrency NetServiceBrowserDelegate, @preconcurrency NetServiceDelegate {
    private let browser = NetServiceBrowser()
    private var services: [NetService] = []
    private var running = false
    private var found: ((String, String) -> Void)?

    func start(found: @escaping (String, String) -> Void) {
        self.found = found
        guard !running else {
            for service in services { resolve(service) }
            return
        }
        running = true
        browser.delegate = self
        browser.searchForServices(ofType: "_blooper._tcp.", inDomain: "local.")
    }
    func stop() {
        browser.stop()
        services.forEach { $0.stop() }
        services.removeAll()
        running = false
    }
    func netServiceBrowser(_ browser: NetServiceBrowser, didFind service: NetService, moreComing: Bool) {
        services.append(service)
        service.delegate = self
        service.resolve(withTimeout: 5)
    }
    func netServiceBrowser(_ browser: NetServiceBrowser, didRemove service: NetService, moreComing: Bool) {
        services.removeAll { $0 == service }
    }
    func netServiceDidResolveAddress(_ sender: NetService) { resolve(sender) }
    private func resolve(_ service: NetService) {
        guard let data = service.txtRecordData(),
              let pinData = NetService.dictionary(fromTXTRecord: data)["pin"],
              let pin = String(data: pinData, encoding: .utf8),
              let host = service.hostName, service.port > 0 else { return }
        var url = URLComponents()
        url.scheme = "https"
        url.host = host.hasSuffix(".") ? String(host.dropLast()) : host
        url.port = service.port
        if let address = url.url?.absoluteString { found?(pin, address) }
    }
}
