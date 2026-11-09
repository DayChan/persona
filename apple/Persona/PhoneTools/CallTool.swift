import Foundation

/// `phone_call_start`: a phone, FaceTime or FaceTime audio call (#2280). Persona's card names the number first, then iOS
/// shows its own call prompt; the tool can only say the call screen opened, not that anyone answered.
enum CallTool {
    static var tools: [PhoneTool] {
        #if os(iOS)
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_call_start",
                    description: """
                    Start a phone call, a FaceTime video call or a FaceTime audio call on the iPhone. Pass one number \
                    (or, for FaceTime, an email address) that the user chose: when a contact has several numbers, ask \
                    which first and never pick one yourself. The user approves on the phone, then iOS asks again \
                    before dialling. The result only says the call was handed to iOS; it cannot tell whether it \
                    connected. Persona must be open on the iPhone. There is no access to call history.
                    """,
                    inputSchema: Schema.object([
                        "number": Schema.string("Phone number to call (digits, +, spaces and dashes allowed); for FaceTime also an email address."),
                        "kind": Schema.string("phone (default), facetime or facetime_audio.", values: ["phone", "facetime", "facetime_audio"]),
                        "name": Schema.string("Who is being called, shown on the approval card."),
                    ], required: ["number"])
                ),
                source: .calls,
                confirmation: { args in confirmation(args) },
                run: { args in try await start(args) }
            ),
        ]
        #else
        []
        #endif
    }

    /// A call worked out from the arguments: the card shows it and the tool dials it.
    nonisolated struct CallRequest: Equatable, Sendable {
        nonisolated enum Kind: String, Sendable { case phone, facetime, facetimeAudio = "facetime_audio" }

        var address: String
        var kind: Kind
        var name: String?

        static func parse(_ args: JSONValue) throws -> CallRequest {
            guard let raw = ToolArgs.string(args, "number") else {
                throw ToolFailure(.invalidArgument, "number is required")
            }
            guard let kind = Kind(rawValue: ToolArgs.string(args, "kind") ?? "phone") else {
                throw ToolFailure(.invalidArgument, "kind must be phone, facetime or facetime_audio")
            }
            let address: String
            if kind != .phone, raw.contains("@") {
                guard !raw.contains(" "), !raw.contains("/") else {
                    throw ToolFailure(.invalidArgument, "\"\(raw)\" is not an email address")
                }
                address = raw
            } else {
                // Only what a dialer takes: a stray letter or a second number in the string must not reach the URL.
                let kept = raw.filter { $0.isNumber || $0 == "+" || $0 == "*" || $0 == "#" || $0 == "," || $0 == ";" }
                guard kept.filter(\.isNumber).count >= 3,
                      raw.allSatisfy({ $0.isNumber || " +-().*#,;".contains($0) }) else {
                    throw ToolFailure(.invalidArgument, "\"\(raw)\" is not one phone number")
                }
                address = kept
            }
            return CallRequest(address: address, kind: kind, name: ToolArgs.string(args, "name"))
        }

        var url: URL? {
            let scheme = switch kind {
            case .phone: "tel"
            case .facetime: "facetime"
            case .facetimeAudio: "facetime-audio"
            }
            let escaped = address.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? address
            return URL(string: "\(scheme):\(escaped)")
        }
    }

    static func summary(_ call: CallRequest) -> String {
        let who = call.name.map { "\($0) (\(call.address))" } ?? call.address
        return switch call.kind {
        case .phone: String(localized: "Call \(who)")
        case .facetime: String(localized: "FaceTime \(who)")
        case .facetimeAudio: String(localized: "FaceTime audio call to \(who)")
        }
    }

    static func confirmation(_ args: JSONValue) -> String {
        guard let call = try? CallRequest.parse(args) else {
            return String(localized: "Invalid parameters; no call will be made.")
        }
        return summary(call)
    }

    #if os(iOS)
    static func start(_ args: JSONValue) async throws -> [ToolContent] {
        let call = try CallRequest.parse(args)
        try Foreground.require("start a call")
        guard let url = call.url, await Foreground.open(url) else {
            throw ToolFailure(.unavailable, "iOS did not start the call (no phone service on this device, or FaceTime is off).")
        }
        return Output.json(Output.object([
            ("handed_to_ios", .bool(true)),
            ("kind", .string(call.kind.rawValue)),
            ("number", .string(call.address)),
            ("note", .string("iOS shows its own call prompt; the call starts only if the user taps Call. Persona cannot tell whether it connected.")),
        ]))
    }
    #endif
}
