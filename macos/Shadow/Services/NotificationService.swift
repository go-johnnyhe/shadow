import UserNotifications

/// Posts macOS notifications for key session events.
enum NotificationService {
    static func requestPermission() {
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
    }

    static func notifyTunnelReady(joinCommand: String?) {
        let content = UNMutableNotificationContent()
        content.title = "Shadow Session Ready"
        content.body = joinCommand != nil
            ? "Join command copied to clipboard."
            : "Your session is now live."
        content.sound = .default

        let request = UNNotificationRequest(identifier: "tunnel_ready", content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request)
    }

    static func notifyStopped() {
        let content = UNMutableNotificationContent()
        content.title = "Shadow Session Ended"
        content.body = "Your collaboration session has stopped."
        content.sound = .default

        let request = UNNotificationRequest(identifier: "stopped", content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request)
    }
}
