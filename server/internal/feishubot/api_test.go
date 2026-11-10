package feishubot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// retryAPIClient serves a token endpoint and h for the rest, counts hits/token fetches, and no-ops backoff. No t.Parallel in this package: callSleep is package-level.
func retryAPIClient(t *testing.T, h func(w http.ResponseWriter, r *http.Request, n *atomic.Int32)) (*Client, *httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var hits, tokens atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			tokens.Add(1)
			okJSON(w, map[string]any{"code": 0, "tenant_access_token": "tok", "expire": 7200})
			return
		}
		hits.Add(1)
		h(w, r, &hits)
	}))
	t.Cleanup(srv.Close)
	c := New("cli_x", "secret", srv.URL)
	prev := callSleep
	callSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { callSleep = prev })
	return c, srv, &hits, &tokens
}

func okJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
func botInfoOK(w http.ResponseWriter) {
	okJSON(w, map[string]any{"code": 0, "bot": map[string]any{"open_id": "o1"}})
}
func callOnce(c *Client) error { _, err := c.BotInfo(context.Background()); return err }

// Transient answers are retried until the third attempt; waits/tokens give the expected backoff waits and token fetches (a token-gone refresh spends no slot, and must not refresh again for later blips).
func TestCallRetriesTransientAnswers(t *testing.T) {
	cases := []struct {
		name   string
		fail   func(w http.ResponseWriter, n *atomic.Int32)
		waits  int // backoff waits before the success
		tokens int // token endpoint fetches
	}{
		{"503 html page", func(w http.ResponseWriter, _ *atomic.Int32) {
			http.Error(w, "bad gateway", http.StatusServiceUnavailable)
		}, 2, 1},
		{"http 429", func(w http.ResponseWriter, _ *atomic.Int32) {
			w.WriteHeader(http.StatusTooManyRequests)
			okJSON(w, map[string]any{"code": 0})
		}, 2, 1},
		{"rate limit code", func(w http.ResponseWriter, _ *atomic.Int32) {
			okJSON(w, map[string]any{"code": codeRateLimited})
		}, 2, 1},
		{"5xx json body", func(w http.ResponseWriter, _ *atomic.Int32) {
			w.WriteHeader(http.StatusBadGateway)
			okJSON(w, map[string]any{"code": 12345})
		}, 2, 1},
		{"token expired then 503", func(w http.ResponseWriter, n *atomic.Int32) {
			if n.Load() == 1 {
				okJSON(w, map[string]any{"code": 99991663})
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}, 1, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var slept []time.Duration
			c, _, hits, tokens := retryAPIClient(t, func(w http.ResponseWriter, r *http.Request, n *atomic.Int32) {
				if n.Load() < 3 {
					tc.fail(w, n)
				} else {
					botInfoOK(w)
				}
			})
			callSleep = func(_ context.Context, d time.Duration) error {
				if i := len(slept); i < len(callBackoff) && (d < callBackoff[i]*3/4 || d > callBackoff[i]*5/4) {
					t.Errorf("wait %d = %v, want +/-25%% of %v", i, d, callBackoff[i])
				}
				slept = append(slept, d)
				return nil
			}
			info, err := c.BotInfo(context.Background())
			if err != nil || info.OpenID != "o1" || hits.Load() != 3 || len(slept) != tc.waits || tokens.Load() != int32(tc.tokens) {
				t.Fatalf("info=%+v hits=%d waits=%d tokens=%d err=%v",
					info, hits.Load(), len(slept), tokens.Load(), err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type errBody struct{ error }

func (b errBody) Read([]byte) (int, error) { return 0, b.error }
func (b errBody) Close() error             { return nil }

// A failing connection and a body read that fails both use all three attempts and return the last error, for a JSON
// call and for a download alike.
func TestCallTransportFailuresBounded(t *testing.T) {
	fails := map[string]func() (*http.Response, error){
		"do error": func() (*http.Response, error) { return nil, errors.New("connection reset by peer") },
		"body read error": func() (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: errBody{errors.New("unexpected EOF")}}, nil
		},
	}
	calls := map[string]func(*Client) error{
		"call":     callOnce,
		"download": func(c *Client) error { _, _, err := downloadOnce(context.Background(), c); return err },
	}
	for name, fail := range fails {
		for op, do := range calls {
			t.Run(name+"/"+op, func(t *testing.T) {
				c, srv, hits, _ := retryAPIClient(t, func(http.ResponseWriter, *http.Request, *atomic.Int32) {})
				c.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
						return srv.Client().Transport.RoundTrip(req)
					}
					hits.Add(1)
					return fail()
				})}
				err := do(c)
				if err == nil || hits.Load() != callMaxAttempts ||
					!strings.Contains(err.Error(), "reset") && !strings.Contains(err.Error(), "EOF") {
					t.Fatalf("err=%v hits=%d", err, hits.Load())
				}
			})
		}
	}
}

// A cancelled context returns at once: before the request goes out, and while the backoff waits.
func TestCallCancelledContextDoesNotRetry(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		c, _, hits, _ := retryAPIClient(t, func(http.ResponseWriter, *http.Request, *atomic.Int32) {})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := c.BotInfo(ctx); !errors.Is(err, context.Canceled) || hits.Load() != 0 {
			t.Fatalf("err=%v hits=%d", err, hits.Load())
		}
	})
	t.Run("during backoff", func(t *testing.T) {
		c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		ctx, cancel := context.WithCancel(context.Background())
		callSleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
		if _, err := c.BotInfo(ctx); !errors.Is(err, context.Canceled) || hits.Load() != 1 {
			t.Fatalf("err=%v hits=%d", err, hits.Load())
		}
	})
}

