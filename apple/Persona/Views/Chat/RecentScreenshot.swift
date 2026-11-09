import Foundation
import Photos
#if os(iOS)
import SwiftUI
import UIKit
#endif

/// The composer's + menu offers the screenshot just taken (#1994), as WeChat offers it beside its +: what counts as
/// "just taken" and which row the menu shows. The state and the menu rows are below (iOS only).
enum RecentScreenshotRule {
    /// How long a screenshot stays offered: time to take it, switch to Persona and type a line about it, short
    /// enough that one from a while ago is not pushed on a message about something else.
    static let window: TimeInterval = 120
    /// A screenshot dated a little after the phone's clock reads still counts (Photos stamps it as it saves).
    static let skew: TimeInterval = 5

    struct Candidate: Equatable, Sendable {
        var id: String
        var taken: Date
    }

    /// The newest screenshot inside the window that was not attached or offered away already.
    static func pick(_ candidates: [Candidate], now: Date, handled: Set<String>) -> Candidate? {
        candidates
            .filter { c in
                let age = now.timeIntervalSince(c.taken)
                return age >= -skew && age <= window && !handled.contains(c.id)
            }
            .max { $0.taken < $1.taken }
    }

    /// When the offer leaves the menu.
    static func expiry(_ c: Candidate) -> Date { c.taken.addingTimeInterval(window) }

    /// The row that asks for access, in place of the offer.
    enum Guide: Equatable {
        /// No row: access is there, or the row was used once.
        case none
        /// Never asked: the system's prompt.
        case ask
        /// Refused or only some photos (new screenshots are never among them): Settings.
        case settings
    }

    /// Without full access there is nothing to offer (limited access never sees a new screenshot); the menu stays as
    /// it was, with one row that asks for it until that row is used once.
    static func guide(_ status: PhoneAuthStatus, used: Bool) -> Guide {
        guard !used else { return .none }
        switch status {
        case .notDetermined: return .ask
        case .denied, .limited: return .settings
        case .granted, .notNeeded: return .none
        }
    }

    /// Photos are read only with full access.
    static func reads(_ status: PhoneAuthStatus) -> Bool { status == .granted }
}

#if os(iOS)
/// The screenshot the + menu offers, kept current while Persona runs: read again when the app comes to the front, when
/// a screenshot is taken in it and when the library changes, and dropped when it gets older than the window.
@MainActor @Observable final class RecentScreenshots {
    static let shared = RecentScreenshots()

    struct Offer: Equatable {
        var id: String
        var taken: Date
        var thumbnail: UIImage?
    }

    private(set) var offer: Offer?
    private(set) var status: PhoneAuthStatus
    /// The access row was used (asked, or Settings opened): it does not come back.
    private(set) var guideUsed: Bool
    /// Attached already: the same screenshot is not offered again.
    @ObservationIgnored private var handled: Set<String> = []
    @ObservationIgnored private var observers: [NSObjectProtocol] = []
    @ObservationIgnored private var watch: LibraryWatch?
    @ObservationIgnored private var expiry: Task<Void, Never>?
    private static let guideKey = "composer.recentScreenshotGuideUsed"

    var guide: RecentScreenshotRule.Guide { RecentScreenshotRule.guide(status, used: guideUsed) }

    private init() {
        status = PhotosAccess.status()
        guideUsed = UserDefaults.standard.bool(forKey: Self.guideKey)
        let center = NotificationCenter.default
        // A screenshot taken in Persona is saved a moment later: the library watch sees it then.
        for name in [UIApplication.didBecomeActiveNotification, UIApplication.userDidTakeScreenshotNotification] {
            observers.append(center.addObserver(forName: name, object: nil, queue: .main) { _ in
                MainActor.assumeIsolated { RecentScreenshots.shared.refresh() }
            })
        }
        refresh()
    }

    func refresh() {
        status = PhotosAccess.status()
        guard RecentScreenshotRule.reads(status) else {
            offer = nil
            return
        }
        if watch == nil {
            let w = LibraryWatch { Task { @MainActor in RecentScreenshots.shared.refresh() } }
            PHPhotoLibrary.shared().register(w)
            watch = w
        }
        let now = Date()
        let options = PHFetchOptions()
        options.predicate = NSPredicate(format: "mediaType == %d AND creationDate >= %@", PHAssetMediaType.image.rawValue,
                                        now.addingTimeInterval(-RecentScreenshotRule.window) as NSDate)
        options.sortDescriptors = [NSSortDescriptor(key: "creationDate", ascending: false)]
        options.fetchLimit = 20
        var found: [RecentScreenshotRule.Candidate] = []
        PHAsset.fetchAssets(with: options).enumerateObjects { asset, _, _ in
            guard asset.mediaSubtypes.contains(.photoScreenshot), let taken = asset.creationDate else { return }
            found.append(.init(id: asset.localIdentifier, taken: taken))
        }
        guard let pick = RecentScreenshotRule.pick(found, now: now, handled: handled) else {
            offer = nil
            return
        }
        if offer?.id != pick.id {
            offer = Offer(id: pick.id, taken: pick.taken)
            Task { await loadThumbnail(pick.id) }
        } else if offer?.thumbnail == nil {
            Task { await loadThumbnail(pick.id) } // the last try came back empty (still in iCloud, say)
        }
        let due = RecentScreenshotRule.expiry(pick).timeIntervalSince(now)
        expiry?.cancel()
        expiry = Task {
            try? await Task.sleep(for: .seconds(max(1, due)))
            guard !Task.isCancelled else { return }
            refresh()
        }
    }

