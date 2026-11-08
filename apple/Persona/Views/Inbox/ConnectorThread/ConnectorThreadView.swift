import SwiftUI

// A chat app's conversation opened from the inbox (Telegram's), laid out like the Feishu chat page: others on the
// left, (in a group) the name over the first of a run and the avatar beside its last, mine on the right; a time
// between messages after a pause or on a new day; pictures, stickers, video thumbnails, voice and files; a reply's
// quote leading back to what it answers. Older pages load as the top comes into view. With a reply action, a box at
// the bottom: what the user sends there goes out as they sent it (the hub's gate is for the AI's writes, #1190).

struct ConnectorThreadView: View {
    var model: ConnectorThreadModel
    /// The connector's name (the inbox item's), for Open in <name>.
    var source: String
    @State private var position = ScrollPosition(idType: String.self)
    @State private var flash: QuoteFlashMark?
    @State private var replyTo: ThreadMessage?
    @State private var text = ""
    @State private var selection: TextSelection?
    @State private var olderFailed = false
    @State private var notice: String?
    @FocusState private var focused: Bool
    /// The page's width (bubbles take ThreadLayout.bubbleShare of it) and how far down the summary bar reaches.
    @State private var width: CGFloat = 390
    @State private var topInset: CGFloat = 0

    private var isGroup: Bool { model.chat?.isGroup ?? false }

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                top
                ForEach(Array(model.messages.enumerated()), id: \.element.id) { i, m in
                    let prev = i > 0 ? model.messages[i - 1] : nil
                    if ThreadLayout.separates(m, after: prev) { LarkTimeSeparator(date: m.at) }
                    let grouped = ThreadLayout.continues(m, after: prev)
                    let next = i + 1 < model.messages.count ? model.messages[i + 1] : nil
                    ConnectorThreadRow(connector: model.connector, message: m, grouped: grouped,
                                       endsRun: ThreadLayout.endsRun(m, before: next), isGroup: isGroup,
                                       canReply: model.chat?.reply != nil, bubbleMax: width * ThreadLayout.bubbleShare,
                                       openTitle: ConnectorText.openInTitle(model.connector, name: source),
                                       reply: { answer($0) }, jump: { await jump(to: $0) })
                        .padding(.top, grouped ? LarkChatLayout.groupedGap : Theme.Space.s)
                        .overlay {
                            if flash?.line == m.id { QuoteFlash().id(flash?.id) }
                        }
                        .id(m.id)
                }
            }
            .scrollTargetLayout()
            .padding(.horizontal, Theme.Space.page)
            .padding(.bottom, Theme.Space.m)
        }
        .scrollPosition($position)
        .defaultScrollAnchor(.bottom)
        // What scrolls up under the summary bar fades out at its edge instead of being cut there.
        .onGeometryChange(for: CGSize.self) { CGSize(width: $0.size.width, height: $0.safeAreaInsets.top) } action: {
            width = $0.width
            topInset = $0.height
        }
        .mask {
            VStack(spacing: 0) {
                Color.clear.frame(height: topInset)
                LinearGradient(colors: [.clear, .black], startPoint: .top, endPoint: .bottom).frame(height: Theme.Space.xxl)
                Color.black
            }
            .ignoresSafeArea()
        }
        .scrollDismissesKeyboard(.interactively)
        .overlay {
            if !model.loaded {
                ProgressView()
            } else if model.messages.isEmpty && model.failure == nil {
                EmptyStateView(symbol: Theme.Icon.messagesApp, title: String(localized: "No Messages"), message: String(localized: "There are no messages to show in this conversation yet."))
            }
        }
        .safeAreaInset(edge: .top) {
            if let line = notice ?? model.failure {
                Label(line, systemImage: Theme.Icon.error)
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .lineLimit(3)
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.vertical, Theme.Space.s)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .surface(in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                    .padding(.horizontal, Theme.Space.page)
                    .padding(.top, Theme.Space.xs)
                    .onTapGesture { notice = nil }
                    .accessibilityIdentifier("thread.notice")
            }
        }
        .safeAreaInset(edge: .bottom) {
            if model.chat?.reply != nil { composer }
        }
        .task {
            if !model.loaded { await model.load() }
        }
    }

    /// Above the oldest message: the next page loading as it comes into view, or (it failed) a button to try again.
    @ViewBuilder private var top: some View {
        if model.hasOlder {
            Group {
                if olderFailed {
                    Button { Task { await older() } } label: {
                        Label("Load Earlier Messages", systemImage: Theme.Icon.retry).font(Theme.Font.label).lineLimit(1)
                    }
                    .buttonStyle(.borderless)
                } else {
                    ProgressView().onAppear { Task { await older() } }
                }
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, Theme.Space.m)
            .accessibilityIdentifier("thread.older")
        } else if model.loaded, !model.messages.isEmpty {
            Text("No earlier messages").font(Theme.Font.caption).foregroundStyle(.tertiary)
                .frame(maxWidth: .infinity).padding(.vertical, Theme.Space.m)
        }
    }

    /// The page before, keeping the message that was at the top where it was.
    private func older() async {
        let first = model.messages.first?.id
        let ok = await model.loadOlder()
        olderFailed = !ok && model.hasOlder
        if ok, let first {
            try? await Task.sleep(for: .milliseconds(30))
            position.scrollTo(id: first, anchor: .top)
        }
    }

    /// Back to what a quote answers: the message shown, highlighted; read back a few pages for one not loaded yet.
    private func jump(to id: String) async -> Bool {
        guard await model.reach(id) else {
            notice = String(localized: "The original message is further back. Scroll up a few pages to find it.")
            return false
        }
        withAnimation(Theme.Motion.standard) { position.scrollTo(id: id, anchor: .center) }
        try? await Task.sleep(for: .milliseconds(250))
        flash = QuoteFlashMark(line: id)
        return true
    }

    private func answer(_ m: ThreadMessage) {
        replyTo = m
        focused = true
    }

    private var composer: some View {
        ThreadComposer(placeholder: String(localized: "Replying to \(model.chat?.name ?? "")"), text: $text, selection: $selection, focused: $focused,
                       quote: replyTo.map { ($0.isMine ? String(localized: "Replying to yourself") : String(localized: "Replying to \($0.sender.name)"), $0.summary) }, sending: model.sending,
                       cancelQuote: { replyTo = nil }, send: send)
    }

    private func send() {
        let words = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let quote = replyTo
        Task {
            if await model.reply(words, to: quote) {
                text = ""
                replyTo = nil
                Haptics.success()
                if let last = model.messages.last?.id { withAnimation(Theme.Motion.standard) { position.scrollTo(id: last, anchor: .bottom) } }
            } else {
                Haptics.attention()
            }
        }
    }
}

