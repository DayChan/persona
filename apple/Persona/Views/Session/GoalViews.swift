import SwiftUI

// MARK: - Goal

extension GoalInfo {
    /// The status's color: blue while it is worked on, green once it holds, orange when it stopped short.
    var tint: Color {
        if isActive { return Theme.loop }
        return isComplete ? Theme.positive : Theme.caution
    }

    /// What it has spent so far, in words ("12.3k tokens · 42 min"), nil when nothing is known.
    var usage: String? {
        var parts: [String] = []
        if let t = tokensUsed, t > 0 {
            let used = t.formatted(.number.notation(.compactName))
            parts.append(tokenBudget.map { $0 > 0 ? "\(used) / \($0.formatted(.number.notation(.compactName))) tokens" : "\(used) tokens" }
                         ?? "\(used) tokens")
        }
        if let s = seconds, s > 0 {
            if s < 3600 {
                let m = max(s / 60, 1)
                parts.append(String(localized: "\(m) min [Task Views 135d12]"))
            } else {
                let h = s / 3600, m = s / 60
                parts.append(String(localized: "\(h) hr \(m % 60) min"))
            }
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}

/// The goal over the chat (docs/integrations/agent-goal.md): its first line and status in one row; a tap opens `GoalSheet`.
struct GoalBar: View {
    var goal: GoalInfo
    @State private var showing = false

    var body: some View {
        Button {
            Haptics.tap()
            showing = true
        } label: {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.goal)
                    .font(Theme.Font.labelHeavy)
                    .foregroundStyle(goal.tint)
                Text(goal.headline)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(.primary)
                    .lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Text(goal.statusLabel)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(goal.tint)
                    .lineLimit(1)
                    .fixedSize()
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            // A plain fill as ActivityChips': the folded header fades the open one out, and glass does not fade with it.
            .background(goal.tint.opacity(0.12), in: .capsule)
            .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Goal, \(goal.statusLabel): \(goal.headline). Tap to view.")
        .sheet(isPresented: $showing) { GoalSheet(goal: goal) }
    }
}

/// The goal opened: the whole objective, its status, the latest check's reason and what it has spent.
struct GoalSheet: View {
    @Environment(\.dismiss) private var dismiss
    var goal: GoalInfo

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.l) {
                    HStack(spacing: Theme.Space.s) {
                        ThemedIcon(Theme.Icon.goal).font(Theme.Font.captionHeavy)
                        Text(goal.statusLabel).font(Theme.Font.captionStrong).lineLimit(1)
                        if let usage = goal.usage {
                            Text(usage).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                        }
                    }
                    .foregroundStyle(goal.tint)
                    Text(goal.objective)
                        .font(Theme.Font.body)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if let reason = goal.reason, !reason.isEmpty {
                        VStack(alignment: .leading, spacing: Theme.Space.xs) {
                            Text(goal.isComplete ? String(localized: "Why It's Achieved") : String(localized: "Last Check")).font(Theme.Font.sectionHeader).foregroundStyle(.secondary)
                            Text(reason).font(Theme.Font.secondary).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                    }
                    // The same sheet opens for Claude Code's /goal and Codex's thread goal, and it does not know which agent it is.
                    Text("Set or clear a goal in the agent; in Claude Code, send /goal <condition> or /goal clear in the message field.")
                        .font(Theme.Font.caption)
                        .foregroundStyle(.tertiary)
                }
                .padding(Theme.Space.l)
            }
            .background(Theme.pageBackground)
            .navigationTitle("Goal")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() } } }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }
}
