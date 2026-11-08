package node

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/tailscale/go-winio"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The Codex App's bus on Windows (codex_desktop.go): the App's main process (ChatGPT.exe, package OpenAI.Codex) serves
// it on one named pipe for the whole user session, \\.\pipe\codex-ipc, not on a socket in its Codex home. The frames
// and the methods are the macOS ones.

// codexDesktopPipe is the App's bus. A variable for tests: none of them may reach the user's App.
var codexDesktopPipe = `\\.\pipe\codex-ipc`

// codexDesktopAvailable: the App runs in this session (its pipe is there) and keeps its sidebar in home (its
// .codex-global-state.json). The pipe is one per session whatever the home, so the state file is what ties it to one.
func codexDesktopAvailable(home string) bool {
	if home == "" || !codexPipeUp(codexDesktopPipe) {
		return false
	}
	_, err := os.Stat(filepath.Join(home, ".codex-global-state.json"))
	return err == nil
}

// codexPipeUp says whether the named pipe exists, from the pipe namespace's listing: opening it (os.Stat does, through
// CreateFile) would take one of the App's free instances for a moment, or connect to it. Kept a few seconds, as the
// session list asks once per App thread every second.
func codexPipeUp(name string) bool {
	codexPipeSeen.Lock()
	defer codexPipeSeen.Unlock()
	if codexPipeSeen.name == name && time.Since(codexPipeSeen.at) < codexPipeTTL {
		return codexPipeSeen.up
	}
	p, err := windows.UTF16PtrFromString(name)
	up := false
	if err == nil {
		var d windows.Win32finddata
		if h, err := windows.FindFirstFile(p, &d); err == nil {
			windows.FindClose(h)
			up = true
		}
	}
	codexPipeSeen.name, codexPipeSeen.at, codexPipeSeen.up = name, time.Now(), up
	return up
}

const codexPipeTTL = 2 * time.Second

var codexPipeSeen struct {
	sync.Mutex
	name string
	at   time.Time
	up   bool
}

func dialCodexDesktopBus(ctx context.Context, _ string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, codexDesktopPipe)
}

// openCodexThread asks the App to open a thread (it comes to the front, showing it). A variable for tests.
var openCodexThread = openCodexLink

// openCodexLink opens codex://threads/<id> through the link the App's package registered — only when it did, so
// nothing else gets the link. ShellExecute opens no console window.
func openCodexLink(thread string) error {
	if !codexLinkRegistered() {
		return errors.New("codex:// is not registered")
	}
	return shellOpen("codex://threads/" + thread)
}

// codexLinkRegistered: codex:// is a URL protocol (HKEY_CLASSES_ROOT\codex has "URL Protocol") with a handler to
// start. The App's package registers it as a packaged app's protocol: no shell\open\command, but an AppUserModelID
// the shell activates.
func codexLinkRegistered() bool {
	k, err := registry.OpenKey(registry.CLASSES_ROOT, "codex", registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	_, _, err = k.GetStringValue("URL Protocol")
	k.Close()
	if err != nil {
		return false
	}
	return assocString("codex", assocStrAppID) != "" || assocString("codex", assocStrCommand) != ""
}

const (
	assocfIsProtocol = 0x1000 // ASSOCF_IS_PROTOCOL
	assocStrCommand  = 1      // ASSOCSTR_COMMAND
	assocStrAppID    = 21     // ASSOCSTR_APPID
)

var assocQueryString = windows.NewLazySystemDLL("shlwapi.dll").NewProc("AssocQueryStringW")

// assocString is what the shell associates with the URL scheme (AssocQueryString), "" for nothing.
func assocString(scheme string, what uint32) string {
	s, err := windows.UTF16PtrFromString(scheme)
	if err != nil || assocQueryString.Find() != nil {
		return ""
	}
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	r, _, _ := assocQueryString.Call(assocfIsProtocol, uintptr(what), uintptr(unsafe.Pointer(s)), 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r != 0 || n == 0 || n > uint32(len(buf)) {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// shellOpen opens a link as the shell would. ShellExecute wants COM set up on its thread (a packaged app's protocol is
// activated through it), so it runs on a thread of its own.
func shellOpen(link string) error {
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil {
			defer windows.CoUninitialize()
		}
		verb, _ := windows.UTF16PtrFromString("open")
		file, err := windows.UTF16PtrFromString(link)
		if err != nil {
			errc <- err
			return
		}
		errc <- windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
	}()
	return <-errc
}