// A 4xx business error is not a transport problem: one attempt, APIError with its code.
func TestCallBusinessErrorNotRetried(t *testing.T) {
	c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
		w.WriteHeader(http.StatusBadRequest)
		okJSON(w, map[string]any{"code": 230002})
	})
	var apiErr *APIError
	if err := callOnce(c); !errors.As(err, &apiErr) || apiErr.Code != 230002 || hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}

// Token fetch failures use the bounded backoff (a uuid-less POST retries pre-send); after tokenGone a failed forced fetch keeps forcing, so the POST lands once on the new token.
func TestCallTokenFetchRetries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		post        bool
		plainFails  int
		forcedFails int32
		fetches     int32
		hits        int32
		landed      int32
		sleeps      int32
	}{
		{"get recovers after a fetch failure", false, 1, 0, 2, 1, 1, 1},
		{"uuid-less post retries before its body lands", true, 1, 0, 2, 1, 1, 1},
		{"all fetches fail returns the token error", false, 3, 0, 3, 0, 0, 2},
		{"get retries the forced fetch with the new token", false, 0, 1, 3, 2, 1, 1},
		{"uuid-less post lands once with the new token", true, 0, 1, 3, 2, 1, 1},
		{"uuid-less post forced fetches all fail", true, 0, 2, 3, 1, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n, forcedN, hits, landed, slept, goneN atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					fail := goneN.Load() > 0 && forcedN.Add(1) <= tc.forcedFails || goneN.Load() == 0 && n.Add(1) <= int32(tc.plainFails)
					if fail {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					okJSON(w, map[string]any{"code": 0, "tenant_access_token": map[bool]string{true: "tok2", false: "tok1"}[goneN.Load() > 0], "expire": 7200})
					return
				}
				hits.Add(1)
				if tc.plainFails == 0 && r.Header.Get("Authorization") != "Bearer tok2" {
					goneN.Store(1) // rejected at the auth layer; the body never lands
					okJSON(w, map[string]any{"code": 99991663})
					return
				}
				landed.Add(1)
				okJSON(w, map[string]any{"code": 0, "data": map[string]any{"message_id": "m1"}, "bot": map[string]any{"open_id": "o1"}})
			}))
			defer srv.Close()
			c := New("cli_x", "secret", srv.URL)
			prev := callSleep
			callSleep = func(context.Context, time.Duration) error { slept.Add(1); return nil }
			defer func() { callSleep = prev }()
			var err error
			if tc.post {
				_, err = c.Send(context.Background(), "chat_id", "c1", TypeText, TextContent("hi"), "")
			} else {
				_, err = c.BotInfo(context.Background())
			}
			wantErr := tc.landed == 0
			if (err != nil) != wantErr || n.Load()+forcedN.Load() != tc.fetches || hits.Load() != tc.hits ||
				landed.Load() != tc.landed || slept.Load() != tc.sleeps || wantErr && !strings.Contains(err.Error(), "token") {
				t.Fatalf("err=%v fetches=%d hits=%d landed=%d sleeps=%d", err, n.Load()+forcedN.Load(), hits.Load(), landed.Load(), slept.Load())
			}
		})
	}
}