/// One message: an event centred; others' with, in a group, the name over the first of a run and the avatar beside
/// its last (as Telegram); mine on the right without either. A bubble is as wide as what it holds, at most `bubbleMax`, its quote as wide as the rest; its
/// time is in its bottom-right corner. A picture, sticker or video alone has no bubble, its time on a capsule.
struct ConnectorThreadRow: View {
    var connector: String
    var message: ThreadMessage
    /// It continues the run of the message before: no name over it.
    var grouped: Bool
    /// The run's last: the avatar beside it.
    var endsRun: Bool
    var isGroup: Bool
    var canReply: Bool
    /// The widest a bubble gets (ThreadLayout.bubbleShare of the page), the same for mine and others'.
    var bubbleMax: CGFloat
    /// The menu's Open in Telegram.
    var openTitle: String
    var reply: (ThreadMessage) -> Void
    var jump: (String) async -> Bool
    @Environment(\.openURL) private var openURL

    private var m: ThreadMessage { message }

    var body: some View {
        if m.isEvent {
            Text(m.text ?? "").font(Theme.Font.label).foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: .infinity)
                .padding(.horizontal, Theme.Space.xl)
                .padding(.vertical, Theme.Space.xs)
        } else {
            let mine = m.isMine
            HStack(alignment: .bottom, spacing: Theme.Space.s) {
                if mine {
                    Spacer(minLength: 0)
                } else {
                    Group {
                        if endsRun { ThreadAvatar(connector: connector, name: m.sender.name, ref: m.sender.avatar) } else { Color.clear }
                    }
                    .scaledFrame(width: Theme.Size.wellM, height: endsRun ? Theme.Size.wellM : Theme.Size.line)
                }
                VStack(alignment: mine ? .trailing : .leading, spacing: Theme.Space.xs) {
                    if isGroup, !mine, !grouped {
                        Text(m.sender.name).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                    }
                    content.contextMenu { menu }
                }
                .frame(maxWidth: bubbleMax, alignment: mine ? .trailing : .leading)
                if !mine { Spacer(minLength: 0) }
            }
            .accessibilityElement(children: .combine)
        }
    }

    private var media: ThreadMedia? { m.media?.first }
    private var words: String { m.text?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "" }
    private var stamp: String { ThreadLayout.stamp(m) }

    /// A picture, sticker or video that ends the message with no words after it: the time goes on it.
    private var pictureLast: Bool {
        guard let media, words.isEmpty else { return false }
        return ["sticker", "image", "video"].contains(media.kind) && media.picture != nil
    }

    @ViewBuilder private var content: some View {
        if let media, pictureLast, m.replyTo == nil {
            ThreadMediaView(connector: connector, media: media) // a picture alone: no bubble around it
                .overlay(alignment: .bottomTrailing) { StampCapsule(text: stamp) }
        } else {
            WidestColumn(spacing: Theme.Space.sm) {
                if let q = m.replyTo {
                    QuoteCard(heading: q.from ?? String(localized: "Messages [Quote Views 4da199]"), summary: q.text.flatMap { $0.isEmpty ? nil : $0 } ?? String(localized: "(Original message)"), tint: LarkColor.blue,
                              ink: .primary) {
                        Task {
                            if await jump(q.id) { Haptics.tap() } else { Haptics.attention() }
                        }
                    }
                }
                if let media {
                    ThreadMediaView(connector: connector, media: media)
                        .overlay(alignment: .bottomTrailing) {
                            if pictureLast { StampCapsule(text: stamp) }
                        }
                }
                if !words.isEmpty {
                    StampedText(text: words, stamp: stamp)
                } else if !pictureLast {
                    Text(stamp).font(Theme.Font.caption).monospacedDigit().foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .trailing)
                }
            }
            .padding(Theme.Space.m)
            .background(m.isMine ? LarkColor.myBubble : LarkColor.otherBubble, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        }
    }

    @ViewBuilder private var menu: some View {
        if canReply {
            Button { reply(m) } label: { Label("Reply", systemImage: Theme.Icon.reply) }
        }
        if !m.summary.isEmpty {
            Button {
                UIPasteboard.general.string = m.text?.isEmpty == false ? m.text : m.summary
                Haptics.success()
            } label: { Label("Copy [Readiness Views d37380]", systemImage: Theme.Icon.copy) }
        }
        if let s = m.url, let u = URL(string: s) {
            Button { openURL(u) } label: { Label(openTitle, systemImage: Theme.Icon.openExternal) }
        }
    }
}

