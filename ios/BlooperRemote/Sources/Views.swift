import SwiftUI

@main
struct BlooperRemoteApp: App {
    @State private var client = Client()
    @State private var discovery = LocalDiscovery()
    @UIApplicationDelegateAdaptor(PushNotifications.self) private var notifications
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            Group {
                if client.isPaired { HomeView() } else { PairView() }
            }
            .environment(client)
            .environmentObject(notifications)
            .task(id: "\(client.server ?? ""):\(notifications.token ?? "")") {
                await notifications.sync(client: client)
            }
            .task(id: "\(client.server ?? ""):\(scenePhase)") {
                guard client.isPaired, scenePhase == .active else { return }
                while !Task.isCancelled {
                    await client.heartbeat()
                    discovery.start { pin, server in Task { await client.rediscover(pin: pin, server: server) } }
                    do { try await Task.sleep(for: .seconds(5)) } catch { return }
                }
            }
            .onChange(of: scenePhase) {
                if scenePhase != .active { discovery.stop() }
                if scenePhase == .active {
                    notifications.refreshRegistration()
                    Task { await notifications.sync(client: client) }
                }
            }
        }
    }
}

/// Runs `work` and puts its error, if any, into `problem`.
@MainActor
func attempt(_ problem: Binding<String?>, _ work: () async throws -> Void) async {
    do {
        try await work()
        problem.wrappedValue = nil
    } catch {
        problem.wrappedValue = error.localizedDescription
    }
}

// MARK: - Pairing

struct PairView: View {
    @Environment(Client.self) private var client
    @Environment(\.dismiss) private var dismiss
    @State private var server = ""
    @State private var code = ""
    @State private var scanning = false
    @State private var busy = false
    @State private var problem: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Text("Open Blooper on your Mac or Windows computer. Choose Devices, create a pairing code, then scan it here. Keep both devices on the same Wi-Fi.")
                    Button {
                        scanning = true
                    } label: {
                        Label("Scan pairing code", systemImage: "qrcode.viewfinder")
                    }
                    .disabled(!ScannerView.isAvailable)
                }
                Section {
 DisclosureGroup("Advanced: self-hosted connection") {
                    TextField("Server address", text: $server).plainInput()
                    TextField("Pairing code", text: $code).plainInput()
                    Button("Pair") { pair(server: server, code: code) }
                        .disabled(server.isEmpty || code.isEmpty || busy)
                    Text("Use a server with a trusted HTTPS certificate. Scanning the desktop app’s QR also verifies its local certificate.").font(.footnote).foregroundStyle(.secondary)
                }
                }
                if let problem {
                    Section { Text(problem).foregroundStyle(.red) }
                }
            }
            .navigationTitle("Blooper Remote")
            .sheet(isPresented: $scanning) {
                ScannerView { payload in
                    scanning = false
                    let items = URLComponents(string: payload)?.queryItems
                    guard let qr = URLComponents(string: payload), qr.scheme == "blooper", qr.host == "pair",
                          let url = items?.first(where: { $0.name == "url" })?.value,
                          let code = items?.first(where: { $0.name == "code" })?.value
                    else {
                        problem = "That isn't a Blooper pairing code."
                        return
                    }
                    guard let pin = items?.first(where: { $0.name == "pin" })?.value else {
                        problem = "This code has no computer identity. Use Advanced for a self-hosted server, or create a new code in Blooper Desktop."
                        return
                    }
                    pair(server: url, code: code, pin: pin, computerName: items?.first(where: { $0.name == "name" })?.value)
                }
            }
        }
    }

    private func pair(server: String, code: String, pin: String? = nil, computerName: String? = nil) {
        busy = true
        Task {
            await attempt($problem) {
                try await client.pair(server: server, code: code, name: Platform.deviceName, pin: pin, computerName: computerName)
                dismiss()
            }
            busy = false
        }
    }
}

// MARK: - Home: ask, and see jobs

struct HomeView: View {
    @EnvironmentObject private var notifications: PushNotifications
    @Environment(Client.self) private var client
    @State private var voice = Voice()
    @State private var text = ""
    @State private var target = ""  // "" lets the coordinator choose; "cloud"; or a device ID
    @State private var requestID = UUID().uuidString
    @State private var jobs: [Job] = []
    @State private var devices: [Device] = []
    @State private var path: [String] = []
    @State private var busy = false
    @State private var problem: String?
    @AppStorage("useDeepgram") private var useDeepgram = false