// 429 honors a Retry-After delta/date on the HTTP status or body rate-limit code; past the cap it gives up at once.
func TestCallRetryAfter(t *testing.T) {
	cases := []struct {
		name    string
		after   string // empty sends an HTTP date 10s out
		status  int
		code    int
		hits    int32
		minWait time.Duration
	}{
		{"delta seconds on 429", "5", http.StatusTooManyRequests, 0, 3, 5 * time.Second},
		{"delta seconds on body code", "2", http.StatusOK, codeRateLimited, 3, 2 * time.Second},
		{"http date on 429", "", http.StatusTooManyRequests, 0, 3, 8 * time.Second},
		{"retry-after exactly at the cap", strconv.Itoa(int(callMaxRetryWait / time.Second)), http.StatusTooManyRequests, 0, 3, callMaxRetryWait},
		{"past the cap gives up", strconv.Itoa(int(callMaxRetryWait/time.Second) + 30), http.StatusTooManyRequests, codeRateLimited, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var slept []time.Duration
			after := tc.after
			if after == "" {
				after = time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
			}
			c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, n *atomic.Int32) {
				if n.Load() < 3 {
					w.Header().Set("Retry-After", after)
					w.WriteHeader(tc.status)
					okJSON(w, map[string]any{"code": tc.code})
					return
				}
				botInfoOK(w)
			})
			callSleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
			_, err := c.BotInfo(context.Background())
			wantErr := tc.hits == 1
			if (err != nil) != wantErr || hits.Load() != tc.hits || len(slept) != int(tc.hits)-1 ||
				wantErr && errorCode(err) != codeRateLimited {
				t.Fatalf("err=%v wantErr=%v hits=%d sleeps=%v", err, wantErr, hits.Load(), slept)
			}
			for _, d := range slept {
				if d < tc.minWait || tc.after != "" && d != tc.minWait {
					t.Fatalf("sleep %v, want %v", d, tc.minWait)
				}
			}
		})
	}
}

// A POST is retried only when it carries a uuid, and every attempt keeps the same uuid.
func TestSendUUIDRetryPolicy(t *testing.T) {
	cases := []struct{ name, uuid string }{{"without uuid", ""}, {"with uuid", "send:c1:answer"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, r *http.Request, n *atomic.Int32) {
				var in map[string]any
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &in)
				if u, _ := in["uuid"].(string); u != tc.uuid {
					t.Errorf("uuid=%q, want %q", u, tc.uuid)
				}
				switch {
				case tc.uuid == "": // uuid-less POST: one rate-limit answer, no retry
					okJSON(w, map[string]any{"code": codeRateLimited})
				case n.Load() == 1:
					w.WriteHeader(http.StatusServiceUnavailable)
				default:
					okJSON(w, map[string]any{"code": 0, "data": map[string]any{"message_id": "m1"}})
				}
			})
			id, err := c.Send(context.Background(), "chat_id", "c1", TypeText, TextContent("hi"), tc.uuid)
			if tc.uuid == "" {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Code != codeRateLimited || hits.Load() != 1 {
					t.Fatalf("err=%v hits=%d", err, hits.Load())
				}
				return
			}
			if err != nil || id != "m1" || hits.Load() != 2 {
				t.Fatalf("id=%q err=%v hits=%d", id, err, hits.Load())
			}
		})
	}
}

