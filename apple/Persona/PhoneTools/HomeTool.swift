import Foundation
#if os(iOS)
import HomeKit
#endif

/// Home accessories and scenes through HomeKit (#2285). Every change is approved on the phone, one at a time; locks,
/// garage doors and security systems say so on the card. Needs the HomeKit capability in the build (`HomeAccess.isBuilt`):
/// without it the source is not offered.
enum HomeTool {
    static var tools: [PhoneTool] {
        #if os(iOS)
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_home_list",
                    description: """
                    List the user's Apple Home: homes, rooms, accessories (id, name, room, category, reachable, \
                    whether it is security-sensitive, and the controls it has with their last known values) and \
                    scenes (id, name). Use the ids with phone_home_control and phone_home_run_scene.
                    """,
                    inputSchema: Schema.object([:])
                ),
                source: .home,
                run: { _ in try await list() }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_home_control",
                    description: """
                    Change one control of one Home accessory: power (true/false), brightness (0–100), \
                    target_temperature (degrees Celsius), lock (true locks, false unlocks) or door (true opens, false \
                    closes). The user approves every change on the phone; locks, garage doors and security systems \
                    are always asked one by one. An accessory that is not responding is reported as such.
                    """,
                    inputSchema: Schema.object([
                        "accessory_id": Schema.string("Accessory id from phone_home_list."),
                        "control": Schema.string("power, brightness, target_temperature, lock or door.",
                                                 values: HomeCommand.Control.allCases.map(\.rawValue)),
                        "value": Schema.string("true / false for power, lock and door; a number for brightness and target_temperature."),
                    ], required: ["accessory_id", "control", "value"])
                ),
                source: .home,
                confirmation: { args in controlConfirmation(args) },
                run: { args in try await control(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_home_run_scene",
                    description: """
                    Run one of the user's Home scenes (for example Good Night). The user approves on the phone; a \
                    scene that touches a lock, garage door or security system says so on the card.
                    """,
                    inputSchema: Schema.object([
                        "scene_id": Schema.string("Scene id from phone_home_list."),
                    ], required: ["scene_id"])
                ),
                source: .home,
                confirmation: { args in sceneConfirmation(args) },
                run: { args in try await runScene(args) }
            ),
        ]
        #else
        []
        #endif
    }

    /// One change, read from the arguments before the card shows.
    nonisolated struct HomeCommand: Equatable, Sendable {
        nonisolated enum Control: String, CaseIterable, Sendable {
            case power, brightness, targetTemperature = "target_temperature", lock, door
        }

        var accessoryID: String
        var control: Control
        /// What gets written: Bool as 0 / 1 where HomeKit wants a number, already converted.
        var number: Double

        static func parse(_ args: JSONValue) throws -> HomeCommand {
            guard let id = ToolArgs.string(args, "accessory_id") else {
                throw ToolFailure(.invalidArgument, "accessory_id is required")
            }
            guard let control = ToolArgs.string(args, "control").flatMap(Control.init(rawValue:)) else {
                throw ToolFailure(.invalidArgument, "control must be power, brightness, target_temperature, lock or door")
            }
            let value = args["value"]
            func flag() throws -> Bool {
                if let bool = value?.bool { return bool }
                switch value?.string?.lowercased() {
                case "true", "on", "1": return true
                case "false", "off", "0": return false
                default: throw ToolFailure(.invalidArgument, "\(control.rawValue) takes true or false")
                }
            }
            func amount(_ range: ClosedRange<Double>) throws -> Double {
                guard let number = value?.double ?? value?.string.flatMap { Double($0) }, range.contains(number) else {
                    throw ToolFailure(.invalidArgument, "\(control.rawValue) takes a number in \(Int(range.lowerBound))–\(Int(range.upperBound))")
                }
                return number
            }
            let number: Double = switch control {
            case .power: try flag() ? 1 : 0
            case .brightness: try amount(0...100).rounded()
            case .targetTemperature: try amount(10...38)
            // HomeKit's lock target: 1 secured, 0 unsecured. Door target: 0 open, 1 closed.
            case .lock: try flag() ? 1 : 0
            case .door: try flag() ? 0 : 1
            }
            return HomeCommand(accessoryID: id, control: control, number: number)
        }

        /// The card's line for the change.
        var line: String {
            switch control {
            case .power:
                return number == 1 ? String(localized: "Turn on") : String(localized: "Turn off")
            case .brightness:
                let percent = (number / 100).formatted(.percent)
                return String(localized: "Brightness: \(percent)")
            case .targetTemperature:
                let degrees = number.formatted(.number.precision(.fractionLength(0...1)))
                return String(localized: "Temperature: \(degrees) °C")
            case .lock:
                return number == 1 ? String(localized: "Lock [Home Accessory]") : String(localized: "Unlock [Home Accessory]")
            case .door:
                return number == 0 ? String(localized: "Open [Home Accessory]") : String(localized: "Close [Home Accessory]")
            }
        }
    }

    /// Categories whose change is asked with a warning line (#2285): someone could get in.
    nonisolated static let sensitiveCategories: Set<String> = ["lock", "garage_door", "security_system", "door", "window"]

    static func summary(accessory: String, room: String?, sensitive: Bool, command: HomeCommand) -> String {
        var title = accessory
        if let room { title = String(localized: "\(accessory) (\(room))") }
        var lines = [title, command.line]
        if sensitive { lines.append(String(localized: "Security accessory: check before you allow it.")) }
        return lines.joined(separator: "\n")
    }

    static func sceneSummary(scene: String, home: String?, sensitive: Bool) -> String {
        var lines = [String(localized: "Run the scene “\(scene)”")]
        if let home { lines.append(String(localized: "Home: \(home)")) }
        if sensitive { lines.append(String(localized: "This scene changes a lock, garage door or security system.")) }
        return lines.joined(separator: "\n")
    }

    #if os(iOS)
    // MARK: - HomeKit

    static func controlConfirmation(_ args: JSONValue) -> String {
        guard let command = try? HomeCommand.parse(args) else {
            return String(localized: "Invalid parameters; nothing will be changed.")
        }
        let found = HomeAccess.loadedAccessory(command.accessoryID)
        let name = found?.name ?? command.accessoryID
        return summary(accessory: name, room: found?.room?.name, sensitive: found.map(isSensitive) ?? true, command: command)
    }

    static func sceneConfirmation(_ args: JSONValue) -> String {
        let id = ToolArgs.string(args, "scene_id") ?? "?"
        guard let found = HomeAccess.loadedScene(id) else {
            return sceneSummary(scene: id, home: nil, sensitive: true)
        }
        return sceneSummary(scene: found.0.name, home: found.1.name, sensitive: touchesSensitive(found.0))
    }

    static func list() async throws -> [ToolContent] {
        let homes = try await HomeAccess.homes()
        let rows = homes.map { home -> JSONValue in
            Output.object([
                ("id", .string(home.uniqueIdentifier.uuidString)),
                ("name", .string(home.name)),
                ("primary", home.isPrimary ? .bool(true) : nil),
                ("rooms", .array(home.rooms.map { .string($0.name) })),
                ("accessories", .array(home.accessories.map(encode))),
                ("scenes", .array(home.actionSets.map { scene in
                    Output.object([("id", .string(scene.uniqueIdentifier.uuidString)), ("name", .string(scene.name)),
                                   ("touches_security", touchesSensitive(scene) ? .bool(true) : nil)])
                })),
            ])
        }
        return Output.json(Output.object([
            ("count", .number(Double(rows.count))),
            ("homes", .array(rows)),
            ("note", .string("Values are the last ones HomeKit reported and may be out of date.")),
        ]))
    }

    static func control(_ args: JSONValue) async throws -> [ToolContent] {
        let command = try HomeCommand.parse(args)
        let homes = try await HomeAccess.homes()
        guard let accessory = homes.lazy.flatMap(\.accessories).first(where: { $0.uniqueIdentifier.uuidString == command.accessoryID }) else {
            throw ToolFailure(.invalidArgument, "no accessory with id \(command.accessoryID); call phone_home_list")
        }
        guard accessory.isReachable else {
            throw ToolFailure(.unavailable, "\(accessory.name) is not responding (offline or out of range); nothing was changed.")
        }
        guard let characteristic = characteristic(of: accessory, for: command.control) else {
            throw ToolFailure(.invalidArgument, "\(accessory.name) has no \(command.control.rawValue) control")
        }
        let value: NSNumber = switch command.control {
        case .power: NSNumber(value: command.number == 1)
        case .targetTemperature: NSNumber(value: command.number)
        default: NSNumber(value: Int(command.number))
        }
        try await write(value, to: characteristic)
        return Output.json(Output.object([
            ("accessory", .string(accessory.name)),
            ("room", Output.opt(accessory.room?.name)),
            ("control", .string(command.control.rawValue)),
            ("value", .number(command.number)),
            ("done", .bool(true)),
        ]))
    }

    static func runScene(_ args: JSONValue) async throws -> [ToolContent] {
        guard let id = ToolArgs.string(args, "scene_id") else { throw ToolFailure(.invalidArgument, "scene_id is required") }
        let homes = try await HomeAccess.homes()
        for home in homes {
            guard let scene = home.actionSets.first(where: { $0.uniqueIdentifier.uuidString == id }) else { continue }
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, any Error>) in
                home.executeActionSet(scene) { @Sendable error in
                    if let error { continuation.resume(throwing: ToolFailure(.unavailable, "the scene did not finish: \(error.localizedDescription)")) } else { continuation.resume() }
                }
            }
            return Output.json(Output.object([("scene", .string(scene.name)), ("home", .string(home.name)), ("done", .bool(true))]))
        }
        throw ToolFailure(.invalidArgument, "no scene with id \(id); call phone_home_list")
    }

    private static func write(_ value: NSNumber, to characteristic: HMCharacteristic) async throws {
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, any Error>) in
            characteristic.writeValue(value) { @Sendable error in
                if let error {
                    continuation.resume(throwing: ToolFailure(.unavailable, "the accessory did not take the change: \(error.localizedDescription)"))
                } else {
                    continuation.resume()
                }
            }
        }
    }

    private static let characteristicTypes: [HomeCommand.Control: String] = [
        .power: HMCharacteristicTypePowerState,
        .brightness: HMCharacteristicTypeBrightness,
        .targetTemperature: HMCharacteristicTypeTargetTemperature,
        .lock: HMCharacteristicTypeTargetLockMechanismState,
        .door: HMCharacteristicTypeTargetDoorState,
    ]

    private static func characteristic(of accessory: HMAccessory, for control: HomeCommand.Control) -> HMCharacteristic? {
        guard let type = characteristicTypes[control] else { return nil }
        return accessory.services.lazy.flatMap(\.characteristics).first { $0.characteristicType == type }
    }

    static func categoryName(_ accessory: HMAccessory) -> String {
        switch accessory.category.categoryType {
        case HMAccessoryCategoryTypeDoorLock: "lock"
        case HMAccessoryCategoryTypeGarageDoorOpener: "garage_door"
        case HMAccessoryCategoryTypeSecuritySystem: "security_system"
        case HMAccessoryCategoryTypeDoor: "door"
        case HMAccessoryCategoryTypeWindow: "window"
        case HMAccessoryCategoryTypeLightbulb: "light"
        case HMAccessoryCategoryTypeOutlet: "outlet"
        case HMAccessoryCategoryTypeSwitch: "switch"
        case HMAccessoryCategoryTypeThermostat: "thermostat"
        case HMAccessoryCategoryTypeAirConditioner: "air_conditioner"
        case HMAccessoryCategoryTypeFan: "fan"
        default: "other"
        }
    }

    static func isSensitive(_ accessory: HMAccessory) -> Bool {
        sensitiveCategories.contains(categoryName(accessory))
            || characteristic(of: accessory, for: .lock) != nil || characteristic(of: accessory, for: .door) != nil
    }

    private static func touchesSensitive(_ scene: HMActionSet) -> Bool {
        scene.actions.contains { action in
            guard let write = action as? HMCharacteristicWriteAction<NSNumber>,
                  let accessory = write.characteristic.service?.accessory else { return false }
            return isSensitive(accessory)
        }
    }

    private static func encode(_ accessory: HMAccessory) -> JSONValue {
        let controls: [JSONValue] = HomeCommand.Control.allCases.compactMap { control in
            guard let c = characteristic(of: accessory, for: control) else { return nil }
            let value: JSONValue? = (c.value as? NSNumber).map { .number($0.doubleValue) }
            return Output.object([("control", .string(control.rawValue)), ("value", value)])
        }
        return Output.object([
            ("id", .string(accessory.uniqueIdentifier.uuidString)),
            ("name", .string(accessory.name)),
            ("room", Output.opt(accessory.room?.name)),
            ("category", .string(categoryName(accessory))),
            ("reachable", .bool(accessory.isReachable)),
            ("security_sensitive", isSensitive(accessory) ? .bool(true) : nil),
            ("controls", controls.isEmpty ? nil : .array(controls)),
        ])
    }
    #endif
}