    var body: some View {
        NavigationStack(path: $path) {
            List {
                Section {
                    TextField("What should your computer do?", text: $text, axis: .vertical)
                        .lineLimit(1...5)
                    Picker("Run on", selection: $target) {
                        Text("Auto").tag("")
                        ForEach(devices.filter { $0.kind != "phone" && !$0.revoked }) { device in
                            Label(device.name, systemImage: device.reachable ? "desktopcomputer" : "desktopcomputer.trianglebadge.exclamationmark").tag(device.id)
                        }
                        Text("Cloud").tag("cloud")
                    }
                    HStack {
                        Button {
                            toggleRecording()
                        } label: {
                            Label(voice.isRecording ? "Stop" : "Speak", systemImage: voice.isRecording ? "stop.circle.fill" : "mic.circle.fill")
                        }
                        .buttonStyle(.bordered)
                        .tint(voice.isRecording ? .red : .accentColor)
                        Spacer()
                        Button("Send") { send() }
                            .buttonStyle(.borderedProminent)
                            .disabled(text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || busy)
                    }
                    Toggle("Transcribe with Deepgram", isOn: $useDeepgram)
                    Text(useDeepgram
                        ? "Audio goes to your Blooper server, which forwards it to Deepgram. It is not stored."
                        : "Audio is transcribed on this phone and never leaves it.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                    if let problem {
                        Text(problem).foregroundStyle(.red)
                    }
                }
                Section(client.computerName) {
                    Label(client.connected ? "Connected" : "Offline", systemImage: client.connected ? "desktopcomputer" : "desktopcomputer.trianglebadge.exclamationmark")
                        .foregroundStyle(client.connected ? .green : .secondary)
                        .accessibilityLabel(client.connected ? "Computer connected" : "Computer offline")
                    if let provider = client.providerConnection {
                        LabeledContent("Model", value: provider.connected ? "\(provider.provider.capitalized) · Connected" : "Not connected")
                        LabeledContent("Deepgram", value: provider.deepgramConnected ? "Connected" : "Not connected")
                    }
                    Text("Manage API keys on your computer. Keys are never sent to this phone.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
                Section("Notifications") {
                    Button(notifications.enabled ? "Disable notifications" : "Enable notifications") {
                        Task {
                            if notifications.enabled { await notifications.disable(client: client) }
                            else { await notifications.enable() }
                        }
                    }
                    Text("Get alerts when a job finishes or needs your input.")
                        .font(.footnote).foregroundStyle(.secondary)
                    if let problem = notifications.problem {
                        Text(problem).foregroundStyle(.red)
                        if notifications.enabled {
                            Button("Retry registration") { Task { await notifications.sync(client: client) } }
                        }
                    }
                }
                Section("Jobs") {
                    if jobs.isEmpty {
                        Text("No jobs yet").foregroundStyle(.secondary)
                    }
                    ForEach(jobs) { job in
                        NavigationLink(value: job.id) {
                            VStack(alignment: .leading, spacing: 4) {
                                Text(job.transcript).lineLimit(2)
                                HStack {
                                    StateBadge(state: job.state)
                                    Text(job.runnerName ?? "").font(.caption).foregroundStyle(.secondary)
                                }
                            }
                        }
                    }
                }
            }
            .navigationTitle("Blooper")
            .navigationDestination(for: String.self) { JobView(jobID: $0) }
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    NavigationLink { ComputersView() } label: {
                        Label("Computers", systemImage: client.connected ? "desktopcomputer" : "wifi.slash")
                            .foregroundStyle(client.connected ? .green : .secondary)
                    }
                }
                ToolbarItem(placement: .primaryAction) {
                    NavigationLink {
                        DevicesView()
                    } label: {
                        Label("Devices", systemImage: "desktopcomputer")
                    }
                }
            }
            .refreshable { await load() }
            .task(id: client.server) {
                jobs = []
                devices = []
                path = []
                await load()
                openNotification()
            }
            .onChange(of: notifications.selectedJobID) { openNotification() }
            // A changed request is a new request. An unchanged one that is resent keeps its ID,
            // so the server returns the job it already recorded.
            .onChange(of: text) { requestID = UUID().uuidString }
            .onChange(of: path) { if path.isEmpty { Task { await load() } } }
        }
    }

