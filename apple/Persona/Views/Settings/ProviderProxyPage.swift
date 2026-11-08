import SwiftUI

/// A provider's proxy, laid out like iOS Settings' Configure Proxy (#6238): Follow This Computer, No Proxy or Manual
/// (its server), and the addresses that go direct.
struct ProviderProxyPage: View {
    @Bindable var wizard: ProviderWizardModel

    var body: some View {
        Form {
            Section {
                ForEach(ProviderWizardModel.ProxyMode.allCases) { mode in
                    Button { wizard.proxyMode = mode } label: {
                        HStack {
                            Text(mode.title).foregroundStyle(Color.primary).lineLimit(1)
                            Spacer()
                            if wizard.proxyMode == mode {
                                ThemedIcon(Theme.Icon.done).font(Theme.Font.label).foregroundStyle(Theme.accent)
                            }
                        }
                        .contentShape(.rect)
                    }
                    .accessibilityIdentifier("wizard.proxy.\(mode.rawValue)")
                    .accessibilityAddTraits(wizard.proxyMode == mode ? .isSelected : [])
                }
            } footer: {
                switch wizard.proxyMode {
                case .follow: Text("No proxy of its own: it goes the way this computer does. A proxy there that no longer answers is skipped when the gateway answers directly.")
                case .direct: Text("Requests to the gateway go direct, whatever proxy this computer has. Everything else, like web pages and the commands it runs, goes the way this computer does.")
                case .manual: Text("The gateway, web pages and the commands the agent runs all go through this proxy.")
                }
            }
            if wizard.proxyMode == .manual {
                Section {
                    LabeledContent(String(localized: "Server")) {
                        TextField(String(localized: "Server"), text: $wizard.proxy, prompt: Text(verbatim: "http://127.0.0.1:7890"))
                            .multilineTextAlignment(.trailing)
                            .font(Theme.Font.body.monospaced())
                            .keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                            .accessibilityIdentifier("wizard.proxy.server")
                    }
                } footer: {
                    if let problem = wizard.proxyProblem { Text(problem).foregroundStyle(Theme.danger) }
                }
            }
            if wizard.proxyMode != .direct {
                Section {
                    TextField(String(localized: "Addresses That Go Direct"), text: $wizard.noProxy,
                              prompt: Text(verbatim: "localhost, 127.0.0.1, .example.com"))
                        .font(Theme.Font.body.monospaced())
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        .accessibilityIdentifier("wizard.proxy.bypass")
                } header: {
                    Text("Addresses That Go Direct")
                } footer: {
                    // The computer's list goes with the computer's proxy (node mergesNoProxy).
                    if wizard.proxyMode == .follow {
                        Text("Hosts or domains, separated by commas; .example.com covers its subdomains. They go direct along with the ones this computer already lists.")
                    } else {
                        Text("Hosts or domains, separated by commas; .example.com covers its subdomains. Only these skip this proxy: the list on this computer is for its own proxy.")
                    }
                }
            }
        }
        .navigationTitle(String(localized: "Proxy"))
        .navigationBarTitleDisplayMode(.inline)
    }
}
