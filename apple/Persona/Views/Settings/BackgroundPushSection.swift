import SwiftUI

/// Settings › Notifications: whether the hub can push to this device while Persona is closed, why not and what to do
/// (#5998).
struct BackgroundPushSection: View {
    var status: PushStatus

    var body: some View {
        Section {
            Label {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(status.title).lineLimit(1)
                    if let reason = status.reason {
                        Text(reason).font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                    if let action = status.action {
                        Text(action).font(Theme.Font.caption)
                    }
                }
            } icon: {
                ThemedIcon(icon).foregroundStyle(tint)
            }
            .accessibilityIdentifier("notifications.backgroundPush")
        } header: {
            Text("Background Push")
        } footer: {
            Text("How the hub reaches this device while Persona is closed, for questions and finished agents.")
        }
    }

    private var icon: String {
        switch status.level {
        case .available: Theme.Icon.successFilled
        case .lastFailed, .unavailable: Theme.Icon.errorFilled
        case .unknown: Theme.Icon.infoFilled
        }
    }

    private var tint: Color {
        switch status.level {
        case .available: Theme.positive
        case .lastFailed: Theme.caution
        case .unavailable: Theme.danger
        case .unknown: .secondary
        }
    }
}

/// Settings › Devices: the way the hub pushes to the device a credential registered, in one line.
struct DevicePushLine: View {
    var route: APIClient.PushRoute

    var body: some View {
        Text(text)
            .font(Theme.Font.label)
            .foregroundStyle(fine ? AnyShapeStyle(.secondary) : AnyShapeStyle(Theme.caution))
            .lineLimit(1)
    }

    private var fine: Bool { route.reaches && route.reason == nil }

    private var text: String {
        if route.reaches {
            if route.reason == "push_failed" { return String(localized: "Push: the last one didn't go through") }
            return route.route == "direct" ? String(localized: "Push: direct to APNs") : String(localized: "Push: through the push relay")
        }
        switch route.reason {
        case "no_token": return String(localized: "Push: unavailable · no push token")
        case "no_relay": return String(localized: "Push: unavailable · no relay or APNs key")
        default:
            guard let detail = route.detail, !detail.isEmpty else { return String(localized: "Push: unavailable") }
            return String(localized: "Push: unavailable · \(detail)", comment: "%@: why, in the relay's words")
        }
    }
}