    private func openNotification() {
        guard let id = notifications.selectedJobID else { return }
        if let owner = client.computerForJob(id), owner.server != client.server {
            do { try client.selectComputer(owner) }
            catch { problem = error.localizedDescription; notifications.selectedJobID = nil }
            return // The new computer's task opens it after loading that profile.
        }
        path = [id]
        notifications.selectedJobID = nil
    }

    private func load() async {
        await attempt($problem) {
            jobs = try await client.jobs()
            devices = try await client.devices()
        }
    }

    private func toggleRecording() {
        Task {
            await attempt($problem) {
                if !voice.isRecording {
                    try await voice.start()
                    return
                }
                guard let file = voice.stop() else { return }
                defer { try? FileManager.default.removeItem(at: file) }  // raw audio is dropped once transcribed
                busy = true
                defer { busy = false }
                if useDeepgram {
                    text = try await client.transcribe(file: file)
                } else {
                    text = try await voice.transcribeOnDevice(file)
                }
            }
        }
    }

    private func send() {
        busy = true
        Task {
            await attempt($problem) {
                let origin = client.server
                let job = try await client.submit(transcript: text, target: target, requestID: requestID)
                if client.server == origin {
                    text = ""
                    path.append(job.id)
                }
            }
            busy = false
        }
    }
}

struct StateBadge: View {
    let state: String

    private var color: Color {
        switch state {
        case "succeeded": .green
        case "running", "queued", "accepted": .blue
        case "failed", "expired", "needs_reconciliation": .red
        case "cancelled": .gray
        default: .orange  // waiting for the owner, the target, or a stop
        }
    }

    var body: some View {
        Text(state.replacingOccurrences(of: "_", with: " "))
            .font(.caption.weight(.medium))
            .padding(.horizontal, 8)
            .padding(.vertical, 2)
            .background(color.opacity(0.15), in: Capsule())
            .foregroundStyle(color)
    }
}

// MARK: - One job: status, approval, result

struct JobView: View {
    let jobID: String
    @Environment(Client.self) private var client
    @State private var voice = Voice()
    @State private var job: Job?
    @State private var problem: String?

