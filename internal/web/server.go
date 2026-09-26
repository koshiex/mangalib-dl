// Package web serves the local UI and JSON API for searching and downloading titles.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/koshiex/mangalib-dl/internal/downloader"
	"github.com/koshiex/mangalib-dl/internal/mangalib"
)

const (
	maxQueryLen      = 200
	maxSearchPage    = 100
	maxBodyBytes     = 1 << 20
	maxChapterIDs    = 20000
	coverCacheMaxAge = "public, max-age=86400"
	contentSecurity  = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'"
)

// DefaultCoverHosts are CDN domains the cover proxy may fetch from.
var DefaultCoverHosts = []string{"cdnlibs.org", "imglib.info", "mangalib.me"}

//go:embed static
var staticFiles embed.FS

// API is the part of the MangaLib client the web server needs.
type API interface {
	Search(ctx context.Context, query string, page int) (mangalib.SearchPage, error)
	Manga(ctx context.Context, slug string) (mangalib.Manga, error)
	Chapters(ctx context.Context, slug string) ([]mangalib.Chapter, error)
	FetchImageURL(ctx context.Context, rawURL string) ([]byte, error)
}

// Config configures the server.
type Config struct {
	// Addr is the listen address; its host is allowed in the Host header (DNS rebinding protection).
	Addr string
	// CoverHosts limits the cover proxy; nil means DefaultCoverHosts.
	CoverHosts []string
}

// Server is the HTTP front end.
type Server struct {
	api          API
	jobs         *JobManager
	allowedHosts map[string]bool
	coverHosts   []string
	log          *slog.Logger
}

// NewServer creates a Server.
func NewServer(api API, jobs *JobManager, cfg Config) *Server {
	coverHosts := cfg.CoverHosts
	if coverHosts == nil {
		coverHosts = DefaultCoverHosts
	}
	return &Server{
		api:          api,
		jobs:         jobs,
		allowedHosts: allowedHosts(cfg.Addr),
		coverHosts:   coverHosts,
		log:          slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
}

// allowedHosts returns nil (any host) when listening on all interfaces.
func allowedHosts(addr string) map[string]bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		return nil
	}
	return map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, strings.ToLower(host): true}
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err) // the embedded directory is fixed at build time
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/manga/{slug}", s.handleManga)
	mux.HandleFunc("GET /api/cover", s.handleCover)
	mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	mux.HandleFunc("POST /api/jobs", s.handleCreateJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleGetJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleCancelJob)
	mux.HandleFunc("GET /api/jobs/{id}/files/{n}", s.handleFile)
	mux.Handle("GET /", http.FileServerFS(static))
	return s.guard(mux)
}

// guard rejects foreign Host headers and cross-origin writes, and sets security headers.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			writeError(w, http.StatusForbidden, "host not allowed")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurity)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(hostport string) bool {
	if s.allowedHosts == nil {
		return true
	}
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	return s.allowedHosts[strings.ToLower(strings.Trim(host, "[]"))]
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

