package proto

// ToolOutcome is what a tool call came to, on its tool_result item (Item.Outcome; docs/protocol/protocol.md "Tool
// outcomes"): read by the node from the runtime's own record of the call (Claude Code's toolUseResult, a Codex
// command's exit code and duration) and from the fixed words a result starts with, so the apps need not guess from
// the text. Every field is optional: absent means the record did not say. It stays small: Tail and Patch are capped
// (transcript/outcome.go) and nothing big the runtime keeps (a file's whole text, a command's whole output) is copied.
type ToolOutcome struct {
	Status string `json:"status,omitempty"` // OutcomeOK, OutcomeFailed…
	// ExitCode: the command's exit code, as recorded (Codex, 0 included) or as a failed Claude Bash result opens with
	// it ("Exit code 2").
	ExitCode *int `json:"exit_code,omitempty"`
	// DurationMS: how long the call ran, when the runtime recorded it (a Codex command, Claude's WebFetch).
	DurationMS *int64 `json:"duration_ms,omitempty"`
	// Tail: the output's last lines, only when the page cut Text (it keeps the first 4 KB): where a build or a test
	// run says what failed. At most 30 lines and 2 KB, starting on a whole line, none of it inside the page's head.
	Tail string `json:"tail,omitempty"`
	// Persisted: Claude Code saved an output too big for the model to a file; Text has only its preview.
	Persisted *OutcomePersisted `json:"persisted,omitempty"`
	// Patch: an edit's change as unified diff hunks ("@@ -12,7 +12,8 @@" headers, the file's real line numbers) for
	// Files[0], at most 8 KB of whole lines; PatchCut says lines were left out past that (each hunk's header counts
	// the lines it kept).
	Patch    string        `json:"patch,omitempty"`
	PatchCut bool          `json:"patch_cut,omitempty"`
	Files    []OutcomeFile `json:"files,omitempty"`
	Git      *OutcomeGit   `json:"git,omitempty"`
	Read     *OutcomeRead  `json:"read,omitempty"`
	// Answers: AskUserQuestion's answers, question → answer as the runtime recorded them (each cut to 1 KB).
	Answers map[string]string `json:"answers,omitempty"`
}

// Outcome statuses. A call that did not run is denied (a permission said no: the user, a rule, a hook, auto mode) or
// blocked (the runtime refused it: bad input, a guard); one that ran is ok, failed, no_match (a search that found
// nothing, not an error), interrupted (stopped mid-way), timed_out (it outran its timeout; Claude Code then moves it
// to the background) or background (it goes on in the background, its task's notice reports the end).
const (
	OutcomeOK          = "ok"
	OutcomeFailed      = "failed"
	OutcomeNoMatch     = "no_match"
	OutcomeDenied      = "denied"
	OutcomeBlocked     = "blocked"
	OutcomeInterrupted = "interrupted"
	OutcomeTimedOut    = "timed_out"
	OutcomeBackground  = "background"
)

// OutcomePersisted is where Claude Code saved a tool's whole output (on the machine the agent runs on) and its size
// in bytes when recorded.
type OutcomePersisted struct {
	Path string `json:"path"`
	Size int64  `json:"size,omitempty"`
}

// OutcomeFile is a file an edit changed: lines added and deleted, counted over the whole change (not only the part
// Patch kept); Created for a file the call wrote new.
type OutcomeFile struct {
	Path    string `json:"path"`
	Adds    int    `json:"adds,omitempty"`
	Dels    int    `json:"dels,omitempty"`
	Created bool   `json:"created,omitempty"`
}

// OutcomeGit is what a command did in git, as Claude Code recorded it (gitOperation).
type OutcomeGit struct {
	Commit *OutcomeCommit `json:"commit,omitempty"`
	Push   *OutcomePush   `json:"push,omitempty"`
	PR     *OutcomePR     `json:"pr,omitempty"`
	Branch *OutcomeBranch `json:"branch,omitempty"`
}

type OutcomeCommit struct {
	SHA    string `json:"sha"`
	Branch string `json:"branch,omitempty"`
	Kind   string `json:"kind,omitempty"` // committed | amended | cherry-picked, as recorded
}

type OutcomePush struct {
	Branch string `json:"branch,omitempty"`
}

type OutcomePR struct {
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	Action string `json:"action,omitempty"` // created | edited | closed…, as recorded
}

type OutcomeBranch struct {
	Ref    string `json:"ref,omitempty"`
	Action string `json:"action,omitempty"` // merged | rebased, as recorded
}

// OutcomeRead is the part of a file a Read returned: from line Start, Lines lines of Total; Unchanged when the
// runtime returned nothing because the file had not changed since the agent last read it.
type OutcomeRead struct {
	Start     int  `json:"start,omitempty"`
	Lines     int  `json:"lines,omitempty"`
	Total     int  `json:"total,omitempty"`
	Unchanged bool `json:"unchanged,omitempty"`
}
