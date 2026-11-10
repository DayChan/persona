import Foundation
import ImageIO
#if canImport(UIKit)
import UIKit
#elseif canImport(AppKit)
import AppKit
#endif

/// The personas on this phone (docs/apple/persona.md「On the phone」): which one is chosen, what the hub offers, and the packages
/// downloaded, one directory per rev under Application Support/Personas/<id>/<rev>. A package is used only once all of
/// it is there (it is downloaded beside and moved in whole), and the chosen one shows from disk at launch, before
/// the hub answers or without it.
///
/// A hub that keeps the choice (GET /api/v1/looks, #2406) is the one truth of it: its choice is shown, a choice made
/// here goes to it (PUT /api/v1/looks/current) and another device's comes as a `look` push (`follow`); the saved id is
/// then this device's copy of the hub's. With an older hub (404) the choice stays this device's, as before.
@MainActor @Observable
final class PersonaStore {
    /// Mochi first, then valid packages the hub has listed (kept across launches).
    private(set) var offered: [Persona] = Persona.builtins
    /// The chosen one as it can be shown now: the built-in until its package is on the phone.
    private(set) var current: Persona = .builtin {
        didSet {
            guard current != oldValue else { return }
            remember(current)
            onChange?()
        }
    }
    /// Ids being downloaded.
    private(set) var downloading: Set<String> = []
    /// Why the last list or download failed (Settings › Avatar shows it).
    private(set) var error: String?
    /// Bumped when pictures land on disk (a package, a thumbnail): the views that asked for one before ask again.
    private(set) var pictures = 0

    /// `current` changed (AppModel lays the colors over the style).
    @ObservationIgnored var onChange: (() -> Void)?
    @ObservationIgnored let root: URL
    @ObservationIgnored private let defaults: UserDefaults
    /// The refresh under way: another caller waits for it instead of starting a second one (connecting to the hub
    /// and Settings opening come together; a second refresh used to clean away the package the first was downloading).
    /// `n` counts them, so `reload` can tell one that began after it was called. It is cleared as the load ends, before
    /// anyone waiting on it carries on: a caller that then asks again gets a new one, not the one just finished.
    @ObservationIgnored private var refreshing: (n: Int, hub: URL, task: Task<Void, Never>)?
    @ObservationIgnored private var refreshes = 0
    /// The hub read now (the current hub's address): what a list from another one says is not taken. Read by the tests
    /// to know the current hub changed.
    @ObservationIgnored private(set) var hub: URL?
    /// Whether the hub at an address keeps the choice, as it answered this launch (GET /api/v1/looks: true; 404: false).
    @ObservationIgnored private var keeps: [URL: Bool] = [:]
    /// Counts the choices made here, those taken from the hub (a `look` push, a list) and the current hub changing: a
    /// list read or a download that started before one does not undo it (`latest`). Read by the tests to know one
    /// got that far.
    @ObservationIgnored private(set) var choices = 0
    /// The choices made here (their `choices`) that put nothing on screen that is still there: waiting for their
    /// packages, given up when one did not come, not taken by the hub, or never sent (a newer one was). They do not
    /// count as newer for `latest`: what was there before them is still in force.
    @ObservationIgnored private var silent: Set<Int> = []
    /// Each hub's choice as far as this device knows (the last it listed, pushed or took from here) and when it was
    /// heard (`heard` then): what comes back when that hub does not take one made here.
    @ObservationIgnored private var hubChoices: [URL: (id: String, heard: Int)] = [:]
    /// Counts what was heard of the hubs' choices (`hubChoices`, `echoes`): a mark taken before a request tells what
    /// came after.
    @ObservationIgnored private(set) var heard = 0
    /// When each hub last pushed each look (`heard` then), followed or not. The hub pushes every choice in the order it
    /// takes them: the answer to a request that chose one is older than what came after that one's push, and newer
    /// than what came before it.
    @ObservationIgnored private var echoes: [URL: [String: Int]] = [:]
    /// The package downloads under way, one per id: another caller waits for it (a second `look` push for the same one).
    @ObservationIgnored private var downloads: [String: (rev: String, token: UUID, task: Task<Void, Never>)] = [:]
    /// The choices made here and not settled yet (waiting for their packages or on their way to the hub), oldest
    /// first: the push of an older one, back while a newer one is still to come, changes nothing.
    @ObservationIgnored private var pending: [String] = []
    /// One PUT at a time (two at once could reach the hub the wrong way round), and only the newest is sent.
    @ObservationIgnored private var sending: Task<Void, Never>?
    /// What was on screen before the choices still on their way to each hub, and how many are: a choice made while
    /// another one is on its way goes back to what was there before both when the hub takes neither.
    @ObservationIgnored private var undos: [URL: (undo: Undo, count: Int)] = [:]
    #if DEBUG
    /// `-openPersona <id>`: chosen at launch, on the hub too (a hub that keeps the choice would put its own back).
    @ObservationIgnored private var launchChoice: String?
    #endif
    @ObservationIgnored private let frameCache = NSCache<NSString, PersonaFrames>()
    @ObservationIgnored private let imageCache = NSCache<NSString, UIImage>()