#if os(iOS)
/// HomeKit's permission and its one manager. Making an `HMHomeManager` shows the Home permission prompt, so the manager is
/// only made once the user turned Home on in Settings (asked), never by a status check.
enum HomeAccess {
    /// The build has the HomeKit capability (Info.plist `PersonaHomeKit`, set with the entitlement; docs/apple/phone-tools.md).
    static var isBuilt: Bool {
        (Bundle.main.object(forInfoDictionaryKey: "PersonaHomeKit") as? String)?.uppercased() == "YES"
    }

    private static let askedKey = "phoneHome.asked"
    private static var loader: HomeLoader?

    private static var asked: Bool {
        get { UserDefaults.standard.bool(forKey: askedKey) }
        set { UserDefaults.standard.set(newValue, forKey: askedKey) }
    }

    private static func manager() -> HomeLoader {
        if let loader { return loader }
        let made = HomeLoader()
        loader = made
        return made
    }

    static func status() -> PhoneAuthStatus {
        guard isBuilt, asked else { return .notDetermined }
        let status = manager().manager.authorizationStatus
        if status.contains(.authorized) { return .granted }
        return status.contains(.determined) ? .denied : .notDetermined
    }

    static func request() async -> PhoneAuthStatus {
        guard isBuilt else { return .notDetermined }
        asked = true
        _ = await manager().ready()
        return status()
    }

