import CoreGraphics
import Foundation
import Testing
@testable import Persona

/// The avatar chosen on the hub and followed by every device (#2406, docs/apple/persona.md "On the phone"): read from
/// GET /api/v1/looks (an older hub's /api/v1/personas, the choice then this device's own), this device's old choice
/// moved up once with `if_unset`, a choice made here sent with PUT /api/v1/looks/current (taken back when the hub does
/// not take it), another device's `look` push shown once its package is here, an upload's choice not sent twice, and
/// with several hubs, the current one's.
@MainActor
struct AvatarSyncTests {
    private func store() -> (PersonaStore, URL, UserDefaults) {
        let root = FileManager.default.temporaryDirectory.appending(path: "avatar-sync-\(UUID().uuidString)")
        let defaults = UserDefaults(suiteName: "avatar-sync-\(UUID().uuidString)")!
        return (PersonaStore(root: root, defaults: defaults), root, defaults)
    }

    private func pick(_ s: PersonaStore, _ id: String) throws -> Persona {
        try #require(s.offered.first { $0.id == id })
    }

    /// Waits (3 s at most) until `done`: the other task got as far as the case needs, not just some time went by.
    private func until(_ what: String, _ done: () -> Bool, sourceLocation: SourceLocation = #_sourceLocation) async throws {
        for _ in 0..<300 {
            if done() { return }
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(done(), "timed out waiting: \(what)", sourceLocation: sourceLocation)
    }

    // MARK: Reading

    @Test func showsTheHubsChoice() async throws {
        let hub = try LookHub(chosen: "nwverse")
        let (s, root, defaults) = store()
        await s.refresh(hub.client)
        #expect(s.current.id == "nwverse" && s.isOnPhone(s.current) && s.chosenID == "nwverse")
        #expect(hub.fake.sent("GET", "/api/v1/personas").isEmpty) // the looks list has the packages
        #expect(hub.puts.isEmpty) // nothing of this device's to move up
        #expect(defaults.bool(forKey: PersonaStore.followsHubKey))
        // The next launch shows it from disk, before the hub answers.
        #expect(PersonaStore(root: root, defaults: defaults).current.id == "nwverse")

        // Chosen elsewhere while this device heard no push: the next read takes it.
        hub.chosen = "persona"
        await s.refresh(hub.client)
        #expect(s.current.isBuiltin && s.chosenID == "persona" && hub.puts.isEmpty)
    }

    @Test func anOlderHubKeepsTheChoiceOnThisDevice() async throws {
        let hub = FakeHub() // no /api/v1/looks: 404
        hub.on("GET", "/api/v1/personas", json: #"{"personas":[{"id":"nwverse","rev":"r1","bytes":10,"manifest":\#(LookHub.manifest("nwverse"))}]}"#)
        for f in try PersonaTests.decode().files { hub.on("GET", "/api/v1/personas/nwverse/\(f)", json: "png:\(f)") }
        let (s, _, defaults) = store()
        await s.refresh(hub.client)
        #expect(hub.sent("GET", "/api/v1/looks").count == 1 && hub.sent("GET", "/api/v1/personas").count == 1)
        #expect(s.offered.map(\.id) == ["persona", "nwverse"] && s.error == nil)
        await s.choose(try pick(s, "nwverse"), api: hub.client)
        #expect(s.current.id == "nwverse" && defaults.string(forKey: PersonaStore.chosenKey) == "nwverse")
        #expect(hub.sent("PUT", "/api/v1/looks/current").isEmpty)
        #expect(!defaults.bool(forKey: PersonaStore.followsHubKey))
    }

    // MARK: This device's old choice

    @Test func thisDevicesOldChoiceMovesUpOnce() async throws {
        let hub = try LookHub(chosen: nil)
        let (s, _, defaults) = store()
        defaults.set("nwverse", forKey: PersonaStore.chosenKey) // chosen by an older app (or with an older hub)
        await s.refresh(hub.client)
        #expect(hub.puts.count == 1 && hub.puts.first?["id"]?.string == "nwverse" && hub.puts.first?["if_unset"]?.bool == true)
        #expect(hub.chosen == "nwverse" && s.current.id == "nwverse" && s.isOnPhone(s.current))

        await s.refresh(hub.client)
        #expect(hub.puts.count == 1) // once
        // The hub lost it (a new database): this device's copy of it is not sent again.
        hub.chosen = nil
        await s.refresh(hub.client)
        #expect(hub.puts.count == 1 && s.current.isBuiltin && s.chosenID == "persona")
    }

    /// Its package is not on the hub now (404): the default shows, the choice is kept, and it moves up when it is back.
    @Test func anOldChoiceTheHubDoesNotListWaitsForIt() async throws {
        let hub = try LookHub(ids: [], chosen: nil)
        let (s, _, defaults) = store()
        defaults.set("nwverse", forKey: PersonaStore.chosenKey)
        await s.refresh(hub.client)
        #expect(hub.puts.count == 1 && hub.chosen == nil)
        #expect(s.current.isBuiltin && s.chosenID == "nwverse" && !defaults.bool(forKey: PersonaStore.followsHubKey))
        hub.ids = ["nwverse"]
        try hub.serveFiles()
        await s.refresh(hub.client)
        #expect(hub.puts.count == 2 && hub.chosen == "nwverse" && s.current.id == "nwverse" && s.isOnPhone(s.current))
    }

    @Test func anotherDevicesChoiceThatGotThereFirstWins() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora"], chosen: nil)
        let (s, _, defaults) = store()
        defaults.set("nwverse", forKey: PersonaStore.chosenKey)
        // The list says none is chosen; by the time this device's arrives, another device's has.
        hub.afterList = { [unowned hub] in hub.chosen = "aurora" }
        await s.refresh(hub.client)
        #expect(hub.puts.count == 1 && hub.puts.first?["if_unset"]?.bool == true)
        #expect(hub.chosen == "aurora" && s.current.id == "aurora" && s.chosenID == "aurora")
    }

    @Test func nothingChosenHereSendsNothing() async throws {
        for saved in [nil, "persona", "persona-visor"] { // never chosen, the default, a retired drawing (shows the default)
            let hub = try LookHub(chosen: nil)
            let (s, _, defaults) = store()
            if let saved { defaults.set(saved, forKey: PersonaStore.chosenKey) }
            await s.refresh(hub.client)
            #expect(hub.puts.isEmpty && hub.chosen == nil && s.current.isBuiltin)
        }
    }

    // MARK: Choosing here

    @Test func aChoiceGoesToTheHub() async throws {
        let hub = try LookHub(chosen: "persona")
        let (s, _, defaults) = store()
        await s.refresh(hub.client)
        await s.choose(try pick(s, "nwverse"), api: hub.client)
        #expect(hub.puts.map { $0["id"]?.string } == ["nwverse"] && hub.puts[0]["if_unset"] == nil)
        #expect(hub.chosen == "nwverse" && s.current.id == "nwverse" && s.error == nil)
        #expect(defaults.string(forKey: PersonaStore.chosenKey) == "nwverse")
        await s.choose(.builtin, api: hub.client)
        #expect(hub.chosen == "persona" && s.current.isBuiltin && hub.puts.count == 2)
    }

    @Test func aChoiceTheHubDoesNotTakeGoesBack() async throws {
        let hub = try LookHub(chosen: "persona")
        let (s, _, defaults) = store()
        await s.refresh(hub.client)
        let frog = try pick(s, "nwverse")
        let gate = Gate()
        let api = gate.client(hub.fake) { $0.method == "PUT" }
        let choosing = Task { await s.choose(frog, api: api) }
        try await gate.untilHeld()
        #expect(s.current.id == "nwverse") // shown at once, while the hub is asked
        hub.refuse = URLError(.notConnectedToInternet)
        gate.open()
        await choosing.value
        #expect(s.current.isBuiltin && s.chosenID == "persona" && defaults.string(forKey: PersonaStore.chosenKey) == "persona")
        #expect(s.error?.contains("奶蛙") == true)
        #expect(hub.chosen == "persona")

        // The hub no longer lists it (404): the same.
        hub.refuse = nil
        hub.ids = []
        await s.choose(frog, api: hub.client)
        #expect(s.current.isBuiltin && s.chosenID == "persona" && s.error != nil)
        // A choice it takes clears the message.
        await s.choose(.builtin, api: hub.client)
        #expect(s.error == nil)
    }

    /// Two quick choices go one at a time, in order; the first one's push, back while the second waits, changes nothing.
    @Test func quickChoicesReachTheHubInOrder() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora"], chosen: "persona")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        for id in ["nwverse", "aurora", "persona"] { await s.choose(try pick(s, id), api: hub.client) } // both on the phone
        let sent = hub.puts.count
        let frog = try pick(s, "nwverse"), aurora = try pick(s, "aurora")
        let gate = Gate()
        let api = gate.client(hub.fake) { $0.method == "PUT" && String(decoding: $0.body ?? Data(), as: UTF8.self).contains("nwverse") }
        let first = Task { await s.choose(frog, api: api) }
        try await gate.untilHeld()
        let second = Task { await s.choose(aurora, api: api) }
        try await until("the second shown") { s.current.id == "aurora" }
        #expect(s.current.id == "aurora")
        await s.follow("nwverse", effective: "nwverse", api: api)
        #expect(s.current.id == "aurora")
        gate.open()
        await first.value
        await second.value
        #expect(hub.puts.dropFirst(sent).map { $0["id"]?.string } == ["nwverse", "aurora"])
        #expect(hub.chosen == "aurora" && s.current.id == "aurora" && s.error == nil)
    }

