package mangalib

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.Handler, opts ...Option) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	base := []Option{WithBaseURL(srv.URL + "/api"), WithAPIInterval(0), WithBackoffBase(time.Millisecond)}
	return NewClient(append(base, opts...)...), srv
}

func TestSearchSendsRequiredHeadersAndDecodes(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checks := map[string]string{
			"Site-Id":       "1",
			"Referer":       SiteReferer,
			"Authorization": "Bearer secret",
		}
		for h, want := range checks {
			if got := r.Header.Get(h); got != want {
				t.Errorf("header %s = %q, want %q", h, got, want)
			}
		}
		if got := r.URL.Query().Get("q"); got != "berserk" {
			t.Errorf("q = %q", got)
		}
		if got := r.URL.Query()["site_id[]"]; !slices.Equal(got, []string{"1"}) {
			t.Errorf("site_id[] = %v", got)
		}
		if got := r.URL.Query().Get("page"); got != "2" {
			t.Errorf("page = %q", got)
		}
		fmt.Fprint(w, `{"data":[{"id":1,"slug_url":"1--a","rus_name":"А","name":"A"}],"meta":{"has_next_page":true}}`)
	}), WithToken("secret"))

	res, err := c.Search(context.Background(), "berserk", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].SlugURL != "1--a" || res.Items[0].DisplayName() != "А" || !res.HasNext {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestRetriesOnTooManyRequests(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))

	if _, err := c.Chapters(context.Background(), "1--a"); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}), WithMaxRetries(2))

	_, err := c.Chapters(context.Background(), "1--a")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadGateway {
		t.Fatalf("err = %v, want APIError 502", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestAPIErrors(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantNotFound bool
		wantInMsg    string
	}{
		{"toast 404", http.StatusNotFound, `{"data":{"toast":{"type":"silent","message":"Not Found"}}}`, true, "Not Found"},
		{"message 401", http.StatusUnauthorized, `{"message":"Unauthenticated."}`, false, "MANGALIB_TOKEN"},
		{"html 403", http.StatusForbidden, `<html>ddos-guard</html>`, false, "Forbidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			_, err := c.Manga(context.Background(), "1--a")
			if err == nil {
				t.Fatal("expected error")
			}
			if got := errors.Is(err, ErrNotFound); got != tt.wantNotFound {
				t.Errorf("errors.Is(ErrNotFound) = %v, want %v", got, tt.wantNotFound)
			}
			if !strings.Contains(err.Error(), tt.wantInMsg) {
				t.Errorf("error %q does not contain %q", err, tt.wantInMsg)
			}
		})
	}
}

func TestMangaWithoutIDIsNotFound(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":{"toast":{"message":"Not Found"}}}`)
	}))
	if _, err := c.Manga(context.Background(), "1--a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestChapterPagesBranchParam(t *testing.T) {
	tests := []struct {
		branch int64
		want   string
	}{
		{0, ""},
		{25159, "25159"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.branch), func(t *testing.T) {
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.URL.Path != "/api/manga/1--a/chapter" || q.Get("volume") != "2" || q.Get("number") != "10.5" {
					t.Errorf("unexpected request %s", r.URL)
				}
				if got := q.Get("branch_id"); got != tt.want {
					t.Errorf("branch_id = %q, want %q", got, tt.want)
				}
				fmt.Fprint(w, `{"data":{"id":7,"pages":[{"url":"//a/1.jpg"},{"url":"//a/2.jpg"}]}}`)
			}))
			cp, err := c.ChapterPages(context.Background(), "1--a", "2", "10.5", tt.branch)
			if err != nil {
				t.Fatal(err)
			}
			if len(cp.Pages) != 2 || cp.Pages[1].URL != "//a/2.jpg" {
				t.Fatalf("pages = %+v", cp.Pages)
			}
		})
	}
}

func TestFetchImageFallsBackAndRemembersServer(t *testing.T) {
	var badHits, goodHits atomic.Int32
	mux := http.NewServeMux()
	var srvURL string
	mux.HandleFunc("/api/constants", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"data":{"imageServers":[
			{"id":"compress","url":"%[1]s/good","site_ids":[1]},
			{"id":"main","url":"%[1]s/bad","site_ids":[1]},
			{"id":"main","url":"%[1]s/other-site","site_ids":[2]}]}}`, srvURL)
	})
	mux.HandleFunc("/bad/", func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		if r.Header.Get("Referer") != SiteReferer {
			t.Error("image request without referer")
		}
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/good/", func(w http.ResponseWriter, r *http.Request) {
		goodHits.Add(1)
		if r.URL.Path != "/good/manga/x/1.jpg" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, "image-bytes")
	})
	c, srv := newTestClient(t, mux)
	srvURL = srv.URL

	for range 2 {
		data, err := c.FetchImage(context.Background(), "//manga/x/1.jpg")
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "image-bytes" {
			t.Fatalf("data = %q", data)
		}
	}
	if badHits.Load() != 1 || goodHits.Load() != 2 {
		t.Fatalf("bad=%d good=%d, want bad=1 good=2", badHits.Load(), goodHits.Load())
	}
}

func TestFetchImageAllServersFail(t *testing.T) {
	mux := http.NewServeMux()
	var srvURL string
	mux.HandleFunc("/api/constants", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"data":{"imageServers":[{"id":"main","url":"%s/bad","site_ids":[1]}]}}`, srvURL)
	})
	mux.HandleFunc("/bad/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	c, srv := newTestClient(t, mux)
	srvURL = srv.URL

	if _, err := c.FetchImage(context.Background(), "/x.jpg"); err == nil || !strings.Contains(err.Error(), "all servers failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestOrderServers(t *testing.T) {
	all := []ImageServer{
		{ID: "compress", URL: "https://c.example/", SiteIDs: []int{1, 3}},
		{ID: "download", URL: "https://c.example", SiteIDs: []int{1}},
		{ID: "main", URL: "https://m.example", SiteIDs: []int{1}},
		{ID: "main", URL: "https://h.example", SiteIDs: []int{2}},
		{ID: "crop", URL: "https://crop.example", SiteIDs: []int{1}},
		{ID: "secondary", URL: "", SiteIDs: []int{1}},
	}
	got := orderServers(all, 1)
	want := []string{"https://m.example", "https://c.example"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLimiterSpacesCalls(t *testing.T) {
	l := &limiter{interval: 20 * time.Millisecond}
	start := time.Now()
	for range 3 {
		if err := l.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("3 calls took %v, want >= 40ms", elapsed)
	}
}

func TestLimiterRespectsContext(t *testing.T) {
	l := &limiter{interval: time.Hour}
	_ = l.wait(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