    static let chosenKey = NativeStateMigration.personaKey
    /// Set: the saved choice is a copy of a hub's that keeps the choice, so it is not moved up to another hub. Not set: it
    /// was chosen on this device (an older app, an older hub, no hub), and it moves up once, with `if_unset`, to the
    /// first hub that keeps the choice (docs/extensions/extensions.md "Avatars").
    static let followsHubKey = "persona.followsHub"
    static var defaultRoot: URL {
        URL.applicationSupportDirectory.appending(path: "Personas", directoryHint: .isDirectory)
    }

    /// The user's choice; kept when its package is gone from the hub, so it comes back with it. With a hub that keeps
    /// the choice, the hub's (its `effective`).
    var chosenID: String { PersonaSelection.resolve(NativeStateMigration.readPersonaID(from: defaults)) }

    private var followsHub: Bool {
        get { defaults.bool(forKey: Self.followsHubKey) }
        set {
            guard newValue != followsHub else { return }
            if newValue { defaults.set(true, forKey: Self.followsHubKey) } else { defaults.removeObject(forKey: Self.followsHubKey) }
        }
    }

    /// Saves the choice (the next launch shows it from disk) and tells the watch.
    private func setChosen(_ id: String) {
        NativeStateMigration.writePersonaID(id, defaults: defaults)
        WatchSync.shared.push(persona: id)
    }

    /// The hub's list came this launch.
    @ObservationIgnored private var listed = false

    /// Nothing newer since choice `n` (a choice, push, list or other hub that put something up), but choices made here
    /// that have not shown.
    private func latest(_ n: Int) -> Bool {
        n >= choices || ((n + 1)...choices).allSatisfy(silent.contains)
    }

    /// `url` is the hub read now. When it was another one, what was under way for that one (a push's download, a
    /// choice waiting for its package) no longer shows.
    private func read(_ url: URL) {
        guard hub != url else { return }
        hub = url
        choices += 1
    }

    /// Another group's hub became the current one (`AppModel.hubChanged`): from now on, before its list is read, a
    /// choice goes to it, and what is still under way for the one before (a late answer, a download) changes nothing
    /// here. (A push from it still waiting to be handled is dropped by `AppModel.handle`.)
    func switchHub(_ api: APIClient?) {
        guard let api, !api.config.isDemo else { return }
        read(api.config.url)
    }

    /// Notes `id` as `url`'s choice, heard now.
    private func hear(_ id: String, on url: URL) {
        heard += 1
        hubChoices[url] = (id, heard)
    }

    /// The home screen icon the choice puts on (`Persona.appIcon`): a package's is known from the list before the
    /// package is on the phone, so the icon does not go to the default's and back while it downloads. Nil while the
    /// chosen one is not listed yet; once the hub has answered without it, the built-in's on screen.
    var appIcon: AppIconChoice? {
        if let chosen = offered.first(where: { $0.id == chosenID }) { return chosen.appIcon }
        return listed ? current.appIcon : nil
    }

