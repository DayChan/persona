//go:build windows

package linktray

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"golang.org/x/sys/windows"
)

// tick: every second while a window is open (for "connected for …" and countdowns), every third otherwise; the
// scheduled task every half minute.
func (a *app) tick() {
	a.ticks++
	open := (a.panel != nil && a.panel.visible()) || a.pair != nil
	if open || a.ticks%3 == 0 {
		a.refresh(false)
	}
}

// refresh reads the state again and shows it wherever it is shown. forceTask asks the scheduled task too.
func (a *app) refresh(forceTask bool) {
	if (forceTask || time.Since(a.taskAt) > taskEvery) && !a.taskBusy {
		a.taskBusy = true
		background(a, func() (Task, error) { return a.host.TaskState(), nil }, func(t Task, _ error) {
			a.task, a.taskAt, a.taskBusy = t, time.Now(), false
			a.refresh(false)
		})
	}
	s := a.host.Snapshot()
	s.Task = a.task
	s.Autostart = Autostart()
	if s.Now.IsZero() {
		s.Now = time.Now()
	}
	a.snap = s
	a.view = Build(a.lang, s)
	a.updateIcon()
	a.notifications()
	if a.panel != nil && a.panel.visible() {
		a.pushView()
	}
	if a.pair != nil {
		a.pushPair()
	}
}

// changed is refresh after an action of the user's: the panel's page gets the new view even while hidden (a switch
// flipped from the menu, an action that ended after the panel closed), so it never opens showing the old one.
func (a *app) changed() {
	a.refresh(true)
	if a.panel != nil && !a.panel.visible() {
		a.pushView()
	}
}

func (a *app) pushView() {
	a.panel.send(map[string]any{"type": "view", "view": a.view})
}

func (a *app) pushPair() {
	ps := BuildPair(a.lang, a.snap)
	b, _ := json.Marshal(ps)
	if string(b) == a.pairJSON {
		return // redrawing would take the cursor out of what the user is typing
	}
	a.pairJSON = string(b)
	a.pair.send(map[string]any{"type": "pair", "pair": ps})
}

// updateIcon draws the icon for the look, at the notification area's size, for the taskbar's color.
func (a *app) updateIcon() {
	dpi := dpiForWindow(a.hwnd)
	size := smallIconSize(dpi)
	dark := systemDark()
	key := fmt.Sprintf("%s/%d/%v", a.view.Look, size, dark)
	if key == a.iconKey && a.icon.tip == a.view.Tooltip {
		return
	}
	icon := a.icon.icon
	if key != a.iconKey {
		img, err := RenderIcon(a.view.Look, size, dark)
		if err != nil {
			log.Printf("tray: icon: %v", err)
			return
		}
		icon = hicon(img)
		a.iconKey = key
	}
	a.icon.set(icon, a.view.Tooltip)
}

func smallIconSize(dpi uint32) int {
	if procGetSystemMetricsForDpi.Find() == nil {
		if r, _, _ := procGetSystemMetricsForDpi.Call(smCxSmIcon, uintptr(dpi)); r > 0 {
			return int(r)
		}
	}
	return int(16 * dpi / 96)
}

// notifications: once when the hub starts waiting for the user's OK, once when a link that was up has been down a
// minute. Nothing at sign-in before the link was ever up: the network may still be coming.
func (a *app) notifications() {
	look := a.view.Look
	if look == LookAwaitingConfirm {
		if !a.awaitingNotified {
			n := AwaitingNotification(a.lang)
			a.icon.balloon(n.Title, n.Body)
			a.awaitingNotified = true
		}
	} else {
		a.awaitingNotified = false
	}
	switch look {
	case LookConnected, LookListening, LookAwaitingConfirm, LookNodeMissing:
		a.connectedOnce, a.downSince, a.downNotified = true, time.Time{}, false
	case LookDown:
		if a.downSince.IsZero() {
			a.downSince = time.Now()
		}
		if a.connectedOnce && !a.downNotified && time.Since(a.downSince) >= offlineAfter {
			n := OfflineNotification(a.lang)
			a.icon.balloon(n.Title, n.Body)
			a.downNotified = true
		}
	}
}

func (a *app) themeChanged() {
	a.iconKey = ""
	a.updateIcon()
	if a.panel != nil {
		a.panel.themeChanged()
	}
	if a.pair != nil {
		a.pair.themeChanged()
	}
}

// watchDir refreshes as soon as something in the data directory changes (link.status.json, link.json), instead of
// waiting for the next tick. stop ends the watch.
func (a *app) watchDir(dir string) (stop func()) {
	h, err := windows.FindFirstChangeNotification(dir, false, windows.FILE_NOTIFY_CHANGE_LAST_WRITE|windows.FILE_NOTIFY_CHANGE_FILE_NAME)
	if err != nil {
		return func() {}
	}
	quit, _ := windows.CreateEvent(nil, 1, 0, nil)
	go func() {
		defer windows.FindCloseChangeNotification(h)
		for {
			r, err := windows.WaitForMultipleObjects([]windows.Handle{h, quit}, false, windows.INFINITE)
			if err != nil || r != windows.WAIT_OBJECT_0 {
				return
			}
			time.Sleep(150 * time.Millisecond) // a write and its rename are one change
			if windows.FindNextChangeNotification(h) != nil {
				return
			}
			a.later(func() { a.refresh(false) })
		}
	}()
	return func() { windows.SetEvent(quit) }
}
