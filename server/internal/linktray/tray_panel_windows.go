//go:build windows

package linktray

import (
	"encoding/json"
	"log"
	"time"
	"unsafe"

	"github.com/nwverse/persona/server/internal/i18n"
)

func (a *app) togglePanel() {
	switch {
	case a.panel != nil && a.panel.visible():
		a.panel.hide()
		a.panelHiddenAt = time.Now()
	case time.Since(a.panelHiddenAt) < clickGrace:
		// The click took the focus from the panel, which closed it: that was the user closing it.
	default:
		a.openPanel()
	}
}

func (a *app) openPanel() {
	if !webViewInstalled() {
		a.icon.balloon("Persona Link", i18n.T(a.lang, "link-tray.no_webview"))
		r, _ := a.icon.rect()
		a.showMenu(r.Left, r.Top)
		return
	}
	a.refresh(true)
	if a.panel == nil {
		html, err := Page("panel", a.lang, Boot{Theme: themeFor(systemDark(), backdropAvailable()), Version: a.host.Version(), View: &a.view})
		if err != nil {
			log.Printf("tray: panel: %v", err)
			return
		}
		a.panel, err = newWebWindow(a.class, a.hwnd, true, "Persona Link", panelWidth, panelGuess, html, a.fromPage(a.panelMessage))
		if err != nil {
			log.Printf("tray: panel: %v", err)
			a.panel = nil
			return
		}
		a.panelPending = true
		// Shown when the page says how tall it is; a page that never does is shown anyway.
		time.AfterFunc(2*time.Second, func() { a.later(a.showPanelNow) })
		return
	}
	a.pushView()
	a.showPanelNow()
}

func (a *app) showPanelNow() {
	if a.panel == nil {
		return
	}
	a.panelPending = false
	a.placePanel()
	a.panel.show()
	a.panel.send(map[string]any{"type": "show"})
}

// placePanel puts the panel by the icon, inside the work area, on the taskbar's side; sized for that monitor's DPI.
func (a *app) placePanel() {
	icon, haveIcon := a.icon.rect()
	var mon uintptr
	if haveIcon {
		mon, _, _ = procMonitorFromRect.Call(uintptr(unsafe.Pointer(&icon)), monitorNearest)
	} else {
		p := cursorPos()
		icon = rect{p.X, p.Y, p.X + 1, p.Y + 1}
		mon, _, _ = procMonitorFromPoint.Call(uintptr(uint32(p.X))|uintptr(uint32(p.Y))<<32, monitorNearest)
	}
	mi, ok := monitorInfoOf(mon)
	if !ok {
		return
	}
	scale := float64(dpiForMonitor(mon)) / 96
	a.panel.setScale(scale)
	w, h := int32(panelWidth*scale), int32(float64(a.panelHeight)*scale)
	margin := int32(panelMargin * scale)
	work, full := mi.Work, mi.Monitor
	clamp := func(v, lo, hi int32) int32 { return max(lo, min(v, hi)) }
	x := clamp((icon.Left+icon.Right)/2-w/2, work.Left+margin, work.Right-w-margin)
	y := work.Bottom - h - margin
	switch {
	case work.Top > full.Top: // taskbar on top
		y = work.Top + margin
	case work.Left > full.Left: // on the left
		x = work.Left + margin
	case work.Right < full.Right: // on the right
		x = work.Right - w - margin
	}
	a.panel.place(x, y, w, h, 0)
}

// panelMessage is what the panel's page asks.
func (a *app) panelMessage(r request) {
	switch r.Cmd {
	case "size":
		var s struct{ Height float64 }
		if json.Unmarshal(r.Args, &s) == nil && s.Height > 0 {
			a.panelHeight = int32(s.Height + 0.5)
			if a.panelPending {
				a.showPanelNow()
			} else if a.panel.visible() {
				a.placePanel()
			}
		}
	case "escape":
		a.panel.hide()
		a.panelHiddenAt = time.Now()
	case "action":
		var act struct {
			ID string
			On bool
		}
		_ = json.Unmarshal(r.Args, &act)
		a.action(act.ID, act.On, func(err error) { a.panel.reply(r.ID, nil, a.localize(err)) })
	case "rename":
		var in struct{ Name string }
		_ = json.Unmarshal(r.Args, &in)
		w := a.panel
		background(a, func() (string, error) { return a.host.Rename(in.Name) }, func(name string, err error) {
			if err == nil {
				a.notice(TonePositive, i18n.T(a.lang, "link-tray.renamed_ok", i18n.Args{"name": name}))
			}
			if w == a.panel && w != nil {
				w.reply(r.ID, map[string]string{"name": name}, a.localize(err))
			}
			a.refresh(false)
		})
	}
}

