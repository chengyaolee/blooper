// iOS-only pieces: the QR scanner, the audio session, and text-field traits.
import AVFoundation
import SwiftUI
import UIKit
import VisionKit

enum Platform {
    static var deviceName: String { UIDevice.current.name }

    static func configureAudioSession() throws {
        let session = AVAudioSession.sharedInstance()
        try session.setCategory(.playAndRecord, mode: .default, options: [.defaultToSpeaker])
        try session.setActive(true)
    }
}

extension View {
    /// For URLs and codes: no autocapitalization or autocorrect.
    func plainInput() -> some View {
        textInputAutocapitalization(.never).autocorrectionDisabled().keyboardType(.asciiCapable)
    }
}

/// A scanner sheet always has an explicit way out, including after a camera error.
struct ScannerSheet: View {
    @Environment(\.dismiss) private var dismiss
    @State private var problem: String?
    @State private var attempt = 0
    let onCode: (String) -> Void

    var body: some View {
        NavigationStack {
            Group {
                if !ScannerView.isSupported {
                    ContentUnavailableView(
                        "QR scanning unavailable",
                        systemImage: "qrcode.viewfinder",
                        description: Text("Use a supported iPhone, or cancel and enter the connection details manually.")
                    )
                } else if let problem {
                    ContentUnavailableView {
                        Label("Camera unavailable", systemImage: "camera")
                    } description: {
                        Text(problem)
                    } actions: {
                        Button("Try again") { self.problem = nil; attempt += 1 }
                        if AVCaptureDevice.authorizationStatus(for: .video) == .denied,
                           let settings = URL(string: UIApplication.openSettingsURLString) {
                            Link("Open Settings", destination: settings)
                        }
                    }
                } else {
                    ScannerView(onError: { problem = $0 }, onCode: { payload in
                        dismiss()
                        onCode(payload)
                    })
                    .id(attempt)
                }
            }
            .safeAreaInset(edge: .bottom) {
                Text("Keep the whole QR code in view. Pinch to zoom if needed.")
                    .font(.footnote)
                    .frame(maxWidth: .infinity)
                    .padding()
                    .background(.regularMaterial)
            }
            .navigationTitle("Scan QR code")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
    }
}

/// SwiftUI owns presentation; the controller starts recognition only after the
/// camera view appears and permission is granted, and stops when it disappears.
struct ScannerView: UIViewControllerRepresentable {
    let onError: (String) -> Void
    let onCode: (String) -> Void

    static var isSupported: Bool { DataScannerViewController.isSupported }

    func makeUIViewController(context: Context) -> ScannerController {
        ScannerController(onError: onError, onCode: onCode)
    }

    func updateUIViewController(_ controller: ScannerController, context: Context) {}

    static func dismantleUIViewController(_ controller: ScannerController, coordinator: ()) {
        controller.stop()
    }
}

final class ScannerController: UIViewController, DataScannerViewControllerDelegate {
    private let scanner = DataScannerViewController(
        recognizedDataTypes: [.barcode(symbologies: [.qr])],
        qualityLevel: .accurate,
        recognizesMultipleItems: false,
        isPinchToZoomEnabled: true,
        isGuidanceEnabled: true,
        isHighlightingEnabled: true
    )
    private let onError: (String) -> Void
    private let onCode: (String) -> Void
    private var startup: Task<Void, Never>?
    private var active = false
    private var delivered = false

    init(onError: @escaping (String) -> Void, onCode: @escaping (String) -> Void) {
        self.onError = onError
        self.onCode = onCode
        super.init(nibName: nil, bundle: nil)
    }

    required init?(coder: NSCoder) { fatalError("Use init(onError:onCode:)") }

    override func viewDidLoad() {
        super.viewDidLoad()
        scanner.delegate = self
        addChild(scanner)
        scanner.view.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(scanner.view)
        NSLayoutConstraint.activate([
            scanner.view.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            scanner.view.trailingAnchor.constraint(equalTo: view.trailingAnchor),
            scanner.view.topAnchor.constraint(equalTo: view.topAnchor),
            scanner.view.bottomAnchor.constraint(equalTo: view.bottomAnchor),
        ])
        scanner.didMove(toParent: self)
    }

    override func viewDidAppear(_ animated: Bool) {
        super.viewDidAppear(animated)
        guard !delivered else { return }
        active = true
        startup = Task { @MainActor [weak self] in
            guard let self else { return }
            let allowed: Bool
            switch AVCaptureDevice.authorizationStatus(for: .video) {
            case .authorized: allowed = true
            case .notDetermined: allowed = await AVCaptureDevice.requestAccess(for: .video)
            default: allowed = false
            }
            // Permission may finish after the user has pressed Cancel.
            guard self.active, !Task.isCancelled else { return }
            guard allowed else {
                self.fail("Allow camera access in Settings to scan a pairing code.")
                return
            }
            guard DataScannerViewController.isSupported, DataScannerViewController.isAvailable else {
                self.fail("QR scanning is unavailable on this device right now. Try again or enter the code manually.")
                return
            }
            do { try self.scanner.startScanning() }
            catch { self.fail("Could not start QR scanning: \(error.localizedDescription)") }
        }
    }

    override func viewWillDisappear(_ animated: Bool) {
        super.viewWillDisappear(animated)
        stop()
    }

    func stop() {
        active = false
        startup?.cancel()
        startup = nil
        scanner.stopScanning()
    }

    private func fail(_ message: String) {
        guard active, !delivered else { return }
        stop()
        onError(message)
    }

    private func recognize(_ items: [RecognizedItem]) {
        guard active, !delivered else { return }
        for item in items {
            if case .barcode(let barcode) = item,
               let payload = barcode.payloadStringValue, !payload.isEmpty {
                delivered = true
                stop()
                onCode(payload)
                return
            }
        }
    }

    func dataScanner(_ scanner: DataScannerViewController, didAdd addedItems: [RecognizedItem], allItems: [RecognizedItem]) {
        recognize(allItems)
    }

    func dataScanner(_ scanner: DataScannerViewController, didUpdate updatedItems: [RecognizedItem], allItems: [RecognizedItem]) {
        recognize(allItems)
    }

    func dataScanner(_ scanner: DataScannerViewController, didTapOn item: RecognizedItem) {
        recognize([item])
    }

    func dataScanner(_ scanner: DataScannerViewController, becameUnavailableWithError error: DataScannerViewController.ScanningUnavailable) {
        fail("QR scanning stopped: \(error.localizedDescription)")
    }
}