    var body: some View {
        List {
            if let job {
                Section {
                    HStack {
                        StateBadge(state: job.state)
                        if let runner = job.runnerName { Text(runner).foregroundStyle(.secondary) }
                    }
                    Text(job.detail)
                    Text("“\(job.transcript)”").foregroundStyle(.secondary)
                }
                if let action = job.pendingApproval {
                    Section("Needs your approval") {
                        Text(action.summary).font(.headline)
                        if let expires = action.approvalExpires {
                            Text("Expires \(Date(timeIntervalSince1970: TimeInterval(expires)), style: .relative)")
                                .font(.footnote)
                                .foregroundStyle(.secondary)
                        }
                        Button("Approve") { decide(job, action, approve: true) }
                            .buttonStyle(.borderedProminent)
                        Button("Reject", role: .destructive) { decide(job, action, approve: false) }
                    }
                }
                if let question = job.question, let options = job.options, job.state == "waiting_for_clarification" {
                    Section(question) {
                        ForEach(options) { option in
                            Button(option.name) {
                                Task { await attempt($problem) { try await client.clarify(job, answer: option.id) } }
                            }
                        }
                    }
                }
                if let result = job.result, !result.isEmpty {
                    Section("Result") {
                        Text(result).textSelection(.enabled)
                        Button {
                            voice.speak(result)
                        } label: {
                            Label("Read aloud", systemImage: "speaker.wave.2")
                        }
                    }
                }
                if let actions = job.actions, !actions.isEmpty {
                    Section("Actions") {
                        ForEach(actions) { action in
                            DisclosureGroup {
                                Text(action.output ?? "No output")
                                    .font(.system(.footnote, design: .monospaced))
                                    .textSelection(.enabled)
                            } label: {
                                VStack(alignment: .leading) {
                                    Text(action.summary)
                                    Text(action.exitCode.map { "\(action.status), exit code \($0)" } ?? action.status)
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                            }
                        }
                    }
                }
                if let events = job.events {
                    Section("Timeline") {
                        ForEach(events) { event in
                            HStack(alignment: .firstTextBaseline) {
                                Text(Date(timeIntervalSince1970: TimeInterval(event.at)), style: .time)
                                    .font(.caption.monospacedDigit())
                                    .foregroundStyle(.secondary)
                                Text(event.message).font(.footnote)
                            }
                        }
                    }
                }
                if !job.isTerminal {
                    Section {
                        Button("Stop", role: .destructive) {
                            Task { await attempt($problem) { try await client.cancel(job) } }
                        }
                        .disabled(job.state == "cancel_requested")
                    }
                }
            } else if problem == nil {
                ProgressView()
            }
            if let problem {
                Section { Text(problem).foregroundStyle(.red) }
            }
        }
        .navigationTitle("Job")
        .task(id: jobID) { await follow() }
    }

    /// Follows the job's event feed. Reopening the view starts over from the full job,
    /// so nothing that happened while the app was closed is missed.
    private func follow() async {
        var seen: Int?
        while !Task.isCancelled {
            do {
                let latest = try await client.job(jobID, after: seen)
                job = latest
                problem = nil
                seen = latest.events?.count ?? 0
                if latest.isTerminal { return }
            } catch is CancellationError {
                return
            } catch {
                if Task.isCancelled { return }
                problem = error.localizedDescription
                try? await Task.sleep(for: .seconds(3))
            }
        }
    }

    private func decide(_ job: Job, _ action: JobAction, approve: Bool) {
        Task {
            if approve, !(await confirmOwner("Approve: \(action.summary)")) { return }
            await attempt($problem) { try await client.decide(job, action: action, approve: approve) }
        }
    }
}

// MARK: - Devices: list, revoke, enroll a computer

struct DevicesView: View {
    @Environment(Client.self) private var client
    @State private var devices: [Device] = []
    @State private var adding = false
    @State private var revoking: Device?
    @State private var problem: String?

    var body: some View {
        List {
            Section {
                ForEach(devices) { device in
                    VStack(alignment: .leading, spacing: 4) {
                        HStack {
                            Label(device.name, systemImage: device.reachable ? (device.kind == "phone" ? "iphone" : "desktopcomputer") : "wifi.slash")
                                .font(.headline).foregroundStyle(device.reachable ? .green : .secondary)
                            Spacer()
                            Text(status(device)).font(.caption).foregroundStyle(.secondary)
                        }
                        Text("\(device.kind) · \(device.os) · \(device.id)").font(.caption).foregroundStyle(.secondary)
                        if let tools = device.tools, !tools.isEmpty {
                            Text("Tools: \(tools.joined(separator: ", "))").font(.footnote)
                        }
                        if let workspaces = device.workspaces, !workspaces.isEmpty {
                            Text("Workspaces: \(workspaces.joined(separator: ", "))").font(.footnote)
                        }
                        if let apps = device.apps, !apps.isEmpty {
                            Text("Apps: \(apps.joined(separator: ", "))").font(.footnote)
                        }
                    }
                    .swipeActions {
                        if !device.revoked {
                            Button("Revoke", role: .destructive) { revoking = device }
                        }
                    }
                }
            } footer: {
                Text("Swipe a device to revoke it. A revoked runner stops its jobs and can no longer claim work.")
            }
            Section {
                Button {
                    adding = true
                } label: {
                    Label("Add computer", systemImage: "plus")
                }
                Button("Unpair this phone", role: .destructive) {
                    Task { await attempt($problem) { try await client.unpair() } }
                }
            }
            if let problem {
                Section { Text(problem).foregroundStyle(.red) }
            }
        }
        .navigationTitle("Devices")
        .task {
            while !Task.isCancelled {
                await load()
                do { try await Task.sleep(for: .seconds(5)) } catch { return }
            }
        }
        .refreshable { await load() }
        .sheet(isPresented: $adding, onDismiss: { Task { await load() } }) {
            AddComputerView()
        }
        .confirmationDialog(
            "Revoke \(revoking?.name ?? "")?",
            isPresented: Binding(get: { revoking != nil }, set: { if !$0 { revoking = nil } }),
            titleVisibility: .visible
        ) {
            Button("Revoke", role: .destructive) {
                guard let device = revoking else { return }
                Task { await attempt($problem) { devices = try await client.revoke(device) } }
            }
        }
    }