    init(root: URL = PersonaStore.defaultRoot, defaults: UserDefaults = .standard) {
        self.root = root
        self.defaults = defaults
        _ = NativeStateMigration.defaults(defaults)
        frameCache.countLimit = 6
        if let data = try? Data(contentsOf: indexURL), let list = try? JSONDecoder().decode([PersonaInfo].self, from: data) {
            offered = Persona.builtins + list.filter(Self.supportedPackage).map(Persona.init(info:))
        }
        #if DEBUG
        // `-openPersona <id>`: start with that one (on the phone already, or downloaded when the list comes).
        if let id = defaults.string(forKey: "openPersona"), !id.isEmpty {
            NativeStateMigration.writePersonaID(id, defaults: defaults)
            launchChoice = PersonaSelection.resolve(id)
        }
        #endif
        if let p = offered.first(where: { $0.id == chosenID }), isOnPhone(p) {
            current = p
        } else if let data = try? Data(contentsOf: shownURL), let info = try? JSONDecoder().decode(PersonaInfo.self, from: data),
                  info.id == chosenID, Self.supportedPackage(info), isOnPhone(Persona(info: info)) {
            // Not in the last list as it is on the phone (a list read since, of another hub or with a new rev, while the
            // hub's choice was still downloading): the one that was on screen.
            current = Persona(info: info)
        }
    }

    private static func supportedPackage(_ info: PersonaInfo) -> Bool {
        info.manifest.id == info.id && info.manifest.problem == nil
    }

    private var indexURL: URL { root.appending(path: "index.json") }
    /// The package on screen, kept beside the list (`remember`).
    private var shownURL: URL { root.appending(path: ".shown.json") }

    /// Keeps the package on screen beside the list: a launch shows it from disk while the hub's choice downloads, also
    /// when the list read meanwhile no longer has it or has a new rev of it (`index.json` is that newer list).
    private func remember(_ p: Persona) {
        guard let m = p.manifest else { try? FileManager.default.removeItem(at: shownURL); return }
        try? FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try? JSONEncoder().encode(PersonaInfo(id: p.id, rev: p.rev, bytes: 0, manifest: m)).write(to: shownURL, options: .atomic)
    }

    func directory(_ p: Persona) -> URL { root.appending(path: p.id).appending(path: p.rev, directoryHint: .isDirectory) }

    func isOnPhone(_ p: Persona) -> Bool {
        p.isBuiltin || FileManager.default.fileExists(atPath: directory(p).path(percentEncoded: false))
    }

    // MARK: The hub

    /// What the hub offers now, and its choice when it keeps one; the chosen one is downloaded when its rev is new, and
    /// revs nobody uses are removed. Another hub's read under way (the current hub changed) is waited for, then this
    /// one is read.
    func refresh(_ api: APIClient?) async {
        guard let api, !api.config.isDemo else { return }
        read(api.config.url)
        while let running = refreshing {
            if running.hub == api.config.url { return await running.task.value }
            await running.task.value
        }
        await startRefresh(api).value
    }

    /// The list as the hub has it now, read after this call: for what was just made or deleted there (#2405). A refresh
    /// already under way may have asked before that, so it is waited for and the list read again, unless another one
    /// of the same hub began meanwhile (that one asked later). Never two at once, as for `refresh`.
    func reload(_ api: APIClient?) async {
        guard let api, !api.config.isDemo else { return }
        read(api.config.url)
        let asked = refreshes
        while let running = refreshing {
            if running.n > asked, running.hub == api.config.url { return await running.task.value }
            await running.task.value
        }
        await startRefresh(api).value
    }

    private func startRefresh(_ api: APIClient) -> Task<Void, Never> {
        refreshes += 1
        let n = refreshes
        let task = Task {
            await load(api)
            if refreshing?.n == n { refreshing = nil }
        }
        refreshing = (n, api.config.url, task)
        return task
    }

