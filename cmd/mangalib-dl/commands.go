package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/koshiex/mangalib-dl/internal/downloader"
	"github.com/koshiex/mangalib-dl/internal/mangalib"
	"github.com/koshiex/mangalib-dl/internal/web"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 5 * time.Second
)

func cmdServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("serve", stderr)
	addr := fs.String("addr", defaultAddr, "listen address")
	out := fs.String("out", defaultOutDir, "output directory")
	workers := fs.Int("workers", downloader.DefaultWorkers, "parallel page downloads")
	if _, err := parseArgs(fs, args); err != nil {
		return errUsage
	}
	outDir, err := filepath.Abs(*out)
	if err != nil {
		return fmt.Errorf("resolve output dir: %w", err)
	}

	client := newClient()
	jobs := web.NewJobManager(ctx, downloader.New(client, outDir, *workers))
	srv := &http.Server{
		Handler:           web.NewServer(client, jobs, web.Config{Addr: *addr}).Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", *addr, err)
	}
	if host, _, _ := net.SplitHostPort(*addr); !isLoopback(host) {
		fmt.Fprintln(stderr, "внимание: сервер доступен не только с этого компьютера")
	}
	fmt.Fprintf(stdout, "MangaLib DL: http://%s\nархивы: %s\n", ln.Addr(), outDir)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("shutdown", "err", err)
		}
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func cmdSearch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("search", stderr)
	positional, err := parseArgs(fs, args)
	if err != nil || len(positional) == 0 {
		fmt.Fprint(stderr, usageText)
		return errUsage
	}
	res, err := newClient().Search(ctx, strings.Join(positional, " "), 1)
	if err != nil {
		return err
	}
	if len(res.Items) == 0 {
		fmt.Fprintln(stdout, "ничего не найдено")
		return nil
	}
	for _, m := range res.Items {
		fmt.Fprintf(stdout, "%-50s %s [%s, %s]\n", m.SlugURL, m.DisplayName(), m.Type.Label, m.ReleaseDateString)
	}
	return nil
}

func cmdInfo(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("info", stderr)
	positional, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	slug, err := slugArg(positional, stderr)
	if err != nil {
		return err
	}
	client := newClient()
	m, err := client.Manga(ctx, slug)
	if err != nil {
		return err
	}
	chapters, err := client.Chapters(ctx, slug)
	if err != nil {
		return err
	}
	volumes := make(map[string]bool)
	for _, ch := range chapters {
		volumes[ch.Volume] = true
	}
	fmt.Fprintf(stdout, "%s (%s)\n%s\nглав: %d, томов: %d\n", m.DisplayName(), m.Type.Label, mangalib.SiteURL+m.SlugURL, len(chapters), len(volumes))
	if len(chapters) > 0 {
		first, last := chapters[0], chapters[len(chapters)-1]
		fmt.Fprintf(stdout, "с тома %s главы %s по том %s главу %s\n", first.Volume, first.Number, last.Volume, last.Number)
	}
	fmt.Fprintln(stdout, "переводы (-branch):")
	for _, b := range downloader.BranchStats(chapters) {
		fmt.Fprintf(stdout, "  %-8d %4d гл.  %s\n", b.Key, b.Chapters, b.Teams)
	}
	return nil
}

func cmdDownload(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("download", stderr)
	out := fs.String("out", defaultOutDir, "output directory")
	mode := fs.String("mode", string(downloader.ModeVolume), "volume, chapter or single")
	format := fs.String("format", string(downloader.FormatZip), "zip or cbz")
	chapters := fs.String("chapters", "", "chapter numbers, e.g. 1-10,12")
	volumes := fs.String("volumes", "", "volume numbers, e.g. 1-3")
	branch := fs.Int64("branch", downloader.BranchAuto, "translation branch id")
	workers := fs.Int("workers", downloader.DefaultWorkers, "parallel page downloads")
	positional, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	slug, err := slugArg(positional, stderr)
	if err != nil {
		return err
	}
	req, err := buildRequest(slug, *mode, *format, *chapters, *volumes, *branch)
	if err != nil {
		return err
	}

	obs := &cliObserver{w: stdout}
	files, err := downloader.New(newClient(), *out, *workers).Run(ctx, req, obs)
	fmt.Fprintf(stdout, "готово: %d архив(ов)\n", len(files))
	return err
}

func buildRequest(slug, mode, format, chapters, volumes string, branch int64) (downloader.Request, error) {
	m, err := downloader.ParseMode(mode)
	if err != nil {
		return downloader.Request{}, err
	}
	f, err := downloader.ParseFormat(format)
	if err != nil {
		return downloader.Request{}, err
	}
	ch, err := downloader.ParseRanges(chapters)
	if err != nil {
		return downloader.Request{}, fmt.Errorf("-chapters: %w", err)
	}
	vol, err := downloader.ParseRanges(volumes)
	if err != nil {
		return downloader.Request{}, fmt.Errorf("-volumes: %w", err)
	}
	if branch < downloader.BranchAuto {
		return downloader.Request{}, fmt.Errorf("-branch: invalid id %d", branch)
	}
	return downloader.Request{Slug: slug, Chapters: ch, Volumes: vol, Branch: branch, Mode: m, Format: f}, nil
}

// cliObserver prints progress to the terminal.
type cliObserver struct {
	w     io.Writer
	total int
	done  atomic.Int32
}

func (o *cliObserver) Planned(p downloader.Plan) {
	o.total = p.Chapters
	fmt.Fprintf(o.w, "%s: глав %d, архивов %d\nкаталог: %s\n", p.Title, p.Chapters, p.Files, p.Dir)
}

func (o *cliObserver) ChapterStarted(label string, pages int) {
	fmt.Fprintf(o.w, "[%d/%d] %s, страниц: %d\n", o.done.Load()+1, o.total, label, pages)
}

func (o *cliObserver) PageDone() {}

func (o *cliObserver) ChapterDone() { o.done.Add(1) }

func (o *cliObserver) FileDone(f downloader.FileResult) {
	if f.Skipped {
		fmt.Fprintf(o.w, "  уже есть: %s\n", f.Name)
		return
	}
	fmt.Fprintf(o.w, "  готов: %s (%.1f МБ)\n", f.Name, float64(f.Size)/(1<<20))
}

func (o *cliObserver) Warn(msg string) {
	fmt.Fprintf(o.w, "  предупреждение: %s\n", msg)
}
