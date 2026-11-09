import Foundation

/// The same component renderer serves extension pages and assistant cards; only their control transport differs.
@MainActor
protocol ExtensionViewControlling: AnyObject {
    var values: [String: String] { get set }
    var query: String { get set }
    var running: String? { get }
    var loadingMore: String? { get }
    var fieldsEnabled: Bool { get }
    var managesConfirmation: Bool { get }
    func isEnabled(_ action: ExtensionAction) -> Bool
    func selected(_ action: String) -> Bool
    func prepared(_ action: ExtensionAction, with chosen: [String: String], displayed: ExtensionDisplayedRevision?) -> @MainActor () async -> ExtensionViewModel.Outcome
    func perform(_ action: ExtensionAction, with chosen: [String: String]) async -> ExtensionViewModel.Outcome
    func choose(_ name: String, _ value: String, action: ExtensionAction) async -> ExtensionViewModel.Outcome
    func filter(_ value: String) async -> ExtensionViewModel.Outcome
    func search() async -> ExtensionViewModel.Outcome
    func more(_ more: ExtensionMore) async -> ExtensionViewModel.Outcome

    // UI version 6's controls in rows and tiles (docs/extensions/extensions.md "UI version 6"): an extension page's
    // model sends them; a card that cannot (an assistant card) draws them as they are, not to be used.
    /// It sends controls; false: they are drawn disabled.
    var drawsControls: Bool { get }
    /// What a control shows while the user's value is not yet the extension's, by its row's or tile's key.
    var controlValues: [String: String] { get }
    /// Controls whose request has been out for over a second.
    var slowControls: Set<String> { get }
    func preview(_ key: String, _ value: String?)
    func change(_ key: String, _ control: ExtensionRowControl, to value: String, after delay: Duration) async -> ExtensionViewModel.Outcome?
}

extension ExtensionViewControlling {
    var fieldsEnabled: Bool { true }
    var managesConfirmation: Bool { false }
    func isEnabled(_ action: ExtensionAction) -> Bool { running == nil }
    func selected(_ action: String) -> Bool { false }
    func prepared(_ action: ExtensionAction, with chosen: [String: String] = [:], displayed: ExtensionDisplayedRevision? = nil) -> @MainActor () async -> ExtensionViewModel.Outcome {
        { await self.perform(action, with: chosen) }
    }
    func perform(_ action: ExtensionAction) async -> ExtensionViewModel.Outcome { await perform(action, with: [:]) }
    var drawsControls: Bool { false }
    var controlValues: [String: String] { [:] }
    var slowControls: Set<String> { [] }
    func preview(_ key: String, _ value: String?) {}
    func change(_ key: String, _ control: ExtensionRowControl, to value: String,
                after delay: Duration) async -> ExtensionViewModel.Outcome? { nil }
}

extension ExtensionViewModel: ExtensionViewControlling {}
