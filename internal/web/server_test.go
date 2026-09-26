package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koshiex/mangalib-dl/internal/downloader"
	"github.com/koshiex/mangalib-dl/internal/mangalib"
)

type fakeAPI struct {
	mu       sync.Mutex
	searched []string
}

func (f *fakeAPI) searches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.searched...)
}

func (f *fakeAPI) Search(_ context.Context, q string, _ int) (mangalib.SearchPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searched = append(f.searched, q)
	return mangalib.SearchPage{Items: []mangalib.MangaSummary{{ID: 1, SlugURL: "1--found", RusName: "Найдено", Name: "Found",
		Cover: mangalib.Cover{Thumbnail: "https://cover.cdnlibs.org/a.jpg"}}}, HasNext: true}, nil
}

func (f *fakeAPI) Manga(_ context.Context, slug string) (mangalib.Manga, error) {
	if slug == "404--missing" {
		return mangalib.Manga{}, fmt.Errorf("manga: %w", mangalib.ErrNotFound)
	}
	return mangalib.Manga{MangaSummary: mangalib.MangaSummary{ID: 2, SlugURL: slug, Name: "Direct"}}, nil
}

func (f *fakeAPI) Chapters(context.Context, string) ([]mangalib.Chapter, error) {
	id := int64(9)
	return []mangalib.Chapter{{ID: 1, Volume: "1", Number: "1", Branches: []mangalib.Branch{{BranchID: &id, Teams: []mangalib.Team{{Name: "T"}}}}}}, nil
}

func (f *fakeAPI) FetchImageURL(context.Context, string) ([]byte, error) {
	return nil, errors.New("not used")
}

// fakeRunner writes one archive and reports progress.
type fakeRunner struct {
	dir   string
	block chan struct{}

	mu   sync.Mutex
	last downloader.Request
}

func (r *fakeRunner) lastReq() downloader.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func (r *fakeRunner) Run(ctx context.Context, req downloader.Request, obs downloader.Observer) ([]downloader.FileResult, error) {
	r.mu.Lock()
	r.last = req
	r.mu.Unlock()
	obs.Planned(downloader.Plan{Title: "Title", Dir: r.dir, Files: 1, Chapters: 1})
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	path := filepath.Join(r.dir, "Title - Vol.01.zip")
	if err := os.WriteFile(path, []byte("PK-archive"), 0o644); err != nil {
		return nil, err
	}
	obs.ChapterDone()
	res := downloader.FileResult{Name: filepath.Base(path), Path: path, Size: 10}
	obs.FileDone(res)
	return []downloader.FileResult{res}, nil
}

func newTestServer(t *testing.T, runner Runner) (*httptest.Server, *fakeAPI) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	api := &fakeAPI{}
	srv := httptest.NewServer(NewServer(api, NewJobManager(ctx, runner), Config{Addr: "127.0.0.1:0"}).Handler())
	t.Cleanup(srv.Close)
	return srv, api
}

func decode[T any](t *testing.T, res *http.Response) (T, envelope) {
	t.Helper()
	defer res.Body.Close()
	var raw struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	var data T
	if raw.OK {
		if err := json.Unmarshal(raw.Data, &data); err != nil {
			t.Fatal(err)
		}
	}
	return data, envelope{OK: raw.OK, Error: raw.Error}
}

func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	res, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestSearchByQueryAndBySlug(t *testing.T) {
	srv, api := newTestServer(t, &fakeRunner{dir: t.TempDir()})

	res, err := http.Get(srv.URL + "/api/search?q=berserk")
	if err != nil {
		t.Fatal(err)
	}
	type items struct {
		Items   []titleDTO `json:"items"`
		HasNext bool       `json:"hasNext"`
	}
	got, env := decode[items](t, res)
	if !env.OK || len(got.Items) != 1 || got.Items[0].Slug != "1--found" || !got.HasNext {
		t.Fatalf("search = %+v %+v", got, env)
	}
	if want := "/api/cover?u=https%3A%2F%2Fcover.cdnlibs.org%2Fa.jpg"; got.Items[0].Cover != want {
		t.Errorf("cover = %q, want %q", got.Items[0].Cover, want)
	}
	if got.Items[0].AltTitle != "Found" {
		t.Errorf("altTitle = %q", got.Items[0].AltTitle)
	}

	res, err = http.Get(srv.URL + "/api/search?q=" + "https://mangalib.me/ru/manga/5--direct")
	if err != nil {
		t.Fatal(err)
	}
	got, _ = decode[items](t, res)
	if len(got.Items) != 1 || got.Items[0].Slug != "5--direct" || len(api.searches()) != 1 {
		t.Fatalf("link search = %+v, searched = %v", got, api.searches())
	}
}

