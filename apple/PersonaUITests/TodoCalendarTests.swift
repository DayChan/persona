import XCTest

/// The to-dos' calendar (#2029) against the mock hub, whose to-dos are due today at 10:00, 16:00 and two at 18:00, on a
/// day in two days and yesterday, and one has no due date: the day, the week and the month show them; a block opens
/// its to-do; a day of the week opens that day; "+" makes a to-do due on the day shown; the bar under the calendar
/// goes back to the list. TEST_RUNNER_PERSONA_SHOTS=<dir> also saves the screens (TEST_RUNNER_PERSONA_SHOTS_APPEARANCE
/// names them light / dark).
final class TodoCalendarTests: XCTestCase {
    private var app: XCUIApplication!

    @MainActor func testCalendarShowsOpensAndAdds() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        continueAfterFailure = false
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // The view and the mode are switched here, not by launch arguments: a value in the argument domain could not be
        // changed by the taps below.
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openAssistantPage", "todos"]
        // An iPad on its side: the calendar beside the sidebar is wide enough for the full week (Theme.CalendarGrid.wide).
        let pad = UIDevice.current.userInterfaceIdiom == .pad
        if pad { XCUIDevice.shared.orientation = .landscapeLeft }
        defer { if pad { XCUIDevice.shared.orientation = .portrait } }
        app.launchPinned()
        let calendarSwitch = app.buttons["日历"]
        XCTAssertTrue(calendarSwitch.waitForExistence(timeout: 60))
        calendarSwitch.tap()
        app.buttons["日"].tap()

        // The day: today's to-dos on the axis, the two at 18:00 side by side.
        let weather = entry("天气 App 发 1.3 版")
        XCTAssertTrue(weather.waitForExistence(timeout: 60))
        XCTAssertTrue(entry("博客迁移到 Astro").exists)
        XCTAssertTrue(app.descendants(matching: .any)["todo.calendar.strip"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["todo.calendar.undated"].exists)
        shot("calendar-day")

        // The week, then a day of it.
        app.buttons["周"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["todo.calendar.dayHeader"].firstMatch.waitForExistence(timeout: 10))
        shot("calendar-week")
        // The month.
        app.buttons["月"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["todo.calendar.monthDay"].firstMatch.waitForExistence(timeout: 10))
        shot("calendar-month")
        // Back to the week; its first day opens as a day.
        app.buttons["周"].tap()
        let header = app.descendants(matching: .any)["todo.calendar.dayHeader"].firstMatch
        XCTAssertTrue(header.waitForExistence(timeout: 10))
        header.tap()
        XCTAssertTrue(app.descendants(matching: .any)["todo.calendar.strip"].waitForExistence(timeout: 10))
        app.buttons["todo.calendar.today"].tap()

        // A block opens its to-do.
        XCTAssertTrue(weather.waitForExistence(timeout: 10))
        weather.tap()
        XCTAssertTrue(app.switches["todo.plan.due"].waitForExistence(timeout: 15))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(weather.waitForExistence(timeout: 10))

        // "+" makes a to-do due on the day shown, all day.
        app.buttons["新建待办"].tap()
        let title = app.textFields["todo.calendar.newTitle"]
        XCTAssertTrue(title.waitForExistence(timeout: 10))
        title.typeText("日历里记的事")
        shot("calendar-add")
        app.buttons["todo.calendar.newAdd"].tap()
        XCTAssertTrue(wait { try self.todos(hub).contains { $0["title"] as? String == "日历里记的事" && $0["due_date"] is String } })
        XCTAssertTrue(entry("日历里记的事").waitForExistence(timeout: 10))

        // The bar under the calendar goes back to the list.
        app.descendants(matching: .any)["todo.calendar.undated"].tap()
        XCTAssertTrue(app.textFields["搜索待办"].waitForExistence(timeout: 10))
    }

    /// An entry on the calendar, by its title (its label starts with it).
    @MainActor private func entry(_ title: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", title + ",")).firstMatch
    }

    private func todos(_ hub: String) throws -> [[String: Any]] {
        let data = try Data(contentsOf: URL(string: hub + "/api/v1/todos")!)
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        return try XCTUnwrap(body["todos"] as? [[String: Any]])
    }

    /// Whether check holds within ten seconds.
    private func wait(_ check: () throws -> Bool) -> Bool {
        let deadline = Date.now.addingTimeInterval(10)
        while Date.now < deadline {
            if (try? check()) == true { return true }
            usleep(200_000)
        }
        return false
    }

    @MainActor private func shot(_ name: String) {
        // The screen's, not the app's: on an iPad on its side the app's comes back as the portrait frame buffer cut to
        // the screen's height. The screen's may also come back upright-portrait; it is turned to how it is held.
        let s = XCUIScreen.main.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"].map { "-" + $0 } ?? ""
            let device = UIDevice.current.userInterfaceIdiom == .pad ? "-ipad" : ""
            let png = upright(s.image).pngData() ?? s.pngRepresentation
            try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + device + look + ".png"))
        }
    }

    /// The image as the device is held: a portrait frame buffer of a device turned left (its top on the right) is
    /// turned a quarter counterclockwise.
    @MainActor private func upright(_ image: UIImage) -> UIImage {
        guard XCUIDevice.shared.orientation == .landscapeLeft, let cg = image.cgImage, cg.width < cg.height else { return image }
        let w = CGFloat(cg.width), h = CGFloat(cg.height)
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        return UIGraphicsImageRenderer(size: CGSize(width: h, height: w), format: format).image { ctx in
            ctx.cgContext.translateBy(x: 0, y: w)
            ctx.cgContext.rotate(by: -.pi / 2)
            UIImage(cgImage: cg).draw(in: CGRect(x: 0, y: 0, width: w, height: h))
        }
    }
}