    /// The offer attached: its picture at the size the composer sends (`attach` scales and encodes it, and says whether
    /// it took it). Only one that was attached leaves the menu: unread, or refused at four images, it stays offered.
    func take(_ attach: @escaping (UIImage) -> Bool) async {
        guard let o = offer else { return }
        guard let asset = PHAsset.fetchAssets(withLocalIdentifiers: [o.id], options: nil).firstObject,
              let data = try? await PhotosTool.loadImageData(asset), let img = UIImage(data: data) else {
            AppModel.shared.toast = Toast(String(localized: "Couldn't read the screenshot"), style: .error)
            return
        }
        guard attach(img) else { return }
        handled.insert(o.id)
        refresh() // an older one of the window may take its place
    }

    /// The access row: the system's prompt, or Settings once it was refused. Access given here is for this menu: Claude's
    /// own photo tools (Settings › Connectors › This iPhone) stay off unless they were turned on before.
    func runGuide() async {
        let row = guide
        guard row != .none else { return }
        guideUsed = true
        UserDefaults.standard.set(true, forKey: Self.guideKey)
        // Before asking or opening Settings: whatever is answered there, now or later, is not Claude's switch.
        Self.keepClaudePhotosAsChosen()
        switch row {
        case .ask:
            _ = await PhotosAccess.request()
            refresh()
            AppModel.shared.registerPhone() // what the hub may call follows the answer
        case .settings:
            if let url = URL(string: UIApplication.openSettingsURLString) { await UIApplication.shared.open(url) }
        case .none:
            break
        }
    }

    /// Claude's Photos switch defaults to on, and the photo permission is one for both: left at its default, it is turned
    /// off, so access given for the composer does not also let Claude read the library. One the user set stays.
    static func keepClaudePhotosAsChosen() {
        if !PhoneDataSource.photos.hasChoice { PhoneDataSource.photos.isEnabled = false }
    }

    private func loadThumbnail(_ id: String) async {
        guard let image = await Self.thumbnail(id), offer?.id == id else { return }
        offer?.thumbnail = image
    }

    /// A square crop the size of a menu icon.
    private nonisolated static func thumbnail(_ id: String) async -> UIImage? {
        guard let asset = PHAsset.fetchAssets(withLocalIdentifiers: [id], options: nil).firstObject else { return nil }
        let options = PHImageRequestOptions()
        options.deliveryMode = .highQualityFormat // one answer, not a blurry one first
        options.resizeMode = .fast
        options.isNetworkAccessAllowed = true
        let side = 96.0
        return await withCheckedContinuation { c in
            PHImageManager.default().requestImage(for: asset, targetSize: CGSize(width: side, height: side), contentMode: .aspectFill,
                                                  options: options) { @Sendable image, _ in
                c.resume(returning: image)
            }
        }
    }
}

/// Photos' change callback, which comes on its own queue.
private nonisolated final class LibraryWatch: NSObject, PHPhotoLibraryChangeObserver, Sendable {
    private let changed: @Sendable () -> Void

    init(_ changed: @escaping @Sendable () -> Void) { self.changed = changed }

    func photoLibraryDidChange(_ changeInstance: PHChange) { changed() }
}

/// The + menu's rows for it, first in the menu: the screenshot just taken, one tap attaches it; without access, the
/// row that asks for it (once).
struct RecentScreenshotMenuItems: View {
    /// Attaches the picture; false when it could not (four already).
    var attach: (UIImage) -> Bool
    @State private var shots = RecentScreenshots.shared

    var body: some View {
        if let offer = shots.offer {
            Button {
                Task { await shots.take(attach) }
            } label: {
                // A menu row's subtitle is a second Text beside the Label (inside the Label only its first line shows).
                Label {
                    Text("Recent Screenshot")
                } icon: {
                    if let thumb = offer.thumbnail {
                        Image(uiImage: thumb).renderingMode(.original)
                    } else {
                        ThemedIcon(Theme.Icon.screenshot)
                    }
                }
                Text(offer.taken, format: .relative(presentation: .named))
            }
            .accessibilityIdentifier("composer-recent-screenshot")
        } else if shots.guide != .none {
            Button {
                Task { await shots.runGuide() }
            } label: {
                Label { Text("Suggest Recent Screenshots") } icon: { ThemedIcon(Theme.Icon.screenshot) }
                if shots.guide == .ask {
                    Text("Allow photo access to see the one you just took here")
                } else {
                    Text("Allow full photo access in Settings")
                }
            }
            .accessibilityIdentifier("composer-recent-screenshot-guide")
        }
    }
}
#endif