// notice shows the result of an action in the panel.
func (a *app) notice(tone, text string) {
	if a.panel != nil {
		a.panel.send(map[string]any{"type": "notice", "notice": map[string]string{"tone": tone, "text": text}})
	}
}

// action does what a row of the panel or the menu says. done is called on the UI thread with how it went.
func (a *app) action(id string, on bool, done func(error)) {
	finish := func(err error, ok string) {
		if err != nil {
			a.notice(ToneDanger, ErrorText(a.lang, err))
		} else if ok != "" {
			a.notice(TonePositive, ok)
		}
		a.changed()
		if done != nil {
			done(err)
		}
	}
	switch id {
	case ActReconnect:
		background(a, func() (struct{}, error) { return struct{}{}, a.host.Reconnect() }, func(_ struct{}, err error) { finish(err, "") })
	case ActRestart:
		background(a, func() (struct{}, error) { return struct{}{}, a.host.RestartService() }, func(_ struct{}, err error) {
			finish(err, i18n.T(a.lang, "link-tray.service_restarted"))
		})
	case ActInstall:
		background(a, func() (struct{}, error) { return struct{}{}, a.host.InstallService() }, func(_ struct{}, err error) {
			finish(err, i18n.T(a.lang, "link-tray.service_installed"))
		})
	case ActPair:
		if a.panel != nil {
			a.panel.hide()
		}
		a.openPair("", "")
		finish(nil, "")
	case ActLogs:
		finish(shellOpen(a.host.DataDir()), "")
	case ActAutostart:
		finish(SetAutostart(a.o.Exe, on), "")
	case ActQuit:
		if done != nil {
			done(nil)
		}
		a.quit()
	case ActPanel:
		a.openPanel()
	case ActRename: // the panel's own field (panel.js): from the menu, the panel opens with it
		a.openPanel()
		if a.panel != nil {
			a.panel.send(map[string]any{"type": "rename"})
		}
		finish(nil, "")
	}
}

// showMenu is the icon's context menu at (x, y): the native menu works without WebView2.
func (a *app) showMenu(x, y int32) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	labels := MenuLabels(a.lang)
	ids := []string{}
	add := func(flags uint32, text string, id string) {
		ids = append(ids, id)
		procAppendMenuW.Call(menu, uintptr(flags), uintptr(len(ids)), uintptr(unsafe.Pointer(utf16Ptr(text))))
	}
	add(mfString|mfGrayed, "Persona Link · "+a.view.Pill, "")
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	add(mfString, labels[ActPanel], ActPanel)
	for _, act := range a.view.Actions {
		if act.ID == ActReconnect {
			flags := uint32(mfString)
			if act.Disabled {
				flags |= mfGrayed
			}
			add(flags, labels[ActReconnect], ActReconnect)
		}
	}
	add(mfString, labels[ActPair], ActPair)
	for _, act := range a.view.Actions {
		if act.ID == ActRename {
			add(mfString, labels[ActRename], ActRename)
		}
	}
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	auto := uint32(mfString)
	if a.snap.Autostart {
		auto |= mfChecked
	}
	add(auto, labels[ActAutostart], ActAutostart)
	add(mfString, labels[ActQuit], ActQuit)

	procSetForegroundWindow.Call(a.hwnd) // or the menu does not close when the user clicks elsewhere
	chosen, _, _ := procTrackPopupMenuEx.Call(menu, tpmReturn|tpmRightBtn, uintptr(x), uintptr(y), a.hwnd, 0)
	postMessage(a.hwnd, 0, 0, 0)
	if chosen == 0 || int(chosen) > len(ids) || ids[chosen-1] == "" {
		return
	}
	id := ids[chosen-1]
	a.action(id, !a.snap.Autostart, nil)
}