// throttled writes the open platform's throttle answer as its frequency-control guide shows it: HTTP 429 (400 on some
// older APIs), code 99991400, and x-ogw-ratelimit-reset, the seconds until the window resets.
// https://open.feishu.cn/document/server-docs/api-call-guide/frequency-control
func throttled(w http.ResponseWriter, status int, reset string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-ogw-ratelimit-limit", "100")
	w.Header().Set("x-ogw-ratelimit-reset", reset)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"code":99991400,"msg":"request trigger frequency limit"}`)
}

// recordSleeps makes callSleep note each wait and return at once (after retryAPIClient, whose cleanup restores it).
func recordSleeps() *[]time.Duration {
	var slept []time.Duration
	callSleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	return &slept
}

func downloadOnce(ctx context.Context, c *Client) ([]byte, string, error) {
	return c.Resource(ctx, "om_example", "img_example", "image", 1024)
}

// A throttled call waits the seconds x-ogw-ratelimit-reset names (or a longer Retry-After), on the 429 and on the older
// APIs' 400 alike. The header counts only on a throttled answer, and a reset past the cap ends the retries at once.
func TestCallWaitsOutTheThrottleReset(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(w http.ResponseWriter)
		wait   time.Duration // the wait before the retry; 0: the jittered backoff
		hits   int32
	}{
		{"429 with the reset header", func(w http.ResponseWriter) { throttled(w, http.StatusTooManyRequests, "2") }, 2 * time.Second, 2},
		{"an older API's 400", func(w http.ResponseWriter) { throttled(w, http.StatusBadRequest, "3") }, 3 * time.Second, 2},
		{"a longer Retry-After wins", func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "5")
			throttled(w, http.StatusTooManyRequests, "2")
		}, 5 * time.Second, 2},
		{"a server failure is no throttle", func(w http.ResponseWriter) {
			w.Header().Set("x-ogw-ratelimit-reset", "40")
			w.WriteHeader(http.StatusServiceUnavailable)
		}, 0, 2},
		{"a reset past the cap gives up", func(w http.ResponseWriter) {
			throttled(w, http.StatusTooManyRequests, strconv.Itoa(int(callMaxRetryWait/time.Second)+1))
		}, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, n *atomic.Int32) {
				if n.Load() == 1 {
					tc.answer(w)
					return
				}
				botInfoOK(w)
			})
			slept := recordSleeps()
			err := callOnce(c)
			if hits.Load() != tc.hits {
				t.Fatalf("hits=%d err=%v sleeps=%v", hits.Load(), err, *slept)
			}
			if tc.hits == 1 {
				if !RateLimited(err) || len(*slept) != 0 {
					t.Fatalf("err=%v sleeps=%v", err, *slept)
				}
				return
			}
			if err != nil || len(*slept) != 1 || tc.wait > 0 && (*slept)[0] != tc.wait || tc.wait == 0 && (*slept)[0] > callBackoff[0]*5/4 {
				t.Fatalf("err=%v sleeps=%v, want one of %v", err, *slept, tc.wait)
			}
		})
	}
}

// The per-chat send limit (230020: 5 messages a second into one chat or to one user) is waited out, a second at least,
// on a keyed send or reply, whose uuid lands the message once. An unkeyed send is never repeated, and a card update
// that meets the limit is left to its caller, which backs off and sends a newer card.
// https://open.feishu.cn/document/server-docs/im-v1/message/create
func TestPerChatLimitRetriedOnKeyedSends(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		do   func(c *Client) error
		hits int32
	}{
		{"keyed send", func(c *Client) error {
			_, err := c.Send(ctx, "chat_id", "oc_example", TypeText, TextContent("hi"), "send:oc_example:answer")
			return err
		}, 2},
		{"keyed reply", func(c *Client) error {
			_, err := c.Reply(ctx, "om_example", TypeText, TextContent("hi"), "reply:om_example:answer", false)
			return err
		}, 2},
		{"unkeyed send", func(c *Client) error {
			_, err := c.Send(ctx, "chat_id", "oc_example", TypeText, TextContent("hi"), "")
			return err
		}, 1},
		{"card update", func(c *Client) error { return c.UpdateCard(ctx, "om_example", `{"elements":[]}`) }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, n *atomic.Int32) {
				if n.Load() == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"code":230020,"msg":"This operation triggers the frequency limit."}`)
					return
				}
				okJSON(w, map[string]any{"code": 0, "data": map[string]any{"message_id": "om_example_sent"}})
			})
			slept := recordSleeps()
			err := tc.do(c)
			if hits.Load() != tc.hits {
				t.Fatalf("hits=%d err=%v sleeps=%v", hits.Load(), err, *slept)
			}
			if tc.hits == 1 {
				if errorCode(err) != 230020 || !RateLimited(err) || len(*slept) != 0 {
					t.Fatalf("err=%v sleeps=%v", err, *slept)
				}
				return
			}
			if err != nil || len(*slept) != 1 || (*slept)[0] < time.Second {
				t.Fatalf("err=%v sleeps=%v, want one of at least 1s", err, *slept)
			}
		})
	}
}