    /// The first tap the hub took is what a later refused one goes back to, not the choice from before both.
    @Test func aRefusedChoiceGoesBackToTheOneTheHubTookBeforeIt() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora"], chosen: "persona")
        let (s, _, defaults) = store()
        await s.refresh(hub.client)
        for id in ["nwverse", "aurora", "persona"] { await s.choose(try pick(s, id), api: hub.client) } // both on the phone
        let frog = try pick(s, "nwverse"), aurora = try pick(s, "aurora")
        let gate = Gate()
        let api = gate.client(hub.fake) { $0.method == "PUT" && String(decoding: $0.body ?? Data(), as: UTF8.self).contains("nwverse") }
        let first = Task { await s.choose(frog, api: api) }
        try await gate.untilHeld()
        let second = Task { await s.choose(aurora, api: api) }
        try await until("the second shown") { s.current.id == "aurora" }
        hub.refuse = URLError(.networkConnectionLost)
        hub.refuseOnly = "aurora"
        gate.open()
        await first.value
        await second.value
        #expect(hub.chosen == "nwverse")
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse" && defaults.string(forKey: PersonaStore.chosenKey) == "nwverse")
        #expect(s.error != nil)
    }

    /// The same when the first tap's push comes back before its answer, while the second is still to come: that push
    /// changes nothing on screen, but it is the hub's choice, and what the refused second goes back to.
    @Test func anEarlierTapsPushIsWhatALaterRefusedOneGoesBackTo() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora"], chosen: "persona")
        let (s, _, defaults) = store()
        await s.refresh(hub.client)
        for id in hub.ids { await s.download(try pick(s, id), hub.client) }
        let frog = try pick(s, "nwverse"), aurora = try pick(s, "aurora")
        let gate = Gate()
        let api = gate.client(hub.fake, answerFirst: true) {
            $0.method == "PUT" && String(decoding: $0.body ?? Data(), as: UTF8.self).contains("nwverse")
        }
        let first = Task { await s.choose(frog, api: api) }
        try await gate.untilHeld() // the hub took it; its answer is on the way back
        let second = Task { await s.choose(aurora, api: api) }
        try await until("the second shown") { s.current.id == "aurora" }
        await s.follow("nwverse", effective: "nwverse", api: api) // the first one's push
        #expect(s.current.id == "aurora")
        hub.refuse = URLError(.networkConnectionLost)
        hub.refuseOnly = "aurora"
        gate.open()
        await first.value
        await second.value
        #expect(hub.chosen == "nwverse")
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse" && defaults.string(forKey: PersonaStore.chosenKey) == "nwverse")
        #expect(s.error != nil)
    }

    /// Another device's choice is still downloading when a tap here is refused: the one on screen before the tap comes
    /// back (not the default, and saved, so a launch shows it too), and the hub's shows once its package is here.
    @Test func aRefusedChoiceWaitsForTheHubsOwnStillDownloading() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora", "u-abc12345"], chosen: "nwverse")
        let (s, root, defaults) = store()
        await s.refresh(hub.client) // Milk Frog on screen
        let aurora = try pick(s, "aurora")
        await s.download(aurora, hub.client)
        hub.chosen = "u-abc12345" // on another device
        let files = Gate()
        let slow = files.client(hub.fake) { $0.path.hasPrefix("/api/v1/personas/u-abc12345/") }
        let following = Task { await s.follow("u-abc12345", effective: "u-abc12345", api: slow) }
        try await files.untilHeld()
        hub.refuse = URLError(.networkConnectionLost)
        await s.choose(aurora, api: hub.client)
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse" && s.error != nil)
        #expect(PersonaStore(root: root, defaults: defaults).current.id == "nwverse")
        files.open()
        await following.value
        #expect(s.current.id == "u-abc12345" && s.chosenID == "u-abc12345" && s.isOnPhone(s.current))
        #expect(PersonaStore(root: root, defaults: defaults).current.id == "u-abc12345")
    }

    /// The hub's choice from a list could not be downloaded (the one before stays), then a tap is refused: the one on
    /// screen stays, not the default, also at the next launch.
    @Test func aRefusedChoiceKeepsTheOneOnScreenWhenTheHubsIsNotOnThePhone() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora", "u-abc12345"], chosen: "nwverse")
        let (s, root, defaults) = store()
        await s.refresh(hub.client)
        let mine = try pick(s, "u-abc12345")
        await s.download(mine, hub.client)
        hub.chosen = "aurora" // on another device; its package does not come
        var failing = hub.client
        let answer = failing.transport!
        failing.transport = { req in
            if req.path.hasPrefix("/api/v1/personas/aurora/") { throw URLError(.networkConnectionLost) }
            return try await answer(req)
        }
        await s.refresh(failing)
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse")
        hub.refuse = URLError(.networkConnectionLost)
        await s.choose(mine, api: hub.client)
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse" && s.error != nil)
        #expect(PersonaStore(root: root, defaults: defaults).current.id == "nwverse")
    }

    /// A list read while a tap is on its way says another device chose one this device missed (no push came): the tap
    /// refused goes back to that one, not to what the hub had before.
    @Test func aRefusedChoiceGoesBackToWhatAListReadMeanwhileSaid() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora"], chosen: "persona")
        let (s, _, defaults) = store()
        await s.refresh(hub.client)
        for id in hub.ids { await s.download(try pick(s, id), hub.client) }
        let aurora = try pick(s, "aurora")
        hub.chosen = "nwverse" // on another device; this one missed the push
        let list = Gate(), put = Gate()
        let api = put.client(list.client(hub.fake, answerFirst: true) { $0.path == "/api/v1/looks" }) { $0.method == "PUT" }
        let reading = Task { await s.refresh(api) }
        try await list.untilHeld()
        let choosing = Task { await s.choose(aurora, api: api) }
        try await put.untilHeld()
        list.open()
        await reading.value
        #expect(s.current.id == "aurora")
        hub.refuse = URLError(.networkConnectionLost)
        put.open()
        await choosing.value
        #expect(hub.chosen == "nwverse" && s.error != nil)
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse" && defaults.string(forKey: PersonaStore.chosenKey) == "nwverse")
    }

    /// A list read while a tap is on its way, before the hub has it (the socket came back): the hub's choice from before
    /// the tap does not come back on screen, and the tap stays once the hub takes it.
    @Test func aListReadWhileAChoiceIsOnItsWayDoesNotUndoIt() async throws {
        let hub = try LookHub(ids: ["nwverse"], chosen: "persona")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        let frog = try pick(s, "nwverse")
        await s.download(frog, hub.client)
        var shown: [String] = []
        s.onChange = { shown.append(s.current.id) }
        let gate = Gate()
        let api = gate.client(hub.fake) { $0.method == "PUT" }
        let choosing = Task { await s.choose(frog, api: api) }
        try await gate.untilHeld()
        await s.refresh(hub.client) // it lists the default still
        #expect(s.current.id == "nwverse")
        gate.open()
        await choosing.value
        #expect(hub.chosen == "nwverse" && s.current.id == "nwverse" && s.chosenID == "nwverse" && shown == ["nwverse"])
    }

    /// Three quick choices while the first is on its way: the first goes, then only the newest; the one between is
    /// never sent.
    @Test func onlyTheNewestWaitingChoiceIsSent() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora", "u-abc12345"], chosen: "persona")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        for id in hub.ids { await s.download(try pick(s, id), hub.client) }
        let frog = try pick(s, "nwverse"), aurora = try pick(s, "aurora"), mine = try pick(s, "u-abc12345")
        let gate = Gate()
        let api = gate.client(hub.fake) { $0.method == "PUT" && String(decoding: $0.body ?? Data(), as: UTF8.self).contains("nwverse") }
        let first = Task { await s.choose(frog, api: api) }
        try await gate.untilHeld()
        let second = Task { await s.choose(aurora, api: api) }
        try await until("the second shown") { s.current.id == "aurora" }
        let third = Task { await s.choose(mine, api: api) }
        try await until("the third shown") { s.current.id == "u-abc12345" }
        gate.open()
        await first.value
        await second.value
        await third.value
        #expect(hub.puts.map { $0["id"]?.string } == ["nwverse", "u-abc12345"])
        #expect(hub.chosen == "u-abc12345" && s.current.id == "u-abc12345" && s.error == nil)
    }

    /// The first tap's push, back while a later tap's package is still downloading, does not drop the later one: it
    /// shows once its package is here and goes to the hub.
    @Test func anEarlierChoicesPushDoesNotDropALaterOneStillDownloading() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora"], chosen: "persona")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        let frog = try pick(s, "nwverse"), aurora = try pick(s, "aurora")
        await s.download(frog, hub.client) // only Milk Frog on the phone
        let answer = Gate(), files = Gate()
        let taken = answer.client(hub.fake, answerFirst: true) {
            $0.method == "PUT" && String(decoding: $0.body ?? Data(), as: UTF8.self).contains("nwverse")
        }
        let api = files.client(taken) { $0.path.hasPrefix("/api/v1/personas/aurora/") }
        let first = Task { await s.choose(frog, api: api) }
        try await answer.untilHeld() // the hub took it; its answer is on the way back
        let second = Task { await s.choose(aurora, api: api) }
        try await files.untilHeld()
        await s.follow("nwverse", effective: "nwverse", api: api) // the first one's push
        answer.open()
        await first.value
        files.open()
        await second.value
        #expect(hub.puts.map { $0["id"]?.string } == ["nwverse", "aurora"])
        #expect(hub.chosen == "aurora" && s.current.id == "aurora" && s.chosenID == "aurora")
    }

    /// The hub refuses a tap while a later one's package is still downloading, and that download fails too (either
    /// first): what the hub has comes back, not the refused one.
    @Test func aRefusedChoiceGoesBackWhileALaterOneIsStillDownloading() async throws {
        for refusedFirst in [true, false] {
            let hub = try LookHub(ids: ["nwverse", "aurora"], chosen: "persona")
            let (s, _, defaults) = store()
            await s.refresh(hub.client)
            let frog = try pick(s, "nwverse"), aurora = try pick(s, "aurora")
            await s.download(frog, hub.client)
            let put = Gate(), files = Gate()
            let held = put.client(hub.fake) { $0.method == "PUT" }
            let api = files.client(held) { $0.path.hasPrefix("/api/v1/personas/aurora/") }
            let first = Task { await s.choose(frog, api: api) }
            try await put.untilHeld()
            let second = Task { await s.choose(aurora, api: api) }
            try await files.untilHeld()
            hub.refuse = URLError(.networkConnectionLost)
            if refusedFirst {
                put.open()
                await first.value
                files.open(failing: URLError(.networkConnectionLost))
                await second.value
            } else {
                files.open(failing: URLError(.networkConnectionLost))
                await second.value
                put.open()
                await first.value
            }
            #expect(hub.chosen == "persona")
            #expect(s.current.isBuiltin && s.chosenID == "persona" && defaults.string(forKey: PersonaStore.chosenKey) == "persona",
                    "refused first: \(refusedFirst)")
            #expect(s.error != nil)
        }
    }

    /// The answer to a choice the hub took, late behind the pushes that came after it (its own, then another device's):
    /// what a later refused choice goes back to is the newest of them, not that answer.
    @Test func aLateAnswerDoesNotOutdoAPushThatCameAfterIt() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora", "u-abc12345"], chosen: "persona")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        for id in hub.ids { await s.download(try pick(s, id), hub.client) }
        let frog = try pick(s, "nwverse"), mine = try pick(s, "u-abc12345")
        let gate = Gate()
        let api = gate.client(hub.fake, answerFirst: true) { $0.method == "PUT" }
        let choosing = Task { await s.choose(frog, api: api) }
        try await gate.untilHeld()
        await s.follow("nwverse", effective: "nwverse", api: api) // its push
        hub.chosen = "aurora" // then another device's
        await s.follow("aurora", effective: "aurora", api: api)
        #expect(s.current.id == "aurora")
        gate.open()
        await choosing.value
        #expect(s.current.id == "aurora")
        hub.refuse = URLError(.networkConnectionLost)
        await s.choose(mine, api: hub.client)
        #expect(s.current.id == "aurora" && s.chosenID == "aurora" && hub.chosen == "aurora")
    }

    /// Another device's choice the hub took before this one's PUT, pushed while that PUT's answer is on its way: the
    /// answer is the newer, and what a later refused choice goes back to.
    @Test func aLateAnswerOutdoesAPushFromBeforeIt() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora", "u-abc12345"], chosen: "persona")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        for id in hub.ids { await s.download(try pick(s, id), hub.client) }
        let frog = try pick(s, "nwverse"), mine = try pick(s, "u-abc12345")
        let gate = Gate()
        let api = gate.client(hub.fake, answerFirst: true) { $0.method == "PUT" }
        let choosing = Task { await s.choose(frog, api: api) }
        try await gate.untilHeld()
        await s.follow("aurora", effective: "aurora", api: api) // taken just before this one's; its own push is still to come
        gate.open()
        await choosing.value
        #expect(hub.chosen == "nwverse")
        hub.refuse = URLError(.networkConnectionLost)
        await s.choose(mine, api: hub.client)
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse")
    }

    /// Two quick taps the hub refuses both, before the old local choice moved up: it stays this device's, and moves up
    /// at the next read.
    @Test func twoRefusedChoicesKeepTheOldLocalChoice() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora", "u-abc12345"], chosen: nil)
        let (s, _, defaults) = store()
        defaults.set("nwverse", forKey: PersonaStore.chosenKey) // chosen by an older app
        hub.refuse = URLError(.notConnectedToInternet)
        await s.refresh(hub.client) // the if_unset does not get there
        #expect(s.current.id == "nwverse" && !defaults.bool(forKey: PersonaStore.followsHubKey))
        for id in ["aurora", "u-abc12345"] { await s.download(try pick(s, id), hub.client) }
        let aurora = try pick(s, "aurora"), mine = try pick(s, "u-abc12345")
        let gate = Gate()
        let api = gate.client(hub.fake) { $0.method == "PUT" && String(decoding: $0.body ?? Data(), as: UTF8.self).contains("aurora") }
        let first = Task { await s.choose(aurora, api: api) }
        try await gate.untilHeld()
        let second = Task { await s.choose(mine, api: api) }
        try await until("the second shown") { s.current.id == "u-abc12345" }
        gate.open()
        await first.value
        await second.value
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse" && !defaults.bool(forKey: PersonaStore.followsHubKey))
        hub.refuse = nil
        await s.refresh(hub.client)
        #expect(hub.chosen == "nwverse" && hub.puts.last?["id"]?.string == "nwverse" && hub.puts.last?["if_unset"]?.bool == true)
        #expect(s.current.id == "nwverse")
    }

    /// A refused choice leaves the old local choice as it was: still this device's, still moved up once the hub takes it.
    @Test func aRefusedChoiceKeepsTheOldLocalChoiceToMoveUp() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora"], chosen: nil)
        let (s, _, defaults) = store()
        defaults.set("nwverse", forKey: PersonaStore.chosenKey) // chosen by an older app
        hub.refuse = URLError(.notConnectedToInternet)
        await s.refresh(hub.client) // the if_unset does not get there
        #expect(hub.chosen == nil && s.current.id == "nwverse" && !defaults.bool(forKey: PersonaStore.followsHubKey))
        await s.choose(try pick(s, "aurora"), api: hub.client) // nor does this
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse" && s.error != nil)
        #expect(!defaults.bool(forKey: PersonaStore.followsHubKey))
        hub.refuse = nil
        await s.refresh(hub.client)
        #expect(hub.chosen == "nwverse" && hub.puts.last?["if_unset"]?.bool == true)
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse")
    }

    // MARK: Another device's

    /// Saved once its package is all here: a launch while it downloads still shows the one on screen, not the default.
    @Test func theHubsChoiceIsSavedOnceItsPackageIsHere() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora"], chosen: "nwverse")
        let (s, root, defaults) = store()
        await s.refresh(hub.client)
        #expect(s.current.id == "nwverse")

        // A list read brings another device's choice.
        hub.chosen = "aurora"
        let gate = Gate()
        let api = gate.client(hub.fake) { $0.path.hasPrefix("/api/v1/personas/aurora/") }
        let reading = Task { await s.refresh(api) }
        try await gate.untilHeld()
        #expect(s.current.id == "nwverse")
        let relaunched = PersonaStore(root: root, defaults: defaults) // killed while it downloads
        #expect(relaunched.current.id == "nwverse")
        gate.open()
        await reading.value
        #expect(s.current.id == "aurora" && s.chosenID == "aurora")
        #expect(PersonaStore(root: root, defaults: defaults).current.id == "aurora")
    }

    /// A launch while the next choice downloads shows the one on screen, also when the list read since no longer has
    /// it (another hub's) or has a new rev of it.
    @Test func aLaunchMidDownloadShowsTheOneOnScreenWhateverTheListSaysNow() async throws {
        let home = try LookHub(url: "https://home.invalid", ids: ["nwverse"], chosen: "nwverse")
        let work = try LookHub(url: "https://work.invalid", ids: ["aurora"], chosen: "aurora")
        let (s, root, defaults) = store()
        await s.refresh(home.client)
        var gate = Gate()
        let slowWork = gate.client(work.fake) { $0.path.hasPrefix("/api/v1/personas/aurora/") }
        let switching = Task { await s.refresh(slowWork) }
        try await gate.untilHeld()
        #expect(PersonaStore(root: root, defaults: defaults).current.id == "nwverse")
        gate.open()
        await switching.value
        #expect(s.current.id == "aurora" && PersonaStore(root: root, defaults: defaults).current.id == "aurora")

        // A new rev of the one on screen.
        await s.refresh(home.client)
        #expect(s.current.id == "nwverse" && s.current.rev == "r1")
        home.rev = "r2"
        gate = Gate()
        let slowHome = gate.client(home.fake) { $0.path.hasPrefix("/api/v1/personas/nwverse/") }
        let reading = Task { await s.refresh(slowHome) }
        try await gate.untilHeld()
        let relaunched = PersonaStore(root: root, defaults: defaults)
        #expect(relaunched.current.id == "nwverse" && relaunched.current.rev == "r1" && relaunched.isOnPhone(relaunched.current))
        gate.open()
        await reading.value
        #expect(s.current.rev == "r2" && PersonaStore(root: root, defaults: defaults).current.rev == "r2")
    }

    /// The same, for the `look` push.
    @Test func aPushedChoiceIsSavedOnceItsPackageIsHere() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora"], chosen: "nwverse")
        let (s, root, defaults) = store()
        await s.refresh(hub.client)
        hub.chosen = "aurora"
        let gate = Gate()
        let api = gate.client(hub.fake) { $0.path.hasPrefix("/api/v1/personas/aurora/") }
        let following = Task { await s.follow("aurora", effective: "aurora", api: api) }
        try await gate.untilHeld()
        #expect(PersonaStore(root: root, defaults: defaults).current.id == "nwverse")
        gate.open()
        await following.value
        #expect(s.current.id == "aurora" && PersonaStore(root: root, defaults: defaults).current.id == "aurora")
    }

    /// The push comes twice (the hub broadcasts every PUT, the same one too) while the package downloads: it shows.
    @Test func aPushRepeatedWhileItsPackageDownloadsShows() async throws {
        let hub = try LookHub(chosen: "persona")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        hub.chosen = "nwverse"
        let gate = Gate()
        let api = gate.client(hub.fake) { $0.path.hasSuffix("/dot/idle.png") }
        let first = Task { await s.follow("nwverse", effective: "nwverse", api: api) }
        try await gate.untilHeld()
        let pushed = s.choices
        let second = Task { await s.follow("nwverse", effective: "nwverse", api: api) }
        try await until("the second push waits for the same download") { s.choices > pushed }
        #expect(s.current.isBuiltin)
        gate.open()
        await first.value
        await second.value
        #expect(s.current.id == "nwverse" && s.isOnPhone(s.current) && s.chosenID == "nwverse")
    }

    /// A list asked for before the push, without the look pushed, never puts the default on screen in between.
    @Test func aListOlderThanThePushDoesNotShowTheDefault() async throws {
        let hub = try LookHub(chosen: "nwverse")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        #expect(s.current.id == "nwverse")
        var shown: [String] = []
        s.onChange = { shown.append(s.current.id) }
        let gate = Gate()
        let stale = gate.client(hub.fake, answerFirst: true) { $0.path == "/api/v1/looks" }
        let reading = Task { await s.refresh(stale) } // its list is the one before
        try await gate.untilHeld()
        hub.ids = ["nwverse", "u-abc12345"] // made and chosen on another device
        hub.chosen = "u-abc12345"
        try hub.serveFiles()
        let pushed = s.choices
        let following = Task { await s.follow("u-abc12345", effective: "u-abc12345", api: hub.client) }
        try await until("the push waits for the list under way") { s.choices > pushed }
        gate.open()
        await reading.value
        await following.value
        #expect(shown == ["u-abc12345"])
        #expect(s.current.id == "u-abc12345" && s.chosenID == "u-abc12345")
    }

    /// A list asked for before another device's choice was pushed answers after the push showed it: the list's older
    /// choice does not come back.
    @Test func aListOlderThanAPushThatShowedDoesNotPutTheOldOneBack() async throws {
        let hub = try LookHub(ids: ["nwverse", "aurora"], chosen: "aurora")
        let (s, root, defaults) = store()
        await s.refresh(hub.client)
        await s.download(try pick(s, "nwverse"), hub.client)
        #expect(s.current.id == "aurora")
        let gate = Gate()
        let stale = gate.client(hub.fake, answerFirst: true) { $0.path == "/api/v1/looks" }
        let reading = Task { await s.refresh(stale) }
        try await gate.untilHeld() // it says Aurora
        hub.chosen = "nwverse" // on another device
        await s.follow("nwverse", effective: "nwverse", api: hub.client)
        #expect(s.current.id == "nwverse")
        gate.open()
        await reading.value
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse")
        #expect(PersonaStore(root: root, defaults: defaults).current.id == "nwverse")
    }

    @Test func anotherDevicesChoiceShowsOnceItsPackageIsHere() async throws {
        let hub = try LookHub(chosen: "persona")
        let (s, root, defaults) = store()
        await s.refresh(hub.client)
        #expect(s.current.isBuiltin)
        hub.chosen = "nwverse" // on another device
        let gate = Gate()
        let api = gate.client(hub.fake) { $0.path.hasSuffix("/dot/idle.png") }
        let following = Task { await s.follow("nwverse", effective: "nwverse", api: api) }
        try await gate.untilHeld()
        #expect(s.current.isBuiltin) // never half a package
        #expect(!FileManager.default.fileExists(atPath: root.appending(path: "nwverse").appending(path: "r1").path(percentEncoded: false)))
        gate.open()
        await following.value
        #expect(s.current.id == "nwverse" && s.isOnPhone(s.current) && s.chosenID == "nwverse")
        #expect(defaults.string(forKey: PersonaStore.chosenKey) == "nwverse")
        #expect(hub.puts.isEmpty) // followed, not sent back

        // The hub went back to the default (the chosen one's extension is off): at once.
        await s.follow("nwverse", effective: "persona", api: hub.client)
        #expect(s.current.isBuiltin && s.chosenID == "persona")
    }

    @Test func aPushForALookNotListedHereYetReadsTheList() async throws {
        let hub = try LookHub(ids: [], chosen: "persona")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        // Made from an image and chosen on another device.
        hub.ids = ["u-abc12345"]
        hub.chosen = "u-abc12345"
        try hub.serveFiles()
        await s.follow("u-abc12345", effective: "u-abc12345", api: hub.client)
        #expect(s.current.id == "u-abc12345" && s.isOnPhone(s.current))
        #expect(hub.puts.isEmpty)
    }

    // MARK: Uploads

    /// The upload's `choose` chose it on the hub, and a delete of the chosen one brings the default back there: neither
    /// is sent again.
    @Test func anUploadOrADeleteIsNotSentAgain() async throws {
        let hub = try LookHub(ids: [], chosen: "persona")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        let model = AvatarUploadModel(image: Self.image(), deps: .init(api: { hub.client }, store: { s }))
        model.name = "豆豆"
        hub.fake.on("POST", "/api/v1/looks/uploads") { [unowned hub] _ in
            hub.ids = ["u-abc12345"]
            hub.chosen = "u-abc12345"
            try hub.serveFiles()
            return Data(#"{"look":{"id":"u-abc12345","source":"hub","name":"奶蛙","app_icon":"persona","rev":"r1","bytes":10,"uploaded":true},"current":{"id":"u-abc12345","effective":"u-abc12345"}}"#.utf8)
        }
        #expect(await model.upload())
        #expect(s.current.id == "u-abc12345" && s.chosenID == "u-abc12345")
        // Its push comes too: nothing more.
        await s.follow("u-abc12345", effective: "u-abc12345", api: hub.client)
        #expect(hub.puts.isEmpty && s.current.id == "u-abc12345")

        let uploads = AvatarUploadsModel(deps: .init(api: { hub.client }, features: { [AvatarUploadsModel.feature] }, isDemo: { false },
                                                     store: { s }))
        hub.fake.on("DELETE", "/api/v1/looks/uploads/u-abc12345") { [unowned hub] _ in
            hub.ids = []
            hub.chosen = "persona"
            return Data(#"{"current":{"id":"persona","effective":"persona"}}"#.utf8)
        }
        #expect(await uploads.delete(try pick(s, "u-abc12345")))
        #expect(s.current.isBuiltin && hub.puts.isEmpty)
    }

    /// The upload's answer comes late, after the hub's push for it and another device's choice since: the newer one
    /// stays, and the upload is done (the avatar is on this device).
    @Test func anUploadAnsweredLateDoesNotPutBackWhatTheHubReplaced() async throws {
        let hub = try LookHub(ids: ["nwverse"], chosen: "persona")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        await s.download(try pick(s, "nwverse"), hub.client)
        hub.fake.on("POST", "/api/v1/looks/uploads") { [unowned hub] _ in
            hub.ids = ["nwverse", "u-abc12345"]
            hub.chosen = "u-abc12345"
            try hub.serveFiles()
            return Data(#"{"look":{"id":"u-abc12345","source":"hub","name":"奶蛙","app_icon":"persona","rev":"r1","bytes":10,"uploaded":true},"current":{"id":"u-abc12345","effective":"u-abc12345"}}"#.utf8)
        }
        let gate = Gate()
        let slow = gate.client(hub.fake, answerFirst: true) { $0.method == "POST" }
        let model = AvatarUploadModel(image: Self.image(), deps: .init(api: { slow }, store: { s }))
        model.name = "豆豆"
        let uploading = Task { await model.upload() }
        try await gate.untilHeld() // made and chosen on the hub; the answer is on its way
        await s.follow("u-abc12345", effective: "u-abc12345", api: hub.client) // its push
        #expect(s.current.id == "u-abc12345")
        hub.chosen = "nwverse" // another device's, after it
        await s.follow("nwverse", effective: "nwverse", api: hub.client)
        gate.open()
        #expect(await uploading.value)
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse" && hub.chosen == "nwverse" && hub.puts.isEmpty)
    }

    /// The upload's answer comes late, after its push and another device's choice whose package is still downloading:
    /// that one still shows once its package is here, and the upload is done.
    @Test func anUploadAnsweredLateDoesNotDropANewerChoiceStillDownloading() async throws {
        let hub = try LookHub(ids: ["nwverse"], chosen: "persona")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        hub.fake.on("POST", "/api/v1/looks/uploads") { [unowned hub] _ in
            hub.ids = ["nwverse", "u-abc12345"]
            hub.chosen = "u-abc12345"
            try hub.serveFiles()
            return Data(#"{"look":{"id":"u-abc12345","source":"hub","name":"奶蛙","app_icon":"persona","rev":"r1","bytes":10,"uploaded":true},"current":{"id":"u-abc12345","effective":"u-abc12345"}}"#.utf8)
        }
        let gate = Gate()
        let slow = gate.client(hub.fake, answerFirst: true) { $0.method == "POST" }
        let model = AvatarUploadModel(image: Self.image(), deps: .init(api: { slow }, store: { s }))
        model.name = "豆豆"
        let uploading = Task { await model.upload() }
        try await gate.untilHeld() // made and chosen on the hub; the answer is on its way
        await s.follow("u-abc12345", effective: "u-abc12345", api: hub.client) // its push
        #expect(s.current.id == "u-abc12345")
        hub.chosen = "nwverse" // another device's, after it; its package is still coming
        let files = Gate()
        let slowFiles = files.client(hub.fake) { $0.path.hasPrefix("/api/v1/personas/nwverse/") }
        let following = Task { await s.follow("nwverse", effective: "nwverse", api: slowFiles) }
        try await files.untilHeld()
        gate.open()
        #expect(await uploading.value)
        files.open()
        await following.value
        #expect(s.current.id == "nwverse" && s.chosenID == "nwverse" && hub.chosen == "nwverse" && hub.puts.isEmpty)
    }

    // MARK: Several hubs

    /// A choice made here with no hub while the old local choice is on its way up: the old one moved up, never to a
    /// second hub, and the new one is this device's own, moved up to the next hub that keeps the choice.
    @Test func aChoiceMadeWithNoHubWhileTheOldOneMovesUpMovesUpNext() async throws {
        let home = try LookHub(url: "https://home.invalid", ids: ["nwverse", "aurora"], chosen: "aurora")
        let work = try LookHub(url: "https://work.invalid", ids: ["nwverse", "aurora"], chosen: nil)
        let (before, root, _) = store()
        await before.refresh(home.client) // Aurora on the phone, and the list kept, from another time
        home.chosen = nil
        let defaults = UserDefaults(suiteName: "avatar-sync-\(UUID().uuidString)")!
        defaults.set("nwverse", forKey: PersonaStore.chosenKey) // chosen by an older app
        let s = PersonaStore(root: root, defaults: defaults)
        let gate = Gate()
        let slowHome = gate.client(home.fake) { $0.method == "PUT" }
        let moving = Task { await s.refresh(slowHome) }
        try await gate.untilHeld()
        await s.choose(try pick(s, "aurora"), api: nil) // with no hub paired meanwhile
        #expect(s.current.id == "aurora")
        gate.open()
        await moving.value
        #expect(home.chosen == "nwverse" && home.puts.count == 1)
        #expect(s.current.id == "aurora" && s.chosenID == "aurora" && !defaults.bool(forKey: PersonaStore.followsHubKey))
        await s.refresh(work.client)
        #expect(!work.puts.contains { $0["id"]?.string == "nwverse" })
        #expect(work.puts.map { $0["id"]?.string } == ["aurora"] && work.puts.first?["if_unset"]?.bool == true)
        #expect(work.chosen == "aurora" && s.current.id == "aurora" && defaults.bool(forKey: PersonaStore.followsHubKey))
    }

    /// The app model's hub-level group's hub is `api`'s (`AppModel.connectOnly`), its requests answered by `api`'s transport.
    private func connect(_ model: AppModel, _ api: APIClient) {
        model.linkHooksForTests = (transport: api.transport!, socket: { SyncSocket() })
        model.connectOnly(api.config)
    }

    /// Another group's hub becomes the current one (`AppModel.hubChanged`) and a card is tapped before its list is read
    /// (the assistant's settings are read first): the choice goes to the new hub, not dropped.
    @Test func aChoiceRightAfterTheHubChangedGoesToTheNewHub() async throws {
        let home = try LookHub(url: "https://home.invalid", ids: ["nwverse", "aurora"], chosen: "nwverse")
        let work = try LookHub(url: "https://work.invalid", ids: ["nwverse", "aurora"], chosen: "persona")
        let (s, _, _) = store()
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "avatar-sync-model-\(UUID().uuidString)"),
                             personas: s)
        defer { for link in model.links { link.live.disconnect() } }
        connect(model, home.client)
        await s.refresh(model.api)
        let aurora = try pick(s, "aurora")
        await s.download(aurora, home.client)
        await s.refresh(work.client) // work's group was the current one before, this launch
        await s.refresh(home.client)
        #expect(s.current.id == "nwverse")
        connect(model, work.client)
        model.hubChanged()
        await s.choose(aurora, api: model.api)
        #expect(work.puts.map { $0["id"]?.string } == ["aurora"] && s.current.id == "aurora")
        await s.refresh(work.client)
        #expect(work.chosen == "aurora" && home.chosen == "nwverse" && s.current.id == "aurora" && s.chosenID == "aurora")
    }

    /// A `look` push from the current group's hub still waiting to be handled when another group's became the current
    /// one: it is the hub before's, and never shows as the new one's.
    @Test func aPushHandledAfterTheHubChangedIsNotTheNewHubs() async throws {
        let home = try LookHub(url: "https://home.invalid", ids: ["nwverse", "aurora"], chosen: "nwverse")
        let work = try LookHub(url: "https://work.invalid", ids: ["nwverse", "aurora"], chosen: "persona")
        let (s, _, _) = store()
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "avatar-sync-model-\(UUID().uuidString)"),
                             personas: s)
        defer { for link in model.links { link.live.disconnect() } }
        connect(model, home.client)
        await s.refresh(model.api)
        await s.download(try pick(s, "aurora"), home.client)
        var shown: [String] = []
        s.onChange = { shown.append(s.current.id) }
        let gate = Gate()
        let slowWork = gate.client(work.fake) { $0.path == "/api/v1/looks" }
        let ev = try #require(ServerEvent.decode(Data(#"{"type":"look","id":"aurora","effective":"aurora"}"#.utf8)))
        model.handle(ev.stamped(""), group: "") // the current group's, handled in a moment
        connect(model, slowWork)
        model.hubChanged()
        try await gate.untilHeld() // work's list is being read
        #expect(shown.isEmpty && s.current.id == "nwverse")
        gate.open()
        try await until("work's choice shown") { s.current.isBuiltin }
        #expect(shown == ["persona"] && s.chosenID == "persona" && home.puts.isEmpty && work.puts.isEmpty)
    }

    /// The avatar is a hub-level setting (docs/protocol/multi-hub.md): the current hub's shows, a choice goes to it alone,
    /// and this device's old choice moves up only to the first hub that keeps the choice.
    @Test func withSeveralHubsTheCurrentOnesShows() async throws {
        let home = try LookHub(url: "https://home.invalid", chosen: nil)
        let work = try LookHub(url: "https://work.invalid", ids: ["nwverse", "aurora"], chosen: nil)
        let (s, _, defaults) = store()
        defaults.set("nwverse", forKey: PersonaStore.chosenKey) // chosen by an older app
        await s.refresh(home.client)
        #expect(home.chosen == "nwverse" && s.current.id == "nwverse")

        // Work's group becomes the current hub: its choice (none yet: the default); home's is not sent there.
        await s.refresh(work.client)
        #expect(work.puts.isEmpty && work.chosen == nil && s.current.isBuiltin && s.chosenID == "persona")
        await s.choose(try pick(s, "aurora"), api: work.client)
        #expect(work.chosen == "aurora" && home.chosen == "nwverse" && home.puts.count == 1)

        // Back to home's: home's.
        await s.refresh(home.client)
        #expect(s.current.id == "nwverse" && home.puts.count == 1)

        // The current hub changed while the one before was still answering: that answer never shows.
        await s.refresh(work.client)
        #expect(s.current.id == "aurora")
        var changes = 0
        s.onChange = { changes += 1 }
        let gate = Gate()
        let slowHome = gate.client(home.fake) { $0.path == "/api/v1/looks" }
        let before = Task { await s.refresh(slowHome) }
        try await gate.untilHeld()
        let now = Task { await s.refresh(work.client) }
        try await until("work's hub is the one read") { s.hub == work.fake.url }
        gate.open()
        await before.value
        await now.value
        #expect(s.current.id == "aurora" && s.chosenID == "aurora" && changes == 0)
    }

    /// The current hub changes while the old choice is on its way to the first: it is not moved up to the second too.
    @Test func theOldChoiceOnItsWayToOneHubIsNotSentToTheNext() async throws {
        let home = try LookHub(url: "https://home.invalid", chosen: nil)
        let work = try LookHub(url: "https://work.invalid", chosen: nil)
        let (s, _, defaults) = store()
        defaults.set("nwverse", forKey: PersonaStore.chosenKey) // chosen by an older app
        let gate = Gate()
        let slowHome = gate.client(home.fake) { $0.method == "PUT" }
        let before = Task { await s.refresh(slowHome) }
        try await gate.untilHeld()
        let now = Task { await s.refresh(work.client) }
        try await until("work's hub is the one read") { s.hub == work.fake.url }
        gate.open()
        await before.value
        await now.value
        #expect(home.chosen == "nwverse" && home.puts.count == 1)
        #expect(work.puts.isEmpty && work.chosen == nil && s.current.isBuiltin)
    }

    /// A push from the hub before, its package still coming when another group's hub became the current one: it never
    /// shows, nor is saved for the next launch.
    @Test func aPushFromTheHubBeforeDoesNotShowAfterTheSwitch() async throws {
        let home = try LookHub(url: "https://home.invalid", ids: ["nwverse", "aurora"], chosen: "persona")
        let work = try LookHub(url: "https://work.invalid", ids: ["nwverse", "aurora"], chosen: "aurora")
        let (s, root, defaults) = store()
        await s.refresh(home.client)
        home.chosen = "nwverse" // on another device
        let gate = Gate()
        let slowHome = gate.client(home.fake) { $0.path.hasPrefix("/api/v1/personas/nwverse/") }
        let following = Task { await s.follow("nwverse", effective: "nwverse", api: slowHome) }
        try await gate.untilHeld()
        await s.refresh(work.client)
        #expect(s.current.id == "aurora")
        gate.open()
        await following.value
        #expect(s.current.id == "aurora" && s.chosenID == "aurora")
        #expect(PersonaStore(root: root, defaults: defaults).current.id == "aurora")
    }

    /// A choice made with the hub before, its package still coming when another group's hub became the current one:
    /// it never shows, it is not sent, and the store still reads the new hub.
    @Test func aChoiceForTheHubBeforeStillDownloadingIsDropped() async throws {
        let home = try LookHub(url: "https://home.invalid", ids: ["nwverse", "aurora"], chosen: "persona")
        let work = try LookHub(url: "https://work.invalid", ids: ["nwverse", "aurora"], chosen: "aurora")
        let (s, _, _) = store()
        await s.refresh(home.client)
        let frog = try pick(s, "nwverse")
        let gate = Gate()
        let slowHome = gate.client(home.fake) { $0.path.hasPrefix("/api/v1/personas/nwverse/") }
        let choosing = Task { await s.choose(frog, api: slowHome) }
        try await gate.untilHeld()
        await s.refresh(work.client)
        gate.open()
        await choosing.value
        #expect(s.current.id == "aurora" && s.chosenID == "aurora" && home.puts.isEmpty && home.chosen == "persona")
        // Work's is still the hub read: a choice it refuses goes back to work's own.
        work.refuse = URLError(.networkConnectionLost)
        await s.choose(.builtin, api: work.client)
        #expect(s.current.id == "aurora" && s.chosenID == "aurora")
    }

    /// What a hub that is no longer the current one answers a choice made on it does not change the current hub's.
    @Test func theHubBeforeAnsweringLateChangesNothingHere() async throws {
        let home = try LookHub(url: "https://home.invalid", ids: ["nwverse", "aurora"], chosen: "persona")
        let work = try LookHub(url: "https://work.invalid", ids: ["nwverse", "aurora"], chosen: "aurora")
        let (s, _, _) = store()
        await s.refresh(home.client)
        for id in ["nwverse", "aurora", "persona"] { await s.choose(try pick(s, id), api: home.client) } // both on the phone
        let frog = try pick(s, "nwverse")

        // Refused, after work's became the current hub.
        var gate = Gate()
        var slowHome = gate.client(home.fake) { $0.method == "PUT" }
        var choosing = Task { await s.choose(frog, api: slowHome) }
        try await gate.untilHeld()
        await s.refresh(work.client)
        #expect(s.current.id == "aurora")
        home.refuse = URLError(.networkConnectionLost)
        gate.open()
        await choosing.value
        #expect(s.current.id == "aurora" && s.chosenID == "aurora" && home.chosen == "persona")

        // Taken, after work's became the current hub: what a refused choice on work's goes back to is still work's.
        home.refuse = nil
        await s.refresh(home.client)
        #expect(s.current.isBuiltin)
        gate = Gate()
        slowHome = gate.client(home.fake) { $0.method == "PUT" }
        choosing = Task { await s.choose(frog, api: slowHome) }
        try await gate.untilHeld()
        await s.refresh(work.client)
        gate.open()
        await choosing.value
        #expect(home.chosen == "nwverse" && s.current.id == "aurora")
        work.refuse = URLError(.networkConnectionLost)
        await s.choose(.builtin, api: work.client)
        #expect(work.chosen == "aurora" && s.current.id == "aurora" && s.chosenID == "aurora")
    }

    // MARK: Demo Mode and the push

    @Test func demoModeReadsAndSendsNothing() async throws {
        let hub = try LookHub(url: "https://demo.invalid", chosen: "nwverse")
        let (s, _, _) = store()
        await s.refresh(hub.client)
        await s.follow("nwverse", effective: "nwverse", api: hub.client)
        await s.choose(.builtin, api: hub.client)
        #expect(hub.fake.requests.isEmpty && s.current.isBuiltin)
    }

    /// The `look` push as the app gets it (`AppModel.handle`): the current group's is followed, another group's is not
    /// (its choice shows when that group is the current one).
    @Test func onlyTheCurrentGroupsLookPushIsFollowed() async throws {
        let hub = try LookHub(url: "https://home.invalid", ids: ["nwverse", "aurora"], chosen: "nwverse")
        let (s, _, _) = store()
        let folder = FileManager.default.temporaryDirectory.appending(path: "avatar-sync-model-\(UUID().uuidString)")
        let model = AppModel(stateFolder: folder, personas: s)
        model.linkHooksForTests = (transport: hub.client.transport!, socket: { SyncSocket() })
        model.connectOnly(ServerConfig(url: hub.fake.url, token: "t"))
        defer { for link in model.links { link.live.disconnect() } }
        let api = try #require(model.api)
        await s.refresh(api)
        await s.download(try pick(s, "aurora"), api)
        #expect(s.current.id == "nwverse")
        var shown: [String] = []
        s.onChange = { shown.append(s.current.id) } // and not the app icon
        func push(_ id: String, from group: String) throws {
            let ev = try #require(ServerEvent.decode(Data(#"{"type":"look","id":"\#(id)","effective":"\#(id)"}"#.utf8)))
            model.handle(ev.stamped(group), group: group)
        }
        try push("aurora", from: "work") // another group's hub
        try push("persona", from: "") // the current one's
        try await until("the current group's push shown") { s.current.isBuiltin }
        #expect(shown == ["persona"] && s.chosenID == "persona" && hub.puts.isEmpty)
    }

    @Test func decodesTheLookPush() throws {
        let data = Data(#"{"type":"look","id":"aurora","effective":"persona"}"#.utf8)
        guard case .look(let id, let effective) = try #require(ServerEvent.decode(data)) else {
            Issue.record("not look")
            return
        }
        #expect(id == "aurora" && effective == "persona")
        #expect(ServerEvent.decode(Data(#"{"type":"look"}"#.utf8)) == nil)
        guard case .look = try #require(ServerEvent.decode(data).map({ $0.stamped("g1") })) else {
            Issue.record("not look in another group")
            return
        }
    }

    private static func image() -> CGImage {
        let ctx = CGContext(data: nil, width: 200, height: 200, bitsPerComponent: 8, bytesPerRow: 0, space: CGColorSpace(name: CGColorSpace.sRGB)!,
                            bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)!
        ctx.setFillColor(CGColor(srgbRed: 0.4, green: 0.6, blue: 0.8, alpha: 1))
        ctx.fill(CGRect(x: 0, y: 0, width: 200, height: 200))
        return ctx.makeImage()!
    }
}

