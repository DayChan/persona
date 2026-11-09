package github

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

const tailIssue = "/repos/example-org/example-repo/issues/7"
const tailPull = "/repos/example-org/example-repo/pulls/7"

var tailPaths = []string{tailIssue + "/comments", tailIssue + "/events", tailPull + "/reviews", tailPull + "/comments"}
var tailPrefixes = []string{"c", "e", "r", "rc"}

type tailRequest struct {
	path        string
	page        int
	conditional bool
}

// All four lists use ascending REST order and share numeric IDs across kinds.
// ETags depend only on the page body: appending can change Link without changing page 1.
type tailAPI struct {
	mu       sync.Mutex
	count    int
	links304 bool
	requests []tailRequest
}

func newTailAPI(t *testing.T, count int, links304 bool) (*Connector, *tailAPI) {
	t.Helper()
	a := &tailAPI{count: count, links304: links304}
	srv := httptest.NewServer(http.HandlerFunc(a.serveHTTP))
	t.Cleanup(srv.Close)
	c := New(Config{}, "", nil)
	c.api, c.token = srv.URL, "synthetic-token"
	c.status = connector.Status{State: connector.StateOK, Account: "example-user"}
	return c, a
}

func (a *tailAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == tailIssue {
		fmt.Fprint(w, `{"title":"Synthetic PR","state":"open","user":{"login":"example-user"},"pull_request":{}}`)
		return
	}
	if r.URL.Path == tailPull {
		fmt.Fprint(w, `{"head":{"ref":"topic"},"base":{"ref":"main"}}`)
		return
	}
	kind := slices.Index(tailPaths, r.URL.Path)
	if kind < 0 {
		http.NotFound(w, r)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 || r.URL.Query().Get("per_page") != "100" {
		http.Error(w, "bad pagination", 400)
		return
	}
	a.mu.Lock()
	count, links304 := a.count, a.links304
	a.requests = append(a.requests, tailRequest{r.URL.Path, page, r.Header.Get("If-None-Match") != ""})
	a.mu.Unlock()
	last := max(1, (count+99)/100)
	var links []string
	if page < last {
		links = append(links, fmt.Sprintf(`<https://api.example.com/repositories/42/list?page=%d&per_page=100>; rel="next"`, page+1))
	}
	if page > 1 {
		links = append(links, fmt.Sprintf(`<https://api.example.com/repositories/42/list?page=%d&per_page=100>; rel="prev"`, page-1))
	}
	if page != last {
		links = append(links, fmt.Sprintf(`<https://api.example.com/repositories/42/list?page=%d&per_page=100>; rel="last"`, last))
	}
	rows := make([]map[string]any, 0, 100)
	for n := (page-1)*100 + 1; n <= min(page*100, count); n++ {
		// Kind order deliberately differs from the order in which Thread reads the lists.
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(n*4+(3-kind)) * time.Second).Format(time.RFC3339)
		row := map[string]any{"id": n, "user": map[string]string{"login": "example-user"}, "body": fmt.Sprintf("Synthetic entry %d", n), "created_at": at, "updated_at": at}
		if kind == 1 {
			row["event"] = "labeled"
			row["actor"] = row["user"]
			row["label"] = map[string]string{"name": "example"}
		}
		if kind == 2 {
			row["state"] = "APPROVED"
			row["submitted_at"] = at
		}
		if kind == 3 {
			row["path"] = "example.go"
			row["line"] = 1
		}
		rows = append(rows, row)
	}
	body, _ := json.Marshal(rows)
	tag := fmt.Sprintf(`"%x"`, sha256.Sum256(body))
	w.Header().Set("ETag", tag)
	if links304 || r.Header.Get("If-None-Match") != tag {
		w.Header().Set("Link", strings.Join(links, ", "))
	}
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(body)
}

func (a *tailAPI) seen() []tailRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.requests)
}

func expireThreadCache(c *Connector) {
	c.etags.mu.Lock()
	defer c.etags.mu.Unlock()
	for _, e := range c.etags.m {
		e.at = time.Now().Add(-time.Hour)
	}
}

func tailIDs(messages []connector.ThreadMessage) []string {
	ids := make([]string, 0, len(messages))
	for _, m := range messages {
		ids = append(ids, m.ID)
	}
	return ids
}

