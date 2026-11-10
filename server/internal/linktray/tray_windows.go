//go:build windows

package linktray

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Options are how the program starts the tray.
type Options struct {
	Host Host
	// Exe is the program that starts the tray at sign-in and opens pairing links: the console-less
	// persona-link-service.exe the install put in place.
	Exe string
	// Open is a link given on the command line (`tray --open <link>`, what Windows runs for a pairing link).
	Open string
	// Lang overrides Windows' language ("" follows it).
	Lang i18n.Lang
}

const (
	className    = "PersonaLinkTray"
	panelWidth   = 360 // DIP: the panel's width, as tokens.css's --size-panel
	panelGuess   = 420 // DIP: its height until the page has said
	panelMargin  = 12  // DIP: between the panel and the taskbar or the screen's edge
	pairWidth    = 560
	pairHeight   = 680
	pairMinW     = 480
	pairMinH     = 520
	tickEvery    = time.Second
	taskEvery    = 30 * time.Second
	clickGrace   = 300 * time.Millisecond // a click on the icon right after the panel closed was what closed it
	offlineAfter = time.Minute            // a link down this long gets a notification
)

// command is what a second start of the tray, or `persona-link install` / `uninstall`, sends to the running one.
type command struct {
	Open bool   `json:"open,omitempty"` // show the panel
	Pair string `json:"pair,omitempty"` // a link to put in the pairing window
	Quit bool   `json:"quit,omitempty"`
}

type app struct {
	o     Options
	host  Host
	lang  i18n.Lang
	hwnd  uintptr
	class *uint16
	icon  notifyIcon

	taskbarCreated uint32
	calls          chan func()

	snap     Snapshot
	view     View
	task     Task
	taskAt   time.Time
	taskBusy bool

	iconKey string // look, size and taskbar theme of the icon shown

	panel         *webWindow
	panelPending  bool // created or opened, waiting for the page's size before showing
	panelHiddenAt time.Time
	panelHeight   int32 // DIP, from the page

	pair     *webWindow
	pairJSON string

	awaitingNotified bool
	downSince        time.Time
	downNotified     bool
	connectedOnce    bool
	ticks            int
}

var current *app // the one tray of this process: the window procedure finds it here

// instance is what tells this data directory's tray from another's on the same computer (as link.PokeEventName).
func instance(dataDir string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(dataDir)))
	return hex.EncodeToString(sum[:8])
}

// Send hands c to the tray running for dataDir; false when none runs.
func Send(dataDir string, c command) bool {
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(utf16Ptr(className))), uintptr(unsafe.Pointer(utf16Ptr(instance(dataDir)))))
	if hwnd == 0 {
		return false
	}
	b, _ := json.Marshal(c)
	cds := copyDataStruct{Data: 1, Bytes: uint32(len(b)), Ptr: uintptr(unsafe.Pointer(&b[0]))}
	r, _, _ := procSendMessageW.Call(hwnd, wmCopyData, 0, uintptr(unsafe.Pointer(&cds)))
	runtime.KeepAlive(b)
	return r != 0
}

// Quit asks the tray running for dataDir to go, and waits up to timeout for it to have gone: `install` replaces the
// program it runs from, `uninstall` removes it. False when one is still there.
func Quit(dataDir string, timeout time.Duration) bool {
	if !Send(dataDir, command{Quit: true}) {
		return true
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if !Running(dataDir) {
			return true
		}
	}
	return !Running(dataDir)
}

// Running says whether a tray runs for dataDir.
func Running(dataDir string) bool {
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(utf16Ptr(className))), uintptr(unsafe.Pointer(utf16Ptr(instance(dataDir)))))
	return hwnd != 0
}

// Run is the tray: the icon, the panel, the pairing window, until the user quits it or it is told to. A second
// one for the same data directory hands its link (or "show the panel") to the first and returns.
func Run(o Options) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	dataDir := o.Host.DataDir()
	mutex, err := windows.CreateMutex(nil, false, utf16Ptr(`Local\PersonaLinkTray.`+instance(dataDir)))
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(mutex)
		c := command{Open: o.Open == "", Pair: o.Open}
		if !Send(dataDir, c) {
			return errors.New("another Persona Link tray is starting; try again in a moment")
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(mutex)
	if procSetProcessDpiAwarenessContext.Find() == nil {
		procSetProcessDpiAwarenessContext.Call(dpiAwarenessPerMonitorV2)
	}

	a := &app{o: o, host: o.Host, lang: o.Lang, calls: make(chan func(), 64), panelHeight: panelGuess}
	if a.lang == "" {
		a.lang = uiLanguage()
	}
	current = a
	if err := a.createWindow(dataDir); err != nil {
		return err
	}
	a.taskbarCreated = registerMessage("TaskbarCreated")
	a.icon = notifyIcon{hwnd: a.hwnd, id: 1}
	a.refresh(true)
	if !a.icon.add() {
		log.Printf("tray: the notification area did not take the icon; trying again when Explorer is back")
	}
	procSetTimer.Call(a.hwnd, 1, uintptr(tickEvery/time.Millisecond), 0)
	stop := a.watchDir(dataDir)
	defer stop()
	if o.Open != "" {
		a.openPair(o.Open, "")
	}

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	a.icon.remove()
	return nil
}

func registerMessage(name string) uint32 {
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(utf16Ptr(name))))
	return uint32(r)
}

var wndProcCallback = syscall.NewCallback(wndProc)

