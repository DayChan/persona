import XCTest

/// The password card (secret_run) in Persona's conversation: the commands verbatim, Run only once a password is typed
/// (and only Run, not Return), the answer carrying exactly that password, and iOS never offering to save it.
///   PORT=18937 MOCK_SECRET_RUN=pending python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_SECRET_RUN_HUB=http://127.0.0.1:18937 xcodebuild test … -only-testing:PersonaUITests/SecretRunUITests
final class SecretRunUITests: XCTestCase {
    /// The mock's main Persona conversation.
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"

    @MainActor func testRunSendsTheTypedPasswordOnce() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_SECRET_RUN_HUB"] else {
            throw XCTSkip("needs MOCK_SECRET_RUN=pending scripts/mock/mock_hub.py (PERSONA_SECRET_RUN_HUB)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant", "-openAssistant", main]
        app.launchPinned()

        let field = app.secureTextFields["interaction.field.password"]
        XCTAssertTrue(field.waitForExistence(timeout: 20), app.debugDescription)
        let commands = app.descendants(matching: .any)["secret-run.commands"]
        XCTAssertTrue(commands.exists, "the card does not list its commands")
        XCTAssertTrue(commands.label.contains("launchctl bootstrap system /Library/LaunchDaemons/com.example.backup.plist"), commands.label)
        let run = app.buttons["secret-run.run"]
        XCTAssertFalse(run.isEnabled, "Run before a password is typed")

        field.tap()
        field.typeText("example-password\n") // Return puts the keyboard away; it does not run the commands
        Thread.sleep(forTimeInterval: 1)
        XCTAssertTrue(resolved(hub).isEmpty, "Return answered the card")
        XCTAssertTrue(run.isEnabled)
        run.tap()
        var answers: [[String: Any]] = []
        let deadline = Date().addingTimeInterval(5)
        while answers.isEmpty, Date() < deadline {
            answers = resolved(hub)
            if answers.isEmpty { Thread.sleep(forTimeInterval: 0.1) }
        }
        guard let row = answers.first else { return XCTFail("mock did not receive the card's answer") }
        let resolution = row["resolution"] as? [String: Any]
        XCTAssertEqual(resolution?["allow"] as? Bool, true)
        XCTAssertEqual(resolution?["values"] as? [String: String], ["password": "example-password"])
        XCTAssertTrue(field.waitForNonExistence(timeout: 5), "the card stayed after Run")
        XCTAssertEqual(resolved(hub).count, 1, "the card was answered more than once")
        PasswordPrompt.assertNone(app, "the password card's field")
    }

    /// The mock's answers to the password card so far (GET /mock/resolved).
    private func resolved(_ hub: String) -> [[String: Any]] {
        guard let data = try? Data(contentsOf: URL(string: hub + "/mock/resolved")!),
              let response = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let rows = response["resolved"] as? [[String: Any]] else { return [] }
        return rows.filter { $0["id"] as? String == "ix_secret_run" }
    }
}
