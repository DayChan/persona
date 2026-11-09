import SwiftUI

/// A settings page's body: the phone's list as it was; on the Mac a grouped form, as System Settings draws its panes
/// (#1169 B1): rounded groups, switches on the trailing side, footers that wrap. A Mac `List` draws a section's
/// footer on one line, cut off with an ellipsis, and the sidebar style leaves it out (#1212).
struct SettingsForm<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        #if os(macOS)
        Form { content }.formStyle(.grouped)
        #else
        List { content }
        #endif
    }
}

/// A form's own action in a row of its own: the phone's full-width button on a clear row as before; on the Mac a
/// standard button at the row's trailing end (#1221; a Mac grouped form keeps the row's card whatever
/// `listRowBackground` says). The label takes `formActionWidth()`.
struct FormAction<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        #if os(macOS)
        HStack { Spacer(minLength: 0); content }
        #else
        content.listRowBackground(Color.clear)
        #endif
    }
}

extension View {
    /// A `FormAction` button's label: the row's whole width on the phone, its own width on the Mac.
    @ViewBuilder func formActionWidth() -> some View {
        #if os(macOS)
        self
        #else
        frame(maxWidth: .infinity)
        #endif
    }

    /// A settings row with a progress indicator at its trailing edge while `active` (an action it started still runs,
    /// such as a disconnect signing this device out); laid out as before otherwise.
    @ViewBuilder func trailingProgress(_ active: Bool) -> some View {
        if active {
            HStack(spacing: Theme.Space.m) {
                self
                Spacer(minLength: 0)
                RowProgress()
            }
        } else {
            self
        }
    }
}

/// The progress indicator a settings row shows while an action it started runs: on the Mac the small one, the height of
/// the form's buttons and text.
struct RowProgress: View {
    var body: some View {
        #if os(macOS)
        ProgressView().controlSize(.small)
        #else
        ProgressView()
        #endif
    }
}

/// Why a settings page has nothing to show. In Demo Mode on the Mac a page whose data the demo does not make up (Lark Bot,
/// Model Providers) says so plainly instead of in red, which read as something broken (#1224 A4); the phone, and the Mac
/// with a real hub, show the error as before.
struct LoadProblem: View {
    @Environment(AppModel.self) private var model
    let text: String
    /// Drawn as a label with the error icon (lists of things) rather than a line of text (a status row).
    var labeled = true

    var body: some View {
        #if os(macOS)
        if model.isDemo {
            Label("Demo mode has no sample data for this part. It shows up once you connect your own hub.", systemImage: Theme.Icon.info)
                .font(Theme.Font.label).foregroundStyle(.secondary)
        } else {
            problem
        }
        #else
        problem
        #endif
    }

    @ViewBuilder private var problem: some View {
        if labeled {
            Label(text, systemImage: Theme.Icon.error).font(Theme.Font.label).foregroundStyle(Theme.danger)
        } else {
            Text(text).font(Theme.Font.label).foregroundStyle(Theme.danger).textSelection(.enabled)
        }
    }
}
