// Recording, on-device transcription, and reading results aloud.
import AVFoundation
import Observation
import Speech

struct VoiceError: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

@MainActor
@Observable
final class Voice {
    private(set) var isRecording = false

    private var recorder: AVAudioRecorder?
    private let synthesizer = AVSpeechSynthesizer()
    private let file = FileManager.default.temporaryDirectory.appendingPathComponent("request.m4a")

    func start() async throws {
        guard await AVAudioApplication.requestRecordPermission() else {
            throw VoiceError(message: "Microphone access is off. Turn it on in Settings, or type the request.")
        }
        try Platform.configureAudioSession()
        let settings: [String: Any] = [
            AVFormatIDKey: Int(kAudioFormatMPEG4AAC),
            AVSampleRateKey: 16_000,
            AVNumberOfChannelsKey: 1,
            AVEncoderAudioQualityKey: AVAudioQuality.high.rawValue,
        ]
        let recorder = try AVAudioRecorder(url: file, settings: settings)
        guard recorder.record() else { throw VoiceError(message: "Could not start recording.") }
        self.recorder = recorder
        isRecording = true
    }

    /// Stops recording and returns the audio file. The caller deletes it once it is transcribed.
    func stop() -> URL? {
        guard let recorder else { return nil }
        recorder.stop()
        self.recorder = nil
        isRecording = false
        return file
    }

    /// Transcribes on this phone. The audio never leaves the device.
    func transcribeOnDevice(_ url: URL) async throws -> String {
        let status = await withCheckedContinuation { continuation in
            SFSpeechRecognizer.requestAuthorization { continuation.resume(returning: $0) }
        }
        guard status == .authorized else {
            throw VoiceError(message: "Speech recognition is off. Turn it on in Settings, or type the request.")
        }
        guard let recognizer = SFSpeechRecognizer(), recognizer.isAvailable, recognizer.supportsOnDeviceRecognition else {
            throw VoiceError(message: "On-device speech recognition isn't available for this language on this phone. Switch to Deepgram or type the request.")
        }
        let request = SFSpeechURLRecognitionRequest(url: url)
        request.requiresOnDeviceRecognition = true
        request.shouldReportPartialResults = false

        final class Once { var done = false }
        let once = Once()
        return try await withCheckedThrowingContinuation { continuation in
            recognizer.recognitionTask(with: request) { result, error in
                guard !once.done else { return }
                if let error {
                    once.done = true
                    continuation.resume(throwing: error)
                } else if let result, result.isFinal {
                    once.done = true
                    continuation.resume(returning: result.bestTranscription.formattedString)
                }
            }
        }
    }

    func speak(_ text: String) {
        synthesizer.speak(AVSpeechUtterance(string: text))
    }
}
