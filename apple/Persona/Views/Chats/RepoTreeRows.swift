import SwiftUI

/// Repository browsing keeps one compact line per Agent, without reply previews.
nonisolated enum WorkspaceList {
    static func age(_ at: Date, now: Date) -> String {
        let seconds = max(0, now.timeIntervalSince(at))
        if seconds < 60 { return String(localized: "workspace.age.now", defaultValue: "now") }
        if seconds < 3600 { return String.localizedStringWithFormat(String(localized: "workspace.age.minutes", defaultValue: "%lldm"), Int64(seconds / 60)) }
        if seconds < 86400 { return String.localizedStringWithFormat(String(localized: "workspace.age.hours", defaultValue: "%lldh"), Int64(seconds / 3600)) }
        return String.localizedStringWithFormat(String(localized: "workspace.age.days", defaultValue: "%lldd"), Int64(seconds / 86400))
    }
    static func rowInsets(level: Int) -> EdgeInsets {
        EdgeInsets(top: Theme.Space.xs, leading: Theme.Space.page + Theme.Space.m * CGFloat(level),
                   bottom: Theme.Space.xs, trailing: Theme.Space.page)
    }
}

extension RepoTree.Worktree {
    var sidebarUnread: Bool { workspace?.unread ?? sessions.contains { $0.state == .unread } }
    var sidebarState: AgentState {
        switch workspace?.sidebarStatus {
        case "working", "monitoring": return .working
        case "permission", "failed", "unconfirmed": return .needsYou
        case "active", "done": return .unread
        case "inactive", "interrupted": return .idle
        default: return sessions.map(\.state).min { $0.priority < $1.priority } ?? .idle
        }
    }
    func contains(workspace key: String?) -> Bool {
        guard let key else { return false }
        return workspace?.key == key || children.contains { $0.contains(workspace: key) }
    }
}

struct WorkspaceSidebarStatus: View {
    var workspace: RepoTree.Worktree

    var body: some View {
        Group {
            if workspace.sidebarUnread {
                Image(systemName: Theme.Icon.workspaceUnread).font(Theme.WorkspaceSidebar.meta)
                    .foregroundStyle(Theme.WorkspaceSidebar.unread)
            } else {
                StatusDot(state: workspace.sidebarState, size: Theme.Size.dot)
            }
        }
        .scaledFrame(width: Theme.WorkspaceSidebar.statusLane, height: Theme.WorkspaceSidebar.titleHeight)
    }
}

/// A card owns its descendants so selection washes and indentation follow the same geometry at every level.
struct WorkspaceSidebarCard<AgentContent: View, Actions: View>: View {
    var workspace: RepoTree.Worktree
    var project: String
    var parentWorkspace: HostWorkspace? = nil
    var selectedSession: SessionKey?
    var selectedWorkspace: String?
    var folded: (String) -> Bool
    var toggle: (String) -> Void
    var open: (HostWorkspace) -> Void
    var openAgent: (SessionKey) -> Void = { _ in }
    var appeared: (Session) -> Void = { _ in }
    var agentIdentifier = "agents.row"
    var ordered: ([Session]) -> [Session]
    @ViewBuilder var agent: (Session) -> AgentContent
    @ViewBuilder var actions: (RepoTree.Worktree, Session?) -> Actions
    @State private var hovered = false

