import Foundation
#if os(iOS)
import MessageUI
import UIKit
#endif

/// `phone_messages_compose`: a text message prefilled in the system compose sheet (#2279). The user sends it there, or
/// cancels; Persona never sends a message itself and cannot read any.
enum MessagesTool {
    static var tools: [PhoneTool] {
        #if os(iOS)
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_messages_compose",
                    description: """
                    Open the iPhone's message compose sheet (SMS / iMessage) with the recipients and text filled in. \
                    The user reviews it and taps Send themselves, or cancels: the result is sent, cancelled or \
                    failed, and nothing is sent without them. Resolve the recipient's number with \
                    phone_contacts_search first, and ask when a contact has several numbers. Persona must be open on \
                    the iPhone. It cannot read messages or message history.
                    """,
                    inputSchema: Schema.object([
                        "recipients": Schema.array("Phone numbers or iMessage email addresses, at least one.", of: Schema.string("A phone number or email address.")),
                        "body": Schema.string("The message text."),
                    ], required: ["recipients", "body"])
                ),
                source: .messages,
                run: { args in try await compose(args) }
            ),
        ]
        #else
        []
        #endif
    }

    /// The message to prefill, checked before the sheet opens.
    nonisolated struct Draft: Equatable, Sendable {
        var recipients: [String]
        var body: String

        static func parse(_ args: JSONValue) throws -> Draft {
            guard case .array(let items)? = args["recipients"] else {
                throw ToolFailure(.invalidArgument, "recipients must be a list of phone numbers or email addresses")
            }
            let recipients = items.compactMap { $0.string?.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
            guard !recipients.isEmpty, recipients.count <= 20 else {
                throw ToolFailure(.invalidArgument, "pass 1–20 recipients")
            }
            for recipient in recipients where !recipient.contains("@") && recipient.filter(\.isNumber).count < 3 {
                throw ToolFailure(.invalidArgument, "\"\(recipient)\" is not a phone number or email address")
            }
            guard let body = args["body"]?.string, !body.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
                throw ToolFailure(.invalidArgument, "body is required")
            }
            return Draft(recipients: recipients, body: body)
        }
    }

    /// What became of the sheet, in the words the tool returns.
    nonisolated enum Outcome: String, Sendable { case sent, cancelled, failed }

    #if os(iOS)
    static func compose(_ args: JSONValue) async throws -> [ToolContent] {
        let draft = try Draft.parse(args)
        guard MFMessageComposeViewController.canSendText() else {
            throw ToolFailure(.unavailable, "This device cannot send text messages (no SIM or iMessage set up).")
        }
        try Foreground.require("open the message sheet")
        guard let presenter = Foreground.topController() else {
            throw ToolFailure(.unavailable, "Persona has no window to show the message sheet on. Ask the user to open Persona and try again.")
        }
        let outcome = await MessageSheet.present(draft, from: presenter)
        let note = switch outcome {
        case .sent: "The user sent the message from the system sheet."
        case .cancelled: "The user closed the sheet without sending: nothing was sent."
        case .failed: "iOS could not send the message: nothing was sent."
        }
        return Output.json(Output.object([
            ("result", .string(outcome.rawValue)),
            ("recipients", .array(draft.recipients.map { .string($0) })),
            ("note", .string(note)),
        ]))
    }
    #endif
}

#if os(iOS)
/// Shows one compose sheet and answers how it ended. It keeps itself alive until then (the controller holds its
/// delegate weakly). MessageUI calls the delegate on the main thread.
final class MessageSheet: NSObject, MFMessageComposeViewControllerDelegate {
    private var finish: ((MessagesTool.Outcome) -> Void)?
    private var keepAlive: MessageSheet?
    private weak var controller: MFMessageComposeViewController?

    static func present(_ draft: MessagesTool.Draft, from presenter: UIViewController) async -> MessagesTool.Outcome {
        await withCheckedContinuation { continuation in
            let sheet = MessageSheet()
            sheet.keepAlive = sheet
            sheet.finish = { continuation.resume(returning: $0) }
            let controller = MFMessageComposeViewController()
            controller.recipients = draft.recipients
            controller.body = draft.body
            controller.messageComposeDelegate = sheet
            sheet.controller = controller
            presenter.present(controller, animated: true)
        }
    }

    nonisolated func messageComposeViewController(_ controller: MFMessageComposeViewController, didFinishWith result: MessageComposeResult) {
        let outcome: MessagesTool.Outcome = switch result {
        case .sent: .sent
        case .failed: .failed
        default: .cancelled
        }
        MainActor.assumeIsolated { done(outcome) }
    }

    private func done(_ outcome: MessagesTool.Outcome) {
        controller?.dismiss(animated: true)
        finish?(outcome)
        finish = nil
        keepAlive = nil
    }
}
#endif
