import Contacts
import Foundation

/// `phone_contacts_search`: find people in the address book by name, company
/// or phone number.
enum ContactsTool {
    static var tools: [PhoneTool] {
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_contacts_search",
                    description: """
                    Search the phone's contacts by name, company, job title, email or phone number. \
                    Returns up to `limit` people with their name, company, job title, phone numbers \
                    (with labels such as mobile/work), email addresses and birthday (yyyy-MM-dd). \
                    Use this to look up a colleague's number before calling or drafting a message.
                    """,
                    inputSchema: Schema.object([
                        "query": Schema.string("Text to match against name, company, job title, email or phone number. Matching is case-insensitive and looks for the text anywhere in the field; a phone number matches with or without spaces and dashes. Pass an empty string to get the first contacts in the address book's own order."),
                        "limit": Schema.integer("Maximum number of contacts to return, default 10, max 50.", min: 1, max: 50),
                    ], required: ["query"])
                ),
                source: .contacts,
                run: { args in try await search(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_contacts_create",
                    description: """
                    Create a contact in the phone's address book. Pass at least a name or a company. phones and \
                    emails are lists of {value, label?} (label: mobile, iphone, home, work, main or other). The user \
                    sees the new card on the phone and must approve. Returns the new contact as \
                    phone_contacts_search does.
                    """,
                    inputSchema: Schema.object([
                        "given_name": Schema.string("First (given) name."),
                        "family_name": Schema.string("Last (family) name."),
                        "company": Schema.string("Company or organization."),
                        "job_title": Schema.string("Job title."),
                        "phones": Schema.array("Phone numbers to add.", of: LabeledEntry.schema),
                        "emails": Schema.array("Email addresses to add.", of: LabeledEntry.schema),
                    ])
                ),
                source: .contacts,
                confirmation: { args in createConfirmation(args) },
                run: { args in try await create(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_contacts_update",
                    description: """
                    Add to or change an existing contact: pass its id from phone_contacts_search and only what \
                    changes. phones and emails are added next to the existing ones (an existing number or address is \
                    skipped). A field that already has a different value (name, company, job title) is only \
                    overwritten, and phones / emails only replaced, with replace=true: pass it only when the user \
                    asked to change it. The user sees each change on the phone and must approve.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("Contact id from phone_contacts_search."),
                        "given_name": Schema.string("First (given) name."),
                        "family_name": Schema.string("Last (family) name."),
                        "company": Schema.string("Company or organization."),
                        "job_title": Schema.string("Job title."),
                        "phones": Schema.array("Phone numbers to add (or, with replace, the new list).", of: LabeledEntry.schema),
                        "emails": Schema.array("Email addresses to add (or, with replace, the new list).", of: LabeledEntry.schema),
                        "replace": Schema.boolean("Overwrite fields that already have a value and replace phones / emails. Default false."),
                    ], required: ["id"])
                ),
                source: .contacts,
                confirmation: { args in updateConfirmation(args) },
                run: { args in try await update(args) }
            ),
        ]
    }

    static func search(_ args: JSONValue) async throws -> [ToolContent] {
        try ContactsAccess.requireAccess()
        let query = ToolArgs.string(args, "query") ?? ""
        let limit = ToolArgs.int(args, "limit", fallback: 10, min: 1, max: 50)

        // CNContactStore is synchronous and can take a while on large address
        // books, so keep it off the main actor.
        let people: [JSONValue] = try await withCheckedThrowingContinuation { continuation in
            DispatchQueue.global(qos: .userInitiated).async {
                do {
                    continuation.resume(returning: try fetch(query: query, limit: limit))
                } catch {
                    continuation.resume(throwing: error)
                }
            }
        }
        if people.isEmpty {
            return Output.json(.object([
                "query": .string(query),
                "count": .number(0),
                "results": .array([]),
                "note": .string("No contacts matched. Try a shorter query, or the company or phone number instead."),
            ]))
        }
        return Output.json(.object([
            "query": .string(query),
            "count": .number(Double(people.count)),
            "results": .array(people),
        ]))
    }

    /// Everything `encode` reads: a contact fetched with fewer keys raises when it reads the rest.
    nonisolated static var fetchKeys: [any CNKeyDescriptor] {
        [
            CNContactIdentifierKey as CNKeyDescriptor,
            CNContactGivenNameKey as CNKeyDescriptor,
            CNContactMiddleNameKey as CNKeyDescriptor,
            CNContactFamilyNameKey as CNKeyDescriptor,
            CNContactNicknameKey as CNKeyDescriptor,
            CNContactOrganizationNameKey as CNKeyDescriptor,
            CNContactJobTitleKey as CNKeyDescriptor,
            CNContactPhoneNumbersKey as CNKeyDescriptor,
            CNContactEmailAddressesKey as CNKeyDescriptor,
            CNContactBirthdayKey as CNKeyDescriptor,
            CNContactFormatter.descriptorForRequiredKeys(for: .fullName),
        ]
    }

    // Runs off the main actor (see search), so it must not be actor isolated.
    nonisolated private static func fetch(query: String, limit: Int) throws -> [JSONValue] {
        let store = CNContactStore()
        let request = CNContactFetchRequest(keysToFetch: fetchKeys)
        request.sortOrder = .userDefault
        request.mutableObjects = false

        var out: [JSONValue] = []
        var seen = Set<String>()
        try store.enumerateContacts(with: request) { contact, stop in
            guard out.count < limit else {
                stop.pointee = true
                return
            }
            guard seen.insert(contact.identifier).inserted else { return }
            guard query.isEmpty || Self.matches(contact, query: query) else { return }
            out.append(Self.encode(contact))
        }
        return out
    }

    nonisolated private static func matches(_ contact: CNContact, query: String) -> Bool {
        let needle = query.lowercased()
        func hit(_ value: String) -> Bool {
            !value.isEmpty && value.lowercased().contains(needle)
        }
        if hit(CNContactFormatter.string(from: contact, style: .fullName) ?? "") { return true }
        if hit(contact.nickname) || hit(contact.organizationName) || hit(contact.jobTitle) { return true }
        if contact.phoneNumbers.contains(where: { hit($0.value.stringValue) }) { return true }
        if contact.emailAddresses.contains(where: { hit($0.value as String) }) { return true }
        // A phone query often arrives with spaces or dashes the .stringValue keeps.
        let digits = query.filter(\.isNumber)
        if digits.count >= 3, contact.phoneNumbers.contains(where: { $0.value.stringValue.filter(\.isNumber).contains(digits) }) {
            return true
        }
        return false
    }

    nonisolated static func encode(_ contact: CNContact) -> JSONValue {
        let name = CNContactFormatter.string(from: contact, style: .fullName) ?? ""
        var birthday: String?
        if let date = contact.birthday, let month = date.month, let day = date.day {
            // Contacts can store a birthday without a year; don't invent one.
            if let year = date.year {
                birthday = String(format: "%04d-%02d-%02d", year, month, day)
            } else {
                birthday = String(format: "--%02d-%02d", month, day)
            }
        }
        let phones: [JSONValue] = contact.phoneNumbers.map { item in
            .object([
                "label": .string(CNLabeledValue<NSString>.localizedString(forLabel: item.label ?? "")),
                "number": .string(item.value.stringValue),
            ])
        }
        let emails: [JSONValue] = contact.emailAddresses.map { item in
            .object([
                "label": .string(CNLabeledValue<NSString>.localizedString(forLabel: item.label ?? "")),
                "email": .string(item.value as String),
            ])
        }
        return Output.object([
            ("id", .string(contact.identifier)),
            ("name", Output.opt(name)),
            ("nickname", Output.opt(contact.nickname)),
            ("company", Output.opt(contact.organizationName)),
            ("job_title", Output.opt(contact.jobTitle)),
            ("phones", phones.isEmpty ? nil : .array(phones)),
            ("emails", emails.isEmpty ? nil : .array(emails)),
            ("birthday", Output.opt(birthday)),
        ])
    }

    // MARK: - Creating and changing (#2286)

    /// One phone number or email address with its label, as the tools take them.
    nonisolated struct LabeledEntry: Equatable, Sendable {
        var value: String
        var label: String?

        static var schema: JSONValue {
            Schema.object([
                "value": Schema.string("The phone number or email address."),
                "label": Schema.string("mobile, iphone, home, work, main or other.", values: ["mobile", "iphone", "home", "work", "main", "other"]),
            ], required: ["value"])
        }

        /// A list argument: objects {value, label?}, or plain strings.
        static func list(_ value: JSONValue?, email: Bool) throws -> [LabeledEntry]? {
            guard let value, value != .null else { return nil }
            guard case .array(let items) = value else {
                throw ToolFailure(.invalidArgument, email ? "emails must be a list" : "phones must be a list")
            }
            return try items.map { item in
                let raw = (item.string ?? ToolArgs.string(item, "value"))?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                if email {
                    guard raw.contains("@"), !raw.contains(" ") else {
                        throw ToolFailure(.invalidArgument, "\"\(raw)\" is not an email address")
                    }
                } else {
                    guard raw.filter(\.isNumber).count >= 3 else {
                        throw ToolFailure(.invalidArgument, "\"\(raw)\" is not a phone number")
                    }
                }
                return LabeledEntry(value: raw, label: ToolArgs.string(item, "label")?.lowercased())
            }
        }

        var cnLabel: String {
            switch label {
            case "iphone": CNLabelPhoneNumberiPhone
            case "main": CNLabelPhoneNumberMain
            case "home": CNLabelHome
            case "work": CNLabelWork
            case "other": CNLabelOther
            default: CNLabelPhoneNumberMobile
            }
        }
    }

    /// The fields a change can touch, as plain values (the contact before it, for the plan and the card).
    nonisolated struct ContactFields: Equatable, Sendable {
        var givenName = ""
        var familyName = ""
        var company = ""
        var jobTitle = ""
        var phones: [String] = []
        var emails: [String] = []

        init(givenName: String = "", familyName: String = "", company: String = "", jobTitle: String = "",
             phones: [String] = [], emails: [String] = []) {
            self.givenName = givenName
            self.familyName = familyName
            self.company = company
            self.jobTitle = jobTitle
            self.phones = phones
            self.emails = emails
        }

        init(_ contact: CNContact) {
            self.init(givenName: contact.givenName, familyName: contact.familyName, company: contact.organizationName,
                      jobTitle: contact.jobTitle, phones: contact.phoneNumbers.map { $0.value.stringValue },
                      emails: contact.emailAddresses.map { $0.value as String })
        }

        var name: String {
            let full = [givenName, familyName].filter { !$0.isEmpty }.joined(separator: " ")
            return full.isEmpty ? company : full
        }
    }

    /// What a create or an update writes, worked out before the card shows: the card lists it, and the tool writes it.
    /// An update never overwrites a field that has another value, or replaces phones / emails, without `replace`.
    nonisolated struct ContactEdit: Equatable, Sendable {
        /// A scalar field: its name on the card, the value before (empty when none) and after.
        nonisolated struct Field: Equatable, Sendable {
            var key: String
            var before: String
            var after: String
        }

        var fields: [Field] = []
        var phones: [LabeledEntry] = []
        var emails: [LabeledEntry] = []
        var replacePhones = false
        var replaceEmails = false

        var isEmpty: Bool { fields.isEmpty && phones.isEmpty && emails.isEmpty && !replacePhones && !replaceEmails }

        /// `current` nil: a new contact.
        static func plan(current: ContactFields?, args: JSONValue) throws -> ContactEdit {
            let replace = ToolArgs.bool(args, "replace") ?? false
            let base = current ?? ContactFields()
            var edit = ContactEdit()
            let scalars: [(String, String)] = [("given_name", base.givenName), ("family_name", base.familyName),
                                               ("company", base.company), ("job_title", base.jobTitle)]
            for (key, before) in scalars {
                guard let after = ToolArgs.string(args, key), after != before else { continue }
                if !before.isEmpty && !replace {
                    throw ToolFailure(.invalidArgument, "the contact's \(key) is already \"\(before)\"; pass replace=true only "
                        + "if the user asked to change it")
                }
                edit.fields.append(Field(key: key, before: before, after: after))
            }
            if let phones = try LabeledEntry.list(args["phones"], email: false) {
                if replace && current != nil {
                    edit.replacePhones = true
                    edit.phones = phones
                } else {
                    let known = Set(base.phones.map { $0.filter(\.isNumber) })
                    var seen = known
                    edit.phones = phones.filter { seen.insert($0.value.filter(\.isNumber)).inserted }
                }
            }
            if let emails = try LabeledEntry.list(args["emails"], email: true) {
                if replace && current != nil {
                    edit.replaceEmails = true
                    edit.emails = emails
                } else {
                    var seen = Set(base.emails.map { $0.lowercased() })
                    edit.emails = emails.filter { seen.insert($0.value.lowercased()).inserted }
                }
            }
            if current == nil {
                let named = edit.fields.contains { ["given_name", "family_name", "company"].contains($0.key) }
                guard named else { throw ToolFailure(.invalidArgument, "a new contact needs given_name, family_name or company") }
            } else if edit.isEmpty {
                throw ToolFailure(.invalidArgument, "nothing to change: the contact already has these values")
            }
            return edit
        }

        func apply(to contact: CNMutableContact) {
            for field in fields {
                switch field.key {
                case "given_name": contact.givenName = field.after
                case "family_name": contact.familyName = field.after
                case "company": contact.organizationName = field.after
                case "job_title": contact.jobTitle = field.after
                default: break
                }
            }
            let newPhones = phones.map { CNLabeledValue(label: $0.cnLabel, value: CNPhoneNumber(stringValue: $0.value)) }
            contact.phoneNumbers = replacePhones ? newPhones : contact.phoneNumbers + newPhones
            let newEmails = emails.map { entry in
                CNLabeledValue(label: entry.label == nil || entry.label == "mobile" ? CNLabelOther : entry.cnLabel,
                               value: entry.value as NSString)
            }
            contact.emailAddresses = replaceEmails ? newEmails : contact.emailAddresses + newEmails
        }

        /// The card's lines below its title.
        var lines: [String] {
            var out: [String] = []
            for field in fields {
                let label = Self.fieldName(field.key)
                out.append(field.before.isEmpty ? String(localized: "\(label): \(field.after)")
                    : String(localized: "\(label): \(field.before) → \(field.after)"))
            }
            let phoneList = phones.map(\.value).joined(separator: ", ")
            if replacePhones {
                out.append(String(localized: "Replace phone numbers with: \(phoneList)"))
            } else if !phones.isEmpty {
                out.append(String(localized: "Add phone: \(phoneList)"))
            }
            let emailList = emails.map(\.value).joined(separator: ", ")
            if replaceEmails {
                out.append(String(localized: "Replace emails with: \(emailList)"))
            } else if !emails.isEmpty {
                out.append(String(localized: "Add email: \(emailList)"))
            }
            return out
        }

        static func fieldName(_ key: String) -> String {
            switch key {
            case "given_name": String(localized: "First name")
            case "family_name": String(localized: "Last name")
            case "company": String(localized: "Company")
            default: String(localized: "Job title")
            }
        }
    }

    static func createSummary(_ edit: ContactEdit) -> String {
        let fields = Dictionary(edit.fields.map { ($0.key, $0.after) }, uniquingKeysWith: { a, _ in a })
        let name = ContactFields(givenName: fields["given_name"] ?? "", familyName: fields["family_name"] ?? "",
                                 company: fields["company"] ?? "").name
        return ([String(localized: "New contact “\(name)”")] + edit.lines).joined(separator: "\n")
    }

    static func updateSummary(_ current: ContactFields, _ edit: ContactEdit) -> String {
        let name = current.name.isEmpty ? String(localized: "(Untitled)") : current.name
        return ([String(localized: "Change contact “\(name)”")] + edit.lines).joined(separator: "\n")
    }

    static func createConfirmation(_ args: JSONValue) -> String {
        guard let edit = try? ContactEdit.plan(current: nil, args: args) else {
            return String(localized: "Invalid parameters; nothing will be saved.")
        }
        return createSummary(edit)
    }

    static func updateConfirmation(_ args: JSONValue) -> String {
        let id = ToolArgs.string(args, "id") ?? "?"
        guard ContactsAccess.status().isUsable, let contact = try? existing(id: id, in: CNContactStore()) else {
            return String(localized: "Can't find this contact (or Persona has no access to Contacts): \(id)")
        }
        let current = ContactFields(contact)
        guard let edit = try? ContactEdit.plan(current: current, args: args) else {
            let name = current.name.isEmpty ? String(localized: "(Untitled)") : current.name
            return [String(localized: "Change contact “\(name)”"),
                    String(localized: "Invalid parameters; nothing will be saved.")].joined(separator: "\n")
        }
        return updateSummary(current, edit)
    }

    private static func existing(id: String, in store: CNContactStore) throws -> CNContact {
        do {
            return try store.unifiedContact(withIdentifier: id, keysToFetch: fetchKeys)
        } catch {
            let limited = ContactsAccess.status() == .limited
            throw ToolFailure(.invalidArgument, "no contact with id \"\(id)\"; call phone_contacts_search to get a current id"
                + (limited ? " (Persona can only see the contacts the user shared with it: limited Contacts access)" : ""))
        }
    }

    static func create(_ args: JSONValue) async throws -> [ToolContent] {
        try ContactsAccess.requireAccess()
        let edit = try ContactEdit.plan(current: nil, args: args)
        let contact = CNMutableContact()
        edit.apply(to: contact)
        let request = CNSaveRequest()
        request.add(contact, toContainerWithIdentifier: nil)
        try CNContactStore().execute(request)
        return Output.json(encode(contact))
    }

    static func update(_ args: JSONValue) async throws -> [ToolContent] {
        try ContactsAccess.requireAccess()
        guard let id = ToolArgs.string(args, "id") else { throw ToolFailure(.invalidArgument, "id is required") }
        let store = CNContactStore()
        let contact = try existing(id: id, in: store)
        let edit = try ContactEdit.plan(current: ContactFields(contact), args: args)
        guard let mutable = contact.mutableCopy() as? CNMutableContact else {
            throw ToolFailure(.unavailable, "this contact cannot be changed")
        }
        edit.apply(to: mutable)
        let request = CNSaveRequest()
        request.update(mutable)
        try store.execute(request)
        return Output.json(encode(mutable))
    }
}