// A card update or an agent message's append that is throttled is tried again on the short backoff only, never after
// the reset the server names: the progress tick makes these calls under the run's progress lock, and a wait there would
// hold back the run's answer. Throttled on every try, the call ends in the rate-limit error its caller backs off on.
func TestMessageUpdatesDoNotWaitOutTheReset(t *testing.T) {
	ctx := context.Background()
	for op, do := range map[string]func(*Client) error{
		"card update": func(c *Client) error { return c.UpdateCard(ctx, "om_example", `{"elements":[]}`) },
		"cot append": func(c *Client) error {
			return c.CotAppend(ctx, "cot_example", "om_example", CotParagraph("p1", "working"))
		},
	} {
		for _, tc := range []struct {
			name      string
			throttles int32 // answers throttled before an OK
		}{
			{"once", 1},
			{"every try", callMaxAttempts},
		} {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, n *atomic.Int32) {
					if n.Load() <= tc.throttles {
						w.Header().Set("Retry-After", "3")
						throttled(w, http.StatusTooManyRequests, "2")
						return
					}
					okJSON(w, map[string]any{"code": 0, "data": map[string]any{}})
				})
				slept := recordSleeps()
				err := do(c)
				for _, d := range *slept {
					if d > callBackoff[len(callBackoff)-1]*5/4 {
						t.Fatalf("slept %v, want the short backoff only; sleeps=%v", d, *slept)
					}
				}
				if tc.throttles < callMaxAttempts {
					if err != nil || hits.Load() != tc.throttles+1 || len(*slept) != int(tc.throttles) {
						t.Fatalf("err=%v hits=%d sleeps=%v", err, hits.Load(), *slept)
					}
					return
				}
				if !RateLimited(err) || hits.Load() != callMaxAttempts || len(*slept) != callMaxAttempts-1 {
					t.Fatalf("err=%v rateLimited=%v hits=%d sleeps=%v", err, RateLimited(err), hits.Load(), *slept)
				}
			})
		}
	}
}

// A wait the server asks for that would outlast the caller's deadline is not slept: the call returns the rate-limit
// error at once, so a caller that backs off on RateLimited still can, rather than a bare deadline error later.
func TestWaitPastTheDeadlineReturnsTheRateLimit(t *testing.T) {
	for op, do := range map[string]func(*Client, context.Context) error{
		"call":     func(c *Client, ctx context.Context) error { _, err := c.BotInfo(ctx); return err },
		"download": func(c *Client, ctx context.Context) error { _, _, err := downloadOnce(ctx, c); return err },
	} {
		t.Run(op, func(t *testing.T) {
			c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
				throttled(w, http.StatusTooManyRequests, "30")
			})
			slept := recordSleeps()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := do(c, ctx)
			if !RateLimited(err) || errors.Is(err, context.DeadlineExceeded) || hits.Load() != 1 || len(*slept) != 0 {
				t.Fatalf("err=%v rateLimited=%v hits=%d sleeps=%v", err, RateLimited(err), hits.Load(), *slept)
			}
		})
	}
}

// A throttled or failed download is tried again like any GET: a 429 waits out its reset, a 503 the backoff, and the
// file comes on the third attempt with the one token.
// https://open.feishu.cn/document/server-docs/im-v1/message/get-2
func TestResourceRetriesThrottledDownloads(t *testing.T) {
	c, _, hits, tokens := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, n *atomic.Int32) {
		switch n.Load() {
		case 1:
			throttled(w, http.StatusTooManyRequests, "2")
		case 2:
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, "png bytes")
		}
	})
	slept := recordSleeps()
	b, ct, err := downloadOnce(context.Background(), c)
	if err != nil || string(b) != "png bytes" || ct != "image/png" || hits.Load() != 3 || tokens.Load() != 1 ||
		len(*slept) != 2 || (*slept)[0] != 2*time.Second || (*slept)[1] > callBackoff[1]*5/4 {
		t.Fatalf("b=%q ct=%q err=%v hits=%d tokens=%d sleeps=%v", b, ct, err, hits.Load(), tokens.Load(), *slept)
	}
}