    private func load(_ api: APIClient) async {
        let asked = choices
        // What was heard of the hubs' choices when the list was asked for: a push or an answer heard since is newer.
        let mark = heard
        let url = api.config.url
        let list: [PersonaInfo]
        var settled: String?
        do {
            if let looks = try await api.looks() {
                keeps[url] = true
                list = looks.packages.filter(Self.supportedPackage)
                if hub == url { settled = await settle(looks.current, api) }
            } else {
                keeps[url] = false // an older hub: its packages, and the choice stays this device's
                list = try await api.personas().filter(Self.supportedPackage)
            }
        } catch let e as APIError where e.status == 404 {
            error = String(localized: "This hub can't host avatars yet. Update the hub and check again.")
            return
        } catch {
            if !error.isCancellation { self.error = String(localized: "Couldn't load the avatar list: \(error.localizedDescription)") }
            return
        }
        // Another hub is read now (the current hub changed while this one answered): its read follows.
        guard hub == url else { return }
        self.error = nil
        listed = true
        // The hub's choice, as this device knows it from now (what a choice it refuses goes back to), unless a push or an
        // answer heard since the list was asked for is newer. It shows unless a choice was made here or pushed since, or
        // one made here is still on its way (the hub may not have had it when it answered: its answer decides). It is
        // saved once its package is on the phone, as a choice made here is: a launch shows the saved one from disk.
        var target = chosenID
        if let id = settled, (hubChoices[url]?.heard ?? 0) <= mark {
            hear(id, on: url)
            if latest(asked), undos[url] == nil {
                followsHub = true
                target = id
            }
        }
        offered = Persona.builtins + list.filter(Self.supportedPackage).map(Persona.init(info:))
        try? FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try? JSONEncoder().encode(list).write(to: indexURL, options: .atomic)
        if let chosen = offered.first(where: { $0.id == target }) {
            if !isOnPhone(chosen) { await download(chosen, api) }
            // Another one chosen, pushed or another hub made current while it downloaded stays.
            if isOnPhone(chosen), hub == url, latest(asked) || chosen.id == chosenID {
                if chosen.id != chosenID { setChosen(chosen.id) }
                current = chosen
            }
        } else if latest(asked) {
            // Not listed (its package gone for a while): the default shows and the choice is kept. A list asked for
            // before a newer choice may not have that one yet: it is not put back to the default.
            if target != chosenID { setChosen(target) }
            current = .builtin
        }
        // The others' faces for Settings' cards, one small picture each (the package itself comes when chosen).
        for p in offered where !isOnPhone(p) && !hasThumbnail(p) {
            guard let file = p.manifest?.avatarFile(points: Self.thumbnailPoints, scale: 3),
                  let data = try? await api.personaFile(p.id, file) else { continue }
            try? FileManager.default.createDirectory(at: thumbnails, withIntermediateDirectories: true)
            try? data.write(to: thumbnail(p), options: .atomic)
            pictures += 1
        }
        prune(keeping: Set(offered.map { "\($0.id)/\($0.rev)" }))
    }

    /// Settings' cards show faces 64 pt wide.
    static let thumbnailPoints: CGFloat = 64
    private var thumbnails: URL { root.appending(path: ".thumbnails", directoryHint: .isDirectory) }
    private func thumbnail(_ p: Persona) -> URL { thumbnails.appending(path: "\(p.id)-\(p.rev).png") }
    private func hasThumbnail(_ p: Persona) -> Bool { FileManager.default.fileExists(atPath: thumbnail(p).path(percentEncoded: false)) }

    /// The choice to show given the hub's: the hub's own once it has one. When it has none, a choice made on this device
    /// (not a copy of another hub's) moves up, unless another device's got there first (`if_unset`): the first device
    /// that meets the hub decides for all. Nil when the hub did not take it (out of reach, or it does not list that one
    /// now: its package gone for a while): this device's stays, and it is tried again at the next read.
    private func settle(_ kept: LookCurrent, _ api: APIClient) async -> String? {
        let shown = PersonaSelection.resolve(kept.effective ?? kept.id)
        #if DEBUG
        if let id = launchChoice {
            launchChoice = nil
            if let answer = try? await api.chooseLook(id) { return PersonaSelection.resolve(answer.effective ?? id) }
        }
        #endif
        // The default is not sent: it is what a hub without a choice shows anyway, and another device's own may come.
        guard kept.id == nil, !followsHub, let mine = NativeStateMigration.readPersonaID(from: defaults).map(PersonaSelection.resolve),
              mine != Persona.builtinID else { return shown }
        guard let answer = try? await api.chooseLook(mine, ifUnset: true) else { return nil }
        // Moved up (or another device's got there first): never again, to this hub or the next, also when the current
        // hub changed while this one answered and its answer is not shown. A choice made here meanwhile with no hub or an
        // older one is this device's own: that one moves up next.
        if NativeStateMigration.readPersonaID(from: defaults).map(PersonaSelection.resolve) == mine { followsHub = true }
        return PersonaSelection.resolve(answer.effective ?? answer.id)
    }