func expectedTailIDs(first, last int) []string {
	var ids []string
	for n := first; n <= last; n++ {
		for k := len(tailPrefixes) - 1; k >= 0; k-- {
			ids = append(ids, tailPrefixes[k]+strconv.Itoa(n))
		}
	}
	return ids
}

func TestThreadNewestWindow(t *testing.T) {
	for _, tc := range []struct {
		count int
		pages []int
	}{
		{3, []int{1}}, {100, []int{1}},
		{500, []int{1, 5, 2, 3, 4}}, {501, []int{1, 6, 2, 3, 4, 5}},
		{600, []int{1, 6, 2, 3, 4, 5}}, {601, []int{1, 7, 2, 3, 4, 5, 6}},
		{10001, []int{1, 101, 96, 97, 98, 99, 100}},
	} {
		count := tc.count
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			c, api := newTailAPI(t, count, true)
			ctx := context.Background()
			page, err := c.Thread(ctx, "example-org/example-repo#7", "", 8)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := tailIDs(page.Messages), expectedTailIDs(count-1, count); !reflect.DeepEqual(got, want) {
				t.Fatalf("latest = %v, want %v", got, want)
			}
			requests := api.seen()
			for _, path := range tailPaths {
				var pages []int
				for _, r := range requests {
					if r.path == path {
						pages = append(pages, r.page)
					}
				}
				if !reflect.DeepEqual(pages, tc.pages) {
					t.Errorf("%s requested pages %v, want %v", path, pages, tc.pages)
				}
				rows, err := cachedList[struct {
					ID int `json:"id"`
				}](c, ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != min(count, 500) {
					t.Fatalf("%s retained %d rows", path, len(rows))
				}
				for i, row := range rows {
					if want := max(1, count-499) + i; row.ID != want {
						t.Fatalf("%s row %d = %d, want %d", path, i, row.ID, want)
					}
				}
			}
			// Traverse the complete retained timeline, asserting order and no repeated identities.
			all := slices.Clone(page.Messages)
			for page.Before != "" {
				if len(all) > 2000 {
					t.Fatal("cursor failed to terminate")
				}
				page, err = c.Thread(ctx, "example-org/example-repo#7", page.Before, 50)
				if err != nil {
					t.Fatal(err)
				}
				all = append(slices.Clone(page.Messages), all...)
			}
			if got, want := tailIDs(all), expectedTailIDs(max(1, count-499), count); !reflect.DeepEqual(got, want) {
				t.Fatalf("retained timeline differs: got %d messages, want %d", len(got), len(want))
			}
			if got := api.seen(); !reflect.DeepEqual(got, requests) {
				t.Fatal("fresh pagination refetched lists")
			}
			expireThreadCache(c)
			page, err = c.Thread(ctx, "example-org/example-repo#7", "", 8)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := tailIDs(page.Messages), expectedTailIDs(count-1, count); !reflect.DeepEqual(got, want) {
				t.Fatalf("304 latest = %v, want %v", got, want)
			}
			for _, r := range api.seen()[len(requests):] {
				if !r.conditional {
					t.Errorf("revalidation was unconditional: %+v", r)
				}
			}
		})
	}
}

func TestThreadGrowingTail(t *testing.T) {
	for _, links304 := range []bool{false, true} {
		for _, counts := range [][2]int{{500, 501}, {501, 601}, {600, 1001}, {601, 602}} {
			t.Run(fmt.Sprintf("%d-to-%d/304-links-%t", counts[0], counts[1], links304), func(t *testing.T) {
				c, api := newTailAPI(t, counts[0], links304)
				ctx := context.Background()
				if _, err := c.Thread(ctx, "example-org/example-repo#7", "", 8); err != nil {
					t.Fatal(err)
				}
				before := len(api.seen())
				api.mu.Lock()
				api.count = counts[1]
				api.mu.Unlock()
				// The old window remains fresh for the ordinary TTL.
				page, err := c.Thread(ctx, "example-org/example-repo#7", "", 8)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(tailIDs(page.Messages), expectedTailIDs(counts[0]-1, counts[0])) || len(api.seen()) != before {
					t.Fatal("fresh cache ignored TTL")
				}
				expireThreadCache(c)
				page, err = c.Thread(ctx, "example-org/example-repo#7", "", 8)
				if err != nil {
					t.Fatal(err)
				}
				if got, want := tailIDs(page.Messages), expectedTailIDs(counts[1]-1, counts[1]); !reflect.DeepEqual(got, want) {
					t.Fatalf("growing latest = %v, want %v", got, want)
				}
				for _, path := range tailPaths {
					requests := 0
					for _, r := range api.seen()[before:] {
						if r.path == path {
							requests++
						}
					}
					if requests > 8 {
						t.Errorf("%s made %d requests; bound is 8", path, requests)
					}
				}
				before = len(api.seen())
				if _, err := c.Thread(ctx, "example-org/example-repo#7", "", 8); err != nil {
					t.Fatal(err)
				}
				if len(api.seen()) != before {
					t.Fatal("refreshed pagination metadata was not cached")
				}
			})
		}
	}
}