type titleDTO struct {
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	AltTitle string `json:"altTitle"`
	Cover    string `json:"cover"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Age      string `json:"age"`
	Year     string `json:"year"`
	Licensed bool   `json:"licensed"`
}

type mangaDTO struct {
	titleDTO
	OtherNames []string `json:"otherNames"`
	Summary    string   `json:"summary"`
	Authors    []string `json:"authors"`
	Artists    []string `json:"artists"`
	Genres     []string `json:"genres"`
	URL        string   `json:"url"`
}

type chapterDTO struct {
	ID       int64   `json:"id"`
	Volume   string  `json:"volume"`
	Number   string  `json:"number"`
	Name     string  `json:"name"`
	Branches []int64 `json:"branches"`
}

func toTitleDTO(m mangalib.MangaSummary) titleDTO {
	alt := m.EngName
	if alt == "" || alt == m.DisplayName() {
		alt = m.Name
	}
	if alt == m.DisplayName() {
		alt = ""
	}
	cover := ""
	if src := m.Cover.Thumbnail; src != "" {
		cover = "/api/cover?u=" + url.QueryEscape(src)
	}
	return titleDTO{
		Slug:     m.SlugURL,
		Title:    m.DisplayName(),
		AltTitle: alt,
		Cover:    cover,
		Type:     m.Type.Label,
		Status:   m.Status.Label,
		Age:      m.AgeRestriction.Label,
		Year:     m.ReleaseDateString,
		Licensed: m.IsLicensed,
	}
}

func toMangaDTO(m mangalib.Manga) mangaDTO {
	names := func(people []mangalib.Person) []string {
		out := make([]string, 0, len(people))
		for _, p := range people {
			out = append(out, p.Name)
		}
		return out
	}
	genres := make([]string, 0, len(m.Genres))
	for _, g := range m.Genres {
		genres = append(genres, g.Name)
	}
	return mangaDTO{
		titleDTO:   toTitleDTO(m.MangaSummary),
		OtherNames: m.OtherNames,
		Summary:    string(m.Summary),
		Authors:    names(m.Authors),
		Artists:    names(m.Artists),
		Genres:     genres,
		URL:        mangalib.SiteURL + m.SlugURL,
	}
}

func toChapterDTOs(chapters []mangalib.Chapter) []chapterDTO {
	out := make([]chapterDTO, len(chapters))
	for i, ch := range chapters {
		keys := make([]int64, len(ch.Branches))
		for j, b := range ch.Branches {
			keys[j] = b.Key()
		}
		out[i] = chapterDTO{ID: ch.ID, Volume: ch.Volume, Number: ch.Number, Name: ch.Name, Branches: keys}
	}
	return out
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > maxQueryLen {
		writeError(w, http.StatusBadRequest, "запрос должен быть от 1 до 200 символов")
		return
	}
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	page = min(page, maxSearchPage)

	type response struct {
		Items   []titleDTO `json:"items"`
		HasNext bool       `json:"hasNext"`
	}
	if slug, err := mangalib.ParseSlug(q); err == nil {
		m, err := s.api.Manga(r.Context(), slug)
		if err != nil {
			s.writeUpstreamError(w, err)
			return
		}
		writeData(w, http.StatusOK, response{Items: []titleDTO{toTitleDTO(m.MangaSummary)}})
		return
	}
	res, err := s.api.Search(r.Context(), q, page)
	if err != nil {
		s.writeUpstreamError(w, err)
		return
	}
	items := make([]titleDTO, len(res.Items))
	for i, m := range res.Items {
		items[i] = toTitleDTO(m)
	}
	writeData(w, http.StatusOK, response{Items: items, HasNext: res.HasNext})
}

func (s *Server) handleManga(w http.ResponseWriter, r *http.Request) {
	slug, err := mangalib.ParseSlug(r.PathValue("slug"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "некорректный slug")
		return
	}
	m, err := s.api.Manga(r.Context(), slug)
	if err != nil {
		s.writeUpstreamError(w, err)
		return
	}
	chapters, err := s.api.Chapters(r.Context(), slug)
	if err != nil {
		s.writeUpstreamError(w, err)
		return
	}
	writeData(w, http.StatusOK, struct {
		Manga    mangaDTO                `json:"manga"`
		Chapters []chapterDTO            `json:"chapters"`
		Branches []downloader.BranchInfo `json:"branches"`
	}{toMangaDTO(m), toChapterDTOs(chapters), downloader.BranchStats(chapters)})
}

func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	u, err := url.Parse(r.URL.Query().Get("u"))
	if err != nil || u.Scheme != "https" || !hostMatches(u.Hostname(), s.coverHosts) {
		writeError(w, http.StatusBadRequest, "cover host not allowed")
		return
	}
	data, err := s.api.FetchImageURL(r.Context(), u.String())
	if err != nil {
		s.writeUpstreamError(w, err)
		return
	}
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		writeError(w, http.StatusBadGateway, "cover is not an image")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", coverCacheMaxAge)
	_, _ = w.Write(data)
}

func hostMatches(host string, suffixes []string) bool {
	host = strings.ToLower(host)
	for _, s := range suffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

type jobRequest struct {
	Slug       string  `json:"slug"`
	ChapterIDs []int64 `json:"chapterIds"`
	Branch     *int64  `json:"branch"`
	Mode       string  `json:"mode"`
	Format     string  `json:"format"`
}

func (jr jobRequest) toRequest() (downloader.Request, error) {
	slug, err := mangalib.ParseSlug(jr.Slug)
	if err != nil {
		return downloader.Request{}, errors.New("некорректный slug")
	}
	if len(jr.ChapterIDs) > maxChapterIDs {
		return downloader.Request{}, errors.New("слишком много глав")
	}
	mode, err := downloader.ParseMode(jr.Mode)
	if err != nil {
		return downloader.Request{}, err
	}
	format, err := downloader.ParseFormat(jr.Format)
	if err != nil {
		return downloader.Request{}, err
	}
	branch := downloader.BranchAuto
	if jr.Branch != nil {
		if *jr.Branch < 0 {
			return downloader.Request{}, errors.New("некорректный перевод")
		}
		branch = *jr.Branch
	}
	return downloader.Request{
		Slug:       slug,
		ChapterIDs: append([]int64(nil), jr.ChapterIDs...),
		Branch:     branch,
		Mode:       mode,
		Format:     format,
	}, nil
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "expected application/json")
		return
	}
	var body jobRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	req, err := body.toRequest()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snap, err := s.jobs.Submit(req)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "очередь загрузок переполнена")
		return
	}
	writeData(w, http.StatusAccepted, snap)
}

func (s *Server) handleListJobs(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, s.jobs.List())
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	snap, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "задача не найдена")
		return
	}
	writeData(w, http.StatusOK, snap)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	if err := s.jobs.Cancel(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "задача не найдена")
		return
	}
	snap, _ := s.jobs.Get(r.PathValue("id"))
	writeData(w, http.StatusOK, snap)
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "некорректный номер файла")
		return
	}
	path, ok := s.jobs.FilePath(r.PathValue("id"), n)
	if !ok {
		writeError(w, http.StatusNotFound, "файл не найден")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "файл не найден")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось прочитать файл")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": st.Name()}))
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

func (s *Server) writeUpstreamError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, mangalib.ErrNotFound) {
		writeError(w, http.StatusNotFound, "не найдено на MangaLib")
		return
	}
	s.log.Warn("mangalib request failed", "err", err)
	writeError(w, http.StatusBadGateway, err.Error())
}

type envelope struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, envelope{OK: true, Data: data})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, envelope{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