func TestSearchValidation(t *testing.T) {
	srv, _ := newTestServer(t, &fakeRunner{dir: t.TempDir()})
	for _, q := range []string{"", "%20%20", strings.Repeat("a", maxQueryLen+1)} {
		res, err := http.Get(srv.URL + "/api/search?q=" + q)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("q=%.10q: status %d, want 400", q, res.StatusCode)
		}
	}
}

func TestMangaEndpoint(t *testing.T) {
	srv, _ := newTestServer(t, &fakeRunner{dir: t.TempDir()})

	res, err := http.Get(srv.URL + "/api/manga/3--title")
	if err != nil {
		t.Fatal(err)
	}
	type mangaResponse struct {
		Manga    mangaDTO                `json:"manga"`
		Chapters []chapterDTO            `json:"chapters"`
		Branches []downloader.BranchInfo `json:"branches"`
	}
	got, env := decode[mangaResponse](t, res)
	if !env.OK || got.Manga.Slug != "3--title" || len(got.Chapters) != 1 || got.Chapters[0].Branches[0] != 9 || got.Branches[0].Teams != "T" {
		t.Fatalf("manga = %+v %+v", got, env)
	}

	for path, want := range map[string]int{"/api/manga/404--missing": 404, "/api/manga/not-a-slug": 400} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("%s: status %d, want %d", path, res.StatusCode, want)
		}
	}
}

func TestCoverProxyRejectsForeignHosts(t *testing.T) {
	srv, _ := newTestServer(t, &fakeRunner{dir: t.TempDir()})
	for _, u := range []string{"http://cover.cdnlibs.org/a.jpg", "https://evil.example/a.jpg", "https://cdnlibs.org.evil.example/a.jpg", "https://127.0.0.1/"} {
		res, err := http.Get(srv.URL + "/api/cover?u=" + u)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", u, res.StatusCode)
		}
	}
}

func TestGuard(t *testing.T) {
	srv, _ := newTestServer(t, &fakeRunner{dir: t.TempDir()})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/jobs", nil)
	req.Host = "evil.example"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("foreign host: status %d, want 403", res.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/jobs", strings.NewReader(`{"slug":"1--a"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin POST: status %d, want 403", res.StatusCode)
	}

	res, err = http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "MangaLib") || res.Header.Get("Content-Security-Policy") == "" {
		t.Errorf("index: status %d, csp %q", res.StatusCode, res.Header.Get("Content-Security-Policy"))
	}
}

