import XCTest

/// The Persona side: the long main conversation, its sidebar and sheets, and the pages reached from it (#5730).
/// Mock (`scripts/app/perf-test.sh`, suite `assistant`): MOCK_LONG_ASSISTANT=1 MOCK_AGENTS=1 MOCK_TASKS=1
/// MOCK_TASKS_NO_ASKS=1 MOCK_TASKS_DOCK=many MOCK_ASSISTANT_SIDEBAR=1 MOCK_KB_MANY_LINKS=1.
final class PerfAssistantTests: PerfTestCase {
    static let main = "server/b2e4d6f8-0000-4000-8000-000000000001"

    @MainActor private func openMain(_ extra: [String] = []) {
        launch(["-dev.openChat", "", "-app.tab", "assistant", "-tab", "assistant", "-openAssistant", Self.main, "-holdChromeOpen", "YES"]
               + extra)
        see(app.buttons["assistant-sidebar-button"], "the main conversation", timeout: 30)
        sleep(2)
    }

    /// A page opened from the drawer: `-openShellPage`.
    @MainActor private func openPage(_ page: String, _ extra: [String] = []) {
        launch(["-dev.openChat", "", "-app.tab", "assistant", "-tab", "assistant"]
               + ["-openShellPage", page] + extra)
        sleep(2)
    }

    // MARK: The main conversation

    /// 60 exchanges, each with tools and a long Markdown reply (headings, lists, a table, code).
    @MainActor func testMainConversationScroll() throws {
        openMain(["-loadAll", "YES"])
        measureScroll(mainScroller(), first: .down)
    }

    /// A long reply streaming in (a delta every 80 ms) while the conversation sits at the newest end.
    @MainActor func testMainConversationStreaming() throws {
        openMain()
        let reply = String(repeating: "这是一段流式输出的回答，包含 **加粗**、`代码` 和列表：\n\n- 第一项\n- 第二项\n\n", count: 6)
        let q = reply.addingPercentEncoding(withAllowedCharacters: .alphanumerics)!
        measureIdle(push: "say?stream=1&steps=40&ms=80&text=" + q, seconds: 5)
    }

    /// The main conversation at rest while agents' sessions churn (pushes for other pages).
    @MainActor func testMainConversationIdleUnderPushes() throws {
        openMain()
        measureIdle(push: "churn?n=12&ms=300")
    }

    /// Typing into the composer over the long conversation (the shell's ShellComposerBar is only typed into).
    @MainActor func testMainConversationTyping() throws {
        openMain()
        let shellField = any("shell-composer-field")
        let field = shellField.waitForExistence(timeout: 3) ? shellField
            : (app.textViews.firstMatch.exists ? app.textViews.firstMatch : app.textFields.firstMatch)
        measureTyping(field)
    }

    // MARK: Sidebar

    /// The sidebar opened from its button and closed again.
    @MainActor func testSidebarOpenClose() throws {
        openMain()
        let open = app.buttons["assistant-sidebar-button"]
        measureTransition(navigation: false) {
            open.tap()
            wait(any("assistant-sidebar"))
            sleep(1)
            let close = app.buttons["assistant-drawer-close"]
            if close.exists && close.isHittable { close.tap() } else { app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap() }
            sleep(1)
        }
    }

    /// The sidebar's list (36 recent conversations) scrolled.
    @MainActor func testSidebarScroll() throws {
        openMain(["-openAssistantSidebar", "YES"])
        let sidebar = see(any("assistant-sidebar"), "the sidebar", timeout: 15)
        let scroller = sidebar.scrollViews.firstMatch.exists ? sidebar.scrollViews.firstMatch
            : (sidebar.collectionViews.firstMatch.exists ? sidebar.collectionViews.firstMatch : sidebar)
        measureScroll(scroller, flicks: 2)
    }

    /// From the sidebar to another conversation and back to the long main one (the chat rebuilt each time).
    @MainActor func testSwitchConversationFromSidebar() throws {
        openMain()
        let open = app.buttons["assistant-sidebar-button"]
        measureTransition(navigation: false) {
            open.tap()
            let other = app.buttons.matching(identifier: "sidebar-conversation").element(boundBy: 3)
            if wait(other, timeout: 5) { other.tap() }
            sleep(2)
            open.tap()
            let main = app.buttons["sidebar-main"]
            if wait(main, timeout: 5) { main.tap() }
            sleep(2)
        }
    }

