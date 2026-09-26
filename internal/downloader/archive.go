package downloader

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/koshiex/mangalib-dl/internal/mangalib"
)

const comicInfoName = "ComicInfo.xml"

// archive writes a zip into a temp file and renames it on commit,
// so an interrupted download never leaves a broken archive under the final name.
type archive struct {
	file  *os.File
	zw    *zip.Writer
	final string
}

func createArchive(final string) (*archive, error) {
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}
	f, err := os.CreateTemp(dir, ".mangalib-*.part")
	if err != nil {
		return nil, fmt.Errorf("create temp archive: %w", err)
	}
	return &archive{file: f, zw: zip.NewWriter(f), final: final}, nil
}

// add stores images without compression (they are already compressed) and deflates everything else.
func (a *archive) add(name string, data []byte, compress bool) error {
	method := zip.Store
	if compress {
		method = zip.Deflate
	}
	w, err := a.zw.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: time.Now()})
	if err != nil {
		return fmt.Errorf("add %s: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func (a *archive) commit() error {
	if err := errors.Join(a.zw.Close(), a.file.Close()); err != nil {
		os.Remove(a.file.Name())
		return fmt.Errorf("finish archive: %w", err)
	}
	if err := os.Rename(a.file.Name(), a.final); err != nil {
		os.Remove(a.file.Name())
		return fmt.Errorf("rename archive: %w", err)
	}
	return nil
}

func (a *archive) abort() {
	a.zw.Close()
	a.file.Close()
	os.Remove(a.file.Name())
}

// comicInfo is the ComicRack metadata file read by KCC, Komga, Kavita and most readers.
type comicInfo struct {
	XMLName     xml.Name `xml:"ComicInfo"`
	Title       string   `xml:"Title,omitempty"`
	Series      string   `xml:"Series"`
	Number      string   `xml:"Number,omitempty"`
	Volume      string   `xml:"Volume,omitempty"`
	Summary     string   `xml:"Summary,omitempty"`
	Writer      string   `xml:"Writer,omitempty"`
	Penciller   string   `xml:"Penciller,omitempty"`
	Genre       string   `xml:"Genre,omitempty"`
	Web         string   `xml:"Web,omitempty"`
	PageCount   int      `xml:"PageCount,omitempty"`
	LanguageISO string   `xml:"LanguageISO"`
	Manga       string   `xml:"Manga,omitempty"`
}

func buildComicInfo(m mangalib.Manga, u unit, pages int) ([]byte, error) {
	info := comicInfo{
		Title:       u.title,
		Series:      m.DisplayName(),
		Number:      u.number,
		Volume:      u.volume,
		Summary:     strings.TrimSpace(string(m.Summary)),
		Writer:      joinPeople(m.Authors),
		Penciller:   joinPeople(m.Artists),
		Genre:       joinGenres(m.Genres),
		Web:         mangalib.SiteURL + m.SlugURL,
		PageCount:   pages,
		LanguageISO: "ru",
	}
	if m.Type.ID == mangalib.TypeManga {
		info.Manga = "YesAndRightToLeft"
	}
	out, err := xml.MarshalIndent(info, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", comicInfoName, err)
	}
	return append([]byte(xml.Header), out...), nil
}

func joinPeople(people []mangalib.Person) string {
	names := make([]string, 0, len(people))
	for _, p := range people {
		if n := strings.TrimSpace(p.Name); n != "" {
			names = append(names, n)
		}
	}
	return strings.Join(names, ", ")
}

func joinGenres(genres []mangalib.Genre) string {
	names := make([]string, 0, len(genres))
	for _, g := range genres {
		names = append(names, g.Name)
	}
	return strings.Join(names, ", ")
}