/// A hub that keeps the choice, as server/internal/hub/looks.go does: GET /api/v1/looks lists Mochi and the packages
/// `ids` with `chosen`; PUT /api/v1/looks/current keeps one it lists (404 otherwise), with `if_unset` only when none is.
@MainActor
private final class LookHub {
    let fake: FakeHub
    var ids: [String]
    var chosen: String?
    /// Every PUT /api/v1/looks/current fails with it (only the one for `refuseOnly` when that is set).
    var refuse: (any Error)?
    var refuseOnly: String?
    /// Runs once a list has been answered (another device choosing right then).
    var afterList: (() -> Void)?
    /// Every package's rev (a new one: its files changed on the hub).
    var rev = "r1"

    init(url: String = "https://fake.invalid", ids: [String] = ["nwverse"], chosen: String?) throws {
        fake = FakeHub(url: url)
        self.ids = ids
        self.chosen = chosen
        fake.on("GET", "/api/v1/looks") { [unowned self] _ in
            defer { afterList?() }
            return Data(listing.utf8)
        }
        fake.on("PUT", "/api/v1/looks/current") { [unowned self] req in try put(req) }
        try serveFiles()
    }

    var client: APIClient { fake.client }

    /// The bodies of the PUTs so far.
    var puts: [[String: JSONValue]] {
        fake.sent("PUT", "/api/v1/looks/current").compactMap { $0.body.flatMap { try? JSONDecoder().decode([String: JSONValue].self, from: $0) } }
    }