    /// The archived conversations sheet (sidebar › Archived) presented and closed.
    @MainActor func testArchivedSheet() throws {
        openMain(["-openAssistantSidebar", "YES"])
        let archived = app.buttons["sidebar-archived"]
        guard archived.waitForExistence(timeout: 8) else {
            throw XCTSkip("no archived conversations in the mock: the sidebar has no 已归档 entry")
        }
        measureTransition(navigation: false) {
            archived.tap()
            let done = app.buttons["完成"].firstMatch
            if wait(done) { sleep(1); done.tap() }
            wait(done, shown: false)
            sleep(1)
        }
    }

    // MARK: Switches and sheets

    /// The Shell switch between Assistant and Agents, and back.
    @MainActor func testTabSwitchAssistantAgents() throws {
        openMain()
        let agents = app.buttons["shell-agents"].firstMatch
        let persona = app.buttons["shell-assistant"].firstMatch
        see(agents, "the Agents switch")
        measureTransition(navigation: false) {
            agents.tap()
            wait(app.buttons["agents.filter.all"])
            sleep(1)
            persona.tap()
            wait(app.buttons["assistant-sidebar-button"])
            sleep(1)
        }
    }

    /// The task board sheet from the action bar's Progress tray (#2879), presented and closed.
    @MainActor func testTaskBoardSheet() throws {
        openMain()
        see(app.buttons["action-bar.progress"], "the action bar's Progress", timeout: 15).tap()
        let board = see(app.buttons["action-tray.board"], "the Progress tray's All", timeout: 5)
        measureTransition(navigation: false) {
            board.tap()
            let close = app.buttons["关闭"].firstMatch
            if wait(close) { sleep(1); close.tap() }
            wait(close, shown: false)
            sleep(1)
        }
    }

    /// The welcome page of a new conversation at rest while agents and the inbox push.
    @MainActor func testWelcomePageIdle() throws {
        launch(["-dev.openChat", "", "-app.tab", "assistant", "-tab", "assistant", "-newAssistant", "YES"])
        see(any("assistant-welcome"), "the welcome page", timeout: 20)
        measureIdle(push: "churn?n=12&ms=300&inbox=1")
    }

    // MARK: Pages from the conversation

    /// The profile page (avatar, scheduled tasks, run history) scrolled.
    @MainActor func testProfileScroll() throws {
        openPage("profile")
        see(app.buttons["定时任务"].firstMatch, "the profile", timeout: 20)
        measureScroll(mainScroller(), flicks: 2)
    }

    /// Profile → Scheduled tasks → back.
    @MainActor func testProfileToRoutinesPushPop() throws {
        openPage("profile")
        let routines = see(app.buttons["定时任务"].firstMatch, "the profile's 定时任务", timeout: 20)
        measureTransition {
            routines.tap()
            sleep(2)
            back()
            wait(routines)
            sleep(1)
        }
    }

    /// The knowledge base: switching the kind chips (each a list loaded from the hub).
    @MainActor func testKnowledgeKinds() throws {
        openPage("knowledge")
        see(app.buttons["kb.kind.people"], "the kind chips", timeout: 20)
        measureTransition(navigation: false) {
            for k in ["people", "chats", "projects"] {
                app.buttons["kb.kind." + k].tap()
                usleep(800_000)
            }
        }
    }

    /// A project's entry page (profile Markdown, people, moments) scrolled.
    @MainActor func testKnowledgeEntryScroll() throws {
        openPage("knowledge", ["-showKnowledgeEntry", "projects/weather-app"])
        sleep(3)
        measureScroll(mainScroller(), flicks: 2)
    }

    /// The to-do list scrolled.
    @MainActor func testTodosScroll() throws {
        openPage("todos")
        see(app.navigationBars.firstMatch, "the to-dos", timeout: 20)
        measureScroll(mainScroller(), flicks: 2)
    }

    /// Typing into the to-do search field (the list filtered and sorted per keystroke).
    @MainActor func testTodosSearchTyping() throws {
        openPage("todos")
        let search = app.searchFields.firstMatch
        if !search.waitForExistence(timeout: 5) { mainScroller().swipeDown() }
        measureTyping(see(search, "搜索待办"), text: "天气小组件单元测试")
    }
}