    /// Picks a persona. A package is downloaded first (on failure, or with no hub to download from, nothing changes and
    /// `error` says why). It shows here at once and goes to a hub that keeps the choice, so every device follows; when
    /// the hub does not take it, the one before comes back and `error` says why. With an older hub, or none, it is this
    /// device's own. A choice made with a hub that is no longer the current one (another group's became current while
    /// it was read or the package downloaded) is dropped. `onHub`: the hub has chosen it already (an upload with
    /// `choose`, a delete of the chosen one), as of `heard` then (taken before asking it), so it is only shown here;
    /// unless the hub replaced it since (`replaced`): that one is newer, and only the package comes.
    func choose(_ p: Persona, api: APIClient?, onHub: Int? = nil) async {
        guard offered.contains(p) else { return }
        let hubAPI = api.flatMap { $0.config.isDemo ? nil : $0 }
        if let api = hubAPI {
            if onHub == nil, keeps[api.config.url] == nil { await refresh(api) } // does it keep it?
            if hub == nil { read(api.config.url) }
            guard hub == api.config.url else { return }
            // Before it counts as a choice: that would drop the newer one's download still under way.
            if let asked = onHub, replaced(p.id, on: api.config.url, since: asked) {
                if !isOnPhone(p) { await download(p, api) }
                return
            }
        }
        if !isOnPhone(p), api == nil {
            error = String(localized: "“\(p.name)” isn’t on this phone yet. Connect to the hub, then choose it.")
            return
        }
        choices += 1
        let mine = choices
        pending.append(p.id)
        defer { if let i = pending.firstIndex(of: p.id) { pending.remove(at: i) } }
        if let api, !isOnPhone(p) {
            // Nothing shows for it until its package is here: an earlier one the hub refuses meanwhile still goes back.
            silent.insert(mine)
            await download(p, api)
            // Another one chosen or pushed, or another hub made current, while it downloaded stays.
            guard isOnPhone(p), mine == choices else { return }
            silent.remove(mine)
        }
        guard let api = hubAPI else {
            if api == nil { followsHub = false } // no hub: this device's, moved up when one that keeps the choice comes
            return show(p)
        }
        let url = api.config.url
        let keeps = keeps[url]
        if let asked = onHub {
            if replaced(p.id, on: url, since: asked) { return } // while its package downloaded
            if keeps == true { followsHub = true; hear(p.id, on: url) }
            return show(p)
        }
        // Not known (the hub did not answer): a device that follows a hub tries it; one that never did keeps it here.
        guard keeps ?? followsHub else {
            followsHub = false
            return show(p)
        }
        // While an earlier choice is still on its way, what was there before it is what comes back.
        let before = undos[url]?.undo ?? Undo(shown: current, chosen: chosenID, followed: followsHub)
        followsHub = true
        show(p)
        await send(p, api, mine: mine, before: before)
    }

    /// The hub chose `id` (as of `heard` mark `asked`) and replaced it since: its push came after that, and another
    /// choice was heard after it (newer: on screen, or its package on the way).
    private func replaced(_ id: String, on url: URL, since asked: Int) -> Bool {
        (echoes[url]?[id] ?? 0) > asked && hubChoices[url].map { $0.id != id } == true
    }

    /// What a choice sent to the hub replaced: what comes back when the hub does not take it and knows no choice of
    /// its own here.
    private struct Undo {
        var shown: Persona
        var chosen: String
        var followed: Bool
    }

    private func show(_ p: Persona) {
        error = nil
        setChosen(p.id)
        current = p
    }

