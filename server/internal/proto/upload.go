package proto

// Files sent from the phone (POST /api/v1/machines/{mid}/files): the hub streams the body to the node in chunks.
const (
	MaxUploadBytes   = 200 << 20
	UploadChunkBytes = 1 << 20
	// A file pasted or dropped into a web terminal (dest=terminal) lands under the node's Persona directory and is
	// smaller: what an agent reads by path is a screenshot or a document, not a video.
	UploadDestTerminal     = "terminal"
	MaxTerminalUploadBytes = 50 << 20
	// A checkup report or lab result for the health space (dest=health, docs/assistant/health.md "Checkup reports"): it lands in the
	// health directory's reports/ and stays, where the health conversations read it and the others are denied.
	UploadDestHealth = "health"
	// A file put into a folder the app is browsing (dest=dir&dir=…, docs/protocol/protocol.md "Changing files"): it keeps its own
	// name, only folders in the home take it (node/writeguard.go), and it may be bigger.
	UploadDestDir     = "dir"
	MaxDirUploadBytes = 2 << 30
	// What dest=dir does when the name is taken: "" refuses with CodeExists (409, the app asks), replace overwrites
	// the file, keep puts it beside as "name 2.ext".
	UploadConflictReplace = "replace"
	UploadConflictKeep    = "keep"
	// CodeReadOnly: the path is outside what the phone may change (409); CodeExists: the name is taken (409, name).
	CodeReadOnly = "read_only"
	CodeExists   = "exists"
	// CodeTooLarge: over MaxUploadBytes (413); CodeNodeOutdated: the machine's node predates the feature (409).
	CodeTooLarge     = "too_large"
	CodeNodeOutdated = "node_outdated"
)

// UploadedFile is where an upload landed on the machine.
type UploadedFile struct {
	Path string `json:"path"` // absolute
	Name string `json:"name"`
	Size int64  `json:"size"`
}
