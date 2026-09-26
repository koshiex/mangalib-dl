package downloader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/koshiex/mangalib-dl/internal/mangalib"
)

// DefaultWorkers is the number of pages downloaded in parallel.
const DefaultWorkers = 4

var (
	// ErrNothingSelected is returned when the selection matches no chapters.
	ErrNothingSelected = errors.New("no chapters match the selection")
	errNoPages         = errors.New("chapter has no pages (paid, removed or requires authorization)")
)

// API is the part of the MangaLib client the downloader needs.
type API interface {
	Manga(ctx context.Context, slug string) (mangalib.Manga, error)
	Chapters(ctx context.Context, slug string) ([]mangalib.Chapter, error)
	ChapterPages(ctx context.Context, slug, volume, number string, branch int64) (mangalib.ChapterPages, error)
	FetchImage(ctx context.Context, pagePath string) ([]byte, error)
}

// Observer receives progress events. Methods may be called from several goroutines.
type Observer interface {
	Planned(p Plan)
	ChapterStarted(label string, pages int)
	PageDone()
	ChapterDone()
	FileDone(f FileResult)
	Warn(msg string)
}

// Plan summarizes a download before it starts.
type Plan struct {
	Title    string
	Dir      string
	Files    int
	Chapters int
	Branch   int64
}

// FileResult is a produced archive.
type FileResult struct {
	Name    string
	Path    string
	Size    int64
	Skipped bool
}

// Downloader builds archives from MangaLib titles.
type Downloader struct {
	api     API
	outDir  string
	workers int
}

// New creates a Downloader that writes into outDir/<title>/.
func New(api API, outDir string, workers int) *Downloader {
	return &Downloader{api: api, outDir: outDir, workers: max(workers, 1)}
}

// Run downloads the request. Existing archives are skipped, so a failed run can be resumed.
// A failed archive does not stop the run; failures are reported via Warn and summarized in the error.
func (d *Downloader) Run(ctx context.Context, req Request, obs Observer) ([]FileResult, error) {
	manga, err := d.api.Manga(ctx, req.Slug)
	if err != nil {
		return nil, err
	}
	chapters, err := d.api.Chapters(ctx, req.Slug)
	if err != nil {
		return nil, err
	}
	selected := selectChapters(chapters, req)
	if len(selected) == 0 {
		return nil, ErrNothingSelected
	}

	branch := resolveBranch(req.Branch, chapters)
	title := sanitizeName(manga.DisplayName())
	units := planUnits(title, selected, chapters, req.Mode)
	dir := filepath.Join(d.outDir, title)
	obs.Planned(Plan{Title: manga.DisplayName(), Dir: dir, Files: len(units), Chapters: len(selected), Branch: branch})
	if n := countFallbacks(selected, branch); n > 0 {
		obs.Warn(fmt.Sprintf("%d chapter(s) are missing in the selected translation and will be taken from another one", n))
	}

	format := req.Format
	if format == "" {
		format = FormatZip
	}
	var results []FileResult
	failed := 0
	for _, u := range units {
		res, err := d.writeUnit(ctx, req.Slug, manga, u, filepath.Join(dir, u.fileBase+"."+string(format)), branch, obs)
		if err != nil {
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			failed++
			obs.Warn(fmt.Sprintf("%s: %v", u.fileBase, err))
			continue
		}
		results = append(results, res)
		obs.FileDone(res)
	}
	if failed > 0 {
		return results, fmt.Errorf("%d of %d archive(s) failed", failed, len(units))
	}
	return results, nil
}

func (d *Downloader) writeUnit(ctx context.Context, slug string, manga mangalib.Manga, u unit, final string, branch int64, obs Observer) (FileResult, error) {
	if st, err := os.Stat(final); err == nil {
		for range u.entries {
			obs.ChapterDone()
		}
		return FileResult{Name: filepath.Base(final), Path: final, Size: st.Size(), Skipped: true}, nil
	}

	arc, err := createArchive(final)
	if err != nil {
		return FileResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			arc.abort()
		}
	}()

	pages := 0
	for _, e := range u.entries {
		images, err := d.fetchChapter(ctx, slug, e, branchFor(e.chapter, branch), obs)
		if err != nil {
			return FileResult{}, fmt.Errorf("%s: %w", e.label, err)
		}
		prefix := ""
		if !u.flat {
			prefix = e.folder + "/"
		}
		width := max(minChapterWidth, len(fmt.Sprint(len(images))))
		for i, img := range images {
			if err := arc.add(fmt.Sprintf("%s%0*d.%s", prefix, width, i+1, img.ext), img.data, false); err != nil {
				return FileResult{}, err
			}
		}
		pages += len(images)
		obs.ChapterDone()
	}

	info, err := buildComicInfo(manga, u, pages)
	if err != nil {
		return FileResult{}, err
	}
	if err := arc.add(comicInfoName, info, true); err != nil {
		return FileResult{}, err
	}
	if err := arc.commit(); err != nil {
		return FileResult{}, err
	}
	committed = true

	st, err := os.Stat(final)
	if err != nil {
		return FileResult{}, err
	}
	return FileResult{Name: filepath.Base(final), Path: final, Size: st.Size()}, nil
}

type pageImage struct {
	ext  string
	data []byte
}

func (d *Downloader) fetchChapter(ctx context.Context, slug string, e entry, branch int64, obs Observer) ([]pageImage, error) {
	cp, err := d.api.ChapterPages(ctx, slug, e.chapter.Volume, e.chapter.Number, branch)
	if err != nil {
		return nil, err
	}
	if len(cp.Pages) == 0 {
		return nil, errNoPages
	}
	obs.ChapterStarted(e.label, len(cp.Pages))

	images := make([]pageImage, len(cp.Pages))
	err = forEachParallel(ctx, len(cp.Pages), d.workers, func(ctx context.Context, i int) error {
		data, err := d.api.FetchImage(ctx, cp.Pages[i].URL)
		if err != nil {
			return fmt.Errorf("page %d: %w", i+1, err)
		}
		ext, out, err := normalizeImage(data)
		if err != nil {
			return fmt.Errorf("page %d: %w", i+1, err)
		}
		images[i] = pageImage{ext: ext, data: out}
		obs.PageDone()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return images, nil
}

// forEachParallel calls fn for 0..n-1 on up to workers goroutines and stops at the first error.
func forEachParallel(ctx context.Context, n, workers int, fn func(context.Context, int) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(workers, n) {
		wg.Go(func() {
			for i := range jobs {
				if err := fn(ctx, i); err != nil {
					cancel(err)
					return
				}
			}
		})
	}
feed:
	for i := range n {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return context.Cause(ctx)
}