    /// The homes, once HomeKit has loaded them.
    static func homes() async throws -> [HMHome] {
        guard isBuilt else { throw ToolFailure(.unavailable, "This build of Persona has no HomeKit capability.") }
        switch status() {
        case .granted, .limited, .notNeeded: break
        case .denied: throw ToolFailure(.denied, "Home")
        case .notDetermined: throw ToolFailure(.denied, "Home access has not been requested yet")
        }
        await manager().ready()
        return manager().manager.homes
    }

    /// An accessory from homes already loaded (the approval card cannot wait for HomeKit).
    static func loadedAccessory(_ id: String) -> HMAccessory? {
        guard let loader, loader.isLoaded else { return nil }
        return loader.manager.homes.lazy.flatMap(\.accessories).first { $0.uniqueIdentifier.uuidString == id }
    }

    static func loadedScene(_ id: String) -> (HMActionSet, HMHome)? {
        guard let loader, loader.isLoaded else { return nil }
        for home in loader.manager.homes {
            if let scene = home.actionSets.first(where: { $0.uniqueIdentifier.uuidString == id }) { return (scene, home) }
        }
        return nil
    }
}

/// Waits for HomeKit's first list of homes (`homeManagerDidUpdateHomes`); HomeKit calls its delegate on the main thread.
final class HomeLoader: NSObject, HMHomeManagerDelegate {
    let manager = HMHomeManager()
    private(set) var isLoaded = false
    private var waiting: [CheckedContinuation<Void, Never>] = []

    override init() {
        super.init()
        manager.delegate = self
    }

    func ready() async {
        if isLoaded { return }
        await withCheckedContinuation { waiting.append($0) }
    }

    nonisolated func homeManagerDidUpdateHomes(_ manager: HMHomeManager) {
        Task { @MainActor in self.loaded() }
    }

    nonisolated func homeManager(_ manager: HMHomeManager, didUpdate status: HMHomeManagerAuthorizationStatus) {
        // Refused: there will be no homes to wait for.
        guard status.contains(.determined) && !status.contains(.authorized) else { return }
        Task { @MainActor in self.loaded() }
    }

    private func loaded() {
        isLoaded = true
        let resumed = waiting
        waiting = []
        resumed.forEach { $0.resume() }
    }
}
#endif
