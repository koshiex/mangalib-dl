// Command mangalib-dl downloads titles from MangaLib into zip/cbz archives
// and serves a local web UI for searching and downloading.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/koshiex/mangalib-dl/internal/mangalib"
)

const (
	defaultAddr   = "127.0.0.1:8080"
	defaultOutDir = "downloads"
	tokenEnv      = "MANGALIB_TOKEN"
)

const usageText = `mangalib-dl — скачивание манги с MangaLib в zip/cbz архивы

Использование:
  mangalib-dl serve    [-addr 127.0.0.1:8080] [-out downloads] [-workers 4]
  mangalib-dl search   <запрос>
  mangalib-dl info     <slug или ссылка>
  mangalib-dl download [флаги] <slug или ссылка>

Флаги download:
  -out dir          каталог для архивов (по умолчанию downloads)
  -mode volume      volume | chapter | single — архив на том, на главу или один на всё
  -format zip       zip | cbz
  -chapters spec    номера глав: 1-10,12,20-
  -volumes spec     номера томов: 1-3
  -branch id        id перевода из "info" (0 — перевод по умолчанию, -1 — авто)
  -workers 4        параллельных загрузок страниц

Переменная окружения ` + tokenEnv + ` — Bearer-токен MangaLib для тайтлов, требующих авторизации.
`

var errUsage = errors.New("invalid usage")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		if !errors.Is(err, errUsage) {
			fmt.Fprintln(os.Stderr, "ошибка:", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return errUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return cmdServe(ctx, rest, stdout, stderr)
	case "search":
		return cmdSearch(ctx, rest, stdout, stderr)
	case "info":
		return cmdInfo(ctx, rest, stdout, stderr)
	case "download", "dl":
		return cmdDownload(ctx, rest, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return nil
	default:
		fmt.Fprintf(stderr, "неизвестная команда %q\n\n%s", cmd, usageText)
		return errUsage
	}
}

func newClient() *mangalib.Client {
	var opts []mangalib.Option
	if token := strings.TrimSpace(os.Getenv(tokenEnv)); token != "" {
		opts = append(opts, mangalib.WithToken(token))
	}
	return mangalib.NewClient(opts...)
}

// parseArgs parses flags placed before and after positional arguments.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usageText) }
	return fs
}

func slugArg(positional []string, stderr io.Writer) (string, error) {
	if len(positional) != 1 {
		fmt.Fprint(stderr, usageText)
		return "", errUsage
	}
	return mangalib.ParseSlug(positional[0])
}
