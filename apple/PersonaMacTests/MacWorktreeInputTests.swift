import AppKit
import Observation
import SwiftUI
import Testing
@testable import Persona

@MainActor struct MacWorktreeInputTests {
    @MainActor @Observable final class Draft {
        var form = WorktreeForm.State()
    }

    struct Fixture: View {
        @Bindable var draft: Draft
        var body: some View {
            Form {
                VStack(alignment: .leading) {
                    Text("Branch")
                    WorktreeInput(title: "Branch placeholder", text: Binding(get: { draft.form.branch }, set: {
                        draft.form = WorktreeForm.typeBranch(draft.form, base: "/worktrees/app", $0)
                    }))
                    Text("Checkout Path")
                    WorktreeInput(title: "Path placeholder", text: Binding(get: { draft.form.path }, set: {
                        draft.form = WorktreeForm.typePath(draft.form, base: "/worktrees/app", $0)
                    }), multiline: true)
                }.frame(maxWidth: .infinity, alignment: .leading)
            }.formStyle(.grouped).frame(width: 640, height: 360)
        }
    }

    @Test func bothNativeEditorsHaveRoomAndSaveTypedValues() throws {
        for appearance in [NSAppearance.Name.aqua, .darkAqua] {
            let draft = Draft()
            let host = NSHostingView(rootView: Fixture(draft: draft))
            let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 640, height: 360),
                                  styleMask: [.titled, .resizable], backing: .buffered, defer: false)
            window.isReleasedWhenClosed = false
            window.appearance = NSAppearance(named: appearance)
            window.contentView = host
            window.makeKeyAndOrderFront(nil)
            defer { window.close() }
            host.layoutSubtreeIfNeeded()
            RunLoop.main.run(until: Date().addingTimeInterval(0.1))
            func fields(_ view: NSView) -> [NSTextField] {
                (view as? NSTextField).map { [$0] } ?? view.subviews.flatMap(fields)
            }
            let editors = fields(host).filter { $0.isEditable && $0.isEnabled }
                .sorted { $0.convert(.zero, to: nil).y > $1.convert(.zero, to: nil).y }
            #expect(editors.count == 2)
            let branch = try #require(editors.first)
            let path = try #require(editors.last)
            for field in [branch, path] {
                #expect(field.isEditable && field.isEnabled)
                #expect(field.bounds.width >= 300 && field.bounds.height >= 16, "A Form must not leave only a label and a zero-width editor")
            }
            func edit(_ field: NSTextField, _ value: String) throws {
                field.selectText(nil)
                let editor = try #require(field.currentEditor() as? NSTextView)
                editor.insertText(value, replacementRange: NSRange(location: 0, length: editor.string.utf16.count))
                window.makeFirstResponder(nil)
                RunLoop.main.run(until: Date().addingTimeInterval(0.05))
            }
            try edit(branch, "feature/typed")
            #expect(draft.form.branch == "feature/typed" && draft.form.path == "/worktrees/app/feature-typed")
            try edit(path, "/custom/checkout")
            #expect(draft.form.path == "/custom/checkout" && draft.form.pathEdited)
        }
    }
}
