import SwiftUI

// How a supervised task stands (#3124, docs/assistant/unified-entry.md ("Supervision")) and what its agent reported (#5112): a chip and
// the count of criteria met on its card and its rows, the checklist on its page, a line in the conversation for each
// review and each report. The words and the logic are in Shared/Supervision.swift; web: views/assistant/Supervision.tsx.

/// The review's chip, with the criteria met ("Rework round 1 · 1/2"); nothing for a task that is not supervised.
/// `compact`: a step quieter, without the mark, for a list row's status line (the to-do lists).
struct ReviewChip: View {
    var todo: TodoItem
    var compact = false

    var body: some View {
        if let badge = todo.reviewBadge {
            Chip(text: badge.text, symbol: compact ? nil : Theme.Icon.supervised, look: .review(badge.tone), compact: compact)
        }
    }
}

/// The task's criteria on its page, each as its latest review found it, and what the supervisor said last.
struct ReviewChecklist: View {
    var todo: TodoItem

    var body: some View {
        if let sup = todo.supervision {
            let list = sup.checklist
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                HStack(spacing: Theme.Space.s) {
                    Text("Done when")
                        .font(Theme.Font.captionStrong)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                    ReviewChip(todo: todo)
                    Spacer(minLength: Theme.Space.s)
                    if let m = sup.expectMinutes, m > 0 {
                        Text("About \(m) min")
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .fixedSize()
                    }
                }
                if list.isEmpty {
                    Text("No criteria were given")
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                } else {
                    VStack(alignment: .leading, spacing: Theme.Space.sm) {
                        ForEach(list, id: \.criterion) { CriterionRow(item: $0) }
                    }
                }
                if let note = sup.latestReview?.note?.trimmingCharacters(in: .whitespacesAndNewlines), !note.isEmpty {
                    Text("Supervisor: \(note)")
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .textSelection(.enabled)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityIdentifier("review-checklist")
        }
    }
}

/// One criterion: met (a check), missing (a cross) or not reviewed yet (a clock), with its evidence or gap below.
private struct CriterionRow: View {
    var item: ReviewCheckItem

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(symbol)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(color)
                .scaledFrame(width: Theme.Size.markM)
                .accessibilityLabel(markLabel)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(item.criterion)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.primary)
                    .fixedSize(horizontal: false, vertical: true)
                if let e = item.evidence {
                    Text(e)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .textSelection(.enabled)
                }
            }
        }
        .accessibilityElement(children: .combine)
    }

    private var symbol: String {
        switch item.mark {
        case .met: Theme.Icon.done
        case .missing: Theme.Icon.unmet
        case .open: Theme.Icon.time
        }
    }

    private var color: Color {
        switch item.mark {
        case .met: Theme.positive
        case .missing: Theme.danger
        case .open: .secondary
        }
    }

    private var markLabel: String {
        switch item.mark {
        case .met: String(localized: "Met")
        case .missing: String(localized: "Missing")
        case .open: String(localized: "Not reviewed yet")
        }
    }
}

/// What the task's agent last told Persona, under the card's current step (only while the task is open).
struct TaskProgressLine: View {
    var line: String

    var body: some View {
        Label {
            Text(line).lineLimit(1)
        } icon: {
            ThemedIcon(Theme.Icon.reported).font(Theme.Font.labelStrong)
        }
        .labelStyle(.titleAndIcon)
        .font(Theme.Font.caption)
        .foregroundStyle(.secondary)
        .accessibilityIdentifier("task-progress-line")
    }
}

/// A review in the conversation (feed kind `review`): a capsule like the answered line, with the task (a tap opens it),
/// the verdict and the supervisor's note.
struct ReviewFeedLine: View {
    var item: FeedItem
    var taskTitle: String
    private var store: TasksStore { .shared }

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.supervised).font(Theme.Font.labelStrong)
            HStack(spacing: Theme.Space.xs) {
                if let id = item.ref, store.todos[id] != nil {
                    Button { store.detailTask = id } label: { Text(taskTitle).lineLimit(1) }
                        .buttonStyle(.plain)
                        .layoutPriority(1)
                } else {
                    Text(taskTitle).lineLimit(1).layoutPriority(1)
                }
                Text("· \(ReviewText.verdict(item.verdict ?? "", round: item.round))")
                    .font(Theme.Font.captionStrong)
                    .lineLimit(1)
                    .fixedSize()
                if let note = item.note?.trimmingCharacters(in: .whitespacesAndNewlines), !note.isEmpty {
                    Text(note).lineLimit(1)
                }
            }
        }
        .font(Theme.Font.caption)
        .foregroundStyle(.secondary)
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.sm)
        .background(Theme.Fill.chip, in: .capsule)
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("review-line")
        .accessibilityAction(named: "Open Task") { if let id = item.ref, store.todos[id] != nil { store.detailTask = id } }
    }
}

/// A supervised task's agent reported to Persona (feed kind `report`: a question, a block, a finish): who and what it said
/// in a bubble (newlines kept). Evidence remains in the feed data, not appended to the agent's words.
struct ReportFeedLine: View {
    var item: FeedItem
    var taskTitle: String
    private var store: TasksStore { .shared }

    var body: some View {
        let kind = item.reportKind ?? ""
        let attention = ReviewText.reportWantsAttention(kind)
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Button {
                if let id = item.ref, store.todos[id] != nil { store.detailTask = id }
            } label: {
                Label("“\(taskTitle)” reported to Persona", systemImage: Theme.Icon.reported)
                    .font(Theme.Font.captionMedium)
                    .foregroundStyle(attention ? Theme.cautionText : .secondary)
                    .lineLimit(1)
            }
            .buttonStyle(.plain)
            .disabled(item.ref.flatMap { store.todos[$0] } == nil)
            Text(ExchangeText.linked(ReviewText.reportLine(kind: kind, text: ExchangeText.withoutHeadingMarks(item.text ?? ""), step: item.step, steps: item.steps)))
                .font(Theme.Font.body)
                .foregroundStyle(.primary)
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
                .padding(.horizontal, Theme.Space.m)
                .padding(.vertical, Theme.Space.s)
                .background(Theme.Fill.chip, in: .rect(cornerRadius: Theme.Radius.bubble, style: .continuous))
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.trailing, Theme.Space.xxl)
        .accessibilityIdentifier("report-line")
    }
}