func TestCreateJobValidation(t *testing.T) {
	srv, _ := newTestServer(t, &fakeRunner{dir: t.TempDir()})
	tests := []struct {
		name        string
		contentType string
		body        string
		want        int
	}{
		{"wrong content type", "text/plain", `{"slug":"1--a"}`, http.StatusUnsupportedMediaType},
		{"bad json", "application/json", `{`, http.StatusBadRequest},
		{"unknown field", "application/json", `{"slug":"1--a","path":"/etc"}`, http.StatusBadRequest},
		{"bad slug", "application/json", `{"slug":"../../etc"}`, http.StatusBadRequest},
		{"bad mode", "application/json", `{"slug":"1--a","mode":"all"}`, http.StatusBadRequest},
		{"bad format", "application/json", `{"slug":"1--a","format":"rar"}`, http.StatusBadRequest},
		{"negative branch", "application/json", `{"slug":"1--a","branch":-5}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := http.Post(srv.URL+"/api/jobs", tt.contentType, strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != tt.want {
				t.Fatalf("status %d, want %d", res.StatusCode, tt.want)
			}
		})
	}
}

func waitJob(t *testing.T, base, id string, want JobState) JobSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(base + "/api/jobs/" + id)
		if err != nil {
			t.Fatal(err)
		}
		snap, _ := decode[JobSnapshot](t, res)
		if snap.State == want {
			return snap
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", id, want)
	return JobSnapshot{}
}

func TestJobLifecycleAndFileDownload(t *testing.T) {
	runner := &fakeRunner{dir: t.TempDir()}
	srv, _ := newTestServer(t, runner)

	created, env := decode[JobSnapshot](t, postJSON(t, srv.URL+"/api/jobs", `{"slug":"https://mangalib.me/ru/manga/1--a","chapterIds":[3,4],"branch":0,"mode":"chapter","format":"cbz"}`))
	if !env.OK || created.ID == "" || created.State != StateQueued {
		t.Fatalf("created = %+v %+v", created, env)
	}

	done := waitJob(t, srv.URL, created.ID, StateDone)
	if done.Title != "Title" || len(done.Files) != 1 || done.DoneChapters != 1 {
		t.Fatalf("done = %+v", done)
	}
	req := runner.lastReq()
	if req.Slug != "1--a" || req.Mode != downloader.ModeChapter || req.Format != downloader.FormatCBZ || req.Branch != 0 || len(req.ChapterIDs) != 2 {
		t.Fatalf("runner got %+v", req)
	}

	res, err := http.Get(srv.URL + "/api/jobs/" + created.ID + "/files/0")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "PK-archive" || !strings.Contains(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("file: %q, disposition %q", body, res.Header.Get("Content-Disposition"))
	}

	for _, path := range []string{"/files/1", "/files/-1", "/files/x"} {
		res, err := http.Get(srv.URL + "/api/jobs/" + created.ID + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			t.Errorf("%s: unexpected 200", path)
		}
	}

	res, err = http.Get(srv.URL + "/api/jobs")
	if err != nil {
		t.Fatal(err)
	}
	list, _ := decode[[]JobSnapshot](t, res)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}
}

func TestAutoBranchWhenOmitted(t *testing.T) {
	runner := &fakeRunner{dir: t.TempDir()}
	srv, _ := newTestServer(t, runner)
	created, _ := decode[JobSnapshot](t, postJSON(t, srv.URL+"/api/jobs", `{"slug":"1--a"}`))
	waitJob(t, srv.URL, created.ID, StateDone)
	if req := runner.lastReq(); req.Branch != downloader.BranchAuto || req.Mode != downloader.ModeVolume || req.Format != downloader.FormatZip {
		t.Fatalf("defaults not applied: %+v", req)
	}
}

func TestCancelRunningAndQueuedJobs(t *testing.T) {
	runner := &fakeRunner{dir: t.TempDir(), block: make(chan struct{})}
	srv, _ := newTestServer(t, runner)

	first, _ := decode[JobSnapshot](t, postJSON(t, srv.URL+"/api/jobs", `{"slug":"1--a"}`))
	second, _ := decode[JobSnapshot](t, postJSON(t, srv.URL+"/api/jobs", `{"slug":"2--b"}`))
	waitJob(t, srv.URL, first.ID, StateRunning)

	for _, id := range []string{second.ID, first.ID} {
		res := postJSON(t, srv.URL+"/api/jobs/"+id+"/cancel", "")
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("cancel %s: status %d", id, res.StatusCode)
		}
	}
	waitJob(t, srv.URL, first.ID, StateCancelled)
	waitJob(t, srv.URL, second.ID, StateCancelled)

	res := postJSON(t, srv.URL+"/api/jobs/unknown/cancel", "")
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("cancel unknown: status %d, want 404", res.StatusCode)
	}
}

func TestFailedJobKeepsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewJobManager(ctx, runnerFunc(func(context.Context, downloader.Request, downloader.Observer) ([]downloader.FileResult, error) {
		return nil, errors.New("upstream down")
	}))
	snap, err := m.Submit(downloader.Request{Slug: "1--a"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := m.Get(snap.ID); s.State == StateFailed {
			if s.Error != "upstream down" {
				t.Fatalf("error = %q", s.Error)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not fail")
}

type runnerFunc func(context.Context, downloader.Request, downloader.Observer) ([]downloader.FileResult, error)

func (f runnerFunc) Run(ctx context.Context, req downloader.Request, obs downloader.Observer) ([]downloader.FileResult, error) {
	return f(ctx, req, obs)
}

func TestAllowedHosts(t *testing.T) {
	if allowedHosts("0.0.0.0:8080") != nil || allowedHosts(":8080") != nil {
		t.Error("wildcard listen address must allow any host")
	}
	s := &Server{allowedHosts: allowedHosts("127.0.0.1:8080")}
	for host, want := range map[string]bool{"127.0.0.1:8080": true, "localhost:8080": true, "[::1]:8080": true, "LOCALHOST": true, "evil.example:8080": false} {
		if got := s.hostAllowed(host); got != want {
			t.Errorf("hostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}
