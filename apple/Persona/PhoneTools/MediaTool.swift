import Foundation
#if os(iOS)
import MusicKit
#endif

/// Music on the iPhone (#2284): Apple Music plays through the system player (MusicKit), which really plays; Spotify and
/// YouTube are only opened at a search, and the result says opened, not playing.
enum MediaTool {
    static var tools: [PhoneTool] {
        #if os(iOS)
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_media_play",
                    description: """
                    Search Apple Music and play the best match in the Music app on the iPhone: a song (default), an \
                    album or a playlist. Needs an Apple Music subscription to play from the catalog; without one the \
                    tool says so. Returns what was queued and whether it is playing.
                    """,
                    inputSchema: Schema.object([
                        "query": Schema.string("What to play, e.g. a song and artist, an album, or a mood such as focus coding."),
                        "kind": Schema.string("song (default), album or playlist.", values: ["song", "album", "playlist"]),
                    ], required: ["query"])
                ),
                source: .media,
                run: { args in try await play(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_media_control",
                    description: """
                    Control the Music app's player on the iPhone: play, pause, next, previous, or status (what is \
                    playing). Only Apple Music's player: other apps cannot be controlled.
                    """,
                    inputSchema: Schema.object([
                        "action": Schema.string("play, pause, next, previous or status.", values: ["play", "pause", "next", "previous", "status"]),
                    ], required: ["action"])
                ),
                source: .media,
                run: { args in try await control(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_media_open",
                    description: """
                    Open a search in Spotify or YouTube (their app when installed, otherwise the web page). This only \
                    opens the search: nothing plays until the user picks something, so say it was opened, not that \
                    it is playing. Persona must be open on the iPhone.
                    """,
                    inputSchema: Schema.object([
                        "service": Schema.string("spotify or youtube.", values: ["spotify", "youtube"]),
                        "query": Schema.string("What to search for."),
                    ], required: ["service", "query"])
                ),
                source: .media,
                run: { args in try await openSearch(args) }
            ),
        ]
        #else
        []
        #endif
    }

    /// The page a search opens in another service's app (a universal link: the app when installed, else the web).
    nonisolated static func searchURL(service: String, query: String) -> URL? {
        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "-._~"))
        let term = query.addingPercentEncoding(withAllowedCharacters: allowed) ?? query
        switch service {
        case "spotify": return URL(string: "https://open.spotify.com/search/\(term)")
        case "youtube": return URL(string: "https://www.youtube.com/results?search_query=\(term)")
        default: return nil
        }
    }

    #if os(iOS)
    static func play(_ args: JSONValue) async throws -> [ToolContent] {
        guard let query = ToolArgs.string(args, "query") else { throw ToolFailure(.invalidArgument, "query is required") }
        let kind = ToolArgs.string(args, "kind") ?? "song"
        try MediaAccess.require()
        let subscription = try await MusicSubscription.current
        guard subscription.canPlayCatalogContent else {
            throw ToolFailure(.unavailable, "This Apple Account has no Apple Music subscription that can play the catalog, so nothing was played.")
        }
        let player = SystemMusicPlayer.shared
        let title: String
        let artist: String?
        switch kind {
        case "album":
            var request = MusicCatalogSearchRequest(term: query, types: [Album.self])
            request.limit = 1
            guard let album = try await request.response().albums.first else { throw notFound(query) }
            player.queue = [album]
            title = album.title
            artist = album.artistName
        case "playlist":
            var request = MusicCatalogSearchRequest(term: query, types: [Playlist.self])
            request.limit = 1
            guard let playlist = try await request.response().playlists.first else { throw notFound(query) }
            player.queue = [playlist]
            title = playlist.name
            artist = playlist.curatorName
        case "song":
            var request = MusicCatalogSearchRequest(term: query, types: [Song.self])
            request.limit = 1
            guard let song = try await request.response().songs.first else { throw notFound(query) }
            player.queue = [song]
            title = song.title
            artist = song.artistName
        default:
            throw ToolFailure(.invalidArgument, "kind must be song, album or playlist")
        }
        try await player.play()
        return Output.json(Output.object([
            ("kind", .string(kind)),
            ("title", .string(title)),
            ("artist", Output.opt(artist)),
            ("playback", .string(status(player.state.playbackStatus))),
            ("app", .string("Music")),
        ]))
    }

    private static func notFound(_ query: String) -> ToolFailure {
        ToolFailure(.invalidArgument, "Apple Music found nothing for \"\(query)\"")
    }

    static func control(_ args: JSONValue) async throws -> [ToolContent] {
        guard let action = ToolArgs.string(args, "action") else { throw ToolFailure(.invalidArgument, "action is required") }
        try MediaAccess.require()
        let player = SystemMusicPlayer.shared
        switch action {
        case "play": try await player.play()
        case "pause": player.pause()
        case "next": try await player.skipToNextEntry()
        case "previous": try await player.skipToPreviousEntry()
        case "status": break
        default: throw ToolFailure(.invalidArgument, "action must be play, pause, next, previous or status")
        }
        let entry = player.queue.currentEntry
        return Output.json(Output.object([
            ("action", .string(action)),
            ("playback", .string(status(player.state.playbackStatus))),
            ("title", Output.opt(entry?.title)),
            ("subtitle", Output.opt(entry?.subtitle)),
        ]))
    }

    static func openSearch(_ args: JSONValue) async throws -> [ToolContent] {
        guard let service = ToolArgs.string(args, "service"), let query = ToolArgs.string(args, "query") else {
            throw ToolFailure(.invalidArgument, "service and query are required")
        }
        guard let url = searchURL(service: service, query: query) else {
            throw ToolFailure(.invalidArgument, "service must be spotify or youtube")
        }
        try Foreground.require("open \(service)")
        guard await Foreground.open(url) else { throw ToolFailure(.unavailable, "iOS did not open \(service).") }
        return Output.json(Output.object([
            ("opened", .string(service)),
            ("query", .string(query)),
            ("playing", .bool(false)),
            ("note", .string("Only the search was opened; nothing plays until the user picks something.")),
        ]))
    }

    private static func status(_ status: MusicPlayer.PlaybackStatus) -> String {
        switch status {
        case .playing: "playing"
        case .paused: "paused"
        case .stopped: "stopped"
        case .interrupted: "interrupted"
        case .seekingForward, .seekingBackward: "seeking"
        @unknown default: "unknown"
        }
    }
    #endif
}

#if os(iOS)
/// Apple Music's permission (`NSAppleMusicUsageDescription`). Catalog requests also need the MusicKit service on the
/// App ID (docs/apple/phone-tools.md).
enum MediaAccess {
    static func status() -> PhoneAuthStatus {
        switch MusicAuthorization.currentStatus {
        case .authorized: .granted
        case .denied, .restricted: .denied
        case .notDetermined: .notDetermined
        @unknown default: .notDetermined
        }
    }

    static func request() async -> PhoneAuthStatus {
        _ = await MusicAuthorization.request()
        return status()
    }

    static func require() throws {
        switch status() {
        case .granted, .limited, .notNeeded: return
        case .denied: throw ToolFailure(.denied, "Apple Music")
        case .notDetermined: throw ToolFailure(.denied, "Apple Music access has not been requested yet")
        }
    }
}
#endif