/// A message's media: a picture (tap: full screen), a sticker, a video's thumbnail with its length, a voice message's
/// length, a file's name and size. What cannot be fetched shows a placeholder of its kind.
struct ThreadMediaView: View {
    var connector: String
    var media: ThreadMedia
    @State private var viewing = false
    @Namespace private var zoom

    var body: some View {
        switch media.kind {
        case "image":
            picture
        case "sticker":
            if let ref = media.picture {
                LarkRemoteImage(url: ConnectorMedia.url(connector, ref: ref), maxPixel: 400, contentMode: .fit)
                    .scaledFrame(width: Theme.Size.box120, height: Theme.Size.box120)
                    .accessibilityLabel("Sticker \(media.emoji ?? "")")
            } else {
                placeholder(Theme.Icon.photo, String(localized: "Sticker \(media.emoji ?? "")"))
            }
        case "video":
            ZStack(alignment: .bottomLeading) {
                if let ref = media.thumb {
                    let size = LarkImagePart.displaySize(width: Double(media.width ?? 0), height: Double(media.height ?? 0))
                    LarkRemoteImage(url: ConnectorMedia.url(connector, ref: ref), maxPixel: 900)
                        .frame(width: size.width, height: size.height)
                        .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
                        .overlay {
                            ThemedIcon(Theme.Icon.playMedia).font(Theme.Font.display).foregroundStyle(.white.opacity(0.9))
                        }
                } else {
                    placeholder(Theme.Icon.video, String(localized: "Video"))
                }
                Text(ThreadMediaText.duration(media.duration ?? 0))
                    .font(Theme.Font.caption).monospacedDigit().foregroundStyle(.white)
                    .padding(.horizontal, Theme.Space.sm).padding(.vertical, Theme.Space.xxs)
                    .background(.black.opacity(0.5), in: .capsule)
                    .padding(Theme.Space.s)
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("Video \(ThreadMediaText.duration(media.duration ?? 0))")
        case "voice":
            Label {
                Text("Voice \(ThreadMediaText.duration(media.duration ?? 0))").font(Theme.Font.secondaryMedium).monospacedDigit()
            } icon: {
                ThemedIcon(Theme.Icon.voiceMessage)
            }
            .foregroundStyle(.primary)
        default:
            let name = media.name ?? String(localized: "Files")
            HStack(spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Text(name).font(Theme.Font.secondaryMedium).foregroundStyle(.primary).lineLimit(2).multilineTextAlignment(.leading)
                    if let size = media.size, size > 0 {
                        Text(size.storageText).font(Theme.Font.label).foregroundStyle(.secondary)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                LarkFileCard.badge((name as NSString).pathExtension.uppercased())
            }
            .scaledFrame(maxWidth: Theme.Size.box240)
            .accessibilityElement(children: .combine)
        }
    }

    @ViewBuilder private var picture: some View {
        if let ref = media.picture {
            let url = ConnectorMedia.url(connector, ref: ref)
            let size = LarkImagePart.displaySize(width: Double(media.width ?? 0), height: Double(media.height ?? 0))
            LarkRemoteImage(url: url, maxPixel: 900)
                .frame(width: size.width, height: size.height)
                .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous).strokeBorder(Color.primary.opacity(0.06)))
                .contentShape(.rect)
                .matchedTransitionSource(id: url, in: zoom)
                .onTapGesture { viewing = true }
                .fullScreenCover(isPresented: $viewing) { ImageViewer(urls: [url], index: 0).navigationTransition(.zoom(sourceID: url, in: zoom)) }
                .accessibilityLabel("images")
        } else {
            placeholder(Theme.Icon.photo, String(localized: "images"))
        }
    }