    /// Tells the hub, one PUT at a time and only the newest choice (one that another followed while it waited is not
    /// sent). When the hub does not take the newest, its choice as known now comes back (an earlier tap it took, what
    /// it listed or pushed meanwhile), else what was there `before`: the one on screen then when it is that one, else
    /// its package when it is on the phone. While that package is not on the phone (still downloading, or it did not
    /// come), what was there `before` stays, as when a list or a push brings it. A hub that is no longer the current
    /// one changes nothing on screen.
    private func send(_ p: Persona, _ api: APIClient, mine: Int, before: Undo) async {
        let url = api.config.url
        undos[url] = (before, (undos[url]?.count ?? 0) + 1)
        let previous = sending
        let task = Task {
            await previous?.value
            defer { if let u = undos[url] { undos[url] = u.count > 1 ? (u.undo, u.count - 1) : nil } }
            // Not sent (a newer one is, or another came since): what it put up is gone, it counts as a choice no more.
            guard latest(mine) else { silent.insert(mine); return }
            let asked = heard
            do {
                _ = try await api.chooseLook(p.id)
                keeps[url] = true
                // Its own push came already: that, or what was pushed after it, is the hub's now.
                if (echoes[url]?[p.id] ?? 0) <= asked { hear(p.id, on: url) }
            } catch {
                guard !error.isCancellation else { return }
                // Not taken: it counts as a choice no more, so what it replaced is in force again (a push or a list from
                // before it whose package is still downloading shows once that is here).
                silent.insert(mine)
                // Since then the hub's push came (it did take it), another choice showed, or another hub became the
                // current one (its own shows): that one stays. A later choice still waiting for its package does not
                // count: nothing shows for it yet.
                guard latest(mine), hub == url else { return }
                let back = hubChoices[url]?.id ?? before.chosen
                // The old local choice stays this device's (moved up later) unless the hub's own comes back.
                followsHub = hubChoices[url] != nil || before.followed
                if let shown = before.shown.id == back ? before.shown : offered.first(where: { $0.id == back && isOnPhone($0) }) {
                    setChosen(back)
                    current = shown
                } else {
                    setChosen(before.chosen)
                    current = before.shown
                }
                self.error = String(localized: "Couldn’t switch to “\(p.name)” on the hub: \(error.localizedDescription)")
            }
        }
        sending = task
        await task.value
    }

    /// Another device chose `effective` (the hub's `look` push), or the hub went back to the default: shown and saved
    /// here once its package is on the phone, as when a list brings it (until then the one before stays, also at the next
    /// launch). This device's own older choice coming back while a newer one is still to come (on its way, or waiting
    /// for its package) changes nothing on screen (the newer one's push follows), but it is still the hub's choice as
    /// known here.
    func follow(_ id: String?, effective: String?, api: APIClient?) async {
        guard let api, !api.config.isDemo, let pushed = effective ?? id, !pushed.isEmpty else { return }
        let id = PersonaSelection.resolve(pushed)
        let url = api.config.url
        heard += 1
        echoes[url, default: [:]][id] = heard
        hear(id, on: url) // what a choice the hub refuses goes back to
        if pending.contains(id), id != pending.last { return }
        read(url)
        choices += 1
        let mine = choices
        keeps[url] = true
        followsHub = true
        guard let p = offered.first(where: { $0.id == id }) else {
            // Not listed here yet (just made on another device): the list read brings it, downloads it and shows it.
            return await reload(api)
        }
        if !isOnPhone(p) { await download(p, api) }
        // Another one chosen or pushed, or another hub made current, while it downloaded stays (a choice made here
        // still waiting for its package does not count).
        guard latest(mine), isOnPhone(p) else { return }
        if id != chosenID { setChosen(id) }
        current = p
    }

    /// Fetches the package's files beside its place and moves them in whole. One download per id at a time: a caller
    /// that comes while one runs waits for it (and fetches its own rev after, when that was another).
    func download(_ p: Persona, _ api: APIClient) async {
        guard p.manifest != nil else { return }
        while let running = downloads[p.id] {
            await running.task.value
            if running.rev == p.rev || isOnPhone(p) { return }
        }
        let token = UUID()
        // The entry goes as the download ends, before anyone waiting on it carries on.
        let task = Task {
            await fetch(p, api)
            if downloads[p.id]?.token == token { downloads[p.id] = nil }
        }
        downloads[p.id] = (p.rev, token, task)
        await task.value
    }

    private func fetch(_ p: Persona, _ api: APIClient) async {
        guard let m = p.manifest else { return }
        downloading.insert(p.id)
        defer { downloading.remove(p.id) }
        let fm = FileManager.default
        let part = root.appending(path: p.id).appending(path: ".\(p.rev).part", directoryHint: .isDirectory)
        try? fm.removeItem(at: part)
        do {
            for file in m.files {
                let data = try await api.personaFile(p.id, file)
                let dest = part.appending(path: file)
                try fm.createDirectory(at: dest.deletingLastPathComponent(), withIntermediateDirectories: true)
                try data.write(to: dest, options: .atomic)
            }
            // Every file there before it is used (nothing else may touch the .part, but a package missing a face
            // would show the built-in's for good).
            if let lost = m.files.first(where: { !fm.fileExists(atPath: part.appending(path: $0).path(percentEncoded: false)) }) {
                throw APIError(status: 0, message: String(localized: "Missing \(lost)"))
            }
            let final = directory(p)
            try? fm.removeItem(at: final)
            try fm.moveItem(at: part, to: final)
            pictures += 1
            error = nil
        } catch {
            try? fm.removeItem(at: part)
            if !error.isCancellation { self.error = String(localized: "Couldn’t download “\(p.name)”: \(error.localizedDescription)") }
        }
    }

