import Foundation

nonisolated enum ExtensionActionValues {
    static func map(_ place: ExtensionShortcutPlace, text: String?, number: Double?, toggle: Bool?, choice: String?, json: String?) throws -> [String: String] {
        let params = place.params ?? []
        var values: [String: String] = [:]
        if let json, !json.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            guard json.utf8.count <= 16_384, let data = json.data(using: .utf8),
                  let parsed = try? JSONDecoder().decode([String: String].self, from: data) else { throw ExtensionActionInputError() }
            values = parsed
        }
        let singles: [String: String?] = ["text": text, "number": number.flatMap { $0.isFinite ? String($0) : nil },
                                        "toggle": toggle.map { $0 ? "true" : "false" }, "choice": choice]
        if let number, !number.isFinite { throw ExtensionActionInputError() }
        for (kind, input) in singles {
            if let input, let parameter = params.first(where: { $0.kind == kind }), values[parameter.name] == nil { values[parameter.name] = input }
        }
        guard values.keys.allSatisfy({ name in params.contains { $0.name == name } }) else { throw ExtensionActionInputError() }
        for parameter in params {
            guard let value = values[parameter.name], !value.isEmpty else {
                if parameter.required == true { throw ExtensionActionInputError() }
                continue
            }
            switch parameter.kind {
            case "number": guard let number = Double(value), number.isFinite else { throw ExtensionActionInputError() }
            case "toggle": guard ["true", "false"].contains(value) else { throw ExtensionActionInputError() }
            case "choice": guard parameter.choices?.contains(where: { $0.value == value }) == true else { throw ExtensionActionInputError() }
            case "text": break
            default: throw ExtensionActionInputError()
            }
        }
        return values
    }
}

nonisolated struct ExtensionActionInputError: LocalizedError {
    var errorDescription: String? { String(localized: "Check the extension action's required values and JSON parameters.") }
}
