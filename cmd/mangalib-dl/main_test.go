package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/koshiex/mangalib-dl/internal/downloader"
)

func TestParseArgsAllowsFlagsAfterPositional(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mode := fs.String("mode", "volume", "")
	positional, err := parseArgs(fs, []string{"1--a", "-mode", "chapter", "extra"})
	if err != nil {
		t.Fatal(err)
	}
	if *mode != "chapter" || !slices.Equal(positional, []string{"1--a", "extra"}) {
		t.Fatalf("mode=%q positional=%v", *mode, positional)
	}
}

func TestBuildRequest(t *testing.T) {
	req, err := buildRequest("1--a", "chapter", "cbz", "1-3", "2", 7)
	if err != nil {
		t.Fatal(err)
	}
	if req.Mode != downloader.ModeChapter || req.Format != downloader.FormatCBZ || req.Branch != 7 || len(req.Chapters) != 1 || len(req.Volumes) != 1 {
		t.Fatalf("req = %+v", req)
	}

	bad := []struct {
		name                    string
		mode, format, chs, vols string
		branch                  int64
	}{
		{"mode", "all", "zip", "", "", -1},
		{"format", "volume", "rar", "", "", -1},
		{"chapters", "volume", "zip", "x", "", -1},
		{"volumes", "volume", "zip", "", "3-1", -1},
		{"branch", "volume", "zip", "", "", -2},
	}
	for _, tt := range bad {
		if _, err := buildRequest("1--a", tt.mode, tt.format, tt.chs, tt.vols, tt.branch); err == nil {
			t.Errorf("%s: expected error", tt.name)
		}
	}
}

func TestRunUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx := context.Background()
	if err := run(ctx, nil, &stdout, &stderr); !errors.Is(err, errUsage) {
		t.Errorf("no args: err = %v", err)
	}
	if err := run(ctx, []string{"nope"}, &stdout, &stderr); !errors.Is(err, errUsage) {
		t.Errorf("unknown command: err = %v", err)
	}
	if err := run(ctx, []string{"help"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "mangalib-dl serve") {
		t.Errorf("help: err = %v", err)
	}
	if err := run(ctx, []string{"download"}, &stdout, &stderr); !errors.Is(err, errUsage) {
		t.Errorf("download without slug: err = %v", err)
	}
	if err := run(ctx, []string{"info", "not a slug"}, &stdout, &stderr); err == nil {
		t.Error("info with bad slug: expected error")
	}
}

func TestCLIObserverOutput(t *testing.T) {
	var out bytes.Buffer
	o := &cliObserver{w: &out}
	o.Planned(downloader.Plan{Title: "T", Dir: "/d", Files: 1, Chapters: 2})
	o.ChapterStarted("Vol.01 Ch.001", 10)
	o.ChapterDone()
	o.ChapterStarted("Vol.01 Ch.002", 5)
	o.FileDone(downloader.FileResult{Name: "T.zip", Size: 1 << 20})
	o.FileDone(downloader.FileResult{Name: "Old.zip", Skipped: true})
	o.Warn("w")
	for _, want := range []string{"[1/2] Vol.01 Ch.001", "[2/2] Vol.01 Ch.002", "готов: T.zip (1.0 МБ)", "уже есть: Old.zip", "предупреждение: w"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output has no %q:\n%s", want, out.String())
		}
	}
}

func TestIsLoopback(t *testing.T) {
	for host, want := range map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, "0.0.0.0": false, "": false, "192.168.1.2": false} {
		if got := isLoopback(host); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", host, got, want)
		}
	}
}
