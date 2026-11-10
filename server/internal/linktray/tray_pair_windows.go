//go:build windows

package linktray

import (
	"encoding/json"
	"log"
	"net"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

func (a *app) openPair(prefill, tab string) {
	if !webViewInstalled() {
		a.icon.balloon("Persona Link", i18n.T(a.lang, "link-tray.no_webview"))
		return
	}
	a.refresh(false)
	if a.pair != nil {
		if prefill != "" {
			a.pair.send(map[string]any{"type": "prefill", "raw": prefill})
		}
		if tab != "" {
			a.pair.send(map[string]any{"type": "tab", "tab": tab})
		}
		a.pair.show()
		return
	}
	ps := BuildPair(a.lang, a.snap)
	b, _ := json.Marshal(ps)
	a.pairJSON = string(b)
	boot := Boot{Theme: themeFor(appsDark(), backdropAvailable()), Version: a.host.Version(), Pair: &ps, Prefill: prefill, Tab: tab}
	if prefill != "" {
		p := ParseLink(a.lang, prefill)
		boot.Preview = &p
	}
	html, err := Page("pair", a.lang, boot)
	if err != nil {
		log.Printf("tray: pair: %v", err)
		return
	}
	title := Labels(a.lang)["pair_window_title"]
	w, err := newWebWindow(a.class, 0, false, title, pairWidth, pairHeight, html, a.fromPage(a.pairMessage))
	if err != nil {
		log.Printf("tray: pair: %v", err)
		return
	}
	a.pair = w
	a.setWindowIcon(w.hwnd)
	// Centered on the monitor the pointer is on, at its DPI.
	p := cursorPos()
	mon, _, _ := procMonitorFromPoint.Call(uintptr(uint32(p.X))|uintptr(uint32(p.Y))<<32, monitorNearest)
	if mi, ok := monitorInfoOf(mon); ok {
		scale := float64(dpiForMonitor(mon)) / 96
		width, height := int32(pairWidth*scale), int32(min(float64(pairHeight)*scale, float64(mi.Work.height())*0.9))
		w.setScale(scale)
		w.place(mi.Work.Left+(mi.Work.width()-width)/2, mi.Work.Top+(mi.Work.height()-height)/2, width, height, 0)
	}
	w.show()
}

// setWindowIcon gives the pairing window the tray's mark in the title bar and Alt+Tab.
func (a *app) setWindowIcon(hwnd uintptr) {
	dpi := dpiForWindow(hwnd)
	for _, which := range []struct {
		kind uintptr
		size int
	}{{0, smallIconSize(dpi)}, {1, int(32 * dpi / 96)}} {
		theme := themeFor(appsDark(), false)
		ink := ToneColors[ToneInfo][0]
		if theme.Dark {
			ink = ToneColors[ToneInfo][1]
		}
		img, err := renderGlyph("hotspot", which.size, ink, iconStroke(which.size))
		if err != nil {
			return
		}
		procSendMessageW.Call(hwnd, wmSetIcon, which.kind, hicon(img))
	}
}

func (a *app) closePair() {
	if a.pair == nil {
		return
	}
	a.pair.destroy()
	a.pair = nil
	a.pairJSON = ""
}

// pairMessage is what the pairing window's page asks.
func (a *app) pairMessage(r request) {
	w := a.pair
	var args struct {
		Raw, Machine, Listen, Mode, URL string
	}
	_ = json.Unmarshal(r.Args, &args)
	reply := func(data any, err error) {
		if w == a.pair && w != nil {
			w.reply(r.ID, data, a.localize(err))
		}
		a.refresh(false)
	}
	switch r.Cmd {
	case "parse_link":
		w.reply(r.ID, ParseLink(a.lang, args.Raw), nil)
	case "pair":
		background(a, func() (string, error) { return a.host.Pair(args.Raw, args.Machine) }, func(machine string, err error) {
			if err != nil {
				reply(nil, err)
				return
			}
			reply(map[string]string{"text": i18n.T(a.lang, "link-tray.paired_ok", i18n.Args{"name": machine})}, nil)
		})
	case "pair_listen":
		background(a, func() (Invitation, error) { return a.host.PairListen(args.Listen) }, func(inv Invitation, err error) {
			if err == nil {
				inv.AddrText = CodeAddrs(a.lang, inv.Addrs)
				_, port, perr := net.SplitHostPort(inv.Listen)
				if perr != nil {
					port = strings.TrimPrefix(inv.Listen, ":")
				}
				inv.Firewall = FirewallCommand(port)
			}
			reply(inv, err)
		})
	case "set_mode":
		background(a, func() (struct{}, error) { return struct{}{}, a.host.SetMode(args.Mode) }, func(_ struct{}, err error) { reply(nil, err) })
	case "address_add":
		background(a, func() (struct{}, error) { return struct{}{}, a.host.AddAddress(args.Raw) }, func(_ struct{}, err error) { reply(nil, err) })
	case "address_remove":
		background(a, func() (struct{}, error) { return struct{}{}, a.host.RemoveAddress(args.URL) }, func(_ struct{}, err error) { reply(nil, err) })
	}
}
