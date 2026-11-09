import UIKit
import XCTest

/// A Claude whose turn ended while a multi-agent workflow runs in the background (#2935): the Agents list shows it as
/// working with the workflow's name, phase and progress; its chat's header has a card per workflow, and the Workflow call
/// that launched one is drawn as its run's card (#3436) with its phases, which follows the hub's pushes. Against
/// scripts/mock/mock_hub.py with `MOCK_WORKFLOW=1` (PERSONA_MOCK_HUB); with PERSONA_SHOTS set the
/// screenshots are written there too (PERSONA_SHOTS_APPEARANCE names the simulator's appearance in the file names).
final class WorkflowUITests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let a = XCTAttachment(screenshot: app.screenshot())
            a.lifetime = .keepAlways
            add(a)
            let t = XCTAttachment(string: app.debugDescription)
            t.lifetime = .keepAlways
            add(t)
        }
        super.tearDown()
    }

    @MainActor private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        let image = app.screenshot()
        let a = XCTAttachment(screenshot: image)
        a.name = "\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = env["PERSONA_SHOTS"] {
            let device = UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone"
            let file = "workflow-\(device)-\(name)-\(env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown").png"
            try? image.pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: file))
        }
    }

    private func labelled(_ text: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    /// Moves the mock's run on (`GET /mock/workflow?step=n`), pushed to the app as the hub would.
    private func step(_ hub: String, _ n: Int) throws {
        _ = try Data(contentsOf: URL(string: hub + "/mock/workflow?step=\(n)")!)
    }

    private func counts(_ text: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "identifier == 'workflow.counts' AND label == %@", text)).firstMatch
    }

    private func agentRow(_ label: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "identifier == 'workflow.agent' AND label CONTAINS %@", label)).firstMatch
    }

    private func phase(_ title: String) -> XCUIElement {
        app.buttons.matching(identifier: "workflow.phase").matching(NSPredicate(format: "label BEGINSWITH %@", title + "，")).firstMatch
    }

    /// Drags the chat until `target`'s top is just under `top` (the header): slowly and held at the end, so the
    /// upside-down list does not fling past it (docs/lessons/ios.md), checking where it is between drags.
    @MainActor private func bring(_ target: XCUIElement, under top: XCUIElement) {
        let window = app.windows.firstMatch.frame
        let origin = app.coordinate(withNormalizedOffset: .zero)
        for _ in 0..<6 {
            let want = top.frame.maxY + 12
            let off = want - target.frame.minY
            if abs(off) < 12 { break }
            // From near the top of the chat down (older content comes down), or from lower up; at most a third of the
            // screen at a time, within the chat (under the header, over the composer).
            let reach = window.height / 3
            let d = max(-reach, min(reach, off))
            let y = d > 0 ? top.frame.maxY + 24 : window.height * 0.72
            let from = origin.withOffset(CGVector(dx: window.midX, dy: y))
            from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: d)), withVelocity: .slow,
                       thenHoldForDuration: 0.4)
            sleep(1)
        }
        XCTAssertGreaterThanOrEqual(target.frame.minY, top.frame.maxY - 2, "\(target) is still under the header")
    }

    /// Where an element's top is now, once the chat has settled (the pin corrects over a few frames).
    @MainActor private func settledTop(_ e: XCUIElement) -> CGFloat {
        sleep(1)
        return e.frame.minY
    }

    @MainActor func testWorkflowInTheListTheChatAndItsAgents() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_WORKFLOW=1 (PERSONA_MOCK_HUB)")
        }
        try step(hub, 0)
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-dev.openChat", "", "-openAgentsFresh", "YES", "-dev.grouping", "state",
                                 "-holdChromeOpen", "YES"] // the header's cards are tapped: it must not fold by itself
        app.launchPinned()

        // The list: under Working, not Seen / Unread, with the workflow's line.
        let working = app.buttons["agents.filter.working"]
        XCTAssertTrue(working.waitForExistence(timeout: 30), "no Agents list")
        working.tap()
        let row = app.buttons.matching(identifier: "agents.row").matching(NSPredicate(format: "label CONTAINS %@", "检查 PR #42")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 15), "the review agent is not among the working ones")
        XCTAssertTrue(labelled("后台 workflow「parity-audit」").waitForExistence(timeout: 5), "no workflow line on its row")
        XCTAssertTrue(labelled("169/185+").exists, "the row's count has no + while the run goes on")
        sleep(1)
        shot("list")

        // Its chat: the header's card (the killed one beside it), and the run's card where its Workflow call was (#3436,
        // the call is in a turn the hub folded: the app fetches that fold).
        row.tap()
        let card = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "后台 workflow parity-audit，在跑")).firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 15), "no workflow card in the chat")
        XCTAssertTrue(card.label.contains("169/185"), card.label)
        XCTAssertTrue(labelled("Error: Workflow aborted").exists, "the killed one's error is missing")
        let call = app.buttons.matching(identifier: "workflow.call").matching(NSPredicate(format: "label CONTAINS %@", "parity-audit")).firstMatch
        XCTAssertTrue(call.waitForExistence(timeout: 15), "the Workflow call is not drawn as its run's card")
        XCTAssertTrue(counts("169/185+").waitForExistence(timeout: 5), "no done / started+ on the card")
        // Open while it runs: its phases, the one it is in, its running agents in the order they started.
        let verify = app.buttons.matching(identifier: "workflow.phase").matching(NSPredicate(format: "label BEGINSWITH %@", "Verify，当前阶段")).firstMatch
        XCTAssertTrue(verify.waitForExistence(timeout: 5), "the current phase is not marked")
        XCTAssertTrue(verify.label.contains("0/5+"), verify.label)
        let first = agentRow("verify:chat-rich")
        let last = agentRow("verify:island")
        XCTAssertTrue(first.waitForExistence(timeout: 5) && last.exists, "a running agent is missing")
        XCTAssertLessThan(first.frame.minY, last.frame.minY, "agents are listed in the order they started")
        // At most 12 rows a phase: the Audit block the hub's cap cut into says so and folds the rest.
        XCTAssertTrue(labelled("更早的 125 个没有列出").exists, "the agents the hub no longer lists are not said")
        XCTAssertTrue(app.buttons.matching(identifier: "workflow.phase.more").firstMatch.exists, "no 「还有 n 个」")
        sleep(1)
        shot("chat-card")

        // The header's card opens to the same phase blocks.
        let expand = app.buttons.matching(identifier: "workflow.agents").firstMatch
        XCTAssertTrue(expand.exists, "the header's card does not open")
        expand.tap()
        sleep(1)
        shot("header-phases")
        expand.tap()

        // The card's own head, the Audit block and its rows in view (under the header): the frames before and after a
        // count changes are taken here.
        let section = app.descendants(matching: .any)["workflow.section"]
        bring(call, under: section)
        let audit = phase("Audit")
        let settings = agentRow("audit:settings-170")
        XCTAssertTrue(audit.isHittable && settings.isHittable, "the Audit block's head and rows are not in view under the card's head")
        sleep(1)
        shot("chat-card-169")

        // A count changes (an Audit agent comes back): it rolls to 170 where it is, in the header too.
        try step(hub, 1)
        XCTAssertTrue(counts("170/185+").waitForExistence(timeout: 10), "the card did not follow the hub's push")
        XCTAssertFalse(counts("169/185+").exists, "a count was left behind")
        XCTAssertTrue(audit.label.contains("170/180+"), audit.label)
        sleep(1)
        shot("chat-card-170")

        // The reader folds a phase: its rows go, its head never goes up (the list is upside down, ChatPin). Near the
        // newest end the list cannot scroll past it, so the head may come down by what the list could not make up.
        let auditTop = settledTop(audit)
        audit.tap()
        XCTAssertGreaterThanOrEqual(settledTop(audit), auditTop - 4, "the phase's head went up when it folded")
        XCTAssertTrue(audit.isHittable && !settings.exists, "the Audit block did not fold")

        // A new agent starts in Verify (185 → 186): its row comes in at the end of its phase; Audit stays as the
        // reader left it.
        try step(hub, 2)
        XCTAssertTrue(agentRow("verify:copy").waitForExistence(timeout: 10), "the new agent is not listed")
        XCTAssertTrue(counts("170/186+").exists)
        XCTAssertTrue(audit.exists && !settings.exists, "a push opened the phase the reader folded")

        // Opened again, then 「还有 n 个」: the earlier agents open under its head, which stays where it is (the hub
        // lists the newest 60: with verify:copy, Audit's listed ones are #127–180).
        let openTop = settledTop(audit)
        audit.tap()
        XCTAssertEqual(settledTop(audit), openTop, accuracy: 4, "the phase's head moved when it opened")
        let more = app.buttons.matching(identifier: "workflow.phase.more").firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 5) && more.isHittable, "no 「还有 n 个」 in view")
        more.tap()
        XCTAssertTrue(agentRow("audit:settings-140").waitForExistence(timeout: 5), "「还有 n 个」 did not show the earlier agents")
        XCTAssertEqual(settledTop(audit), openTop, accuracy: 4, "the phase's head moved when 42 rows opened under it")
        XCTAssertTrue(audit.isHittable, "the phase's head left the screen")
        // Folded again by the reader, for the frames below (once the list has settled: 55 rows go).
        audit.tap()
        sleep(1)
        XCTAssertTrue(audit.isHittable && !settings.exists, "the Audit block did not fold again")

        // Audit all back with one failed: its block is marked (and stays folded); completed, the card folds to its head.
        try step(hub, 3)
        let auditDone = app.buttons.matching(identifier: "workflow.phase").matching(NSPredicate(format: "label BEGINSWITH %@", "Audit，完成，有失败")).firstMatch
        XCTAssertTrue(auditDone.waitForExistence(timeout: 10), "the Audit phase is not marked done with failures")
        XCTAssertTrue(labelled("1 个失败").exists)
        sleep(1)
        shot("chat-card-audit-done")
        try step(hub, 4)
        XCTAssertTrue(counts("185/186").waitForExistence(timeout: 10), "an ended run still says +")
        sleep(1)
        shot("chat-card-completed")
        try step(hub, 0)
    }

    /// Under iPhone Duo's side bars the header never folds (#2161): a running card is its head until tapped, so the header
    /// leaves the chat most of a short screen; a tap opens where it is now, its progress and its phases.
    @MainActor func testSideBarsHeaderKeepsRunningWorkflowsToTheirHeads() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_WORKFLOW=1 (PERSONA_MOCK_HUB)")
        }
        try step(hub, 0)
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-dev.openChat", "", "-openAgentsFresh", "YES", "-dev.grouping", "state",
                               "-sideBars", "YES"]
        app.launchPinned()

        let working = app.buttons["agents.filter.working"]
        XCTAssertTrue(working.waitForExistence(timeout: 30), "no Agents list")
        working.tap()
        let row = app.buttons.matching(identifier: "agents.row").matching(NSPredicate(format: "label CONTAINS %@", "检查 PR #42")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 15), "the review agent is not among the working ones")
        row.tap()
        let section = app.descendants(matching: .any)["workflow.section"]
        let card = section.buttons.matching(identifier: "workflow.agents").firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 15), "no running workflow in the header")
        XCTAssertTrue(card.label.hasPrefix("后台 workflow parity-audit，在跑"), card.label)
        XCTAssertTrue(section.staticTexts.matching(NSPredicate(format: "identifier == 'workflow.counts' AND label == %@", "169/185+")).firstMatch.exists,
                      "the head lost its done / started")
        XCTAssertFalse(section.descendants(matching: .any)["workflow.now"].exists, "the folded card says where it is now")
        XCTAssertFalse(section.descendants(matching: .any)["workflow.progress"].exists, "the folded card has its progress bar")
        let window = app.windows.firstMatch.frame
        XCTAssertLessThan(section.frame.height, window.height * 0.2, "the header's workflows still take a big share of the screen")
        sleep(1)
        shot("side-bars-header")

        card.tap()
        XCTAssertTrue(section.descendants(matching: .any)["workflow.now"].waitForExistence(timeout: 5), "the opened card does not say where it is now")
        XCTAssertTrue(section.descendants(matching: .any)["workflow.progress"].exists, "the opened card has no progress bar")
        XCTAssertTrue(section.buttons.matching(identifier: "workflow.phase").firstMatch.exists, "the opened card has no phases")
        sleep(1)
        shot("side-bars-header-open")
        card.tap()
        XCTAssertTrue(section.descendants(matching: .any)["workflow.progress"].waitForNonExistence(timeout: 3), "the card did not fold again")
    }

    /// Three workflows (#4532, `MOCK_WORKFLOW=crowded`): the running one in full, the ended ones a line each (the stopped
    /// one's error kept); the section stays in the top part of the screen, its phases and ten agents scroll inside it.
    @MainActor func testCrowdedWorkflowsLeaveRoomForTheChat() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB_CROWDED"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_WORKFLOW=crowded (PERSONA_MOCK_HUB_CROWDED)")
        }
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-dev.openChat", "", "-openAgentsFresh", "YES", "-dev.grouping", "state",
                                 "-holdChromeOpen", "YES"] // the header's cards are tapped: it must not fold by itself
        app.launchPinned()

        let working = app.buttons["agents.filter.working"]
        XCTAssertTrue(working.waitForExistence(timeout: 30), "no Agents list")
        working.tap()
        let row = app.buttons.matching(identifier: "agents.row").matching(NSPredicate(format: "label CONTAINS %@", "新手引导")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 15), "the onboarding agent is not among the working ones")
        row.tap()
        let card = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "后台 workflow onboarding-round4，在跑")).firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 15), "no workflow card in the chat")
        XCTAssertTrue(labelled("Error: Workflow aborted").exists, "the stopped one's error is missing")
        let window = app.windows.firstMatch.frame
        let section = app.descendants(matching: .any)["workflow.section"]
        XCTAssertTrue(section.exists, "no workflow section")
        XCTAssertLessThan(section.frame.maxY, window.minY + window.height * 0.5, "the folded section takes half the screen")
        sleep(1)
        shot("crowded-chat")

        app.buttons.matching(identifier: "workflow.agents").firstMatch.tap()
        XCTAssertTrue(labelled("verify:copy").waitForExistence(timeout: 5), "a running agent is missing")
        sleep(1)
        shot("crowded-agents")

        // Open, it takes at most its share of the page (`Theme.Size.workflowShare`) and the chat stays under it.
        XCTAssertLessThanOrEqual(section.frame.height, window.height * 0.4, "the open agents push the chat off the screen")
        XCTAssertLessThan(section.frame.maxY, window.minY + window.height * 0.6, "the open section reaches the conversation's half")

        // The agents scroll inside the section: the last phase's last agent (the latest started) comes into view.
        let last = app.buttons.matching(NSPredicate(format: "identifier == 'workflow.agent' AND label CONTAINS %@", "verify:copy")).firstMatch
        for _ in 0..<6 where !(last.exists && last.isHittable && section.frame.contains(last.frame)) {
            section.swipeUp(velocity: .slow)
        }
        XCTAssertTrue(last.isHittable, "the last agent cannot be scrolled to")
        sleep(1)
        shot("crowded-agents-scrolled")
    }
}
