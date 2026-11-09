import Foundation
import Testing
@testable import Persona

/// Settings › Connectors' rows from the hub's list (`ConnectorRow.rows`, migration M6): mail and Feishu keep their rows and
/// their place whether the hub lists them or not.
@MainActor struct ConnectorRowTests {
    private func c(_ id: String) -> HubConnector { HubConnector(id: id, name: id, status: "ok") }

    @Test func aHubThatListsThemAll() {
        let rows = ConnectorRow.rows([c("lark"), c("mail"), c("github"), c("imessage")])
        #expect(rows.map(\.id) == ["mail", "lark", "github", "imessage"])
        #expect(rows.first == .mail && rows[1] == .lark, "drawn as their own rows, not as a hub connector")
    }

    @Test func aHubFromBeforeOrNoListYet() {
        #expect(ConnectorRow.rows([]).map(\.id) == ["mail", "lark"])
        #expect(ConnectorRow.rows([c("github")]).map(\.id) == ["mail", "lark", "github"])
    }

    @Test func aHubWithoutMail() {
        #expect(ConnectorRow.rows([c("lark"), c("github")]).map(\.id) == ["lark", "github"])
    }

    /// Feishu's page reads `/api/v1/lark/…`: without the Lark extension (no `lark` in the features) it has no row,
    /// whatever the list says (#1530).
    @Test func noFeishuRowWithoutTheExtension() {
        #expect(ConnectorRow.rows([], lark: false).map(\.id) == ["mail"])
        #expect(ConnectorRow.rows([c("lark"), c("mail"), c("github")], lark: false).map(\.id) == ["mail", "github"])
        #expect(ConnectorRow.rows([c("lark"), c("github")], lark: false).map(\.id) == ["github"])
    }
}

/// An extension's page draws a section per contributed connector that has an inbox switch (#3228).
@MainActor struct ExtensionConnectorsTests {
    private func c(_ id: String, inbox: Bool?) -> HubConnector {
        var c = HubConnector(id: id, name: id, status: "ok")
        c.inbox = inbox
        return c
    }

    @Test func onlyContributedOnesWithTheSwitchInTheExtensionsOrder() {
        let all = [c("telegram", inbox: true), c("github", inbox: false), c("imessage", inbox: true)]
        #expect(ExtensionConnectorsModel.shown(all, ids: ["github", "telegram"]).map(\.id) == ["github", "telegram"])
        #expect(ExtensionConnectorsModel.shown(all, ids: ["examplenotes"]).isEmpty, "one the hub does not list")
    }

    @Test func aHubFromBeforeTheSwitchShowsNothing() {
        #expect(ExtensionConnectorsModel.shown([c("github", inbox: nil)], ids: ["github"]).isEmpty)
    }

    @Test func messagesKeepsItsOwnSettingsEntry() {
        #expect(ExtensionConnectorsModel.shown([c("imessage", inbox: nil)], ids: ["imessage"]).map(\.id) == ["imessage"])
    }
}
