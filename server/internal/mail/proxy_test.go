package mail

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeConnectProxy is a minimal HTTP CONNECT proxy that counts tunnels and checks basic auth.
func fakeConnectProxy(t *testing.T, wantAuth string) (addr string, tunnels *int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	tunnels = new(int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				if wantAuth != "" && req.Header.Get("Proxy-Authorization") != wantAuth {
					io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
					return
				}
				up, err := net.Dial("tcp", req.Host)
				if err != nil {
					io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer up.Close()
				atomic.AddInt32(tunnels, 1)
				io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				go io.Copy(up, c)
				io.Copy(c, up)
			}(c)
		}
	}()
	return ln.Addr().String(), tunnels
}

func TestGmailThroughProxy(t *testing.T) {
	srv := newTestIMAP(t)
	srv.appendMessage(t, "INBOX", rawMessage("via proxy", "a@example.com", "hello"))
	s := newServiceWithAccount(t, srv)
	// "user:pass" in base64.
	addr, tunnels := fakeConnectProxy(t, "Basic dXNlcjpwYXNz")
	if err := s.SetGoogleProxy("http://user:pass@" + addr); err != nil {
		t.Fatal(err)
	}

	accts, _ := s.ListAccounts()
	id := accts[0].ID
	// Only Gmail accounts use the proxy.
	if _, err := s.ListMessages(context.Background(), ListOptions{AccountID: id}); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(tunnels); n != 0 {
		t.Fatalf("non-Gmail account used the proxy %d times", n)
	}

	s.mu.Lock()
	s.accts[id].Provider = ProviderGmail
	s.mu.Unlock()
	s.dropConn(id)
	res, err := s.ListMessages(context.Background(), ListOptions{AccountID: id})
	if err != nil {
		t.Fatalf("list through proxy: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Subject != "via proxy" {
		t.Fatalf("messages = %+v", res.Messages)
	}
	if n := atomic.LoadInt32(tunnels); n == 0 {
		t.Fatal("Gmail account did not use the proxy")
	}

	// Wrong credentials surface as a proxy error.
	_ = s.SetGoogleProxy("http://user:nope@" + addr)
	s.dropConn(id)
	if _, err := s.ListMessages(context.Background(), ListOptions{AccountID: id}); err == nil || !strings.Contains(err.Error(), "407") {
		t.Fatalf("bad proxy auth: %v", err)
	}
}

func TestSetGoogleProxyValidates(t *testing.T) {
	s, _ := newOAuthService(t)
	for _, bad := range []string{"socks5://127.0.0.1:1080", "127.0.0.1:8080", "http://"} {
		if err := s.SetGoogleProxy(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if err := s.SetGoogleProxy(""); err != nil || s.googleProxy != nil {
		t.Fatalf("empty proxy: %v", err)
	}
	// dialTCP dials googleProxy.Host as it is: a proxy set without a port must come with 80.
	if err := s.SetGoogleProxy("http://proxy.example"); err != nil || s.googleProxy == nil || s.googleProxy.Host != "proxy.example:80" {
		t.Fatalf("proxy without a port: %v %v", s.googleProxy, err)
	}
}

// A proxy that sends the server's first bytes in the same packet as its 200 must not lose them.
func TestDialTCPKeepsBytesAfterConnectReply(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if _, err := http.ReadRequest(bufio.NewReader(c)); err != nil {
			return
		}
		io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n* OK IMAP4rev1 ready\r\n")
	}()
	u, _ := url.Parse("http://" + ln.Addr().String())
	c, err := dialTCP(context.Background(), u, "imap.example.com:993")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	line, err := bufio.NewReader(c).ReadString(10)
	if err != nil || line != "* OK IMAP4rev1 ready\r\n" {
		t.Fatalf("greeting = %q, %v", line, err)
	}
}

func TestServerAddr(t *testing.T) {
	for _, c := range []struct{ host, want string }{
		{"imap.example.com", "imap.example.com:993"},
		{"192.0.2.1", "192.0.2.1:993"},
		{"2001:db8::1", "[2001:db8::1]:993"},
		{"[2001:db8::1]", "[2001:db8::1]:993"},
	} {
		if got := serverAddr(c.host, 993); got != c.want {
			t.Errorf("serverAddr(%q, 993) = %q, want %q", c.host, got, c.want)
		}
	}
}

// Every IMAP and SMTP dial connects to a mail server set as an IPv6 literal, with or without brackets.
func TestDialMailServerAtIPv6Literal(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	reached := make(chan struct{}, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			reached <- struct{}{}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()
	for _, host := range []string{"::1", "[::1]"} {
		a := srv.testAccount()
		a.IMAPHost, a.IMAPPort, a.SMTPHost, a.SMTPPort = host, port, host, port
		dials := []struct {
			name string
			dial func() error
		}{
			{"IMAP", func() error {
				c, err := dialAndLogin(a, "", nil)
				if c != nil {
					c.Close()
				}
				return err
			}},
			{"raw IMAP", func() error {
				c, err := s.rawDial(ctx, a)
				if c != nil {
					c.conn.Close()
				}
				return err
			}},
			{"SMTP check", func() error { return s.checkSMTP(ctx, a) }},
			{"SMTP send", func() error { return s.smtpSend(ctx, a, a.Email, []string{"li@example.com"}, []byte("x")) }},
		}
		for _, d := range dials {
			for len(reached) > 0 {
				<-reached
			}
			err := d.dial()
			select {
			case <-reached:
			case <-time.After(2 * time.Second):
				t.Errorf("%s at %q never reached the server: %v", d.name, host, err)
			}
		}
	}
}
