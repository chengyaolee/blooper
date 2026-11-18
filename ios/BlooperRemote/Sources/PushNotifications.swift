import Combine
import UIKit
import UserNotifications

/// Notifications only navigate to jobs. Approval still happens inside the app.
@MainActor
final class PushNotifications: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate, ObservableObject {
    @Published private(set) var token: String?
    @Published private(set) var enabled = UserDefaults.standard.bool(forKey: "pushEnabled")
    @Published var selectedJobID: String?
    @Published var problem: String?

    func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        refreshRegistration()
        return true
    }

    func refreshRegistration() {
        guard enabled else { return }
        UIApplication.shared.registerForRemoteNotifications()
    }

    func enable() async {
        do {
            let granted = try await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])
            guard granted else {
                problem = "Notifications are disabled. Allow them in iOS Settings, then try again."
                return
            }
            enabled = true
            UserDefaults.standard.set(true, forKey: "pushEnabled")
            problem = nil
            refreshRegistration()
        } catch { problem = error.localizedDescription }
    }

    func disable(client: Client) async {
        do {
            try await client.unregisterPush()
            enabled = false
            UserDefaults.standard.set(false, forKey: "pushEnabled")
            UIApplication.shared.unregisterForRemoteNotifications()
            token = nil
            problem = nil
        } catch { problem = error.localizedDescription }
    }

    func sync(client: Client) async {
        guard enabled, client.isPaired, let token else { return }
        do {
            try await client.registerPush(token: token)
            problem = nil
        } catch { problem = error.localizedDescription }
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        token = deviceToken.map { String(format: "%02x", $0) }.joined()
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        problem = error.localizedDescription
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        [.banner, .sound]
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        guard let id = response.notification.request.content.userInfo["job_id"] as? String else { return }
        await MainActor.run { self.selectedJobID = id }
    }
}
