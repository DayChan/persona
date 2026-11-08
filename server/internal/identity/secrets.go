package identity

// The data directory's secrets: one list for the two things that keep them from what is asked for, the node's path
// guard (node/pathguard.go: files a client names by path) and the agent sandbox (extension/sandbox/agent.go: files
// an agent's own processes open). Both match them where they are written, at the top of the data directory (the path
// guard also at the top of a manager's folder, managers/<id>/, which the sandbox closes whole, and of a manager's
// folder anywhere, one holding a manager's manager.json); docs/server/agent-sandbox.md "Which files" says where the two
// differ.

// DataSecrets are the credential files in the data directory by the start of their name, so a backup beside one is
// matched too: hub.json (the token, the database password), Persona Link's credential, the tunnel identities (a
// private key), the daemons' tokens, a manager's bus token, the APNs key.
var DataSecrets = []string{"hub.json", "link.json", "tailcat.json", "tailcat-hub.json", "daemons.json", "manager.json", "apns",
	"relay.json", "relay.secret", "devices.json", "mail_accounts.json"}

// DataKeyPattern matches the end of a key file's name, backups included (secret.key, secret.key.bak, handles.key~,
// the APNs .p8): a regular expression both Go's regexp and sandbox-exec's read the same way.
const DataKeyPattern = `\.(key|p8|pem|crt)([.~-][^/]*)?$`

// DataSecretDirs are folders in the data directory that are secret whole, by their path in it: the launchers' merged
// settings hold their tokens, tls/ the hub's TLS key (hub/public.go), link-tls/ Persona Link's (link/tlsid.go),
// extensions/data/ the extensions' own data, their sign-ins and tokens among it, and extensions/staging/ what an
// uninstall or an upgrade sets aside of it (extension/registry_install.go, plan.go); plugins/, whole, is the
// extensions' folder of a hub from before #1844 (or the link to extensions/ it left, extmgr/extension_compat.go) and a
// node's from before #1921; node-plugins/, whole, the programs extensions run on this computer and their data
// (node/plugins.go). Only Persona's own processes use these.
var DataSecretDirs = []string{"launcher-settings", "tls", "link-tls", "extensions/data", "extensions/staging",
	"plugins", "node-plugins"}