func TestCachedListPaginationContracts(t *testing.T) {
	for _, tc := range []struct {
		name, link, body string
		want             int
		fail             bool
	}{
		{"empty", "", `[]`, 0, false},
		{"short", "", `[{"id":1}]`, 1, false},
		{"no-next", `<https://api.example.com/list?page=1>; rel="first"`, `[{"id":1}]`, 1, false},
		{"malformed", `not a link`, `[]`, 0, true},
		{"bad-page", `<https://api.example.com/list?page=-2>; rel="last"`, `[]`, 0, true},
		{"overflow", `<https://api.example.com/list?page=999999999999999999999>; rel="last"`, `[]`, 0, true},
		{"missing-last", `<https://api.example.com/list?page=2>; rel="next"`, `[]`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				if r.URL.Path != tailPaths[0] || r.URL.Query().Get("filter") != "example" {
					t.Error("original endpoint lost")
				}
				w.Header().Set("Link", tc.link)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c := New(Config{}, "", nil)
			c.api, c.token = srv.URL, "synthetic-token"
			rows, err := cachedList[threadComment](c, context.Background(), tailPaths[0]+"?filter=example")
			if (err != nil) != tc.fail || len(rows) != tc.want || hits != 1 {
				t.Fatalf("rows=%d error=%v requests=%d", len(rows), err, hits)
			}
		})
	}
}

func TestCachedListFailureRetryAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel-%t", cancel), func(t *testing.T) {
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			var fail atomic.Bool
			fail.Store(true)
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.URL.Query().Get("page") == "1" {
					w.Header().Set("Link", `<https://api.example.com/list?page=9>; rel="last"`)
					w.Header().Set("ETag", `"first"`)
					fmt.Fprint(w, `[]`)
					if cancel {
						stop()
					}
					return
				}
				if fail.Load() {
					http.Error(w, "synthetic failure", http.StatusServiceUnavailable)
					return
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				fmt.Fprintf(w, `[{"id":%d}]`, (page-1)*100+1)
			}))
			defer srv.Close()
			c := New(Config{}, "", nil)
			c.api, c.token = srv.URL, "synthetic-token"
			if _, err := cachedList[threadComment](c, ctx, tailPaths[0]); err == nil {
				t.Fatal("failed read returned a partial window")
			}
			if cancel {
				if hits.Load() != 1 {
					t.Fatalf("canceled traversal made %d requests", hits.Load())
				}
				return
			}
			fail.Store(false)
			rows, err := cachedList[threadComment](c, ctx, tailPaths[0])
			if err != nil || len(rows) != 6 || rows[0].ID != 301 || rows[5].ID != 801 {
				t.Fatalf("retry rows=%d error=%v", len(rows), err)
			}
			stop()
			if _, err := cachedList[threadComment](c, ctx, tailPaths[0]); !errors.Is(err, context.Canceled) {
				t.Fatalf("fresh cached canceled request = %v", err)
			}
		})
	}
}