    private func status(_ device: Device) -> String {
        if device.revoked { return "revoked" }
        if device.kind == "phone" { return "phone" }
        return device.reachable ? "online" : "offline"
    }

    private func load() async {
        await attempt($problem) { devices = try await client.devices() }
    }
}

/// Scan the QR a runner shows, review what it asks for, then enroll it.
struct AddComputerView: View {
    @Environment(Client.self) private var client
    @Environment(\.dismiss) private var dismiss
    @State private var code = ""
    @State private var pending: Enrollment?
    @State private var problem: String?

    var body: some View {
        NavigationStack {
            Form {
                if let pending {
                    Section("Enroll this computer?") {
                        LabeledContent("Name", value: pending.name)
                        LabeledContent("Type", value: "\(pending.kind) · \(pending.os)")
                        LabeledContent("Tools", value: (pending.tools ?? []).joined(separator: ", "))
                        LabeledContent("Workspaces", value: (pending.workspaces ?? []).joined(separator: ", "))
                        LabeledContent("Apps", value: (pending.apps ?? []).joined(separator: ", "))
                    }
                    Section {
                        Button("Enroll") { enroll() }.buttonStyle(.borderedProminent)
                    } footer: {
                        Text("Routine tools run on this computer without asking for 90 days. Opening an app always asks first.")
                    }
                } else {
                    Section {
                        Text("On the computer, run `blooper runner`. It shows a QR code.")
                        if ScannerView.isAvailable {
                            ScannerView { payload in
                                let items = URLComponents(string: payload)?.queryItems
                                guard payload.hasPrefix("blooper://enroll"),
                                      let scanned = items?.first(where: { $0.name == "code" })?.value
                                else {
                                    problem = "That isn't a Blooper enrollment code."
                                    return
                                }
                                review(scanned)
                            }
                            .frame(height: 280)
                        }
                    }
                    Section("Or type the code") {
                        TextField("Enrollment code", text: $code).plainInput()
                        Button("Continue") { review(code) }.disabled(code.isEmpty)
                    }
                }
                if let problem {
                    Section { Text(problem).foregroundStyle(.red) }
                }
            }
            .navigationTitle("Add computer")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
    }

    private func review(_ scanned: String) {
        code = scanned
        Task { await attempt($problem) { pending = try await client.enrollment(code: scanned) } }
    }

    private func enroll() {
        Task {
            guard await confirmOwner("Enroll \(pending?.name ?? "this computer")") else { return }
            await attempt($problem) {
                try await client.approveEnrollment(code: code)
                dismiss()
            }
        }
    }
}


/// A phone can pair with both a Mac and a Windows computer without sharing
/// provider credentials or moving either computer's coordinator into the cloud.
struct ComputersView: View {
    @Environment(Client.self) private var client
    @Environment(\.dismiss) private var dismiss
    @State private var pairing = false
    @State private var problem: String?

    var body: some View {
        List {
            Section("Paired computers") {
                ForEach(client.computers) { computer in
                    Button {
                        Task {
                            await attempt($problem) {
                                try client.selectComputer(computer)
                                dismiss()
                            }
                        }
                    } label: {
                        HStack {
                            Label(computer.name, systemImage: "desktopcomputer")
                            Spacer()
                            if computer.server == client.server { Image(systemName: "checkmark").accessibilityLabel("Selected") }
                        }
                    }
                }
                Button("Pair another computer", systemImage: "qrcode.viewfinder") { pairing = true }
            }
            Section {
                Text("Each computer keeps its own keys, permissions, and jobs. Select a computer to control it.")
            }
            if let problem { Section { Text(problem).foregroundStyle(.red) } }
        }
        .navigationTitle("Computers")
        .sheet(isPresented: $pairing) { PairView() }
    }
}