// createWindow makes the tray's own window: never shown, it gets the icon's clicks, the timer, the theme changes
// and other programs' commands (found by its class and title, the data directory's instance).
func (a *app) createWindow(dataDir string) error {
	a.class = utf16Ptr(className)
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	wc := wndClassEx{WndProc: wndProcCallback, ClassName: a.class, Cursor: cursor}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("register window class: %w", err)
	}
	hwnd, _, err := procCreateWindowExW.Call(wsExToolWindow, uintptr(unsafe.Pointer(a.class)),
		uintptr(unsafe.Pointer(utf16Ptr(instance(dataDir)))), wsPopup, 0, 0, 0, 0, 0, 0, 0, 0)
	if hwnd == 0 {
		return fmt.Errorf("create window: %w", err)
	}
	a.hwnd = hwnd
	return nil
}

func wndProc(hwnd uintptr, m uint32, w, l uintptr) uintptr {
	a := current
	if a == nil {
		r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(m), w, l)
		return r
	}
	if r, handled := a.handle(hwnd, m, w, l); handled {
		return r
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(m), w, l)
	return r
}

func loword(v uintptr) uint32 { return uint32(v) & 0xFFFF }
func hiword(v uintptr) uint32 { return (uint32(v) >> 16) & 0xFFFF }

func (a *app) handle(hwnd uintptr, m uint32, w, l uintptr) (uintptr, bool) {
	isPanel := a.panel != nil && hwnd == a.panel.hwnd
	isPair := a.pair != nil && hwnd == a.pair.hwnd
	switch {
	case m == wmTrayIcon:
		switch loword(l) {
		case ninSelect, ninKeySelect:
			a.togglePanel()
		case wmContextMenu:
			a.showMenu(int32(int16(loword(w))), int32(int16(hiword(w))))
		case ninBalloonUserClik:
			a.openPanel()
		}
		return 0, true
	case m == a.taskbarCreated && m != 0:
		a.iconKey = "" // a new taskbar may be another color or size
		a.updateIcon()
		a.icon.add()
		return 0, true
	case m == wmRun:
		for {
			select {
			case f := <-a.calls:
				f()
			default:
				return 0, true
			}
		}
	case m == wmTimer && hwnd == a.hwnd:
		a.tick()
		return 0, true
	case m == wmSettingChange || m == wmDwmColorChanged:
		if hwnd == a.hwnd {
			a.themeChanged()
		}
		return 0, false
	case m == wmCopyData && hwnd == a.hwnd:
		cds := (*copyDataStruct)(fromLParam(l))
		if cds.Data != 1 || cds.Bytes == 0 || cds.Bytes > 64<<10 {
			return 0, true
		}
		var c command
		if json.Unmarshal(unsafe.Slice((*byte)(fromLParam(cds.Ptr)), cds.Bytes), &c) != nil {
			return 0, true
		}
		a.later(func() { a.command(c) }) // not inside the sender's SendMessage
		return 1, true
	case m == wmActivate && isPanel:
		if loword(w) == waInactive && a.panel.visible() {
			a.panel.hide()
			a.panelHiddenAt = time.Now()
		}
		return 0, false
	case m == wmSize && (isPanel || isPair):
		web := a.panel
		if isPair {
			web = a.pair
		}
		if web.web != nil {
			web.web.Resize()
		}
		return 0, false
	case m == wmDpiChanged && isPair:
		r := (*rect)(fromLParam(l))
		a.pair.setScale(float64(loword(w)) / 96)
		a.pair.place(r.Left, r.Top, r.width(), r.height(), swpNoActivate)
		return 0, true
	case m == wmGetMinMaxInfo && isPair:
		mm := (*minMaxInfo)(fromLParam(l))
		scale := float64(dpiForWindow(hwnd)) / 96
		mm.MinTrackSize = point{int32(pairMinW * scale), int32(pairMinH * scale)}
		return 0, true
	case m == wmClose && isPair:
		a.closePair()
		return 0, true
	case m == wmDestroy && hwnd == a.hwnd:
		procPostQuitMessage.Call(0)
		return 0, true
	}
	return 0, false
}

// later runs f on the UI thread, after what it is doing now.
func (a *app) later(f func()) {
	a.calls <- f
	postMessage(a.hwnd, wmRun, 0, 0)
}

// localize is err as the tray says it (ErrorText); nil stays nil.
func (a *app) localize(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(ErrorText(a.lang, err))
}

// fromPage handles a page's messages after WebView2's message event has returned: WebView2 holds back its other
// callbacks until then, a new window's WebView being ready among them, so the pairing window could not open from
// the panel.
func (a *app) fromPage(handle func(request)) func(request) {
	return func(r request) { a.later(func() { handle(r) }) }
}

// background runs work off the UI thread and then done on it with work's result.
func background[T any](a *app, work func() (T, error), done func(T, error)) {
	go func() {
		v, err := work()
		a.later(func() { done(v, err) })
	}()
}

func (a *app) command(c command) {
	switch {
	case c.Quit:
		a.quit()
	case c.Pair != "":
		a.openPair(c.Pair, "")
	case c.Open:
		a.openPanel()
	}
}

func (a *app) quit() {
	procKillTimer.Call(a.hwnd, 1)
	if a.panel != nil {
		a.panel.destroy()
		a.panel = nil
	}
	a.closePair()
	a.icon.remove()
	procDestroyWindow.Call(a.hwnd)
}