// A download that keeps failing ends after callMaxAttempts with the last answer's error; Feishu's word on the file (not
// in that message) is not asked again.
func TestResourceGivesUp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(w http.ResponseWriter)
		hits   int32
		code   int
	}{
		{"keeps throttling", func(w http.ResponseWriter) { throttled(w, http.StatusTooManyRequests, "1") }, callMaxAttempts, codeRateLimited},
		{"keeps failing", func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) }, callMaxAttempts, http.StatusBadGateway},
		{"file not in the message", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":234003,"msg":"File not in message."}`)
		}, 1, 234003},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, _ *atomic.Int32) { tc.answer(w) })
			slept := recordSleeps()
			b, _, err := downloadOnce(context.Background(), c)
			if b != nil || errorCode(err) != tc.code || hits.Load() != tc.hits || len(*slept) != int(tc.hits)-1 {
				t.Fatalf("b=%q err=%v hits=%d sleeps=%v", b, err, hits.Load(), *slept)
			}
		})
	}
}

// A file whose length is said up front to be over the limit is refused before its body is read.
func TestResourceTooLargeIsRefusedUnread(t *testing.T) {
	var wrote atomic.Int64
	c, _, _, _ := retryAPIClient(t, func(w http.ResponseWriter, r *http.Request, _ *atomic.Int32) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 4; i++ {
			n, err := w.Write(make([]byte, 1024))
			wrote.Add(int64(n))
			if err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	})
	_, _, err := c.Resource(context.Background(), "om_x", "file_x", "file", 1024)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err %v", err)
	}
}

// A JSON file the user sent comes back from a 200 download labeled application/json: it is the file, not an error,
// even when it looks like an API error answer (a saved error response, say), and it is not downloaded twice.
func TestResourceJSONFileIsTheFile(t *testing.T) {
	for _, body := range []string{
		`{"name":"settings","debug":true}`,
		`[{"code":7,"msg":"x"}]`,
		`{"code":7,"name":"no msg"}`,
		`{"code":"E1","msg":"a string code"}`,
		`{"code":0,"msg":"ok"}`,
		`{"code":40001,"msg":"invalid token"}`,
		`{"code":99991663,"msg":"token expired"}`,
	} {
		t.Run(body, func(t *testing.T) {
			c, _, hits, tokens := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			})
			b, ct, err := c.Resource(context.Background(), "om_x", "file_x", "file", 1024)
			if err != nil || string(b) != body || ct != "application/json" || hits.Load() != 1 || tokens.Load() != 1 {
				t.Fatalf("b=%q ct=%q err=%v hits=%d tokens=%d", b, ct, err, hits.Load(), tokens.Load())
			}
		})
	}
}

// A JSON file over the limit is refused as too large, like any other file.
func TestResourceJSONFileTooLarge(t *testing.T) {
	c, _, _, _ := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"pad":"`+strings.Repeat("x", 2048)+`"}`)
	})
	if _, _, err := c.Resource(context.Background(), "om_x", "file_x", "file", 1024); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err %v", err)
	}
}

// A token-gone answer (non-200) is retried once with a fresh token; a second one is the error.
func TestResourceTokenGoneIsRetriedOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gone  int32 // how many answers are token-gone before the file
		isErr bool
	}{{"fresh token fixes it", 1, false}, {"still gone after one retry", 2, true}} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, hits, tokens := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, n *atomic.Int32) {
				if n.Load() <= tc.gone {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					okJSON(w, map[string]any{"code": 99991663, "msg": "token expired"})
					return
				}
				_, _ = io.WriteString(w, "file bytes")
			})
			b, _, err := c.Resource(context.Background(), "om_x", "file_x", "file", 1024)
			if hits.Load() != 2 || tokens.Load() != 2 {
				t.Fatalf("hits=%d tokens=%d", hits.Load(), tokens.Load())
			}
			if tc.isErr {
				if errorCode(err) != 99991663 || b != nil {
					t.Fatalf("b=%q err=%v", b, err)
				}
				return
			}
			if err != nil || string(b) != "file bytes" {
				t.Fatalf("b=%q err=%v", b, err)
			}
		})
	}
}