    /// persona.json of `id`: PersonaTests' Milk Frog under that id; made from an upload for a `u-` id.
    static func manifest(_ id: String) -> String {
        let m = PersonaTests.manifest.replacingOccurrences(of: #""id":"nwverse""#, with: #""id":"\#(id)""#)
        return id.hasPrefix("u-") ? m.replacingOccurrences(of: #""name":"奶蛙","#, with: #""name":"奶蛙","source":{"kind":"upload"},"#) : m
    }

    func serveFiles() throws {
        for id in ids {
            for f in try PersonaTests.decode(Self.manifest(id)).files { fake.on("GET", "/api/v1/personas/\(id)/\(f)", json: "png:\(f)") }
        }
    }

    private var current: String {
        let effective = chosen.flatMap { c in c == "persona" || ids.contains(c) ? c : nil } ?? "persona"
        guard let chosen else { return #"{"id":null,"effective":"persona"}"# }
        return #"{"id":"\#(chosen)","effective":"\#(effective)","updated_at":"2026-11-09T08:00:00Z"}"#
    }

    private var listing: String {
        let looks = [#"{"id":"persona","source":"builtin","name":"团子","names":{"en":"Mochi"},"app_icon":"persona"}"#]
            + ids.map { #"{"id":"\#($0)","source":"hub","name":"奶蛙","app_icon":"persona","rev":"\#(rev)","bytes":10,"manifest":\#(Self.manifest($0))}"# }
        return #"{"looks":[\#(looks.joined(separator: ","))],"current":\#(current)}"#
    }

    private func put(_ req: LocalRequest) throws -> Data {
        let body = try JSONDecoder().decode([String: JSONValue].self, from: req.body ?? Data())
        if let refuse, refuseOnly == nil || refuseOnly == body["id"]?.string { throw refuse }
        if body["if_unset"]?.bool == true, chosen != nil { return Data(current.utf8) }
        guard let id = body["id"]?.string, id == "persona" || ids.contains(id) else { throw APIError(status: 404, message: "no look") }
        chosen = id
        return Data(current.utf8)
    }
}

/// Holds the requests `holding` picks until `open()`: a request still on its way. `answerFirst`: the hub answers it
/// at once and the answer is held on its way back (what it says is from before whatever happens meanwhile).
@MainActor
private final class Gate {
    private var waiting: [CheckedContinuation<Void, Never>] = []
    private var opened = false
    /// What the held requests fail with once let go (`open(failing:)`).
    private var failure: (any Error)?
    private(set) var held = 0

    func client(_ hub: FakeHub, answerFirst: Bool = false, holding: @escaping @Sendable (LocalRequest) -> Bool) -> APIClient {
        client(hub.client, answerFirst: answerFirst, holding: holding)
    }

    /// Over another client (another gate's): two kinds of request held apart.
    func client(_ base: APIClient, answerFirst: Bool = false, holding: @escaping @Sendable (LocalRequest) -> Bool) -> APIClient {
        var c = base
        let answer = base.transport!
        c.transport = { [self] req in
            guard holding(req) else { return try await answer(req) }
            if !answerFirst { try await hold() }
            let data = try await answer(req)
            if answerFirst { try await hold() }
            return data
        }
        return c
    }

    private func hold() async throws {
        guard !opened else { return }
        held += 1
        await withCheckedContinuation { waiting.append($0) }
        if let failure { throw failure }
    }

    /// Lets the held requests go on; `failing`: they fail with it instead (the network dropped).
    func open(failing: (any Error)? = nil) {
        failure = failing
        opened = true
        waiting.forEach { $0.resume() }
        waiting = []
    }

    func untilHeld() async throws {
        for _ in 0..<300 where held == 0 { try await Task.sleep(for: .milliseconds(10)) }
        #expect(held > 0, "nothing was asked for")
    }
}
