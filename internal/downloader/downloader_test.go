package downloader

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/koshiex/mangalib-dl/internal/mangalib"
)

func encodeImage(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "png":
		err = png.Encode(&buf, img)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeAPI serves a title with chapters; pages of a chapter are keyed by "volume/number".
type fakeAPI struct {
	manga    mangalib.Manga
	chapters []mangalib.Chapter
	pages    map[string][]mangalib.Page
	images   map[string][]byte

	mu       sync.Mutex
	branches []int64
}

func (f *fakeAPI) Manga(context.Context, string) (mangalib.Manga, error) { return f.manga, nil }

func (f *fakeAPI) Chapters(context.Context, string) ([]mangalib.Chapter, error) {
	return f.chapters, nil
}

func (f *fakeAPI) ChapterPages(_ context.Context, _, volume, number string, branch int64) (mangalib.ChapterPages, error) {
	f.mu.Lock()
	f.branches = append(f.branches, branch)
	f.mu.Unlock()
	return mangalib.ChapterPages{Volume: volume, Number: number, Pages: f.pages[volume+"/"+number]}, nil
}

func (f *fakeAPI) FetchImage(_ context.Context, path string) ([]byte, error) {
	data, ok := f.images[path]
	if !ok {
		return nil, fmt.Errorf("no image %s", path)
	}
	return data, nil
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	jpg, pngData := encodeImage(t, "jpeg"), encodeImage(t, "png")
	return &fakeAPI{
		manga: mangalib.Manga{
			MangaSummary: mangalib.MangaSummary{ID: 1, RusName: "Тест: манга", SlugURL: "1--test", Type: mangalib.Label{ID: mangalib.TypeManga}},
			Authors:      []mangalib.Person{{Name: "Author"}},
			Genres:       []mangalib.Genre{{Name: "Драма"}},
		},
		chapters: []mangalib.Chapter{
			chapter(10, "1", "1", branch(0, "A")),
			chapter(11, "1", "2", branch(0, "A")),
			chapter(12, "2", "3", branch(0, "A")),
		},
		pages: map[string][]mangalib.Page{
			"1/1": {{URL: "/1-1.jpg"}, {URL: "/1-2.png"}},
			"1/2": {{URL: "/2-1.jpg"}},
			"2/3": {{URL: "/3-1.jpg"}},
		},
		images: map[string][]byte{"/1-1.jpg": jpg, "/1-2.png": pngData, "/2-1.jpg": jpg, "/3-1.jpg": jpg},
	}
}

type recorder struct {
	mu       sync.Mutex
	plan     Plan
	pages    atomic.Int32
	chapters atomic.Int32
	files    []FileResult
	warnings []string
}

func (r *recorder) Planned(p Plan)             { r.plan = p }
func (r *recorder) ChapterStarted(string, int) {}
func (r *recorder) PageDone()                  { r.pages.Add(1) }
func (r *recorder) ChapterDone()               { r.chapters.Add(1) }
func (r *recorder) FileDone(f FileResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files = append(r.files, f)
}
func (r *recorder) Warn(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = append(r.warnings, msg)
}

func zipEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := make(map[string][]byte)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = data
	}
	return out
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func TestRunVolumeMode(t *testing.T) {
	api := newFakeAPI(t)
	out := t.TempDir()
	rec := &recorder{}

	files, err := New(api, out, 2).Run(context.Background(), Request{Slug: "1--test", Branch: BranchAuto, Mode: ModeVolume, Format: FormatCBZ}, rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || rec.plan.Files != 2 || rec.plan.Chapters != 3 {
		t.Fatalf("files=%d plan=%+v", len(files), rec.plan)
	}
	if rec.pages.Load() != 4 || rec.chapters.Load() != 3 {
		t.Fatalf("pages=%d chapters=%d", rec.pages.Load(), rec.chapters.Load())
	}

	wantPath := filepath.Join(out, "Тест - манга", "Тест - манга - Vol.01.cbz")
	if files[0].Path != wantPath {
		t.Fatalf("path = %s, want %s", files[0].Path, wantPath)
	}
	entries := zipEntries(t, files[0].Path)
	wantNames := []string{"ComicInfo.xml", "Vol.01 Ch.001/001.jpg", "Vol.01 Ch.001/002.png", "Vol.01 Ch.002/001.jpg"}
	if got := sortedKeys(entries); !slices.Equal(got, wantNames) {
		t.Fatalf("entries = %q, want %q", got, wantNames)
	}
	info := string(entries["ComicInfo.xml"])
	for _, want := range []string{"<Series>Тест: манга</Series>", "<Volume>1</Volume>", "<PageCount>3</PageCount>", "<Writer>Author</Writer>", "<Manga>YesAndRightToLeft</Manga>"} {
		if !strings.Contains(info, want) {
			t.Errorf("ComicInfo.xml has no %s:\n%s", want, info)
		}
	}

	leftovers, _ := filepath.Glob(filepath.Join(out, "*", "*.part"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left: %v", leftovers)
	}
}

func TestRunSkipsExistingArchives(t *testing.T) {
	api := newFakeAPI(t)
	out := t.TempDir()
	d := New(api, out, 2)
	req := Request{Slug: "1--test", Branch: BranchAuto, Mode: ModeChapter}
	if _, err := d.Run(context.Background(), req, &recorder{}); err != nil {
		t.Fatal(err)
	}
	api.images = nil // any download attempt would fail now

	rec := &recorder{}
	files, err := d.Run(context.Background(), req, rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !f.Skipped {
			t.Errorf("%s was downloaded again", f.Name)
		}
	}
	if rec.chapters.Load() != 3 {
		t.Errorf("skipped chapters must still count as done, got %d", rec.chapters.Load())
	}
}

func TestRunChapterModeIsFlat(t *testing.T) {
	api := newFakeAPI(t)
	files, err := New(api, t.TempDir(), 1).Run(context.Background(), Request{Slug: "1--test", ChapterIDs: []int64{10}, Branch: BranchAuto, Mode: ModeChapter}, &recorder{})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "Тест - манга - Vol.01 Ch.001.zip" {
		t.Fatalf("files = %+v", files)
	}
	if got := sortedKeys(zipEntries(t, files[0].Path)); !slices.Equal(got, []string{"001.jpg", "002.png", "ComicInfo.xml"}) {
		t.Fatalf("entries = %q", got)
	}
}

func TestRunContinuesAfterFailedArchive(t *testing.T) {
	api := newFakeAPI(t)
	api.pages["1/2"] = nil // paid chapter without pages
	out := t.TempDir()
	rec := &recorder{}

	files, err := New(api, out, 2).Run(context.Background(), Request{Slug: "1--test", Branch: BranchAuto, Mode: ModeVolume}, rec)
	if err == nil || !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("err = %v, want 1 of 2 failed", err)
	}
	if len(files) != 1 || !strings.HasSuffix(files[0].Name, "Vol.02.zip") {
		t.Fatalf("files = %+v", files)
	}
	if len(rec.warnings) != 1 || !strings.Contains(rec.warnings[0], "no pages") {
		t.Fatalf("warnings = %q", rec.warnings)
	}
	if _, err := os.Stat(filepath.Join(out, "Тест - манга", "Тест - манга - Vol.01.zip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed archive must not exist, stat err = %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(out, "*", ".mangalib-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left: %v", leftovers)
	}
}

func TestRunUsesPreferredBranchWithFallback(t *testing.T) {
	api := newFakeAPI(t)
	api.chapters[0].Branches = []mangalib.Branch{branch(0, "A"), branch(5, "B")}
	api.chapters[1].Branches = []mangalib.Branch{branch(5, "B")}
	rec := &recorder{}

	if _, err := New(api, t.TempDir(), 1).Run(context.Background(), Request{Slug: "1--test", Branch: 0, Mode: ModeSingle}, rec); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(api.branches, []int64{0, 5, 0}) {
		t.Fatalf("branches requested = %v, want [0 5 0]", api.branches)
	}
	if len(rec.warnings) != 1 {
		t.Fatalf("want one fallback warning, got %q", rec.warnings)
	}
}

func TestRunNothingSelected(t *testing.T) {
	_, err := New(newFakeAPI(t), t.TempDir(), 1).Run(context.Background(), Request{Slug: "1--test", Chapters: []Range{{100, 200}}}, &recorder{})
	if !errors.Is(err, ErrNothingSelected) {
		t.Fatalf("err = %v, want ErrNothingSelected", err)
	}
}

func TestRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(newFakeAPI(t), t.TempDir(), 1).Run(ctx, Request{Slug: "1--test", Mode: ModeVolume}, &recorder{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestNormalizeImage(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantExt string
		wantErr bool
	}{
		{"jpeg kept", encodeImage(t, "jpeg"), "jpg", false},
		{"png kept", encodeImage(t, "png"), "png", false},
		{"gif converted", encodeImage(t, "gif"), "png", false},
		{"html rejected", []byte("<html>403</html>"), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext, out, err := normalizeImage(tt.data)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if ext != tt.wantExt {
				t.Fatalf("ext = %q, want %q", ext, tt.wantExt)
			}
			if ext == "png" {
				if _, err := png.Decode(bytes.NewReader(out)); err != nil {
					t.Fatalf("output is not png: %v", err)
				}
			}
		})
	}
}

func TestForEachParallelStopsOnError(t *testing.T) {
	boom := errors.New("boom")
	var calls atomic.Int32
	err := forEachParallel(context.Background(), 100, 4, func(_ context.Context, i int) error {
		calls.Add(1)
		if i == 3 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if calls.Load() == 100 {
		t.Error("work was not stopped after the error")
	}

	var seen sync.Map
	if err := forEachParallel(context.Background(), 10, 3, func(_ context.Context, i int) error {
		seen.Store(i, true)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		if _, ok := seen.Load(i); !ok {
			t.Errorf("index %d not processed", i)
		}
	}
}