    /// Removes the revs that are neither offered nor on screen; never while a download runs, never a download's .part.
    private func prune(keeping keep: Set<String>) {
        let fm = FileManager.default
        guard downloading.isEmpty, let ids = try? fm.contentsOfDirectory(atPath: root.path(percentEncoded: false)) else { return }
        for t in (try? fm.contentsOfDirectory(atPath: thumbnails.path(percentEncoded: false))) ?? []
        where !keep.contains(where: { t == $0.replacingOccurrences(of: "/", with: "-") + ".png" }) {
            try? fm.removeItem(at: thumbnails.appending(path: t))
        }
        for id in ids where !id.hasPrefix(".") && id != "index.json" {
            let dir = root.appending(path: id)
            for rev in (try? fm.contentsOfDirectory(atPath: dir.path(percentEncoded: false))) ?? [] where !rev.hasPrefix(".") {
                let key = "\(id)/\(rev)"
                if !keep.contains(key), key != "\(current.id)/\(current.rev)" { try? fm.removeItem(at: dir.appending(path: rev)) }
            }
            if ((try? fm.contentsOfDirectory(atPath: dir.path(percentEncoded: false))) ?? []).isEmpty { try? fm.removeItem(at: dir) }
        }
    }

    // MARK: Pictures

    /// The face `points` wide, from the package, or its thumbnail while the package is not on the phone (nil for the
    /// built-in, or when there is neither).
    func avatar(_ p: Persona, points: CGFloat, scale: CGFloat) -> UIImage? {
        _ = pictures
        guard let file = p.manifest?.avatarFile(points: points, scale: scale) else { return nil }
        let url = isOnPhone(p) ? directory(p).appending(path: file) : thumbnail(p)
        let key = url.path(percentEncoded: false) as NSString
        if let img = imageCache.object(forKey: key) { return img }
        guard let raw = UIImage(contentsOfFile: url.path(percentEncoded: false)) else { return nil }
        let img = raw.preparingForDisplay() ?? raw
        imageCache.setObject(img, forKey: key)
        return img
    }

    /// The state's frames, decoded once (a few dozen 144-pixel images) and kept for the states last shown.
    func frames(_ p: Persona, _ state: PersonaState) -> PersonaFrames? {
        _ = pictures
        guard let spec = p.manifest?.dotState(state), isOnPhone(p) else { return nil }
        let url = directory(p).appending(path: spec.file)
        let key = url.path(percentEncoded: false) as NSString
        if let f = frameCache.object(forKey: key) { return f }
        guard let f = PersonaFrames(url: url, fps: spec.fps) else { return nil }
        frameCache.setObject(f, forKey: key)
        return f
    }
}

/// A dot state's frames and the time one loop takes.
final class PersonaFrames: Sendable {
    let images: [UIImage]
    let duration: TimeInterval

    /// From an APNG (ImageIO composes each frame whole) or a still PNG; `fps` when the file has no frame delays.
    nonisolated init?(url: URL, fps: Double?) {
        guard let src = CGImageSourceCreateWithURL(url as CFURL, nil) else { return nil }
        let opts = [kCGImageSourceShouldCacheImmediately: true] as CFDictionary
        var images: [UIImage] = []
        var total = 0.0
        for i in 0..<CGImageSourceGetCount(src) {
            guard let cg = CGImageSourceCreateImageAtIndex(src, i, opts) else { continue }
            #if os(iOS) || os(visionOS)
            images.append(UIImage(cgImage: cg))
            #else
            images.append(NSImage(cgImage: cg, size: NSSize(width: cg.width, height: cg.height))) // AppKit's own init: callable off the main actor
            #endif
            let props = CGImageSourceCopyPropertiesAtIndex(src, i, nil) as? [CFString: Any]
            let png = props?[kCGImagePropertyPNGDictionary] as? [CFString: Any]
            let delay = (png?[kCGImagePropertyAPNGUnclampedDelayTime] as? Double) ?? (png?[kCGImagePropertyAPNGDelayTime] as? Double) ?? 0
            total += delay > 0.005 ? delay : 1 / max(1, fps ?? 8)
        }
        guard !images.isEmpty else { return nil }
        self.images = images
        duration = total
    }
}

