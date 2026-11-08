import BeautifulUI
import PersonaKit
import SwiftUI

/// The card for an `APIErrorInfo`: the reason with a warning mark, BeautifulUI's chips (the status in the error
/// style, temporary / Action Needed, how many times in a row), then — folded — the ids to copy, the raw text, and Retry when the
/// error is the last thing in an idle chat and likely to pass (a login error is not fixed by trying again).
/// In the BeautifulUI style it is that library's failed task row, its pill the retry (`taskRow`).
struct APIErrorCard: View {
    var info: APIErrorInfo
    var count: Int
    var at: Date?
    var retry: (() -> Void)?
    @Environment(AppModel.self) private var model
    #if DEBUG
    /// `-showErrorDetails YES`: cards start open, for screenshots.
    @State private var open = UserDefaults.standard.bool(forKey: "showErrorDetails")
    #else
    @State private var open = false
    #endif

    private var chips: [ToolChipGroup.Chip] {
        var out: [ToolChipGroup.Chip] = []
        if let s = info.status {
            out.append(ToolChipGroup.Chip(symbol: Theme.Icon.error, label: "\(s)", isError: true))
        }
        switch info.transient {
        case true?: out.append(ToolChipGroup.Chip(symbol: Theme.Icon.retry, label: String(localized: "Temporary")))
        case false?: out.append(ToolChipGroup.Chip(symbol: Theme.Icon.askFirst, label: String(localized: "Action Needed")))
        case nil: break
        }
        if count > 1 { out.append(ToolChipGroup.Chip(symbol: Theme.Icon.recurring, label: String(localized: "\(count) times in a row"))) }
        return out
    }

    /// The status code and how many times in a row, on the right of the row: "502 ×3".
    private var amount: String? {
        let parts = [info.status.map { "\($0)" }, count > 1 ? "×\(count)" : nil].compactMap { $0 }
        return parts.isEmpty ? nil : parts.joined(separator: " ")
    }

    /// Whether it passes by itself, under the reason.
    private var subtitle: String? {
        switch info.transient {
        case true?: String(localized: "Temporary. It will likely clear up soon.")
        case false?: String(localized: "Needs you. Retrying won't help.")
        case nil: nil
        }
    }

    @Environment(\.chatPin) private var pin
    private var pinKey: String { "error-\(info.signature)-\(at?.timeIntervalSince1970 ?? 0)" }
    @State private var unfold = Unfolding()

    /// Its top is its header: opening or closing keeps it there in the chat (`ChatPin`).
    var body: some View {
        Group { if AppStyle.current.usesBeautifulUI { taskRow } else { content } }.chatPinned(pinKey)
    }

    @ViewBuilder private var content: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Button {
                Haptics.tap()
                toggleInPlace(pin, pinKey, animation: Theme.Motion.state, unfold: $unfold, closing: open) { open.toggle() }
            } label: {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                        ThemedIcon(Theme.Icon.errorFilled).font(Theme.Font.heading).foregroundStyle(Theme.danger)
                        Text(info.reason).font(Theme.Font.heading).foregroundStyle(Color.primary).multilineTextAlignment(.leading)
                    }
                    ToolChipGroup(chips, isOpen: open)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("API error: \(info.reason)")
            .accessibilityValue(open ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]"))

            if open {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    if let id = info.requestID { field(String(localized: "Request ID"), id) }
                    if let s = info.session { field("Session", s) }
                    if let at { field(String(localized: "Time"), at.formatted(.dateTime.month().day().hour().minute().second()), copyable: false) }
                    // Wrapped, not a sideways-scrolling block: the raw text is usually one long line.
                    Text(info.raw)
                        .font(Theme.Font.code)
                        .lineHeight(.code)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                        .padding(Theme.Space.m)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                    Button {
                        copy(info.raw)
                    } label: {
                        Label("Copy Raw Text", systemImage: Theme.Icon.copy).font(Theme.Font.labelMedium).lineLimit(1)
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.accent)
                }
                .unfolding(unfold)
                .transition(.opacity)
            }

            if let retry {
                Button {
                    Haptics.tap()
                    retry()
                } label: {
                    Label("Retry [Components b8784c]", systemImage: Theme.Icon.retry).font(Theme.Font.secondaryStrong).lineLimit(1)
                        .padding(.horizontal, Theme.Space.xs)
                }
                .themedButton()
            }
        }
        .padding(Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(Theme.danger.opacity(0.22), lineWidth: 1))
    }

    /// BeautifulUI's failed task row: the red cross, the reason, the code; the pill retries when trying again can help.
    private var taskRow: some View {
        TaskRow(TaskItem(id: pinKey, title: info.reason, subtitle: subtitle, amount: amount, status: .failed),
                isOpen: open, titleLines: 2, subtitleLines: 2,
                action: {
                    Haptics.tap()
                    toggleInPlace(pin, pinKey, animation: Theme.Motion.state, unfold: $unfold, closing: open) { open.toggle() }
                }) {
            TaskBadge(.failed)
        } trailing: {
            if let retry {
                Button {
                    Haptics.tap()
                    retry()
                } label: {
                    BUIPill(String(localized: "Retry [Components b8784c]"), tone: .accent, icon: .retry)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Retry [Components b8784c]")
            } else {
                BUIPill(info.transient == false ? String(localized: "Action Needed") : String(localized: "Failed"), tone: info.transient == false ? .orange : .red)
            }
        } detail: {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                if let id = info.requestID { field(String(localized: "Request ID"), id) }
                if let s = info.session { field("Session", s) }
                if let at { field(String(localized: "Time"), at.formatted(.dateTime.month().day().hour().minute().second()), copyable: false) }
                // Wrapped, not a sideways-scrolling block: the raw text is usually one long line.
                Text(info.raw)
                    .font(Theme.Font.code)
                    .lineHeight(.code)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(Theme.Space.m)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                Button {
                    copy(info.raw)
                } label: {
                    Label("Copy Raw Text", systemImage: Theme.Icon.copy).font(Theme.Font.labelMedium).lineLimit(1)
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.accent)
            }
            .unfolding(unfold)
        }
    }

    private func field(_ name: String, _ value: String, copyable: Bool = true) -> some View {
        HStack(spacing: Theme.Space.s) {
            Text(name).font(Theme.Font.caption).foregroundStyle(.secondary).scaledFrame(width: Theme.Size.box64, alignment: .leading)
            Text(value).font(Theme.Font.code).lineLimit(1).truncationMode(.middle).textSelection(.enabled)
            Spacer(minLength: 0)
            if copyable {
                Button { copy(value) } label: {
                    ThemedIcon(Theme.Icon.copy).font(Theme.Font.caption)
                }
                .buttonStyle(.plain)
                .foregroundStyle(.secondary)
                .accessibilityLabel("Copy \(name)")
            }
        }
    }

    private func copy(_ s: String) {
        UIPasteboard.general.string = s
        Haptics.tap()
        model.toast = Toast(String(localized: "Copied [V N C Pi P Diagnostics 8f6f8d]"))
    }
}
