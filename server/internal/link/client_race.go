package link

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

// race tries the candidates best first, starting the next one stagger after the previous, and keeps the first that
// works; drop closes any that succeed after it.
func race[T any](ctx context.Context, eps []Endpoint, try func(context.Context, Endpoint) (T, error), drop func(T)) (T, Endpoint, error) {
	eps = ranked(eps)
	var zero T
	if len(eps) == 0 {
		return zero, Endpoint{}, errors.New("没有 hub 的候选地址：重新配对，或用 persona-link address add 加一个")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		v   T
		ep  Endpoint
		err error
	}
	results := make(chan result, len(eps))
	for i, ep := range eps {
		go func() {
			select {
			case <-time.After(time.Duration(i) * stagger):
			case <-ctx.Done():
				results <- result{ep: ep, err: ctx.Err()}
				return
			}
			actx, acancel := context.WithTimeout(ctx, dialTimeout)
			defer acancel()
			v, err := try(actx, ep)
			results <- result{v, ep, err}
		}()
	}
	var errs []error
	for left := len(eps); left > 0; left-- {
		r := <-results
		if r.err == nil {
			go func(n int) {
				for ; n > 0; n-- {
					if late := <-results; late.err == nil {
						drop(late.v)
					}
				}
			}(left - 1)
			return r.v, r.ep, nil
		}
		if !errors.Is(r.err, context.Canceled) {
			errs = append(errs, &attemptError{r.ep, r.err})
		}
	}
	if len(errs) == 0 { // every attempt was called off: ctx ended (errors.Join of nothing would be no error)
		return zero, Endpoint{}, cmp.Or(ctx.Err(), context.Canceled)
	}
	return zero, Endpoint{}, errors.Join(errs...)
}

// attemptError is how one candidate failed in a race.
type attemptError struct {
	ep  Endpoint
	err error
}

func (e *attemptError) Error() string { return e.ep.URL + ": " + e.err.Error() }
func (e *attemptError) Unwrap() error { return e.err }

var kindNames = map[string]string{"lan": "内网", "ipv6": "公网 IPv6", "name": "域名", "ipv4": "公网 IPv4"}

// summary says in one line why the link failed, for the menu bar app: per candidate when no candidate worked.
func summary(err error) string {
	if err == nil {
		return ""
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var parts []string
		for _, e := range joined.Unwrap() {
			parts = append(parts, summary(e))
		}
		return strings.Join(parts, "；")
	}
	var ae *attemptError
	if errors.As(err, &ae) {
		host := ae.ep.URL
		if u, err := url.Parse(ae.ep.URL); err == nil {
			host = u.Host + u.Path // a reverse proxy's path says which hub
		}
		return kindNames[ae.ep.Kind] + " " + host + "：" + describe(ae.err)
	}
	switch websocket.CloseStatus(err) {
	case websocket.StatusGoingAway:
		return "hub 重启了"
	case CloseRefused:
		return "hub 不认这台的 hello（机器 id 和凭据对不上）"
	}
	return err.Error()
}

func describe(err error) string {
	var he *httpError
	var ne net.Error
	switch {
	case errors.As(err, &he):
		return "hub 回了 " + he.Error()
	case errors.Is(err, errPin):
		return errPin.Error()
	case errors.As(err, new(x509.HostnameError)):
		return "证书上的域名对不上（系统证书校验）"
	case errors.As(err, new(x509.UnknownAuthorityError)), errors.As(err, new(*tls.CertificateVerificationError)):
		return "证书不受系统信任（系统证书校验）"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "超时"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "拒绝连接"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EADDRNOTAVAIL):
		return "不可达"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "域名解析不了"
	}
	return err.Error()
}