    private func placeholder(_ symbol: String, _ label: String) -> some View {
        Label(label, systemImage: symbol).font(Theme.Font.secondary).foregroundStyle(.secondary)
            .padding(Theme.Space.m)
            .background(Color.primary.opacity(0.05), in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
    }
}

/// A message's words with its time in the corner, as Telegram: after the last line when it fits there, else on a line
/// of its own at the right. A clear copy of the time ends the words, so they leave it room; the shown one sits over it.
struct StampedText: View {
    var text: String
    var stamp: String

    var body: some View {
        (Text(text).font(Theme.Font.body) + Text("\u{2003}" + stamp).font(Theme.Font.caption).monospacedDigit().foregroundStyle(.clear))
            .lineHeight(.body)
            .foregroundStyle(.primary)
            .textSelection(.enabled)
            .overlay(alignment: .bottomTrailing) {
                Text(stamp).font(Theme.Font.caption).monospacedDigit().foregroundStyle(.secondary).accessibilityHidden(true)
            }
    }
}

/// The time over a picture's corner: on a dark, half-clear capsule, readable on any picture.
struct StampCapsule: View {
    var text: String

    var body: some View {
        Text(text).font(Theme.Font.caption).monospacedDigit().foregroundStyle(.white)
            .padding(.horizontal, Theme.Space.sm)
            .padding(.vertical, Theme.Space.xxs)
            .background(.black.opacity(0.45), in: .capsule)
            .padding(Theme.Space.s)
    }
}
