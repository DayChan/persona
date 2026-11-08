import SwiftUI

/// 「给这台电脑改名」 (Rename This Computer): what the hub calls this Mac, asked of the running helper
/// (`persona-link name`, docs/operations/link.md "The computer's name"). The hub writes it, and the menu, the apps and the
/// web follow; the id stays.
struct RenameView: View {
    let model: LinkModel
    @State private var name: String
    @State private var result: (ok: Bool, message: String)?
    @State private var busy = false
    @Environment(\.dismissWindow) private var dismissWindow

    init(model: LinkModel, name: String? = nil, result: (ok: Bool, message: String)? = nil) {
        self.model = model
        let s = model.status
        // The id is no name to edit: an empty field means "the id again".
        let current = s?.machineName == s?.machine ? "" : (s?.machineName ?? "")
        _name = State(initialValue: name ?? current)
        _result = State(initialValue: result)
    }

    private var unchanged: Bool {
        let n = name.trimmingCharacters(in: .whitespacesAndNewlines)
        return n == (model.status?.machineName == model.status?.machine ? "" : (model.status?.machineName ?? ""))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("在 hub 上的名字").font(.headline)
            TextField(model.status?.machine ?? "", text: $name)
                .textFieldStyle(.roundedBorder)
                .onSubmit { save() }
                .disabled(busy)
            Text("手机 App、网页和管理界面里显示这个名字；留空就用 id（\(model.status?.machine ?? "")），id 不变。")
                .font(.callout).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let result {
                Label(result.message, systemImage: result.ok ? "checkmark.circle" : "exclamationmark.triangle")
                    .font(.callout)
                    .foregroundStyle(result.ok ? Color.green : Color.orange)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
            }
            HStack {
                Spacer()
                Button("取消") { dismissWindow(id: "rename") }
                    .keyboardShortcut(.cancelAction)
                Button {
                    save()
                } label: {
                    Text(busy ? "正在保存…" : "保存").lineLimit(1)
                }
                .keyboardShortcut(.defaultAction)
                .disabled(busy || unchanged || model.status?.renamable != true)
            }
        }
        .padding(20)
        .frame(width: 380)
    }

    private func save() {
        guard !busy, !unchanged else { return }
        busy = true
        Task {
            let r = await model.rename(name)
            result = r
            busy = false
        }
    }
}
