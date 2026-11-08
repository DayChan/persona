package proto

import (
	"encoding/json"
	"time"
)

// Computer is a Mac the assistant may operate through Cua Driver (the built-in plugin cua, docs/extensions/computer-use.md). In
// proto.Machine only while the plugin is on, and only for a Mac. Enabled: the user turned it on for this machine; the
// rest is what the node found when last asked (computer.status), and only for an enabled one.
type Computer struct {
	Enabled   bool   `json:"enabled"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// Daemon: Cua Driver's daemon (CuaDriver.app) runs; nothing works without it.
	Daemon bool `json:"daemon"`
	// Accessibility, ScreenRecording: the daemon's macOS permissions, granted | missing | unknown.
	Accessibility   string `json:"accessibility,omitempty"`
	ScreenRecording string `json:"screen_recording,omitempty"`
	// Desktop: active (this user's desktop is on screen) | locked (its screen is locked) | away (logged out, or another
	// user is on the console). Cua Driver needs it active.
	Desktop string `json:"desktop,omitempty"`
	// ScreenSharing: macOS's Screen Sharing answers on 127.0.0.1:5900, so the app can show this Mac's live screen
	// (docs/extensions/computer-use.md "Live view"). Not needed to operate it.
	ScreenSharing bool `json:"screen_sharing"`
	// Displays: this Mac's displays as arranged in System Settings, read with ScreenSharing (only while it answers).
	// The app lays them onto the Screen Sharing picture to offer one display at a time.
	Displays []Display `json:"displays,omitempty"`
	// NoAutoLock: "Don't auto-lock while the extension is on" is on for this Mac (the hub's, from the plugin's settings).
	NoAutoLock bool `json:"no_auto_lock,omitempty"`
	// Backend is which driver this status is for: "cua" (Cua Driver) or "lcu" (amontlabs/lcu). Empty from a hub that
	// only knows Cua Driver.
	Backend string `json:"backend,omitempty"`
	// ChatGPT: the official ChatGPT desktop app is installed. LCU needs it for the runtime; Cua Driver does not.
	ChatGPT bool `json:"chatgpt,omitempty"`
	// Ready: all of the above in place. Note says what is missing and what to do, in Chinese for the app.
	Ready   bool      `json:"ready"`
	Note    string    `json:"note,omitempty"`
	Checked time.Time `json:"checked,omitempty"`
	// Job: installing Cua Driver or asking for its permissions from the app (computer.install / computer.grant),
	// the one running or the last one since the node started.
	Job *ComputerJob `json:"job,omitempty"`
}

// Browser is a Mac's ego lite, the agent browser the assistant may drive (the built-in plugin ego,
// docs/plans/plan-ego-browser.md). In proto.Machine only while the plugin is on, and only for a Mac; Enabled: the user turned
// it on for this machine. The rest is what the node found when last asked (browser.status).
type Browser struct {
	Enabled bool `json:"enabled"`
	// Installed: /Applications/ego lite.app is there; Version, its version.
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// SetUp: its first-run setup is done (it registers ego-browser and records it in its Local State); Imported: that
	// setup imported another browser's data (sign-ins, cookies…).
	SetUp    bool `json:"set_up"`
	Imported bool `json:"imported"`
	// Analytics: Chromium's usage statistics, on | off | unknown.
	Analytics string `json:"analytics,omitempty"`
	// Ready: a read-only call through ego-browser worked a moment ago. Note: what is missing, in Chinese for the app.
	Ready         bool         `json:"ready"`
	Note          string       `json:"note,omitempty"`
	ScreenSharing bool         `json:"screen_sharing"`
	Displays      []Display    `json:"displays,omitempty"` // as in Computer
	Job           *ComputerJob `json:"job,omitempty"`
	Checked       time.Time    `json:"checked,omitempty"`
}

// Display is one of a Mac's displays: its frame in points in the global display space (origin at the top-left corner
// of the main display, the one with the menu bar; y grows down), as System Settings › Displays arranges them. ID is
// macOS's display number. Names are left out: they are the monitors' models, not needed to tell them apart.
type Display struct {
	ID     int  `json:"id"`
	X      int  `json:"x"`
	Y      int  `json:"y"`
	Width  int  `json:"width"`
	Height int  `json:"height"`
	Main   bool `json:"main,omitempty"`
}

// BrowserCookie is one cookie imported into ego lite's default profile (hub → node browser.cookies.import, #1304). The
// client parses the file the user picked (cookies.txt or an extension's JSON) into these; the hub passes them on and
// keeps nothing. Expires is Unix seconds, 0 for a session cookie; HostOnly: Domain is the host itself (no leading dot).
type BrowserCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	HTTPOnly bool    `json:"http_only,omitempty"`
	SameSite string  `json:"same_site,omitempty"` // Strict | Lax | None
	HostOnly bool    `json:"host_only,omitempty"`
}

// BrowserCookieSite is a site with cookies in ego lite's profile (browser.cookies.list): never a value or a name.
type BrowserCookieSite struct {
	Domain  string    `json:"domain"` // the registrable part shown to the user: example.com for .example.com and www.example.com
	Count   int       `json:"count"`
	Expires time.Time `json:"expires,omitzero"` // the latest, zero when all are session cookies
}

// BrowserCookies answers browser.cookies.*: the sites now (after an import or a clear), and what was done.
type BrowserCookies struct {
	Sites    []BrowserCookieSite `json:"sites"`
	Imported int                 `json:"imported,omitempty"`
	Skipped  int                 `json:"skipped,omitempty"` // expired or malformed
	Removed  int                 `json:"removed,omitempty"`
}

// ComputerJob is one install or grant the node runs in the background (docs/extensions/computer-use.md "One-click install").
type ComputerJob struct {
	Kind  string `json:"kind"`  // install | grant | open
	State string `json:"state"` // running | done | failed | cancelled
	// Step: what it does now, in Chinese for the app; Error: why it failed (the installer's last lines).
	Step    string    `json:"step,omitempty"`
	Error   string    `json:"error,omitempty"`
	Started time.Time `json:"started"`
}

// ComputerResult is what one Cua Driver tool answered (node computer.call): its JSON (Output), or Text when it was not
// JSON, and the screenshot it took, if any.
type ComputerResult struct {
	Output json.RawMessage `json:"output,omitempty"`
	Text   string          `json:"text,omitempty"`
	Image  *Image          `json:"image,omitempty"`
}