    private var active: Bool {
        workspace.contains(workspace: selectedWorkspace) || selectedSession.map { key in workspace.treeSessions.contains { $0.key == key } } == true
    }
    private var childOpen: Bool { !folded(workspace.childrenFoldID) }
    private var agentOpen: Bool { !folded(workspace.agentsFoldID) }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.sm) {
            WorkspaceSidebarControl(open: openWorktree) {
                HStack(alignment: .top, spacing: Theme.Space.xxs) {
                    WorkspaceSidebarStatus(workspace: workspace)
                    VStack(alignment: .leading, spacing: Theme.Space.sm) {
                        HStack(spacing: Theme.Space.sm) {
                            Text(workspace.name)
                                .font(workspace.sidebarUnread ? Theme.WorkspaceSidebar.titleUnread : Theme.WorkspaceSidebar.title)
                                .foregroundStyle(Theme.WorkspaceSidebar.foreground).lineLimit(1)
                            if workspace.main {
                                Text(String(localized: "workspace.primary", defaultValue: "Primary"))
                                    .font(Theme.WorkspaceSidebar.meta).foregroundStyle(Theme.WorkspaceSidebar.foreground.opacity(0.7))
                                    .padding(.horizontal, Theme.Space.sm)
                                    .scaledFrame(height: Theme.WorkspaceSidebar.badgeHeight)
                                    .background(Theme.WorkspaceSidebar.foreground.opacity(0.06), in: .rect(cornerRadius: Theme.WorkspaceSidebar.chipRadius))
                                    .overlay { RoundedRectangle(cornerRadius: Theme.WorkspaceSidebar.chipRadius).stroke(Theme.WorkspaceSidebar.foreground.opacity(0.2)) }
                                    .fixedSize()
                            }
                            Spacer(minLength: 0)
                        }
                        .scaledFrame(minHeight: Theme.WorkspaceSidebar.titleHeight)
                        .frame(maxWidth: .infinity, alignment: .leading).contentShape(.rect)
                        metadata
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading).contentShape(.rect)
            } actions: { actions(workspace, nil) }
            .accessibilityIdentifier("agents.worktree")
            .accessibilityLabel(workspace.name)
            .accessibilityValue([workspace.main ? String(localized: "workspace.primary", defaultValue: "Primary") : "",
                                 workspace.workspace?.branch ?? ""].filter { !$0.isEmpty }.joined(separator: ", "))
            if !workspace.sessions.isEmpty || !workspace.children.isEmpty {
                VStack(alignment: .leading, spacing: Theme.Space.sm) {
                    if workspace.sessions.count > 1 {
                        agentDisclosure
                        if agentOpen { ForEach(ordered(workspace.sessions)) { agentRow($0) } }
                    } else {
                        ForEach(workspace.sessions) { agentRow($0) }
                    }
                    if !workspace.children.isEmpty {
                        Button { toggle(workspace.childrenFoldID) } label: {
                            WorkspaceChildrenChip(children: workspace.children, open: childOpen)
                        }
                        .buttonStyle(.plain).accessibilityIdentifier("agents.childSpaces")
                    }
                }
                .padding(.leading, Theme.WorkspaceSidebar.statusLane + Theme.Space.xxs)
            }
            if !workspace.children.isEmpty && childOpen {
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    ForEach(workspace.children) { child in
                        WorkspaceSidebarCard(workspace: child, project: workspace.workspace?.projectName.flatMap { $0.isEmpty ? nil : $0 } ?? project,
                                             parentWorkspace: workspace.workspace ?? parentWorkspace, selectedSession: selectedSession,
                                             selectedWorkspace: selectedWorkspace, folded: folded, toggle: toggle,
                                             open: open, openAgent: openAgent, appeared: appeared, agentIdentifier: agentIdentifier,
                                             ordered: ordered, agent: agent, actions: actions)
                    }
                }.padding(.leading, Theme.WorkspaceSidebar.childInset)
            }
        }
        .padding(.leading, Theme.WorkspaceSidebar.cardLeading)
        .padding(.trailing, Theme.Space.sm)
        .padding(.vertical, Theme.Space.sm)
        .background(active ? Theme.WorkspaceSidebar.selected : hovered ? Theme.WorkspaceSidebar.hovered : Color.clear,
                    in: .rect(cornerRadius: Theme.WorkspaceSidebar.cardRadius))
        .overlay {
            if active { RoundedRectangle(cornerRadius: Theme.WorkspaceSidebar.cardRadius).stroke(Theme.WorkspaceSidebar.border.opacity(0.4)) }
        }
        .onHover { hovered = $0 }
        .accessibilityElement(children: .contain)
    }

    private func openWorktree() {
        if let first = ordered(workspace.sessions).first { openAgent(first.key) }
        else if let target = workspace.workspace { open(target) }
    }

    /// The Agent row and the title/branch control receive their worktree actions from the same factory.
    private func agentRow(_ session: Session) -> some View {
        WorkspaceSidebarControl(open: { openAgent(session.key) }) {
            agent(session)
                .frame(maxWidth: .infinity, alignment: .leading)
                #if os(macOS)
                .background {
                    if selectedWorkspace == nil && selectedSession == session.key {
                        RoundedRectangle(cornerRadius: Theme.Radius.control).fill(Theme.Fill.macSelected)
                    }
                }
                #endif
                .contentShape(.rect)
        } actions: { actions(workspace, session) }
        .macHoverShade(in: RoundedRectangle(cornerRadius: Theme.Radius.control), outset: 0)
        #if os(macOS)
        .macOptionClick { MacShellState.shared.chatWindow = session.key }
        #endif
        .accessibilityIdentifier(agentIdentifier)
        .id(session.id)
        .onAppear { appeared(session) }
    }

    private var metadata: some View {
        HStack(spacing: Theme.Space.sm) {
            if let name = workspace.repositoryBadge(parent: parentWorkspace, inheritedName: project) {
                HStack(spacing: Theme.Space.xxs) {
                    Rectangle().fill(Theme.WorkspaceSidebar.muted).scaledFrame(width: Theme.Size.dotXS, height: Theme.Size.dotXS)
                    Text(name).lineLimit(1)
                }
                .font(Theme.WorkspaceSidebar.meta).foregroundStyle(Theme.WorkspaceSidebar.foreground)
                .padding(.horizontal, Theme.Space.xxs).scaledFrame(height: Theme.WorkspaceSidebar.badgeHeight)
                .scaledFrame(maxWidth: Theme.WorkspaceSidebar.projectBadgeWidth)
                .background(Theme.WorkspaceSidebar.background, in: .rect(cornerRadius: Theme.WorkspaceSidebar.chipRadius))
                .overlay { RoundedRectangle(cornerRadius: Theme.WorkspaceSidebar.chipRadius).stroke(Theme.WorkspaceSidebar.border) }
                .accessibilityIdentifier("agents.workspaceProject")
            }
            if let target = workspace.workspace {
                if !target.branch.isEmpty {
                    Text(target.branch).font(Theme.WorkspaceSidebar.meta).foregroundStyle(Theme.WorkspaceSidebar.muted).lineLimit(1)
                        .accessibilityIdentifier("agents.workspaceBranch")
                } else if target.workspaceKind == "git" {
                    Label {
                        Text(String(localized: "Detached HEAD") + (target.head.isEmpty ? "" : " @ " + String(target.head.prefix(7))))
                    } icon: { Image(systemName: Theme.Icon.gitBranch) }
                        .font(Theme.WorkspaceSidebar.meta).foregroundStyle(Theme.cautionText).lineLimit(1)
                        .padding(.horizontal, Theme.Space.xs).padding(.vertical, Theme.Space.xxs)
                        .background(Theme.caution.opacity(0.06), in: .rect(cornerRadius: Theme.WorkspaceSidebar.chipRadius))
                        .overlay { RoundedRectangle(cornerRadius: Theme.WorkspaceSidebar.chipRadius).stroke(Theme.caution.opacity(0.35)) }
                        .accessibilityIdentifier("agents.workspaceDetached")
                } else if target.workspaceKind == "folder-workspace" || target.workspaceKind == "folder" {
                    Text((target.cwd as NSString).lastPathComponent).font(Theme.WorkspaceSidebar.meta).foregroundStyle(Theme.WorkspaceSidebar.muted).lineLimit(1)
                }
                Spacer(minLength: 0)
                Image(systemName: Theme.Icon.workspaceHost).font(Theme.WorkspaceSidebar.meta)
                    .rotationEffect(Theme.WorkspaceSidebar.hostRotation)
                    .foregroundStyle(Theme.WorkspaceSidebar.muted)
                    .accessibilityLabel(target.available ? target.source : String(localized: "This computer is offline"))
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var agentDisclosure: some View {
        Button { toggle(workspace.agentsFoldID) } label: {
            HStack(spacing: Theme.Space.xs) {
                if let s = workspace.sessions.first {
                    StatusDot(state: s.state, size: Theme.Size.dotS)
                    AgentMark(s)
                }
                Text("+\(workspace.sessions.count)").font(Theme.WorkspaceSidebar.meta).foregroundStyle(Theme.WorkspaceSidebar.muted)
                Spacer(minLength: 0)
                FoldChevron(folded: !agentOpen)
            }
            .padding(.horizontal, Theme.Space.sm).scaledFrame(height: Theme.WorkspaceSidebar.agentHeight)
            .background(Theme.WorkspaceSidebar.foreground.opacity(0.02), in: .rect(cornerRadius: Theme.WorkspaceSidebar.chipRadius))
            .overlay { RoundedRectangle(cornerRadius: Theme.WorkspaceSidebar.chipRadius).stroke(Theme.WorkspaceSidebar.border) }
            .contentShape(.rect)
        }
        .buttonStyle(.plain).accessibilityIdentifier("agents.foldWorkspace")
        .accessibilityLabel(String(localized: "workspace.toggle", defaultValue: "Expand or collapse worktree"))
    }
}

struct WorkspaceChildrenChip: View {
    var children: [RepoTree.Worktree]
    var open: Bool

    var body: some View {
        ViewThatFits(in: .horizontal) {
            contents(words: true)
            contents(words: false)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([String(localized: "\(children.count) items [Repo Tree Rows 8af700]"), open ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]")].joined(separator: ", "))
    }

    private func contents(words: Bool) -> some View {
        HStack(spacing: Theme.Space.xs) {
            WorkspaceLineageMark()
            if words { Text("\(children.count) items [Repo Tree Rows 8af700]") }
            else { Text(children.count.formatted()) }
            Rectangle().fill(Theme.WorkspaceSidebar.border).scaledFrame(width: Theme.WorkspaceSidebar.rule, height: Theme.WorkspaceSidebar.statusLane)
            ForEach([AgentState.needsYou, .working, .unread, .idle, .offline, .unknown], id: \.rawValue) { state in
                let count = children.filter { $0.sidebarState == state }.count
                if count > 0 {
                    StatusDot(state: state, size: Theme.Size.dotS)
                    Text(count.formatted()).monospacedDigit()
                }
            }
            FoldChevron(folded: !open)
        }
        .font(Theme.WorkspaceSidebar.meta).foregroundStyle(Theme.WorkspaceSidebar.foreground)
        .padding(.horizontal, Theme.Space.xs).padding(.vertical, Theme.Space.xxs)
        .fixedSize(horizontal: true, vertical: false)
        .background(Theme.WorkspaceSidebar.chip, in: .rect(cornerRadius: Theme.WorkspaceSidebar.chipRadius))
        .overlay { RoundedRectangle(cornerRadius: Theme.WorkspaceSidebar.chipRadius).stroke(Theme.WorkspaceSidebar.border) }
    }
}

struct WorkspaceAgentRow: View {
    var session: Session
    var time: AgentTime?
    var muted: Bool
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        Group {
            if typeSize.isAccessibilitySize {
                // Keep the Agent's name above model and age at accessibility text sizes.
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    HStack(spacing: Theme.Space.xs) {
                        title
                        Spacer(minLength: 0)
                    }
                    .fixedSize(horizontal: false, vertical: true)
                    HStack(spacing: Theme.Space.xs) { metadata }
                }
                .padding(.vertical, Theme.Space.xs)
            } else {
                HStack(spacing: Theme.Space.xs) {
                    title
                    Spacer(minLength: 0)
                    metadata
                }
                .scaledFrame(height: Theme.WorkspaceSidebar.agentHeight)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading).contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityValue(session.stateLabel(muted: muted))
    }

    @ViewBuilder private var title: some View {
        if session.state == .unread || session.state == .idle {
            Image(systemName: Theme.Icon.success).font(Theme.WorkspaceSidebar.meta).foregroundStyle(Theme.WorkspaceSidebar.success)
        } else { StatusDot(state: session.state, size: Theme.Size.dotS) }
        AgentMark(session, side: Theme.Size.markXS)
        Text(session.displayTitle).font(session.state == .unread ? Theme.Font.labelStrong : Theme.WorkspaceSidebar.agent)
            .foregroundStyle(session.state == .unread ? Theme.WorkspaceSidebar.foreground : Theme.WorkspaceSidebar.muted)
            .lineLimit(typeSize.isAccessibilitySize ? 2 : 1).layoutPriority(1)
    }

    @ViewBuilder private var metadata: some View {
        Text(session.model.flatMap { $0.isEmpty ? nil : $0 } ?? session.agentLabel)
            .font(Theme.WorkspaceSidebar.meta.monospaced()).foregroundStyle(Theme.WorkspaceSidebar.muted).lineLimit(1)
            .scaledWidthCap(Theme.WorkspaceSidebar.projectBadgeWidth).fixedSize(horizontal: !typeSize.isAccessibilitySize, vertical: false)
            .accessibilityIdentifier("agents.workspaceModel")
        if let at = time?.updatedAt ?? session.updatedAt, at.isMeaningful {
            TimelineView(.periodic(from: .now, by: 30)) { tick in
                Text(WorkspaceList.age(at, now: tick.date)).font(Theme.WorkspaceSidebar.meta.monospacedDigit())
                    .foregroundStyle(Theme.WorkspaceSidebar.muted).lineLimit(1)
            }.fixedSize()
        }
    }
}

/// A machine's heading where the Agents list is split by machine (By Repository, By Project with agents on more than one machine,
/// #2108): its mark in a tile and its name a level above the headers under it, its agents' state counts on the right.
struct HostHeading: View {
    var symbol: String
    var title: String
    var group: SpaceGroup
    @Environment(\.compactList) private var compact

    var body: some View {
        CountsHeading {
            ThemedIcon(symbol)
                .font(Theme.Font.secondaryStrong)
                .foregroundStyle(Color.primary)
                .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
                .background(Theme.Fill.chip, in: .rect(cornerRadius: Theme.Radius.badge))
            // Compact List's spaces and repositories are titled at the heading's size: the machine goes a level up.
            Text(title).font(compact ? Theme.Font.title : Theme.Font.heading).foregroundStyle(Color.primary).textCase(nil).lineLimit(1)
        } counts: {
            StateCounts(group: group)
        }
    }
}

/// A working tree's row in its repository's card (By Repository): ⌂ for the main checkout, the branch mark for the others, how
/// many agents (Idle when nothing goes on), and the chevron that turns as it opens.
struct WorktreeRow: View {
    var name: String
    var main: Bool
    var count: Int
    var quiet: Bool
    var open: Bool
    var showChevron = true
    @Environment(\.compactList) private var compact

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(main ? Theme.Icon.home : Theme.Icon.gitBranch)
                .font(compact ? CompactList.meta : Theme.Font.caption)
                .foregroundStyle(.secondary)
                .scaledFrame(width: compact ? CompactList.mark : Theme.Size.iconXS)
            Text(name)
                .font(compact ? CompactList.metaStrong : Theme.Font.secondaryMedium)
                .foregroundStyle(quiet ? Color.secondary : Color.primary)
                .lineLimit(1)
            Spacer(minLength: Theme.Space.xs)
            Text(quiet ? String(localized: "Idle") : String(localized: "\(count) agents"))
                .font(compact ? CompactList.meta : Theme.Font.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .fixedSize()
            if showChevron { FoldChevron(folded: !open) }
        }
        .padding(.vertical, compact ? 0 : Theme.Space.xxs)
        .contentShape(.rect)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([main ? String(localized: "Main worktree") : String(localized: "Worktree"), name,
                             quiet ? String(localized: "Idle") : String(localized: "\(count) agents"),
                             showChevron ? (open ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]")) : ""].filter { !$0.isEmpty }
            .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
        .accessibilityAddTraits(.isButton)
    }
}

/// The "N items" row under a working tree (By Repository): the spaces its host nests under it (Orca's child worktrees, #3075),
/// their agents' state counts, and the chevron that turns as it opens.
struct ChildSpacesRow: View {
    var count: Int
    var open: Bool
    /// Everything nested under it, for the counts.
    var group: SpaceGroup
    var dense = false
    @Environment(\.compactList) private var compact

    var body: some View {
        CountsHeading {
            ThemedIcon(Theme.Icon.childSpaces)
                .font(compact ? CompactList.meta : Theme.Font.caption)
                .foregroundStyle(.secondary)
                .scaledFrame(width: compact ? CompactList.mark : Theme.Size.iconXS)
            Text("\(count) items [Repo Tree Rows 8af700]")
                .font(compact ? CompactList.metaStrong : Theme.Font.secondaryMedium)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .fixedSize()
        } counts: {
            StateCounts(group: group)
        } trailing: {
            FoldChevron(folded: !open)
        }
        .padding(.horizontal, dense ? Theme.Space.sm : 0)
        .padding(.vertical, dense ? Theme.Space.xxs : compact ? 0 : Theme.Space.xxs)
        .background(dense ? Theme.Fill.chip : Color.clear, in: .rect(cornerRadius: Theme.Radius.badge))
        .fixedSize(horizontal: dense, vertical: false)
        .contentShape(.rect)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([String(localized: "\(count) items [Repo Tree Rows 8af700]"), StateCounts.spoken(group), open ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]")].filter { !$0.isEmpty }
            .joined(separator: String(localized: ", ", comment: "Separator between the parts of a VoiceOver label")))
        .accessibilityAddTraits(.isButton)
    }
}

/// The ⋯ of a repository whose agents are in more than one space (By Repository): what applies to all of them. Equal while
/// its unread agents are: the menu held open is not rebuilt by every push.
struct RepoMenuItems: View, Equatable {
    @Environment(AppModel.self) private var model
    nonisolated let unread: [SessionKey]

    nonisolated static func == (a: Self, b: Self) -> Bool { a.unread == b.unread }

    var body: some View {
        if !unread.isEmpty {
            Button { model.markSeen(unread) } label: { Label(StateName.markAllSeen(unread.count), systemImage: Theme.Icon.success) }
        }
    }
}
