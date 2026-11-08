package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// parsePairLink reads what `personad hub pair` printed: persona://link?… (made for one machine) or persona://pair?…
// (the apps' link; then the machine is named on the command line).
func parsePairLink(raw string) (token string, c Config, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !pairScheme(u.Scheme) || (u.Host != "link" && u.Host != "pair") {
		return "", c, errors.New("这不是 personad hub pair 给的配对链接（persona://link?… 或 persona://pair?…）")
	}
	q := u.Query()
	token, c.Hub, c.FP, c.Machine = q.Get("token"), q.Get("hub"), q.Get("fp"), q.Get("machine")
	if token == "" {
		return "", c, errors.New("配对链接里没有 token")
	}
	if c.FP != "" {
		// The hub's own TLS port, pinned: url names the host, tls the port (the hub serves the API at its root).
		port := q.Get("tls")
		if port == "" {
			return "", c, errors.New("配对链接有指纹却没有 TLS 端口：在 hub 上重新运行 personad hub pair --machine")
		}
		if base, err := url.Parse(q.Get("url")); err == nil && base.Hostname() != "" {
			u := "https://" + net.JoinHostPort(base.Hostname(), port)
			c.Endpoints = append(c.Endpoints, Endpoint{URL: u, Kind: kindOf(u)})
		}
	} else {
		// No pin: the hub has no TLS of its own and url is the HTTPS reverse proxy in front of it, path and all.
		e, err := proxyEndpoint(q.Get("url"))
		if err != nil {
			return "", c, errors.New("这台 hub 没开 TLS（hub.json 的 public.tls_listen），public_url 也不是 https 的反向代理地址：电脑链路只走 HTTPS")
		}
		c.Trust = TrustSystem
		c.Endpoints = append(c.Endpoints, e)
	}
	for _, alt := range q["alt"] {
		if e, err := normalize(alt, nil); err == nil {
			e.Manual = false
			c.Endpoints = append(c.Endpoints, e)
		}
	}
	if len(c.Endpoints) == 0 {
		return "", c, errors.New("配对链接里没有 hub 的地址")
	}
	return token, c, nil
}

// proxyEndpoint is the candidate for a hub behind an HTTPS reverse proxy: its https URL with the port spelled out (as
// the hub's own list has it) and the path prefix kept.
func proxyEndpoint(raw string) (Endpoint, error) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "https://") {
		return Endpoint{}, errors.New("not an https URL")
	}
	e, err := normalize(raw, nil)
	if err != nil {
		return Endpoint{}, err
	}
	u, _ := url.Parse(e.URL)
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "443")
	}
	u.Host = strings.ToLower(u.Host)
	return Endpoint{URL: u.String(), Kind: kindOf(u.String())}, nil
}

// Pair redeems a pairing link for this computer's credential and writes link.json. machine names the computer in
// the hub's hub.json when the link does not carry it; name is how the hub lists the credential.
func Pair(ctx context.Context, raw, machine, name, path string) (*Config, error) {
	token, c, err := parsePairLink(raw)
	if err != nil {
		return nil, err
	}
	if c.Machine != "" && machine != "" && machine != c.Machine {
		return nil, fmt.Errorf("这条配对链接是给 %q 的，不是 %q", c.Machine, machine)
	}
	if c.Machine == "" {
		c.Machine = machine
	}
	if c.Machine == "" {
		return nil, errors.New("要填这台电脑在 hub 里的机器 id（hub.json 里 kind 为 link 的那台）")
	}
	// system_name: a hub that has no name of its own for this computer yet names it so (docs/operations/link.md "The
	// computer's name").
	body, _ := json.Marshal(map[string]string{"name": name, "kind": "link", "machine": c.Machine, "system_name": SystemName()})
	client := hubClient(&c, nil)
	defer client.CloseIdleConnections()
	type redeemed struct {
		Token string `json:"token"`
		Role  string `json:"role"`
	}
	got, _, err := race(ctx, c.Endpoints, func(ctx context.Context, ep Endpoint) (redeemed, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL+"/api/v1/auth/tokens", bytes.NewReader(body))
		if err != nil {
			return redeemed{}, err
		}
		req.Header = bearer(token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return redeemed{}, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode != http.StatusOK {
			// With the cause a localized summary moved to detail (as apierr.Describe, #5728).
			var e struct {
				Error  string `json:"error"`
				Detail any    `json:"detail"`
			}
			_ = json.Unmarshal(b, &e)
			why := e.Error
			if d, ok := e.Detail.(string); ok && d != "" && d != why {
				why = strings.TrimPrefix(why+"：", "：") + d
			}
			if why == "" {
				why = resp.Status
			}
			return redeemed{}, fmt.Errorf("hub 拒绝了：%s", why)
		}
		var out redeemed
		if err := json.Unmarshal(b, &out); err != nil || out.Token == "" || out.Role != "link" {
			return redeemed{}, errors.New("hub 没给出链路凭据：升级 hub")
		}
		return out, nil
	}, func(redeemed) {})
	if err != nil {
		return nil, fmt.Errorf("配对失败：%w", err)
	}
	c.Token = got.Token
	err = Update(path, func(old *Config) error {
		var manual []Endpoint
		if old.Hub == c.Hub {
			for _, e := range old.Endpoints {
				if e.Manual {
					manual = append(manual, e)
				}
			}
		}
		keep := c
		keep.Endpoints = append(manual, c.Endpoints...)
		// Pairing with a hub's link means dialing it; what passive mode had stays for switching back.
		keep.Mode, keep.Passive = ModeActive, old.Passive
		*old = keep
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// AddAddress adds a candidate typed on this computer (a domain name, say); RemoveAddress takes one out again.
func AddAddress(path, raw string) (Endpoint, error) {
	var added Endpoint
	err := Update(path, func(c *Config) error {
		if c.Token == "" {
			return errors.New("还没配对")
		}
		e, err := normalize(raw, c.Endpoints)
		if err != nil {
			return err
		}
		added = e
		c.Endpoints = append([]Endpoint{e}, slicesDelete(c.Endpoints, e.URL)...)
		return nil
	})
	return added, err
}

func RemoveAddress(path, raw string) error {
	return Update(path, func(c *Config) error {
		e, err := normalize(raw, c.Endpoints)
		if err != nil {
			return err
		}
		c.Endpoints = slicesDelete(c.Endpoints, e.URL)
		return nil
	})
}

func slicesDelete(eps []Endpoint, u string) []Endpoint {
	out := eps[:0:0]
	for _, e := range eps {
		if e.URL != u {
			out = append(out, e)
		}
	}
	return out
}
