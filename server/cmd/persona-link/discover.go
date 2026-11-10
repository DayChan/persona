package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/mdns"
)

// `persona-link discover`: the hubs on this LAN, as they advertise themselves over mDNS (_persona-hub._tcp, docs/server/mesh.md).
// Finding the hub is one of the connector's three jobs; the installer asks it whether this computer needs a hub of its
// own or the node service is enough (docs/archive/operations/plan-hub-center.md section 0). Nothing is paired by it: pairing takes a link.

const hubService = "_persona-hub._tcp"

// foundHub is a hub heard on the LAN: what its TXT record says (never a credential) and where it is.
type foundHub struct {
	HubID     string   `json:"hub_id"`
	Name      string   `json:"name,omitempty"`
	Version   string   `json:"version,omitempty"`
	Addresses []string `json:"addresses"` // https://host:tls-port, LAN first
}

// browseHubs listens for hubs for d (tests replace it).
var browseHubs = browseCompatibleHubs

var runMDNS = mdns.Run

func browseCompatibleHubs(ctx context.Context, d time.Duration) ([]foundHub, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	var mu sync.Mutex
	hubs := map[string]map[string]foundHub{}
	results := make(chan error, 2)
	for _, service := range []string{hubService, legacyHubService} {
		go func() {
			results <- runMDNS(ctx, mdns.Options{Browse: service, QueryEvery: time.Second, Found: func(e mdns.Entry) {
				id := e.TXT["hub_id"]
				if id == "" {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if e.TTL == 0 {
					delete(hubs[service], id)
					return
				}
				port := e.Port
				if p, err := strconv.Atoi(e.TXT["tls"]); err == nil && p > 0 {
					port = p
				}
				h := foundHub{HubID: id, Name: clip(e.TXT["name"], 60), Version: clip(e.TXT["v"], 60)}
				for _, a := range e.Addrs {
					if a.Is6() && a.Zone() != "" {
						continue // link-local: only usable with its interface
					}
					h.Addresses = append(h.Addresses, "https://"+net.JoinHostPort(a.Unmap().String(), strconv.Itoa(port)))
				}
				slices.SortStableFunc(h.Addresses, func(a, b string) int { return addressRank(a) - addressRank(b) })
				if hubs[service] == nil {
					hubs[service] = map[string]foundHub{}
				}
				hubs[service][id] = h
			}})
		}()
	}
	// One unavailable service browser must not hide the other one's results.
	var errs []error
	for range 2 {
		if err := <-results; err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 2 {
		return nil, errors.Join(errs...)
	}
	merged := map[string]foundHub{}
	// Prefer canonical metadata, but keep addresses heard through either name.
	for _, service := range []string{legacyHubService, hubService} {
		for id, h := range hubs[service] {
			old := merged[id]
			for _, addr := range old.Addresses {
				if !slices.Contains(h.Addresses, addr) {
					h.Addresses = append(h.Addresses, addr)
				}
			}
			slices.SortStableFunc(h.Addresses, func(a, b string) int { return addressRank(a) - addressRank(b) })
			merged[id] = h
		}
	}
	out := make([]foundHub, 0, len(merged))
	for _, h := range merged {
		out = append(out, h)
	}
	slices.SortFunc(out, func(a, b foundHub) int {
		if a.Name != b.Name {
			if a.Name < b.Name {
				return -1
			}
			return 1
		}
		if a.HubID < b.HubID {
			return -1
		}
		return 1
	})
	return out, nil
}

// addressRank puts LAN addresses first, as the link tries them.
func addressRank(u string) int {
	if k := link.KindOf(u); k == "lan" {
		return 0
	} else if k == "ipv6" {
		return 1
	}
	return 2
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// discover is `persona-link discover [--wait <seconds>] [--json]`: exit code 0 when a hub was heard, 1 when none.
func discover(e *env, args []string) error {
	wait, asJSON := 3*time.Second, false
	for i := 0; i < len(args); i++ {
		if args[i] == "--json" {
			asJSON = true
			continue
		}
		v, next, ok, err := flagValue(args, i, "--wait")
		switch {
		case err != nil:
			return err
		case !ok:
			return errUsage
		}
		s, err := strconv.ParseFloat(v, 64)
		if err != nil || s <= 0 || s > 60 {
			return fmt.Errorf("--wait %q：要 0 到 60 秒", v)
		}
		wait, i = time.Duration(s*float64(time.Second)), next
	}
	hubs, err := browseHubs(context.Background(), wait)
	if err != nil {
		return fmt.Errorf("在局域网里找不了 hub（mDNS）：%w", err)
	}
	if hubs == nil {
		hubs = []foundHub{}
	}
	if asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(hubs); err != nil {
			return err
		}
	} else if len(hubs) == 0 {
		fmt.Fprintf(e.stdout, "%s内局域网里没听到 hub（mDNS；Docker 桥接网络里的 hub 听不到）。\n", duration(wait))
	} else {
		// What a hub advertises is any LAN host's text, printed above the installer's question: inert, one line per hub.
		for _, h := range hubs {
			name := h.Name
			if name == "" {
				name = h.HubID
			}
			addr := ""
			if len(h.Addresses) > 0 {
				addr = " · " + clitext.Inert(h.Addresses[0])
			}
			fmt.Fprintf(e.stdout, "hub「%s」%s\n", clitext.Inert(name), addr)
		}
	}
	if len(hubs) == 0 {
		return exitCode(1)
	}
	return nil
}