/// GET /api/v1/looks's `current`, and PUT /api/v1/looks/current's answer.
nonisolated struct LookCurrent: Decodable, Sendable, Equatable {
    /// The one chosen; nil: none was ever chosen on this hub.
    var id: String?
    /// The one to show: the chosen one, or the default while it is not there (its package gone, its extension off).
    var effective: String?
}

/// GET /api/v1/looks: every look the hub offers and the one chosen (docs/protocol/protocol.md; only what the phone uses).
nonisolated struct LookList: Decodable, Sendable {
    struct Look: Decodable, Sendable {
        var id: String
        var rev: String?
        var bytes: Int64?
        /// Nil for the looks the apps draw themselves (`source: builtin`).
        var manifest: PersonaManifest?

        enum CodingKeys: String, CodingKey { case id, rev, bytes, manifest }

        /// A manifest this build cannot read leaves that look out (as an unchecked package is), not the whole list.
        init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            rev = try? c.decodeIfPresent(String.self, forKey: .rev)
            bytes = try? c.decodeIfPresent(Int64.self, forKey: .bytes)
            manifest = try? c.decodeIfPresent(PersonaManifest.self, forKey: .manifest)
        }
    }

    var looks: [Look]
    var current: LookCurrent

    /// The packages among them, as GET /api/v1/personas lists them (the built-in looks are drawn here).
    var packages: [PersonaInfo] {
        looks.compactMap { l in
            guard let m = l.manifest, let rev = l.rev else { return nil }
            return PersonaInfo(id: l.id, rev: rev, bytes: l.bytes ?? 0, manifest: m)
        }
    }
}

nonisolated extension APIClient {
    /// GET /api/v1/looks: the looks and the choice the hub keeps for every device (#3200). Nil from an older hub (404):
    /// it lists its packages at /api/v1/personas, and the choice stays the device's.
    func looks() async throws -> LookList? {
        do {
            return try JSONDecoder().decode(LookList.self, from: try await request("GET", "/api/v1/looks"))
        } catch let e as APIError where e.status == 404 {
            return nil
        }
    }

    /// PUT /api/v1/looks/current: chooses the look for every device (404 when the hub does not list it). `ifUnset`: only
    /// when the hub has no choice yet; otherwise the answer is its choice, unchanged.
    func chooseLook(_ id: String, ifUnset: Bool = false) async throws -> LookCurrent {
        struct Body: Encodable {
            var id: String
            var ifUnset: Bool?
            enum CodingKeys: String, CodingKey {
                case id
                case ifUnset = "if_unset"
            }
        }
        let data = try await request("PUT", "/api/v1/looks/current", body: Body(id: id, ifUnset: ifUnset ? true : nil))
        return (try? JSONDecoder().decode(LookCurrent.self, from: data)) ?? LookCurrent(id: id, effective: id)
    }

    /// GET /api/v1/personas: the packages on the hub that check out (docs/protocol/protocol.md「REST (App → hub)」).
    func personas() async throws -> [PersonaInfo] {
        struct List: Decodable { var personas: [PersonaInfo] }
        return try JSONDecoder().decode(List.self, from: try await request("GET", "/api/v1/personas")).personas
    }

    /// A file of a package (`PersonaManifest.files`), past the system's URL cache: the store keeps it on disk.
    func personaFile(_ id: String, _ file: String) async throws -> Data {
        let path = "/api/v1/personas/\(id)/\(file)"
        if transport != nil || config.isDemo { return try await request("GET", path) }
        var req = URLRequest(url: url(path), timeoutInterval: 60)
        req.setValue("Bearer \(config.token)", forHTTPHeaderField: "Authorization")
        req.setValue(LanguageStore.shared.launched, forHTTPHeaderField: "Accept-Language")
        let (data, resp) = try await Self.uncachedSession.data(for: req)
        let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            throw APIError(status: status, message: APIError.message(in: data) ?? HTTPURLResponse.localizedString(forStatusCode: status))
        }
        return data
    }
}