func TestCachedPageMetadataAndValidators(t *testing.T) {
	var stage atomic.Int32
	var hits atomic.Int32
	first := `<https://api.example.com/list?page=6>; rel="last"`
	updated := `<https://api.example.com/list?page=7>; rel="last"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch stage.Load() {
		case 0:
			w.Header().Set("ETag", `"first"`)
			w.Header().Set("Link", first)
			fmt.Fprint(w, `[{"id":1}]`)
		case 1:
			if r.Header.Get("If-None-Match") != `"first"` {
				t.Error("lost original validator")
			}
			w.WriteHeader(http.StatusNotModified)
		case 2:
			w.Header().Set("ETag", `"updated"`)
			w.Header().Set("Link", updated)
			w.WriteHeader(http.StatusNotModified)
		case 3:
			if r.Header.Get("If-None-Match") != `"updated"` {
				t.Error("lost updated validator")
			}
			fmt.Fprint(w, `[{"id":2}]`)
		case 4:
			if r.Header.Get("If-None-Match") != "" {
				t.Error("sent stale validator for untagged response")
			}
			fmt.Fprint(w, `[{"id":3}]`)
		}
	}))
	defer srv.Close()
	c := New(Config{}, "", nil)
	c.api, c.token = srv.URL, "synthetic-token"
	for n := int32(0); n <= 4; n++ {
		stage.Store(n)
		expireThreadCache(c)
		var rows []threadComment
		link, err := c.cachedPage(context.Background(), tailPaths[0], &rows, false)
		wantLink, wantID := first, int64(1)
		if n == 2 {
			wantLink = updated
		}
		if n >= 3 {
			wantLink, wantID = "", int64(n-1)
		}
		if err != nil || link != wantLink || len(rows) != 1 || rows[0].ID != wantID {
			t.Fatalf("stage %d: link=%q rows=%v err=%v", n, link, rows, err)
		}
		if n < 3 {
			if got, err := c.cachedPage(context.Background(), tailPaths[0], &rows, false); err != nil || got != wantLink || hits.Load() != n+1 {
				t.Fatalf("fresh stage %d: link=%q requests=%d err=%v", n, got, hits.Load(), err)
			}
		}
	}
}

func TestThreadConcurrentCachedReads(t *testing.T) {
	c, _ := newTailAPI(t, 601, true)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			page, err := c.Thread(context.Background(), "example-org/example-repo#7", "", 8)
			if err != nil || !reflect.DeepEqual(tailIDs(page.Messages), expectedTailIDs(600, 601)) {
				t.Errorf("concurrent latest=%v err=%v", tailIDs(page.Messages), err)
			}
		}()
	}
	wg.Wait()
	expireThreadCache(c)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			page, err := c.Thread(context.Background(), "example-org/example-repo#7", "", 8)
			if err != nil || !reflect.DeepEqual(tailIDs(page.Messages), expectedTailIDs(600, 601)) {
				t.Errorf("concurrent revalidation=%v err=%v", tailIDs(page.Messages), err)
			}
		}()
	}
	wg.Wait()
}

func TestThreadGrowingTailWithStaggeredCacheExpiry(t *testing.T) {
	for _, links304 := range []bool{false, true} {
		for _, counts := range [][2]int{{501, 601}, {601, 602}} {
			t.Run(fmt.Sprintf("%d-to-%d/304-links-%t", counts[0], counts[1], links304), func(t *testing.T) {
				c, api := newTailAPI(t, counts[0], links304)
				ctx := context.Background()
				if _, err := c.Thread(ctx, "example-org/example-repo#7", "", 8); err != nil {
					t.Fatal(err)
				}
				api.mu.Lock()
				api.count = counts[1]
				api.mu.Unlock()
				// Page 1 was fetched first, so it naturally expires before its tail pages.
				c.etags.mu.Lock()
				for path, e := range c.etags.m {
					if strings.Contains(path, "?page=1&") {
						e.at = time.Now().Add(-time.Hour)
					}
				}
				c.etags.mu.Unlock()
				before := len(api.seen())
				page, err := c.Thread(ctx, "example-org/example-repo#7", "", 8)
				if err != nil {
					t.Fatal(err)
				}
				if got, want := tailIDs(page.Messages), expectedTailIDs(counts[1]-1, counts[1]); !reflect.DeepEqual(got, want) {
					t.Errorf("staggered latest=%v want=%v", got, want)
				}
				for _, path := range tailPaths {
					rows, err := cachedList[threadComment](c, ctx, path)
					if err != nil {
						t.Fatal(err)
					}
					if len(rows) != 500 {
						t.Errorf("%s retained %d rows, want 500", path, len(rows))
						continue
					}
					for i, row := range rows {
						if want := int64(counts[1] - 499 + i); row.ID != want {
							t.Errorf("%s retained row %d=%d want=%d", path, i, row.ID, want)
							break
						}
					}
					requests := 0
					for _, r := range api.seen()[before:] {
						if r.path == path {
							requests++
							if r.page > 1 && !r.conditional && r.page <= (counts[0]+99)/100 {
								t.Error("tail revalidation lost its validator")
							}
						}
					}
					if requests > 8 {
						t.Errorf("%s made %d requests; bound is 8", path, requests)
					}
				}
			})
		}
	}
}

// An old partial tail can finish after a newer discovery or a newer full tail.
// Neither response completion nor a delayed 304 may make that old body current.
func TestThreadLateTailResponse(t *testing.T) {
	for _, conditional := range []bool{false, true} {
		for _, publishNewTail := range []bool{false, true} {
			t.Run(fmt.Sprintf("conditional-%t/new-tail-%t", conditional, publishNewTail), func(t *testing.T) {
				api := &tailAPI{count: 501, links304: true}
				captured, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				finish := func() { once.Do(func() { close(release) }) }
				var hold atomic.Bool
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == tailPaths[0] && r.URL.Query().Get("page") == "6" && hold.CompareAndSwap(true, false) {
						old := httptest.NewRecorder()
						api.serveHTTP(old, r)
						wantCode := http.StatusOK
						if conditional {
							wantCode = http.StatusNotModified
						}
						if old.Code != wantCode {
							t.Errorf("held HTTP status=%d want=%d", old.Code, wantCode)
						}
						close(captured)
						<-release
						for key, values := range old.Header() {
							for _, value := range values {
								w.Header().Add(key, value)
							}
						}
						w.WriteHeader(old.Code)
						_, _ = w.Write(old.Body.Bytes())
						return
					}
					api.serveHTTP(w, r)
				}))
				defer srv.Close()
				defer finish()
				c := New(Config{}, "", nil)
				c.api, c.token = srv.URL, "synthetic-token"
				c.status = connector.Status{State: connector.StateOK, Account: "example-user"}
				ctx := context.Background()
				if conditional {
					if _, err := cachedList[threadComment](c, ctx, tailPaths[0]); err != nil {
						t.Fatal(err)
					}
					expireThreadCache(c)
				}
				hold.Store(true)
				type result struct {
					rows []threadComment
					err  error
				}
				oldRead := make(chan result, 1)
				go func() { rows, err := cachedList[threadComment](c, ctx, tailPaths[0]); oldRead <- result{rows, err} }()
				select {
				case <-captured:
				case <-time.After(5 * time.Second):
					t.Fatal("old tail was not captured")
				}
				api.mu.Lock()
				api.count = 601
				api.mu.Unlock()
				firstPath := tailPaths[0] + "?page=1&per_page=100"
				c.etags.mu.Lock()
				c.etags.m[firstPath].at = time.Now().Add(-time.Hour)
				c.etags.mu.Unlock()
				if publishNewTail {
					rows, err := cachedList[threadComment](c, ctx, tailPaths[0])
					if err != nil || len(rows) != 500 || rows[0].ID != 102 || rows[499].ID != 601 {
						t.Fatalf("newer window control rows=%d err=%v", len(rows), err)
					}
				} else {
					var first []threadComment
					if _, err := c.cachedPage(ctx, firstPath, &first, true); err != nil {
						t.Fatal(err)
					}
				}
				finish()
				select {
				case old := <-oldRead:
					if old.err != nil || len(old.rows) != 500 || old.rows[499].ID != 501 {
						t.Fatalf("old window control rows=%d err=%v", len(old.rows), old.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("old read did not finish")
				}
				// Exercise the actual caller after the late response has tried to publish.
				page, err := c.Thread(ctx, "example-org/example-repo#7", "", 8)
				if err != nil {
					t.Fatal(err)
				}
				if got, want := tailIDs(page.Messages), expectedTailIDs(600, 601); !reflect.DeepEqual(got, want) {
					t.Errorf("late-response latest=%v want=%v", got, want)
				}
				rows, err := cachedList[threadComment](c, ctx, tailPaths[0])
				if err != nil || len(rows) != 500 {
					t.Fatalf("late-response retained rows=%d err=%v", len(rows), err)
				}
				for i, row := range rows {
					if row.ID != int64(102+i) {
						t.Fatalf("late-response retained row %d=%d want=%d", i, row.ID, 102+i)
					}
				}
			})
		}
	}
}
